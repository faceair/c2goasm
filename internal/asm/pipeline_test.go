package asm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/faceair/c2goasm/arch"
)

type functionStateEncoder struct {
	current string
}

func (*functionStateEncoder) BeginProgram([]string, []string, arch.Descriptor) error { return nil }
func (*functionStateEncoder) BeginFunctions([]*Function, arch.Descriptor) error      { return nil }
func (e *functionStateEncoder) BeginFunction(fn *Function, _ arch.Descriptor) error {
	e.current = fn.Name
	return nil
}

func (e *functionStateEncoder) Encode(inst *Inst, _ arch.Descriptor) (string, error) {
	if e.current == "" {
		return "", fmt.Errorf("encode %q without a selected function", inst.Raw)
	}
	return fmt.Sprintf("    ENC %s %s", e.current, inst.Opcode), nil
}

func TestProcessSelectsEachFunctionBeforeEncoding(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"add x0, x0, x1",
		"ret",
		".size foo, .-foo",
		".globl bar",
		"bar:",
		"sub x0, x0, x1",
		"ret",
		".size bar, .-bar",
	}
	companion := []byte("package p\n")

	out, err := Process(lines, companion, arch.ARM64(), &functionStateEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out, "\n")
	for _, want := range []string{"ENC foo add", "ENC bar sub"} {
		if !strings.Contains(joined, want) {
			t.Errorf("output missing %q:\n%s", want, joined)
		}
	}
}

func TestProcessAcceptsCompanionWithGoBodies(t *testing.T) {
	lines := []string{
		".globl probe",
		"probe:",
		"ret",
		".size probe, .-probe",
	}
	out, err := Process(lines, []byte("package p\nfunc helper() {}\n"), arch.ARM64(), &functionStateEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "(FP)") {
		t.Fatalf("native body reconstructed fictitious Go arguments:\n%s", joined)
	}
}

func TestProcessEmitsOnlyNativeCEntries(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"ret",
		".size foo, .-foo",
		".globl bar",
		"bar:",
		"ret",
		".size bar, .-bar",
	}
	out, err := Process(lines, []byte("package p\n"), arch.ARM64(), &functionStateEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out, "\n")
	for _, want := range []string{
		"TEXT ·_c2goasm_native_foo(SB)",
		"TEXT ·_c2goasm_native_bar(SB)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("output missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"TEXT ·_foo(SB)", "TEXT ·_bar(SB)"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unsafe direct Go wrapper %q was emitted:\n%s", forbidden, joined)
		}
	}
}

func TestProcessEmitsCertifiedDirectLeaf(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"ret",
		".size foo, .-foo",
	}
	out, err := Process(lines, []byte("package p\nfunc _foo()\n"), arch.ARM64(), &functionStateEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out, "\n")
	for _, want := range []string{
		"TEXT ·_c2goasm_native_foo(SB), 516, $0-0",
		"TEXT ·_foo(SB), 4, $0-0",
		"CALL ·_c2goasm_native_foo(SB)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("output missing %q:\n%s", want, joined)
		}
	}
}
