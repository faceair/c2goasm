package asm

import (
	"strings"
	"testing"

	"github.com/faceair/c2goasm/arch"
)

type stubEncoder struct{}

func (stubEncoder) BeginProgram([]string, []string, arch.Descriptor) error { return nil }
func (stubEncoder) BeginFunctions([]*Function, arch.Descriptor) error      { return nil }
func (stubEncoder) BeginFunction(*Function, arch.Descriptor) error         { return nil }
func (stubEncoder) Encode(inst *Inst, _ arch.Descriptor) (string, error) {
	return "    ENC " + inst.Opcode, nil
}

func TestEmitNativeBodyPreservesCompilerFrame(t *testing.T) {
	fn := &Function{
		Name: "framed",
		Body: []Node{
			&Inst{Opcode: "stp", Raw: "stp x29, x30, [sp, #-16]!"},
			&Inst{Opcode: "mov", Raw: "mov x29, sp"},
			&Inst{Opcode: "sub", Raw: "sub sp, sp, #32"},
			&Inst{Opcode: "add", Raw: "add x0, x0, x1"},
			&Inst{Opcode: "add", Raw: "add sp, sp, #32"},
			&Inst{Opcode: "ldp", Raw: "ldp x29, x30, [sp], #16"},
			&Inst{Opcode: "ret", Raw: "ret"},
		},
	}
	out, err := Emit(fn, arch.ARM64(), stubEncoder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "TEXT ·_c2goasm_native_framed(SB), 516, $0-0") {
		t.Fatalf("native header missing:\n%s", joined)
	}
	for _, opcode := range []string{"stp", "mov", "sub", "add", "ldp", "ret"} {
		if !strings.Contains(joined, "ENC "+opcode) {
			t.Errorf("compiler frame instruction %q was dropped:\n%s", opcode, joined)
		}
	}
	for _, obsolete := range []string{"c2goasm-stack-delta", "MOVD a0+0(FP)", "SUB $160"} {
		if strings.Contains(joined, obsolete) {
			t.Errorf("native body retained obsolete ABI0 lowering %q:\n%s", obsolete, joined)
		}
	}
}

func TestEmitPreservesEveryNativeReturn(t *testing.T) {
	fn := &Function{
		Name: "returns",
		Body: []Node{
			&Inst{Opcode: "cbz", Raw: "cbz x0, Learly"},
			&Inst{Opcode: "ret", Raw: "ret"},
			&LabelLine{Name: "Learly"},
			&Inst{Opcode: "ret", Raw: "ret"},
		},
	}
	out, err := Emit(fn, arch.ARM64(), stubEncoder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(out, "\n")
	if got := strings.Count(joined, "ENC ret"); got != 2 {
		t.Fatalf("native returns = %d, want 2:\n%s", got, joined)
	}
	if strings.Contains(joined, "ret+") {
		t.Fatalf("native body wrote a Go ABI return slot:\n%s", joined)
	}
	if strings.Contains(joined, "Learly:\n    NOP") {
		t.Fatalf("label emission changed native byte geometry:\n%s", joined)
	}
}

func TestEmitSkipsLiftedConstTableLabels(t *testing.T) {
	fn := &Function{
		Name: "constants",
		Body: []Node{
			&LabelLine{Name: "L.str"},
			&Inst{Opcode: "ret", Raw: "ret"},
		},
	}
	tables := []*ConstTable{{Name: "LCDATA1", Labels: map[string]uint{"L.str": 0}}}
	out, err := Emit(fn, arch.ARM64(), stubEncoder{}, tables)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(out, "\n"); strings.Contains(joined, "L.str:") {
		t.Fatalf("lifted data label leaked into TEXT:\n%s", joined)
	}
}

func TestNativeNameIsIdempotent(t *testing.T) {
	const lowered = "_c2goasm_native_foo"
	if got := NativeName(lowered); got != lowered {
		t.Fatalf("NativeName(%q) = %q", lowered, got)
	}
}
