package asm

import (
	"github.com/faceair/c2goasm/arch"
)

// Process runs the full conversion pipeline: parse, analyze, rewrite, encode,
// and emit. Any failure aborts with context.
func Process(lines []string, companionSrc []byte, desc arch.Descriptor, enc Encoder) ([]string, error) {
	entries, err := parseCompanion(companionSrc)
	if err != nil {
		return nil, err
	}
	prog, err := ParseSource(lines, desc.Name())
	if err != nil {
		return nil, err
	}
	funcs, err := analyze(prog)
	if err != nil {
		return nil, err
	}
	if len(funcs) == 0 {
		if len(entries) != 0 {
			return nil, attachDirectEntries(nil, entries, desc)
		}
		return nil, nil
	}
	functionSet := make(map[string]bool, len(funcs))
	symbols := make([]string, 0, len(funcs))
	for _, fn := range funcs {
		functionSet[fn.Name] = true
		symbols = append(symbols, fn.Name)
	}
	if err := RewriteLabelsGlobal(funcs); err != nil {
		return nil, err
	}
	tables, err := collectConstTables(prog)
	if err != nil {
		return nil, err
	}
	for _, fn := range funcs {
		if err := rewriteFunction(fn, desc, tables, functionSet); err != nil {
			return nil, err
		}
	}
	if err := attachDirectEntries(funcs, entries, desc); err != nil {
		return nil, err
	}

	// Rewritten local-label names distinguish control-flow branches from
	// native tail calls.
	var labels []string
	for _, fn := range funcs {
		for _, node := range fn.Body {
			switch node := node.(type) {
			case *LabelLine:
				if !isDataLabel(node.Name) {
					labels = append(labels, node.Name)
				}
			case *Inst:
				if node.Label != "" {
					labels = append(labels, node.Label)
				}
			}
		}
	}
	if err := enc.BeginProgram(symbols, labels, desc); err != nil {
		return nil, err
	}
	// Batch the final rewritten body geometry before selecting each function
	// immediately before emission.
	if err := enc.BeginFunctions(funcs, desc); err != nil {
		return nil, err
	}

	var out []string
	out = append(out, "DATA gclocals·untyped(SB)/8, $1")
	out = append(out, "GLOBL gclocals·untyped(SB), 8, $8")
	out = append(out, "")
	out = append(out, EmitTables(tables)...)
	if len(tables) > 0 {
		out = append(out, "")
	}
	for i, fn := range funcs {
		if err := enc.BeginFunction(fn, desc); err != nil {
			return nil, err
		}
		body, err := Emit(fn, desc, enc, tables)
		if err != nil {
			return nil, err
		}
		out = append(out, body...)
		if i < len(funcs)-1 {
			out = append(out, "", "")
		}
	}
	return out, nil
}
