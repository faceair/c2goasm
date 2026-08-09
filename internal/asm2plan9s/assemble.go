package asm2plan9s

import (
	"debug/elf"
	"debug/macho"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/arch/arm64/arm64asm"
	"golang.org/x/arch/x86/x86asm"

	"github.com/faceair/c2goasm/arch"
)

// assembleOne assembles a single instruction with clang and returns the
// machine code of its .text section.
func assembleOne(raw string, desc arch.Descriptor) ([]byte, error) {
	var content string
	var candidates [][]string
	if desc.Name() == "amd64" {
		content = ".intel_syntax noprefix\n" + normalizeGCCIntel(raw) + "\n"
		candidates = amd64Candidates
	} else {
		content = ".text\n" + raw + "\n"
		candidates = arm64Candidates
	}
	asmFile, cleanupAsm, err := writeTempFile("c2goasm-enc", ".s", []byte(content))
	if err != nil {
		return nil, err
	}
	defer cleanupAsm()

	base := strings.TrimSuffix(asmFile, ".s")
	objFile := base + ".o"
	defer os.Remove(objFile)

	var attemptErrs []string
	for _, cand := range candidates {
		args := append(append([]string{}, cand...), "-o", objFile, asmFile)
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err == nil {
			opcodes, err := extractTextOpcodes(objFile)
			if err != nil {
				return nil, fmt.Errorf("extract opcodes for %q: %w", raw, err)
			}
			return opcodes, nil
		} else {
			detail := strings.TrimSpace(string(out))
			if detail == "" {
				detail = err.Error()
			}
			attemptErrs = append(attemptErrs, fmt.Sprintf("%s: %s", strings.Join(args, " "), detail))
		}
	}
	return nil, fmt.Errorf("clang failed for %q; attempts:\n%s", raw, strings.Join(attemptErrs, "\n"))
}

var (
	amd64Candidates = [][]string{
		{"clang", "--target=x86_64-unknown-linux-gnu", "-c"},
		{"clang", "-c"},
		{"clang", "--target=x86_64-apple-darwin", "-c"},
		{"clang", "-arch", "x86_64", "-c"},
	}
	arm64Candidates = [][]string{
		{"clang", "--target=aarch64-none-elf", "-march=armv8.4-a+simd+crypto", "-c"},
		{"clang", "--target=aarch64-none-elf", "-c"},
		{"clang", "-c"},
	}
)

func extractTextOpcodes(objFile string) ([]byte, error) {
	if ef, err := elf.Open(objFile); err == nil {
		defer ef.Close()
		if sec := ef.Section(".text"); sec != nil {
			if data, err := sec.Data(); err == nil && len(data) > 0 {
				return data, nil
			}
		}
	}
	if mf, err := macho.Open(objFile); err == nil {
		defer mf.Close()
		if sec := mf.Section("__text"); sec != nil {
			if data, err := sec.Data(); err == nil && len(data) > 0 {
				return data, nil
			}
		}
	}
	return nil, errors.New("unable to extract text section from object file")
}

// decodeToPlan9 disassembles machine code into Go Plan 9 syntax.
// Returns false when the disassembly is not acceptable to the Go
// assembler (callers then emit raw byte literals instead).
func decodeToPlan9(opcodes []byte, desc arch.Descriptor) (string, bool) {
	if desc.Name() == "amd64" {
		return decodeAMD64(opcodes)
	}
	return decodeARM64(opcodes)
}

func decodeAMD64(opcodes []byte) (string, bool) {
	pc := uint64(0)
	parts := make([]string, 0, len(opcodes)/15+1)
	for len(opcodes) > 0 {
		inst, err := x86asm.Decode(opcodes, 64)
		if err != nil || inst.Op == 0 {
			return "", false
		}
		syntax := x86asm.GoSyntax(inst, pc, nil)
		if syntax == "" || strings.Contains(syntax, "IP") || unsupportedAMD64Syntax(syntax) {
			return "", false
		}
		parts = append(parts, "    "+normalizeAMD64Regs(syntax))
		pc += uint64(inst.Len)
		opcodes = opcodes[inst.Len:]
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

// normalizeAMD64Regs rewrites GoSyntax register spellings to the names
// the Go assembler accepts: memory operands come back as RCX/RSP/...
// while Go asm wants CX/SP/... (R8-R15 keep their names).
func normalizeAMD64Regs(syntax string) string {
	replacer := strings.NewReplacer(
		"RAX", "AX", "RBX", "BX", "RCX", "CX", "RDX", "DX",
		"RSI", "SI", "RDI", "DI", "RBP", "BP", "RSP", "SP",
	)
	return replacer.Replace(syntax)
}

func decodeARM64(opcodes []byte) (string, bool) {
	if len(opcodes) == 0 || len(opcodes)%4 != 0 {
		return "", false
	}
	pc := uint64(0)
	parts := make([]string, 0, len(opcodes)/4)
	for len(opcodes) >= 4 {
		inst, err := arm64asm.Decode(opcodes[:4])
		if err != nil || unsupportedARM64(inst) {
			return "", false
		}
		syntax := arm64asm.GoSyntax(inst, pc, nil, nil)
		if unsupportedARM64Syntax(syntax) {
			return "", false
		}
		parts = append(parts, "    "+normalizeArm64Syntax(inst, syntax))
		pc += 4
		opcodes = opcodes[4:]
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

// unsupportedAMD64Syntax rejects GoSyntax output the Go assembler is
// known not to accept.
func unsupportedAMD64Syntax(syntax string) bool {
	// Memory operands referencing the instruction pointer must be
	// rewritten to SB; reject them until rewrite handles it.
	if strings.Contains(syntax, "(IP)") {
		return true
	}
	fields := strings.Fields(syntax)
	if len(fields) == 0 {
		return true
	}
	switch fields[0] {
	case "BSWAP", "CMPSD_XMM", "CVTSI2SDL", "CVTSI2SDQ",
		"CVTTSD2SIL", "CVTTSD2SIQ", "DS", "FLD", "FSTP",
		"MOVSD_XMM", "SHLDQ", "TZCNT":
		// x86asm.GoSyntax names these differently from cmd/asm's accepted
		// spellings (or exposes an encoding prefix as a standalone op).
		return true
	}
	// Go asm spells sign/zero extensions with explicit widths
	// (MOVBZX, MOVWQSX, ...), not MOVZX/MOVSX.
	if strings.HasPrefix(syntax, "MOVZX") || strings.HasPrefix(syntax, "MOVSX") {
		return true
	}
	// Go asm has no MOVDQU/MOVDQA; the byte-literal fallback preserves
	// the exact memory-access semantics.
	if strings.HasPrefix(syntax, "MOVDQU") || strings.HasPrefix(syntax, "MOVDQA") {
		return true
	}
	// SETcc/CMOVcc spellings differ (Go: SETEQ/CMOVEQ vs Intel SETE/CMOVE).
	if strings.HasPrefix(syntax, "SET") || strings.HasPrefix(syntax, "CMOV") {
		return true
	}
	// GoSyntax emits TESTL AL, AL (8-bit register with L suffix), which
	// the Go assembler rejects; byte/word test variants fall back too.
	if strings.HasPrefix(syntax, "TESTL") || strings.HasPrefix(syntax, "TESTW") || strings.HasPrefix(syntax, "TESTB") {
		return true
	}
	// GoSyntax may pair narrow registers with L/W suffixes (CMPL DL, $61);
	// the byte-literal fallback is safe.
	if strings.HasPrefix(syntax, "CMPL") || strings.HasPrefix(syntax, "CMPW") || strings.HasPrefix(syntax, "CMPB") {
		return true
	}
	return false
}

func unsupportedARM64(inst arm64asm.Inst) bool {
	if inst.Op == arm64asm.MUL {
		if _, ok := inst.Args[0].(arm64asm.RegisterWithArrangement); ok {
			return true // Go assembler doesn't accept VMUL alias
		}
	}
	switch inst.Op {
	case arm64asm.ADDP:
		return true // Go assembler rejects scalar ADDP forms emitted by clang
	case arm64asm.MOVI:
		return true // not all MOVI arrangements are accepted
	}
	return false
}

func unsupportedARM64Syntax(syntax string) bool {
	if strings.Contains(syntax, ".P") && !strings.HasPrefix(syntax, "LDP.P") && !strings.HasPrefix(syntax, "STP.P") {
		return true
	}
	if strings.Contains(syntax, "(PC)") {
		fields := strings.Fields(syntax)
		if len(fields) == 0 || fields[0] == "BL" {
			return !strings.HasPrefix(fields[0], "B")
		}
		if !strings.HasPrefix(fields[0], "B") && !strings.HasPrefix(fields[0], "CB") && !strings.HasPrefix(fields[0], "TB") {
			return true
		}
	}
	if strings.Contains(syntax, "R28") {
		return true
	}
	switch {
	case strings.HasPrefix(syntax, "LDPSW"),
		strings.HasPrefix(syntax, "STPSW"),
		strings.HasPrefix(syntax, "LDPW"),
		strings.HasPrefix(syntax, "STPW"),
		strings.HasPrefix(syntax, "MOVBW"),
		strings.HasPrefix(syntax, "MOVHW"),
		strings.HasPrefix(syntax, "MOVK"),
		strings.HasPrefix(syntax, "LDUR"),
		strings.HasPrefix(syntax, "STUR"),
		strings.HasPrefix(syntax, "VSXTL"),
		strings.HasPrefix(syntax, "SCVTF"),
		strings.HasPrefix(syntax, "VCM"),
		strings.HasPrefix(syntax, "VXTN"),
		strings.HasPrefix(syntax, "VMVN"),
		strings.HasPrefix(syntax, "VF"),
		strings.HasPrefix(syntax, "F"):
		return true
	}
	return false
}

// normalizeArm64Syntax rewrites decoded instructions into spellings that both
// the Go assembler accepts and that preserve the original AArch64 semantics.
func normalizeArm64Syntax(inst arm64asm.Inst, syntax string) string {
	fields := strings.Fields(syntax)
	if len(fields) == 0 {
		return syntax
	}
	if strings.HasPrefix(fields[0], "B.") && len(fields[0]) > 2 {
		fields[0] = "B" + fields[0][2:]
		return strings.Join(fields, " ")
	}
	// x/arch renders the 32-bit MOV alias as MOVW. For register operands,
	// Go's MOVW is SXTW, while MOVWU emits the zero-extending W-register move
	// required by AArch64. Keep immediate MOVW forms unchanged.
	if inst.Op == arm64asm.MOV && fields[0] == "MOVW" && len(fields) > 1 && !strings.HasPrefix(fields[1], "$") {
		fields[0] = "MOVWU"
		return strings.Join(fields, " ")
	}
	return syntax
}

// rawBytesLine renders machine code as Plan 9 byte literals.
func rawBytesLine(opcodes []byte, desc arch.Descriptor) string {
	var parts []string
	if desc.Name() == "arm64" {
		// clang exposes little-endian text bytes; cmd/asm WORD takes the
		// numeric AArch64 instruction word, so reverse each four-byte group.
		for len(opcodes) >= 4 {
			parts = append(parts, fmt.Sprintf(
				"WORD $0x%02x%02x%02x%02x",
				opcodes[3], opcodes[2], opcodes[1], opcodes[0],
			))
			opcodes = opcodes[4:]
		}
		for _, opcode := range opcodes {
			parts = append(parts, fmt.Sprintf("BYTE $0x%02x", opcode))
		}
		return "    " + strings.Join(parts, "; ")
	}

	for len(opcodes) >= 8 {
		parts = append(parts, fmt.Sprintf("QUAD $0x%02x%02x%02x%02x%02x%02x%02x%02x",
			opcodes[7], opcodes[6], opcodes[5], opcodes[4], opcodes[3], opcodes[2], opcodes[1], opcodes[0]))
		opcodes = opcodes[8:]
	}
	for len(opcodes) >= 4 {
		parts = append(parts, fmt.Sprintf("LONG $0x%02x%02x%02x%02x", opcodes[3], opcodes[2], opcodes[1], opcodes[0]))
		opcodes = opcodes[4:]
	}
	if len(opcodes) >= 2 {
		parts = append(parts, fmt.Sprintf("WORD $0x%02x%02x", opcodes[1], opcodes[0]))
		opcodes = opcodes[2:]
	}
	if len(opcodes) == 1 {
		parts = append(parts, fmt.Sprintf("BYTE $0x%02x", opcodes[0]))
	}
	return "    " + strings.Join(parts, "; ")
}

func writeTempFile(prefix, ext string, content []byte) (string, func(), error) {
	tmp, err := os.CreateTemp("", prefix)
	if err != nil {
		return "", nil, err
	}
	name := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", nil, err
	}
	path := name + ext
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(path) }
	return path, cleanup, nil
}
