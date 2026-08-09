package asm

import (
	"fmt"
	"strings"

	"github.com/faceair/c2goasm/arch"
)

func rewriteFunction(fn *Function, desc arch.Descriptor, tables []*ConstTable, symbols map[string]bool) error {
	if desc.Name() == "amd64" {
		if err := rewriteAMD64SymbolReferences(fn, tables, symbols); err != nil {
			return err
		}
	}
	if err := rewriteSymbolAddresses(fn, desc, tables, symbols); err != nil {
		return err
	}
	if err := rewriteGotLoads(fn, desc, tables, symbols); err != nil {
		return err
	}
	for _, node := range fn.Body {
		instruction, ok := node.(*Inst)
		if !ok {
			continue
		}
		if desc.Name() == "arm64" &&
			(strings.Contains(instruction.Raw, "@PAGE") || strings.Contains(instruction.Raw, "@GOT")) {
			return fmt.Errorf("function %s line %d %q: unconsumed ARM64 relocation", fn.Name, instruction.Line, instruction.Raw)
		}
		if desc.Name() == "amd64" {
			for _, operand := range instruction.Operands {
				memory, ok := operand.(Memory)
				if ok && memory.Symbol != "" {
					return fmt.Errorf("function %s line %d %q: unconsumed amd64 symbolic memory operand %q",
						fn.Name, instruction.Line, instruction.Raw, memory.Symbol)
				}
			}
		}
	}
	return nil
}

// RewriteLabelsGlobal renames local labels (.LBB0_2, LBB26_7, ...)
// across the whole program to function-scoped unique names and rewrites
// all references. Cross-function references (clang cold-outline blocks)
// resolve to the defining function's renamed label.
func RewriteLabelsGlobal(funcs []*Function) error {
	local := make([]map[string]string, len(funcs))
	definitions := make(map[string][]string)
	for functionIndex, fn := range funcs {
		prefix := sanitizeLabelPrefix(fn.Name)
		if prefix == "" {
			continue
		}
		sep := "_"
		if strings.HasSuffix(prefix, "_") {
			sep = ""
		}
		local[functionIndex] = make(map[string]string)
		for nodeIndex, node := range fn.Body {
			var name string
			switch node := node.(type) {
			case *LabelLine:
				if isDataLabelAt(fn.Body, nodeIndex) {
					continue
				}
				name = node.Name
			case *Inst:
				name = node.Label
			}
			if name == "" || isRelocationLabel(name) {
				continue
			}
			if _, exists := local[functionIndex][name]; exists {
				return fmt.Errorf("function %s defines local label %q more than once", fn.Name, name)
			}
			mapped := prefix + sep + strings.TrimPrefix(name, ".")
			local[functionIndex][name] = mapped
			definitions[name] = append(definitions[name], mapped)
		}
	}

	resolve := func(functionIndex int, name string) (string, bool, error) {
		if mapped, ok := local[functionIndex][name]; ok {
			return mapped, true, nil
		}
		switch matches := definitions[name]; len(matches) {
		case 0:
			return "", false, nil
		case 1:
			return matches[0], true, nil
		default:
			return "", false, fmt.Errorf("function %s references ambiguous cross-function label %q (%s)",
				funcs[functionIndex].Name, name, strings.Join(matches, ", "))
		}
	}

	for functionIndex, fn := range funcs {
		for nodeIndex, node := range fn.Body {
			if label, ok := node.(*LabelLine); ok {
				if isRelocationLabel(label.Name) || isDataLabelAt(fn.Body, nodeIndex) {
					continue
				}
				if mapped, ok := local[functionIndex][label.Name]; ok {
					label.Name = mapped
				}
				continue
			}
			inst, ok := node.(*Inst)
			if !ok {
				continue
			}
			if mapped, ok := local[functionIndex][inst.Label]; ok {
				inst.Raw = replaceAssemblySymbol(inst.Raw, inst.Label, mapped)
				inst.Label = mapped
			}
			for operandIndex, operand := range inst.Operands {
				switch value := operand.(type) {
				case Symbol:
					mapped, ok, err := resolve(functionIndex, value.Name)
					if err != nil {
						return err
					}
					if ok {
						inst.Raw = replaceAssemblySymbol(inst.Raw, value.Name, mapped)
						inst.Operands[operandIndex] = Symbol{Name: mapped, Offset: value.Offset}
					}
				case Memory:
					mapped, ok, err := resolve(functionIndex, value.Symbol)
					if err != nil {
						return err
					}
					if ok {
						inst.Raw = replaceAssemblySymbol(inst.Raw, value.Symbol, mapped)
						value.Symbol = mapped
						inst.Operands[operandIndex] = value
					}
				}
			}
		}
	}
	return nil
}

func replaceAssemblySymbol(text, old, replacement string) string {
	if old == "" || old == replacement {
		return text
	}
	var out strings.Builder
	cursor, search := 0, 0
	replaced := false
	for search < len(text) {
		relative := strings.Index(text[search:], old)
		if relative < 0 {
			break
		}
		start := search + relative
		end := start + len(old)
		if (start > 0 && assemblySymbolByte(text[start-1])) ||
			(end < len(text) && assemblySymbolByte(text[end])) {
			search = start + 1
			continue
		}
		if !replaced {
			out.Grow(len(text) + len(replacement) - len(old))
			replaced = true
		}
		out.WriteString(text[cursor:start])
		out.WriteString(replacement)
		cursor = end
		search = end
	}
	if !replaced {
		return text
	}
	out.WriteString(text[cursor:])
	return out.String()
}

func assemblySymbolByte(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9' || char == '_' || char == '.' || char == '$'
}

func isRelocationLabel(name string) bool {
	return strings.HasPrefix(name, "Lloh") ||
		strings.HasPrefix(name, "Ltmp") ||
		strings.Contains(name, "_Lloh") ||
		strings.Contains(name, "_Ltmp")
}

// sanitizeLabelPrefix turns a function name into a safe label prefix.
func sanitizeLabelPrefix(name string) string {
	if name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		return "L" + out
	}
	return out
}

// tableFor finds the constant table and byte offset for a data label.
func tableFor(tables []*ConstTable, name string) (string, uint, bool) {
	for _, t := range tables {
		if off, ok := t.Labels[name]; ok {
			return t.Name, off, true
		}
	}
	return "", 0, false
}

func formatTableAddress(table string, base uint, delta int64) string {
	if delta >= 0 {
		return fmt.Sprintf("%s<>+0x%x(SB)", table, base+uint(delta))
	}
	neg := uint(-delta)
	if neg <= base {
		return fmt.Sprintf("%s<>+0x%x(SB)", table, base-neg)
	}
	return fmt.Sprintf("%s<>-0x%x(SB)", table, neg-base)
}

func formatFunctionAddress(target string, offset int64) string {
	name := NativeName(target)
	if offset == 0 {
		return fmt.Sprintf("·%s(SB)", name)
	}
	return fmt.Sprintf("·%s%+d(SB)", name, offset)
}

// rewriteAMD64SymbolReferences converts every ELF RIP-relative relocation
// into an explicit Go assembler SB relocation before encoding.
func rewriteAMD64SymbolReferences(fn *Function, tables []*ConstTable, symbols map[string]bool) error {
	for index, node := range fn.Body {
		inst, ok := node.(*Inst)
		if !ok {
			continue
		}
		memoryIndex := -1
		var memory Memory
		for operandIndex, operand := range inst.Operands {
			value, ok := operand.(Memory)
			if !ok || value.Symbol == "" {
				continue
			}
			if memoryIndex >= 0 {
				return fmt.Errorf("rewrite amd64 line %d %q: multiple symbolic memory operands", inst.Line, inst.Raw)
			}
			memoryIndex, memory = operandIndex, value
		}
		if memoryIndex < 0 {
			continue
		}
		if memory.Base != "SB" || memory.Index != "" || memory.Scale != 0 {
			return fmt.Errorf("rewrite amd64 line %d %q: unsupported symbolic address base=%q index=%q scale=%d",
				inst.Line, inst.Raw, memory.Base, memory.Index, memory.Scale)
		}

		symbol, modifier := splitAMD64SymbolModifier(memory.Symbol)
		if modifier != "" && modifier != "GOTPCREL" {
			return fmt.Errorf("rewrite amd64 line %d %q: unsupported symbol modifier @%s", inst.Line, inst.Raw, modifier)
		}
		table, offset, inTable := tableForAMD64(tables, symbol)
		target := sanitizeLabelPrefix(strings.TrimPrefix(symbol, "_"))
		inFunctions := symbols[target]
		if !inTable && !inFunctions {
			return fmt.Errorf("rewrite amd64 line %d %q: unresolved RIP-relative symbol %q", inst.Line, inst.Raw, memory.Symbol)
		}

		var address string
		if inTable {
			address = formatTableAddress(table, offset, memory.Disp)
		} else {
			address = formatFunctionAddress(target, memory.Disp)
		}
		addressLoad := inst.Opcode == "lea" && modifier == ""
		gotPointerLoad := inst.Opcode == "mov" && modifier == "GOTPCREL" &&
			strings.Contains(strings.ToLower(inst.Raw), "qword ptr")
		if addressLoad || gotPointerLoad {
			if len(inst.Operands) != 2 || memoryIndex != 1 {
				return fmt.Errorf("rewrite amd64 line %d %q: address load requires register destination", inst.Line, inst.Raw)
			}
			destination, ok := inst.Operands[0].(Register)
			if !ok {
				return fmt.Errorf("rewrite amd64 line %d %q: address destination is %T", inst.Line, inst.Raw, inst.Operands[0])
			}
			fn.Body[index] = &SyntheticInst{
				Text: fmt.Sprintf("    LEAQ %s, %s", address, amd64Plan9Register(destination.Name)),
				Raw:  inst.Raw,
				Line: inst.Line,
			}
			continue
		}
		if modifier == "GOTPCREL" {
			return fmt.Errorf("rewrite amd64 line %d %q: unsupported GOTPCREL opcode %q", inst.Line, inst.Raw, inst.Opcode)
		}
		if !inTable {
			return fmt.Errorf("rewrite amd64 line %d %q: function symbol %q used as data", inst.Line, inst.Raw, memory.Symbol)
		}
		if len(inst.Operands) != 2 {
			return fmt.Errorf("rewrite amd64 line %d %q: symbolic %s with %d operands", inst.Line, inst.Raw, inst.Opcode, len(inst.Operands))
		}
		mnemonic, ok := amd64SymbolMnemonic(inst)
		if !ok {
			return fmt.Errorf("rewrite amd64 line %d %q: unsupported RIP-relative opcode %q", inst.Line, inst.Raw, inst.Opcode)
		}
		rendered := [2]string{}
		for operandIndex, operand := range inst.Operands {
			if operandIndex == memoryIndex {
				rendered[operandIndex] = address
				continue
			}
			text, err := renderAMD64SymbolOperand(operand)
			if err != nil {
				return fmt.Errorf("rewrite amd64 line %d %q: %w", inst.Line, inst.Raw, err)
			}
			rendered[operandIndex] = text
		}
		fn.Body[index] = &SyntheticInst{
			Text: fmt.Sprintf("    %s %s, %s", mnemonic, rendered[1], rendered[0]),
			Raw:  inst.Raw,
			Line: inst.Line,
		}
	}
	return nil
}

func splitAMD64SymbolModifier(name string) (string, string) {
	if index := strings.LastIndexByte(name, '@'); index >= 0 {
		return name[:index], strings.ToUpper(name[index+1:])
	}
	return name, ""
}

func tableForAMD64(tables []*ConstTable, symbol string) (string, uint, bool) {
	for _, candidate := range []string{symbol, strings.TrimPrefix(symbol, "_")} {
		if table, offset, ok := tableFor(tables, candidate); ok {
			return table, offset, true
		}
	}
	return "", 0, false
}

func amd64SymbolMnemonic(inst *Inst) (string, bool) {
	switch inst.Opcode {
	case "mov", "add", "cmp":
		width, ok := amd64MemoryWidth(inst.Raw)
		if !ok {
			return "", false
		}
		return strings.ToUpper(inst.Opcode) + string(width), true
	case "movzx", "movsx", "movsxd":
		return amd64ExtendMnemonic(inst)
	case "movsd", "comisd", "andpd", "mulsd", "xorpd", "subsd", "ucomisd", "addsd", "divsd":
		return strings.ToUpper(inst.Opcode), true
	case "movdqa":
		return "MOVO", true
	default:
		return "", false
	}
}

func amd64MemoryWidth(raw string) (byte, bool) {
	raw = strings.ToLower(raw)
	switch {
	case strings.Contains(raw, "qword ptr"):
		return 'Q', true
	case strings.Contains(raw, "dword ptr"):
		return 'L', true
	case strings.Contains(raw, "word ptr"):
		return 'W', true
	case strings.Contains(raw, "byte ptr"):
		return 'B', true
	default:
		return 0, false
	}
}

func amd64ExtendMnemonic(inst *Inst) (string, bool) {
	if len(inst.Operands) != 2 {
		return "", false
	}
	destination, ok := inst.Operands[0].(Register)
	if !ok {
		return "", false
	}
	raw := strings.ToLower(inst.Raw)
	var sourceWidth byte
	var sourceBits int
	switch {
	case strings.Contains(raw, "dword ptr"):
		sourceWidth, sourceBits = 'L', 32
	case strings.Contains(raw, "word ptr"):
		sourceWidth, sourceBits = 'W', 16
	case strings.Contains(raw, "byte ptr"):
		sourceWidth, sourceBits = 'B', 8
	default:
		return "", false
	}
	var destinationWidth byte
	var destinationBits int
	name := strings.ToUpper(destination.Name)
	switch {
	case strings.HasPrefix(name, "R") && !strings.HasSuffix(name, "D") &&
		!strings.HasSuffix(name, "W") && !strings.HasSuffix(name, "B"):
		destinationWidth, destinationBits = 'Q', 64
	case strings.HasPrefix(name, "E") || strings.HasSuffix(name, "D"):
		destinationWidth, destinationBits = 'L', 32
	default:
		destinationWidth, destinationBits = 'W', 16
	}
	if sourceBits >= destinationBits {
		return "", false
	}
	suffix := "ZX"
	if inst.Opcode == "movsx" || inst.Opcode == "movsxd" {
		suffix = "SX"
	}
	return fmt.Sprintf("MOV%c%c%s", sourceWidth, destinationWidth, suffix), true
}

func renderAMD64SymbolOperand(operand Operand) (string, error) {
	switch value := operand.(type) {
	case Register:
		return amd64Plan9Register(value.Name), nil
	case Immediate:
		return fmt.Sprintf("$%d", value.Value), nil
	default:
		return "", fmt.Errorf("unsupported symbolic operand %T", operand)
	}
}

func amd64Plan9Register(name string) string {
	upper := strings.ToUpper(name)
	switch upper {
	case "AH", "BH", "CH", "DH":
		return upper
	case "RAX", "EAX", "AX", "AL":
		return "AX"
	case "RBX", "EBX", "BX", "BL":
		return "BX"
	case "RCX", "ECX", "CX", "CL":
		return "CX"
	case "RDX", "EDX", "DX", "DL":
		return "DX"
	case "RSI", "ESI", "SI", "SIL":
		return "SI"
	case "RDI", "EDI", "DI", "DIL":
		return "DI"
	case "RBP", "EBP", "BP", "BPL":
		return "BP"
	case "RSP", "ESP", "SP", "SPL":
		return "SP"
	}
	for _, prefix := range []string{"XMM", "YMM", "ZMM"} {
		if strings.HasPrefix(upper, prefix) {
			return prefix[:1] + strings.TrimPrefix(upper, prefix)
		}
	}
	if strings.HasPrefix(upper, "R") && len(upper) > 2 {
		last := upper[len(upper)-1]
		if last == 'D' || last == 'W' || last == 'B' {
			return upper[:len(upper)-1]
		}
	}
	return upper
}

// nextInst skips only assembler relocation labels and comments between the
// two instructions emitted for one address calculation. A compiler block
// label is a real branch target and must remain visible to the rewriter.
func nextInst(body []Node, start int) (int, *Inst) {
	for i := start; i < len(body); i++ {
		switch n := body[i].(type) {
		case *Inst:
			return i, n
		case *LabelLine:
			if isRelocationLabel(n.Name) {
				continue
			}
			return -1, nil
		case *CommentLine, *BlankLine:
			continue
		default:
			return -1, nil
		}
	}
	return -1, nil
}

func isDataLabelAt(body []Node, index int) bool {
	label, ok := body[index].(*LabelLine)
	if !ok || isDataLabel(label.Name) {
		return ok
	}
	for i := index + 1; i < len(body); i++ {
		switch n := body[i].(type) {
		case *CommentLine, *BlankLine:
			continue
		case *Directive:
			switch n.Name {
			case "align", "p2align":
				continue
			case "ascii", "asciz", "byte", "short", "long", "quad", "xword", "word", "space", "zero":
				return true
			}
			return false
		default:
			return false
		}
	}
	return false
}

// rewriteSymbolAddresses converts arm64 adrp/add symbol-address pairs
// (adrp x8, sym@PAGE; add x8, x8, sym@PAGEOFF) into a single Plan 9
// address load (MOVD $·sym(SB), x8). Function symbols resolve directly;
// data labels resolve into the constant tables.
func rewriteSymbolAddresses(fn *Function, desc arch.Descriptor, tables []*ConstTable, symbols map[string]bool) error {
	if desc.Name() != "arm64" {
		return nil
	}
	out := fn.Body[:0]
	for i := 0; i < len(fn.Body); i++ {
		node := fn.Body[i]
		inst, ok := node.(*Inst)
		if !ok || inst.Opcode != "adrp" || len(inst.Operands) < 2 {
			out = append(out, node)
			continue
		}
		reg, ok := inst.Operands[0].(Register)
		if !ok {
			out = append(out, node)
			continue
		}
		sym, ok := inst.Operands[1].(Symbol)
		if !ok {
			out = append(out, node)
			continue
		}
		// Look for the paired add, allowing the assembler's Lloh relocation
		// labels and comments between the two instructions.
		j, next := nextInst(fn.Body, i+1)
		if j < 0 || next.Opcode != "add" || len(next.Operands) != 3 {
			out = append(out, node)
			continue
		}
		r0, ok0 := next.Operands[0].(Register)
		r1, ok1 := next.Operands[1].(Register)
		s2, ok2 := next.Operands[2].(Symbol)
		if !ok0 || !ok1 || !ok2 || r1.Name != reg.Name {
			out = append(out, node)
			continue
		}
		dst := r0.Name
		rawTarget := strings.TrimPrefix(strings.TrimSuffix(sym.Name, "@PAGE"), "_")
		addTarget := strings.TrimPrefix(strings.TrimSuffix(s2.Name, "@PAGEOFF"), "_")
		if rawTarget != addTarget {
			out = append(out, node)
			continue
		}
		target := sanitizeLabelPrefix(rawTarget)
		var text string
		if table, off, ok := tableFor(tables, strings.TrimSuffix(sym.Name, "@PAGE")); ok {
			text = fmt.Sprintf("    MOVD $%s, %s", formatTableAddress(table, off, s2.Offset), dst)
		} else {
			if symbols != nil && !symbols[target] {
				return fmt.Errorf("function %s line %d %q: unresolved ARM64 function address %q",
					fn.Name, inst.Line, inst.Raw, rawTarget)
			}
			// Function pointer: reference the native C-ABI body directly.
			text = fmt.Sprintf("    MOVD $%s, %s", formatFunctionAddress(target, s2.Offset), dst)
		}
		out = append(out, &SyntheticInst{
			Text: text,
			Line: inst.Line,
		})
		i = j // consume relocation labels and the add
	}
	fn.Body = out
	return nil
}

// rewriteGotLoads converts arm64 GOT loads and page-relative data accesses.
func rewriteGotLoads(fn *Function, desc arch.Descriptor, tables []*ConstTable, symbols map[string]bool) error {
	if desc.Name() != "arm64" {
		return nil
	}
	out := fn.Body[:0]
	for i := 0; i < len(fn.Body); i++ {
		node := fn.Body[i]
		inst, ok := node.(*Inst)
		if !ok || inst.Opcode != "adrp" || len(inst.Operands) < 2 {
			out = append(out, node)
			continue
		}
		reg, ok := inst.Operands[0].(Register)
		if !ok {
			out = append(out, node)
			continue
		}
		j, next := nextInst(fn.Body, i+1)
		if j < 0 || len(next.Operands) != 2 {
			out = append(out, node)
			continue
		}
		dst, ok0 := next.Operands[0].(Register)
		mem, ok1 := next.Operands[1].(Memory)
		if !ok0 || !ok1 || mem.Base != reg.Name || mem.Symbol == "" {
			out = append(out, node)
			continue
		}
		table, off, inTable := tableFor(tables, mem.Symbol)
		isGOT := strings.Contains(next.Raw, "@GOTPAGEOFF")
		if isGOT {
			if next.Opcode != "ldr" || !strings.HasPrefix(dst.Name, "R") {
				out = append(out, node)
				continue
			}
			target := sanitizeLabelPrefix(strings.TrimPrefix(mem.Symbol, "_"))
			var address string
			if inTable {
				address = formatTableAddress(table, off, mem.Disp)
			} else {
				if symbols != nil && !symbols[target] {
					return fmt.Errorf("function %s line %d %q: unresolved ARM64 GOT function address %q",
						fn.Name, inst.Line, inst.Raw, mem.Symbol)
				}
				address = formatFunctionAddress(target, mem.Disp)
			}
			out = append(out, &SyntheticInst{
				Text: fmt.Sprintf("    MOVD $%s, %s", address, dst.Name),
				Line: inst.Line,
			})
			rewriteRemainingGOTPageUses(fn.Body[j+1:], reg.Name, mem.Symbol, address)
			i = j
			continue
		}
		if !inTable {
			out = append(out, node)
			continue
		}
		text := fmt.Sprintf("    MOVD $%s, %s", formatTableAddress(table, off, 0), reg.Name)
		out = append(out, &SyntheticInst{Text: text, Line: inst.Line})
		symbol := mem.Symbol
		rewritePageBaseUses(fn.Body[j+1:], reg.Name, symbol)
		next.Raw = rewritePageOffsetRaw(next.Raw, symbol, mem.Disp)
		mem.Symbol = ""
		next.Operands[1] = mem
		out = append(out, next)
		i = j
	}
	fn.Body = out
	if err := rewriteRemainingPageBases(fn, tables, symbols); err != nil {
		return err
	}
	return rewriteRemainingPageOffsets(fn, tables, symbols)
}

// rewriteRemainingPageBases handles page bases whose PAGEOFF use is separated
// by control flow or unrelated instructions. Raw ADRP bytes cannot retain a
// Mach-O relocation in Go assembly, so every remaining module reference must
// become an absolute SB-relative address.
func rewriteRemainingPageBases(fn *Function, tables []*ConstTable, symbols map[string]bool) error {
	out := make([]Node, 0, len(fn.Body))
	for i, node := range fn.Body {
		inst, ok := node.(*Inst)
		if !ok || inst.Opcode != "adrp" || len(inst.Operands) < 2 {
			out = append(out, node)
			continue
		}
		reg, regOK := inst.Operands[0].(Register)
		sym, symOK := inst.Operands[1].(Symbol)
		if !regOK || !symOK {
			out = append(out, node)
			continue
		}

		target := sanitizeLabelPrefix(strings.TrimPrefix(sym.Name, "_"))
		table, off, inTable := tableFor(tables, sym.Name)
		var address string
		if inTable {
			address = formatTableAddress(table, off, 0)
		} else {
			if symbols != nil && !symbols[target] {
				return fmt.Errorf("line %d: unresolved page reference %q", inst.Line, sym.Name)
			}
			address = formatFunctionAddress(target, 0)
		}
		if strings.Contains(inst.Raw, "@GOTPAGE") {
			if !rewriteRemainingGOTPageUses(fn.Body[i+1:], reg.Name, sym.Name, address) {
				return fmt.Errorf("line %d: GOT page base %q has no load", inst.Line, sym.Name)
			}
			continue
		}
		if !rewritePageBaseUses(fn.Body[i+1:], reg.Name, sym.Name) {
			// Some compiler-generated table walks use ADRP with a relocation
			// addend directly as the base and then only register offsets.
			if inTable {
				address = formatTableAddress(table, off, sym.Offset)
			} else {
				address = formatFunctionAddress(target, sym.Offset)
			}
		}
		out = append(out, &SyntheticInst{
			Text: fmt.Sprintf("    MOVD $%s, %s", address, reg.Name),
			Line: inst.Line,
		})
	}
	fn.Body = out
	return nil
}

// rewriteRemainingPageOffsets resolves PAGEOFF uses independently of lexical
// adjacency. Optimized compiler output can share one ADRP across disjoint CFG
// paths, so a linear reaching-definition scan is insufficient.
func rewriteRemainingPageOffsets(fn *Function, tables []*ConstTable, symbols map[string]bool) error {
nodeLoop:
	for nodeIndex, node := range fn.Body {
		instruction, ok := node.(*Inst)
		if !ok {
			continue
		}
		for operandIndex, operand := range instruction.Operands {
			switch operand := operand.(type) {
			case Memory:
				if operand.Symbol == "" || !strings.Contains(instruction.Raw, "@PAGEOFF") {
					continue
				}
				if strings.Contains(instruction.Raw, "@GOTPAGEOFF") {
					if instruction.Opcode != "ldr" || operandIndex != 1 || len(instruction.Operands) != 2 {
						return fmt.Errorf("line %d %q: unsupported orphan GOT page use", instruction.Line, instruction.Raw)
					}
					destination, ok := instruction.Operands[0].(Register)
					if !ok || !strings.HasPrefix(destination.Name, "R") {
						return fmt.Errorf("line %d %q: GOT page destination is %T",
							instruction.Line, instruction.Raw, instruction.Operands[0])
					}
					table, offset, inTable := tableFor(tables, operand.Symbol)
					target := sanitizeLabelPrefix(strings.TrimPrefix(operand.Symbol, "_"))
					var address string
					if inTable {
						address = formatTableAddress(table, offset, operand.Disp)
					} else {
						if symbols != nil && !symbols[target] {
							return fmt.Errorf("line %d: unresolved GOT page reference %q", instruction.Line, operand.Symbol)
						}
						address = formatFunctionAddress(target, operand.Disp)
					}
					fn.Body[nodeIndex] = &SyntheticInst{
						Text: fmt.Sprintf("    MOVD $%s, %s", address, destination.Name),
						Line: instruction.Line,
					}
					continue nodeLoop
				}
				instruction.Raw = rewritePageOffsetRaw(instruction.Raw, operand.Symbol, operand.Disp)
				operand.Symbol = ""
				instruction.Operands[operandIndex] = operand
			case Symbol:
				if instruction.Opcode != "add" || operandIndex != 2 ||
					!strings.Contains(instruction.Raw, "@PAGEOFF") {
					continue
				}
				instruction.Raw = rewritePageOffsetRaw(instruction.Raw, operand.Name, operand.Offset)
				instruction.Operands[operandIndex] = Immediate{Value: operand.Offset}
			}
		}
	}
	return nil
}

func rewritePageBaseUses(nodes []Node, base, symbol string) bool {
	rewritten := false
	for _, node := range nodes {
		inst, ok := node.(*Inst)
		if !ok {
			continue
		}
		if len(inst.Operands) > 0 {
			if dst, ok := inst.Operands[0].(Register); ok && inst.Opcode == "adrp" && dst.Name == base {
				break
			}
		}
		for i, operand := range inst.Operands {
			switch value := operand.(type) {
			case Memory:
				if value.Base == base && value.Symbol == symbol {
					oldSymbol := value.Symbol
					value.Symbol = ""
					inst.Raw = rewritePageOffsetRaw(inst.Raw, oldSymbol, value.Disp)
					inst.Operands[i] = value
					rewritten = true
				}
			case Symbol:
				if inst.Opcode == "add" && i == 2 && value.Name == symbol {
					inst.Raw = rewritePageOffsetRaw(inst.Raw, value.Name, value.Offset)
					inst.Operands[i] = Immediate{Value: value.Offset}
					rewritten = true
				}
			}
		}
	}
	return rewritten
}

func rewritePageOffsetRaw(raw, symbol string, offset int64) string {
	needle := symbol + "@PAGEOFF"
	start := strings.Index(raw, needle)
	if start < 0 {
		return raw
	}
	end := start + len(needle)
	pos := end
	for pos < len(raw) && (raw[pos] == ' ' || raw[pos] == '\t') {
		pos++
	}
	if pos < len(raw) && (raw[pos] == '+' || raw[pos] == '-') {
		pos++
		for pos < len(raw) && (raw[pos] == ' ' || raw[pos] == '\t') {
			pos++
		}
		numberStart := pos
		for pos < len(raw) &&
			(raw[pos] >= '0' && raw[pos] <= '9' ||
				raw[pos] >= 'a' && raw[pos] <= 'f' ||
				raw[pos] >= 'A' && raw[pos] <= 'F' ||
				raw[pos] == 'x' || raw[pos] == 'X') {
			pos++
		}
		if pos > numberStart {
			end = pos
		}
	}
	return raw[:start] + fmt.Sprintf("#%d", offset) + raw[end:]
}

func rewriteRemainingGOTPageUses(nodes []Node, base, symbol, address string) bool {
	rewritten := false
	for i, node := range nodes {
		inst, ok := node.(*Inst)
		if !ok {
			continue
		}
		if len(inst.Operands) > 0 {
			if nextBase, ok := inst.Operands[0].(Register); ok && inst.Opcode == "adrp" && nextBase.Name == base {
				break
			}
		}
		if inst.Opcode != "ldr" || len(inst.Operands) != 2 {
			continue
		}
		dst, dstOK := inst.Operands[0].(Register)
		mem, memOK := inst.Operands[1].(Memory)
		if !dstOK || !memOK || mem.Base != base || mem.Symbol != symbol {
			continue
		}
		nodes[i] = &SyntheticInst{
			Text: fmt.Sprintf("    MOVD $%s, %s", address, dst.Name),
			Line: inst.Line,
		}
		rewritten = true
	}
	return rewritten
}
