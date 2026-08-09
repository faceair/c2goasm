package asm2plan9s

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/arch/x86/x86asm"

	"github.com/faceair/c2goasm/arch"
	"github.com/faceair/c2goasm/internal/asm"
)

// batchEncodeFunction assembles all encodable instructions of one
// function in a single clang invocation, preserving label distances
// (branches, PC-relative loads) exactly as the source file had them.
// Returns a map from source line to machine code.
func batchEncodeFunction(fn *asm.Function, desc arch.Descriptor, indirectLines map[int]bool) (map[int][]byte, error) {
	type entry struct {
		line   int
		target bool
	}
	var text []string
	var entries []entry
	targets := 0
	for _, node := range fn.Body {
		switch node := node.(type) {
		case *asm.LabelLine:
			text = append(text, node.Name+":")
		case *asm.SyntheticInst:
			placeholders, err := syntheticBatchLines(node, desc)
			if err != nil {
				return nil, err
			}
			text = append(text, placeholders...)
			for range placeholders {
				entries = append(entries, entry{line: node.Line})
			}
		case *asm.Inst:
			if isAddressPair(node, desc) || isGotPair(node, desc) {
				return nil, fmt.Errorf("batch line %d %q: unconsumed symbolic relocation", node.Line, node.Raw)
			}
			lines, err := batchInstructionLines(node, desc, indirectLines[node.Line])
			if err != nil {
				return nil, err
			}
			for index, source := range lines {
				line := source.text
				if index == 0 && node.Label != "" {
					line = node.Label + ": " + line
				}
				if desc.Name() == "arm64" {
					line = stripArm64InlineComment(line)
				} else {
					line = normalizeGCCIntel(line)
				}
				text = append(text, line)
				entries = append(entries, entry{line: node.Line, target: source.target})
				if source.target {
					targets++
				}
			}
		}
	}
	if targets == 0 {
		return nil, nil
	}

	var content string
	if desc.Name() == "amd64" {
		content = ".intel_syntax noprefix\n" + strings.Join(text, "\n") + "\n"
	} else {
		content = ".text\n" + strings.Join(text, "\n") + "\n"
	}
	opcodes, err := assembleText(content, desc)
	if err != nil {
		return nil, err
	}

	result := make(map[int][]byte, targets)
	cursor := 0
	for _, item := range entries {
		length, err := instLen(opcodes[cursor:], desc)
		if err != nil {
			return nil, fmt.Errorf("batch decode at line %d bytes %x: %w", item.line, opcodePreview(opcodes[cursor:]), err)
		}
		if item.target {
			result[item.line] = append([]byte(nil), opcodes[cursor:cursor+length]...)
		}
		cursor += length
	}
	if cursor != len(opcodes) {
		return nil, fmt.Errorf("batch assembly size mismatch: consumed %d of %d bytes", cursor, len(opcodes))
	}
	return result, nil
}

// batchEncodeProgram assembles the already globally-renamed function bodies in
// one invocation while preserving source-line mappings and byte geometry.
func batchEncodeProgram(funcs []*asm.Function, desc arch.Descriptor, indirectLines map[int]bool) (map[int][]byte, error) {
	type entry struct {
		line   int
		target bool
	}
	labels := make(map[string]bool)
	for _, function := range funcs {
		for _, node := range function.Body {
			var name string
			switch node := node.(type) {
			case *asm.LabelLine:
				name = node.Name
			case *asm.Inst:
				name = node.Label
			}
			if name == "" {
				continue
			}
			if _, duplicate := labels[name]; duplicate {
				return nil, fmt.Errorf("program batch label %q is not unique", name)
			}
			labels[name] = true
		}
	}

	var text []string
	var entries []entry
	for _, function := range funcs {
		for _, node := range function.Body {
			switch node := node.(type) {
			case *asm.LabelLine:
				text = append(text, node.Name+":")
			case *asm.SyntheticInst:
				placeholders, err := syntheticBatchLines(node, desc)
				if err != nil {
					return nil, err
				}
				for _, placeholder := range placeholders {
					text = append(text, placeholder)
					entries = append(entries, entry{line: node.Line})
				}
			case *asm.Inst:
				if isAddressPair(node, desc) || isGotPair(node, desc) {
					return nil, fmt.Errorf("program batch line %d %q: unconsumed symbolic relocation", node.Line, node.Raw)
				}
				lines, err := batchInstructionLines(node, desc, indirectLines[node.Line])
				if err != nil {
					return nil, err
				}
				for index, source := range lines {
					line := source.text
					if index == 0 && node.Label != "" {
						line = node.Label + ": " + line
					}
					if desc.Name() == "arm64" {
						line = stripArm64InlineComment(line)
					} else {
						line = normalizeGCCIntel(line)
					}
					text = append(text, line)
					entries = append(entries, entry{line: node.Line, target: source.target})
				}
			}
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}
	content := ".text\n"
	if desc.Name() == "amd64" {
		content += ".intel_syntax noprefix\n"
	}
	content += strings.Join(text, "\n") + "\n"
	opcodes, err := assembleText(content, desc)
	if err != nil {
		return nil, err
	}
	result := make(map[int][]byte)
	cursor := 0
	for _, item := range entries {
		length, err := instLen(opcodes[cursor:], desc)
		if err != nil {
			return nil, fmt.Errorf("program batch decode at line %d bytes %x: %w", item.line, opcodePreview(opcodes[cursor:]), err)
		}
		if item.target {
			result[item.line] = append([]byte(nil), opcodes[cursor:cursor+length]...)
		}
		cursor += length
	}
	if cursor != len(opcodes) {
		return nil, fmt.Errorf("program batch size mismatch: consumed %d of %d bytes", cursor, len(opcodes))
	}
	return result, nil
}

type batchSourceLine struct {
	text   string
	target bool
}

func batchInstructionLines(instruction *asm.Inst, desc arch.Descriptor, indirect bool) ([]batchSourceLine, error) {
	if !directlyMapped(instruction, desc) {
		return []batchSourceLine{{text: instruction.Raw, target: true}}, nil
	}
	if indirect {
		count := 13 // AMD64: imm64 MOVQ plus register CALL/JMP.
		if desc.Name() == "arm64" {
			count = 3 // ARM64: ADRP+ADD plus register CALL/JMP.
		}
		lines := make([]batchSourceLine, count)
		for index := range lines {
			lines[index].text = "nop"
		}
		return lines, nil
	}
	if len(instruction.Operands) == 1 {
		if memory, ok := instruction.Operands[0].(asm.Memory); ok &&
			(instruction.Opcode == "call" || instruction.Opcode == "jmp") {
			if desc.Name() == "arm64" {
				return []batchSourceLine{{text: "nop"}, {text: "nop"}}, nil
			}
			operand, err := amd64IntelMemory(memory)
			if err != nil {
				return nil, fmt.Errorf("batch line %d %q: %w", instruction.Line, instruction.Raw, err)
			}
			return []batchSourceLine{
				{text: "mov r11, qword ptr " + operand},
				{text: instruction.Opcode + " r11"},
			}, nil
		}
	}
	return []batchSourceLine{{text: instruction.Raw}}, nil
}

func amd64IntelMemory(memory asm.Memory) (string, error) {
	if memory.Symbol != "" || memory.Base == "SB" {
		return "", fmt.Errorf("cannot model symbolic indirect call target %q", memory.Symbol)
	}
	var address strings.Builder
	if memory.Base != "" {
		address.WriteString(amd64IntelRegister(memory.Base))
	}
	if memory.Index != "" {
		if address.Len() != 0 {
			address.WriteByte('+')
		}
		address.WriteString(amd64IntelRegister(memory.Index))
		scale := memory.Scale
		if scale == 0 {
			scale = 1
		}
		if scale != 1 {
			fmt.Fprintf(&address, "*%d", scale)
		}
	}
	if memory.Disp != 0 || address.Len() == 0 {
		if memory.Disp >= 0 && address.Len() != 0 {
			address.WriteByte('+')
		}
		fmt.Fprintf(&address, "%d", memory.Disp)
	}
	return "[" + address.String() + "]", nil
}

func amd64IntelRegister(register string) string {
	switch strings.ToUpper(register) {
	case "AX", "RAX":
		return "rax"
	case "BX", "RBX":
		return "rbx"
	case "CX", "RCX":
		return "rcx"
	case "DX", "RDX":
		return "rdx"
	case "SI", "RSI":
		return "rsi"
	case "DI", "RDI":
		return "rdi"
	case "BP", "RBP":
		return "rbp"
	case "SP", "RSP":
		return "rsp"
	default:
		return strings.ToLower(register)
	}
}

func syntheticBatchLines(instruction *asm.SyntheticInst, desc arch.Descriptor) ([]string, error) {
	if desc.Name() == "arm64" {
		// Every ARM64 synthetic is one SB-relative MOVD pseudo-instruction.
		// cmd/asm expands it to ADRP+ADD/LDR, matching two native words.
		return []string{"nop", "nop"}, nil
	}
	if instruction.Raw == "" {
		return nil, fmt.Errorf("program batch line %d: amd64 synthetic instruction has no source geometry", instruction.Line)
	}
	return []string{normalizeGCCIntel(amd64SyntheticGeometry(instruction.Raw))}, nil
}

func amd64SyntheticGeometry(raw string) string {
	lower := strings.ToLower(raw)
	end := strings.Index(lower, "[rip")
	if end < 0 {
		return raw
	}
	start := end
	for start > 0 {
		char := raw[start-1]
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("_.$@", rune(char)) {
			start--
			continue
		}
		break
	}
	if start == end {
		return raw
	}
	return raw[:start] + "c2goasm_geometry_symbol" + raw[end:]
}

func opcodePreview(opcodes []byte) []byte {
	if len(opcodes) > 8 {
		return opcodes[:8]
	}
	return opcodes
}

// isGotPair reports whether the instruction is part of an arm64 GOT
// load (adrp @GOTPAGE / ldr @GOTPAGEOFF) rewritten by
// rewriteGotLoads.
func isGotPair(inst *asm.Inst, desc arch.Descriptor) bool {
	if strings.Contains(inst.Raw, "@GOTPAGEOFF") {
		return true
	}
	if desc.Name() != "arm64" {
		return false
	}
	if inst.Opcode == "adrp" && len(inst.Operands) == 2 {
		if s, ok := inst.Operands[1].(asm.Symbol); ok && strings.HasSuffix(s.Name, "@GOTPAGE") {
			return true
		}
	}
	if inst.Opcode == "ldr" && len(inst.Operands) == 2 {
		if m, ok := inst.Operands[1].(asm.Memory); ok && m.Symbol != "" && strings.HasSuffix(m.Symbol, "@GOTPAGEOFF") {
			return true
		}
	}
	return false
}

// isAddressPair reports whether the instruction is part of an arm64
// adrp/add symbol-address pair that rewriteSymbolAddresses converts to
// a synthetic MOVD (its batch bytes would be relocation placeholders).
func isAddressPair(inst *asm.Inst, desc arch.Descriptor) bool {
	if desc.Name() == "amd64" {
		for _, operand := range inst.Operands {
			if memory, ok := operand.(asm.Memory); ok && memory.Symbol != "" {
				return true
			}
		}
		return false
	}
	if strings.Contains(inst.Raw, "@PAGEOFF") {
		return true
	}
	if desc.Name() != "arm64" {
		return false
	}
	if inst.Opcode == "adrp" {
		return true
	}
	if inst.Opcode == "add" && len(inst.Operands) == 3 {
		if s, ok := inst.Operands[2].(asm.Symbol); ok && strings.HasSuffix(s.Name, "@PAGEOFF") {
			return true
		}
	}
	return false
}

// directlyMapped reports whether the instruction is handled by the
// direct-mnemonic path (jumps, calls, nop, ret) and thus does not need
// assembly.
func directlyMapped(inst *asm.Inst, desc arch.Descriptor) bool {
	if len(inst.Prefixes) != 0 {
		return false
	}
	switch inst.Opcode {
	case "nop", "ret", "call", "bl", "blr", "br":
		return true
	case "jmp":
		return len(inst.Operands) == 1
	}
	if strings.HasPrefix(inst.Opcode, "j") || (desc.Name() == "arm64" && isArm64Branch(inst.Opcode)) {
		if len(inst.Operands) == 0 {
			return false
		}
		_, ok := inst.Operands[len(inst.Operands)-1].(asm.Symbol)
		return ok
	}
	return false
}

// instLen decodes one instruction at the front of opcodes and returns
// its length in bytes.
func instLen(opcodes []byte, desc arch.Descriptor) (int, error) {
	if desc.Name() == "amd64" {
		if len(opcodes) >= 4 && opcodes[0] == 0xf3 && opcodes[1] == 0x0f &&
			opcodes[2] == 0x1e && (opcodes[3] == 0xfa || opcodes[3] == 0xfb) {
			return 4, nil
		}
		inst, err := x86asm.Decode(opcodes, 64)
		if err != nil {
			return 0, err
		}
		return inst.Len, nil
	}
	if len(opcodes) < 4 {
		return 0, fmt.Errorf("short opcode stream")
	}
	return 4, nil
}

// assembleText assembles a full text block (label definitions included)
// and returns the .text section bytes.
func assembleText(content string, desc arch.Descriptor) ([]byte, error) {
	asmFile, cleanupAsm, err := writeTempFile("c2goasm-batch", ".s", []byte(content))
	if err != nil {
		return nil, err
	}
	defer cleanupAsm()

	base := strings.TrimSuffix(asmFile, ".s")
	objFile := base + ".o"
	defer os.Remove(objFile)

	var candidates [][]string
	if desc.Name() == "amd64" {
		candidates = amd64Candidates
	} else {
		candidates = arm64Candidates
	}
	var attemptErrs []string
	for _, cand := range candidates {
		args := append(append([]string{}, cand...), "-o", objFile, asmFile)
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err == nil {
			opcodes, err := extractTextOpcodes(objFile)
			if err != nil {
				return nil, err
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
	if os.Getenv("C2GOASM_KEEP_BATCH") != "" {
		dump, err := os.ReadFile(asmFile)
		if err == nil {
			fmt.Fprintf(os.Stderr, "--- batch text (failed):\n%s\n---\n", dump)
		}
	}
	return nil, fmt.Errorf("clang batch assembly failed; attempts:\n%s", strings.Join(attemptErrs, "\n"))
}

// stripArm64InlineComment removes a trailing ';' comment from an arm64
// instruction line (clang emits them as "add x0, x0, x1 ; comment").
func stripArm64InlineComment(body string) string {
	if idx := strings.Index(body, ";"); idx >= 0 {
		body = body[:idx]
	}
	return strings.TrimSpace(body)
}

// gccDispBracketRe matches GCC Intel displacement-outside-bracket
// syntax: 32[rcx], -1[rdi], QWORD PTR 32[rcx] -> [rcx+32] etc.
var gccDispBracketRe = regexp.MustCompile(`([^\[\s,]+)\s*\[([^\]]+)\]`)

// GCC accepts movsx as an alias for the 32-to-64-bit movsxd form. Clang's
// integrated assembler requires the architectural mnemonic.
var gccMovsxDwordRe = regexp.MustCompile(`(?i)^(\s*)movsx(\s+[^,]+,\s*(?:(?:e(?:ax|bx|cx|dx|si|di|bp|sp)|r(?:[89]|1[0-5])d)\b|dword\s+ptr\b))`)

// normalizeGCCIntel converts GCC-specific Intel syntax to the form
// clang's integrated assembler accepts.
func normalizeGCCIntel(line string) string {
	line = gccMovsxDwordRe.ReplaceAllString(line, "${1}movsxd${2}")
	if !strings.Contains(line, "[") {
		return line
	}
	return gccDispBracketRe.ReplaceAllStringFunc(line, func(m string) string {
		parts := gccDispBracketRe.FindStringSubmatch(m)
		if len(parts) != 3 {
			return m
		}
		disp := parts[1]
		inner := parts[2]
		// Skip size qualifiers (QWORD PTR ...) that precede the disp.
		if strings.EqualFold(disp, "ptr") ||
			strings.EqualFold(disp, "byte") || strings.EqualFold(disp, "word") ||
			strings.EqualFold(disp, "dword") || strings.EqualFold(disp, "qword") ||
			strings.EqualFold(disp, "xmmword") || strings.EqualFold(disp, "ymmword") ||
			strings.EqualFold(disp, "oword") || strings.EqualFold(disp, "yword") {
			return m
		}
		if _, err := strconv.ParseInt(strings.TrimPrefix(disp, "-"), 0, 64); err != nil {
			return m // not a numeric displacement
		}
		sign := "+"
		if strings.HasPrefix(disp, "-") {
			sign = "-"
			disp = disp[1:]
		}
		return "[" + inner + sign + disp + "]"
	})
}
