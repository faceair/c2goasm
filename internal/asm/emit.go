package asm

import (
	"fmt"

	"github.com/faceair/c2goasm/arch"
)

// Emit renders one rewritten C function. The native body keeps the compiler's
// C ABI and frame instructions unchanged. A companion declaration adds a Go
// ABI0 wrapper only after the body has passed direct-leaf certification.
func Emit(fn *Function, desc arch.Descriptor, enc Encoder, tables []*ConstTable) ([]string, error) {
	var out []string
	out = append(
		out,
		fmt.Sprintf("TEXT ·%s(SB), 516, $0-0", NativeName(fn.Name)),
		"",
	)
	for _, node := range fn.Body {
		if label, ok := node.(*LabelLine); ok && tableLabel(tables, label.Name) {
			continue
		}
		line, err := emitNode(node, desc, enc)
		if err != nil {
			return nil, err
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if fn.direct != nil {
		out = append(out, "", "")
		out = append(out, emitDirectWrapper(fn, desc)...)
	}
	return out, nil
}

func tableLabel(tables []*ConstTable, name string) bool {
	_, _, ok := tableFor(tables, name)
	return ok
}

func emitNode(node Node, desc arch.Descriptor, enc Encoder) (string, error) {
	switch n := node.(type) {
	case *Inst:
		line, err := enc.Encode(n, desc)
		if err != nil {
			return "", err
		}
		if n.Label != "" {
			// Go assembler requires a real instruction after a label.
			return n.Label + ":\n" + line, nil
		}
		return line, nil
	case *LabelLine:
		if isDataLabel(n.Name) {
			return "", nil // lifted into ConstTables
		}
		return n.Name + ":", nil
	case *SyntheticInst:
		return n.Text, nil
	case *Directive:
		return "", nil // directives are dropped in the body
	case *CommentLine:
		return "", nil
	default:
		return "", nil
	}
}
