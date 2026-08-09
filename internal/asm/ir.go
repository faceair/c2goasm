package asm

import "github.com/faceair/c2goasm/arch"

// Operand kinds for parsed instruction operands.
type OperandKind int

const (
	OperandRegister OperandKind = iota
	OperandImmediate
	OperandMemory
	OperandSymbol
)

// Operand is one parsed operand of an instruction.
type Operand interface {
	Kind() OperandKind
}

// Register is a named register (canonical uppercase, e.g. "RDI", "V0.4S").
type Register struct{ Name string }

func (Register) Kind() OperandKind { return OperandRegister }

// Immediate is an integer literal.
type Immediate struct{ Value int64 }

func (Immediate) Kind() OperandKind { return OperandImmediate }

// Memory is a memory operand. For amd64: base + index*scale + disp,
// with an optional symbol (e.g. foo[rip + 8] -> Symbol=foo, Base=SB).
// For arm64: base + disp, Writeback marks post-index (")" or pre-index "!").
type Memory struct {
	Base      string
	Index     string
	Scale     int
	Disp      int64
	Symbol    string
	Writeback bool
}

func (Memory) Kind() OperandKind { return OperandMemory }

// Symbol is a bare symbol reference (call target, label operand).
type Symbol struct {
	Name   string
	Offset int64
}

func (Symbol) Kind() OperandKind { return OperandSymbol }

// Node is one line of a parsed source file.
type Node interface{ node() }

// Inst is a single instruction.
type Inst struct {
	Opcode   string   // lowercase canonical opcode without instruction prefixes
	Prefixes []string // lowercase amd64 encoding prefixes (rep, lock, data16, ...)
	Operands []Operand
	Label    string // leading label on the same line, if any
	Comment  string // trailing comment, verbatim
	Line     int    // 1-based source line
	Raw      string // original line text (error reporting)
}

func (*Inst) node() {}

// Directive is a pseudo-op line (.globl, .p2align, .byte, ...).
type Directive struct {
	Name    string
	Args    []string
	Line    int
	Raw     string
	Comment string // trailing comment (macOS "Begin function" markers)
}

func (*Directive) node() {}

// LabelLine is a standalone label definition.
type LabelLine struct {
	Name string
	Line int
	Raw  string
}

func (*LabelLine) node() {}

// CommentLine carries a pure comment line (kept for round-tripping).
type CommentLine struct {
	Text string
	Line int
}

func (*CommentLine) node() {}

// SyntheticInst is a converter-generated instruction with its final
// Plan 9 text already determined (e.g. symbol-address loads rewritten
// from adrp/add pairs).
type SyntheticInst struct {
	Text string
	Raw  string // original amd64 instruction, retained only for batch geometry
	Line int
}

func (*SyntheticInst) node() {}

// BlankLine is a blank source line (kept for line number fidelity).
type BlankLine struct{ Line int }

func (*BlankLine) node() {}

// Program is a fully parsed source file.
type Program struct {
	Nodes []Node
}

// Function is one function extracted from the input.
type Function struct {
	Name    string // demangled name used for the native TEXT symbol
	RawName string // original symbol
	Body    []Node
	direct  *directEntry
	// Export records the source-level .globl marker.
	Export bool
}

// NativeName returns the package symbol that owns a converted C body.
func NativeName(name string) string {
	const prefix = "_c2goasm_native_"
	if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
		return name
	}
	return prefix + name
}

// Encoder turns one instruction into its Plan 9 representation.
// Implemented by the encoding chain (internal/asm2plan9s).
type Encoder interface {
	// BeginProgram is called once per input file with the set of
	// module-defined symbols (call targets resolvable in this module)
	// and the set of rewritten local label names.
	BeginProgram(symbols, labels []string, desc arch.Descriptor) error
	// BeginFunctions may pre-compute independent function batches in parallel.
	BeginFunctions(funcs []*Function, desc arch.Descriptor) error
	// BeginFunction selects the current function's pre-computed batch.
	BeginFunction(fn *Function, desc arch.Descriptor) error
	Encode(inst *Inst, desc arch.Descriptor) (string, error)
}
