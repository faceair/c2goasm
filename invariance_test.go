package main

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/faceair/c2goasm/arch"
	"github.com/faceair/c2goasm/internal/asm"
	"github.com/faceair/c2goasm/internal/asm2plan9s"
)

// TestMachineCodeInvariance proves that a native converted body preserves
// the compiler's instruction bytes, including its C prologue and epilogue.
// Go ABI wrappers are deliberately excluded from this fixture.
func TestMachineCodeInvariance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping machine-code invariance in short mode")
	}
	src, companion := writeInvarianceFixture(t)
	objA := assembleWithClang(t, src)
	objB := convertAndAssemble(t, src, companion)
	textA := textSection(t, objA)
	textB := textSection(t, objB)
	if len(textA) == 0 {
		t.Fatal("empty reference .text")
	}
	if !bytes.Equal(textA, textB) {
		limit := len(textA)
		if len(textB) < limit {
			limit = len(textB)
		}
		first := 0
		for first < limit && textA[first] == textB[first] {
			first++
		}
		t.Fatalf(
			"native machine code differs at byte %d: reference=%x converted=%x",
			first,
			textA,
			textB,
		)
	}
	t.Logf("native machine code preserved exactly: %d bytes", len(textA))
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// assembleWithClang assembles GNU assembly into an object file.
func assembleWithClang(t *testing.T, src string) string {
	t.Helper()
	obj := filepath.Join(t.TempDir(), "ref.o")
	cmd := exec.Command("clang", "--target=aarch64-none-elf", "-c", "-o", obj, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clang assemble: %v\n%s", err, out)
	}
	return obj
}

// convertAndAssemble runs the converter and assembles the result.
func convertAndAssemble(t *testing.T, src, companion string) string {
	t.Helper()
	dir := t.TempDir()
	outAsm := filepath.Join(dir, "out.s")
	companionCopy := filepath.Join(dir, "out.go")
	copyFile2(t, companion, companionCopy)

	desc, err := arch.Resolve("arm64")
	if err != nil {
		t.Fatal(err)
	}
	lines := mustReadLines(t, src)
	companionSrc := mustReadFile(t, companionCopy)
	enc := asm2plan9s.NewEncoder(desc)
	result, err := asm.Process(lines, companionSrc, desc, enc)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if err := writeLines2(result, outAsm); err != nil {
		t.Fatal(err)
	}
	obj := filepath.Join(dir, "out.o")
	cmd := exec.Command("go", "tool", "asm", "-o", obj, outAsm)
	cmd.Env = append(os.Environ(), "GOARCH=arm64")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go tool asm: %v\n%s", err, out)
	}
	return obj
}

// textSection extracts machine code bytes: ELF/macho for clang output,
// go tool objdump for go tool asm output (goobj format).
func textSection(t *testing.T, obj string) []byte {
	t.Helper()
	if ef, err := elf.Open(obj); err == nil {
		defer ef.Close()
		if sec := ef.Section(".text"); sec != nil {
			if data, err := sec.Data(); err == nil && len(data) > 0 {
				return data
			}
		}
	}
	if mf, err := macho.Open(obj); err == nil {
		defer mf.Close()
		if sec := mf.Section("__text"); sec != nil {
			if data, err := sec.Data(); err == nil && len(data) > 0 {
				return data
			}
		}
	}
	// Fall back to goobj via go tool objdump.
	cmd := exec.Command("go", "tool", "objdump", obj)
	cmd.Env = append(os.Environ(), "GOARCH=arm64")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("objdump: %v\n%s", err, out)
	}
	var code []byte
	started := false
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "TEXT ") {
			if started {
				break // first TEXT header covers the whole .text segment
			}
			started = true
			continue
		}
		if !started {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[len(fields)-1] == "?" {
			continue // alignment padding, not code
		}
		// Find the first pure-hex byte column (objdump pads with tabs).
		var hex string
		for _, f := range fields[2:] {
			if isHexBytes(f) {
				hex = f
				break
			}
		}
		if hex == "" {
			continue
		}
		// arm64 objdump prints the numeric 32-bit instruction word
		// (a9bf7bfd), whereas ELF .text exposes its little-endian bytes
		// (fd7bbfa9). Normalize both sources to memory order.
		if len(hex) == 8 {
			for i := 6; i >= 0; i -= 2 {
				b, err := strconv.ParseUint(hex[i:i+2], 16, 8)
				if err != nil {
					t.Fatalf("parse objdump instruction %q: %v", hex, err)
				}
				code = append(code, byte(b))
			}
			continue
		}
		for i := 0; i+1 < len(hex); i += 2 {
			b, err := strconv.ParseUint(hex[i:i+2], 16, 8)
			if err != nil {
				break
			}
			code = append(code, byte(b))
		}
	}
	if len(code) == 0 {
		t.Fatalf("no code bytes extracted from %s", obj)
	}
	return code
}

// isHexBytes reports whether s is a run of hex digits with even length.
func isHexBytes(s string) bool {
	if len(s) < 4 || len(s)%2 != 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func writeInvarianceFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "invariance.s")
	companion := filepath.Join(dir, "invariance.go")
	assembly := `.text
.globl _invariance_sum
.p2align 2
_invariance_sum:
stp x29, x30, [sp, #-16]!
mov x29, sp
sub sp, sp, #16
add x0, x0, x1
add x0, x0, #7
add sp, sp, #16
ldp x29, x30, [sp], #16
ret
.size _invariance_sum, .-_invariance_sum
`
	goSource := "package fixture\n"
	if err := os.WriteFile(src, []byte(assembly), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(companion, []byte(goSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, companion
}

func mustReadLines(t *testing.T, path string) []string {
	t.Helper()
	data := mustReadFile(t, path)
	var lines []string
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeLines2(lines []string, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := fmt.Fprintln(f, l); err != nil {
			return err
		}
	}
	return nil
}

func copyFile2(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.WriteFile(dst, mustReadFile(t, src), 0o644); err != nil {
		t.Fatal(err)
	}
}
