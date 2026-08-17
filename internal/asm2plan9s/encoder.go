// Package asm2plan9s encodes instructions into Plan 9 assembly via a
// three-way decision: direct mnemonic, disassembly of clang-assembled
// machine code, or raw byte literals. There is no silent fallback:
// every instruction either encodes or fails with context.
//
// Performance: the encoder assembles a whole input program in one batch;
// failed programs retain the per-function batch and single-instruction fallbacks.
package asm2plan9s

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/faceair/c2goasm/arch"
	"github.com/faceair/c2goasm/internal/asm"
)

// Encoder implements asm.Encoder.
type Encoder struct {
	desc          arch.Descriptor
	batch         map[int][]byte // source line -> machine code for current function
	programBatch  map[int][]byte // one batch covering the whole input program
	batchByFn     map[string]map[int][]byte
	symbols       map[string]bool
	labels        map[string]bool // rewritten local label names
	indirectLines map[int]bool    // DFS back edges hidden from NOSPLIT analysis
	fnName        string
}

// NewEncoder creates an encoder for the given architecture descriptor.
func NewEncoder(desc arch.Descriptor) *Encoder {
	return &Encoder{desc: desc}
}

// BeginProgram records the module-defined symbol and local-label sets.
func (e *Encoder) BeginProgram(symbols, labels []string, desc arch.Descriptor) error {
	e.symbols = make(map[string]bool, len(symbols))
	for _, s := range symbols {
		e.symbols[s] = true
	}
	e.labels = make(map[string]bool, len(labels))
	for _, l := range labels {
		e.labels[l] = true
	}
	return nil
}

// BeginFunctions pre-computes function batches in one assembly invocation.
// If the combined source cannot be assembled, independent function batches
// retain the existing fallback behavior.
func (e *Encoder) BeginFunctions(funcs []*asm.Function, desc arch.Descriptor) error {
	e.programBatch = nil
	e.batchByFn = nil
	e.indirectLines = nil
	if err := validateReservedScratch(funcs, desc); err != nil {
		return err
	}
	if err := validateNativeControlFlow(funcs, desc); err != nil {
		return err
	}
	e.indirectLines = cycleBreakingCallLines(funcs)
	if len(funcs) == 0 {
		return nil
	}
	batch, err := batchEncodeProgram(funcs, desc, e.indirectLines)
	if err == nil {
		e.programBatch = batch
		return nil
	}
	e.batchByFn = make(map[string]map[int][]byte, len(funcs))
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers > len(funcs) {
		workers = len(funcs)
	}
	jobs := make(chan *asm.Function)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fn := range jobs {
				batch, err := batchEncodeFunction(fn, desc, e.indirectLines)
				if err != nil || len(batch) == 0 {
					continue
				}
				mu.Lock()
				e.batchByFn[fn.Name] = batch
				mu.Unlock()
			}
		}()
	}
	for _, fn := range funcs {
		jobs <- fn
	}
	close(jobs)
	wg.Wait()
	return nil
}

// BeginFunction selects the pre-computed batch for the function body.
func (e *Encoder) BeginFunction(fn *asm.Function, desc arch.Descriptor) error {
	e.batch = nil
	e.fnName = fn.Name
	if e.programBatch != nil {
		e.batch = e.programBatch
		return nil
	}
	if e.batchByFn != nil {
		e.batch = e.batchByFn[fn.Name]
		return nil
	}
	enc, err := batchEncodeFunction(fn, desc, e.indirectLines)
	if err != nil {
		return nil // fall back to single-instruction encoding
	}
	e.batch = enc
	return nil
}

// validateReservedScratch makes the x86 call-lowering contract explicit.
// GCC's interprocedural register allocation may keep live values in R11 across
// known direct calls, so call lowering may use R11 only when the input was
// compiled with -ffixed-r11. Direct-only leaves do not use that scratch path
// and may keep R11 as a caller-saved working register.
func validateReservedScratch(funcs []*asm.Function, desc arch.Descriptor) error {
	if desc.Name() != "amd64" {
		return nil
	}
	labels := make(map[string]bool)
	for _, function := range funcs {
		for _, node := range function.Body {
			switch node := node.(type) {
			case *asm.LabelLine:
				labels[node.Name] = true
			case *asm.Inst:
				if node.Label != "" {
					labels[node.Label] = true
				}
			}
		}
	}
	for _, function := range funcs {
		needsScratch := false
		for _, node := range function.Body {
			instruction, ok := node.(*asm.Inst)
			if !ok {
				continue
			}
			if instructionNeedsReservedScratch(instruction, labels, desc) {
				needsScratch = true
				break
			}
		}
		if !needsScratch {
			continue
		}
		for _, node := range function.Body {
			var line int
			var text string
			usesR11 := false
			switch node := node.(type) {
			case *asm.Inst:
				line, text = node.Line, node.Raw
				for _, operand := range node.Operands {
					switch operand := operand.(type) {
					case asm.Register:
						usesR11 = usesR11 || isReservedR11(operand.Name)
					case asm.Memory:
						usesR11 = usesR11 || isReservedR11(operand.Base) || isReservedR11(operand.Index)
					}
				}
			case *asm.SyntheticInst:
				line, text = node.Line, node.Text
				usesR11 = textUsesReservedR11(text)
			}
			if usesR11 {
				return fmt.Errorf("function %s line %d %q uses reserved scratch register R11; compile amd64 input with -ffixed-r11",
					function.Name, line, text)
			}
		}
	}
	return nil
}

func instructionNeedsReservedScratch(instruction *asm.Inst, labels map[string]bool, desc arch.Descriptor) bool {
	if symbol, ok := nativeControlSymbol(instruction, desc); ok {
		return !labels[symbol.Name]
	}
	if instruction.Opcode != "call" && instruction.Opcode != "jmp" {
		return false
	}
	if len(instruction.Operands) != 1 {
		return false
	}
	_, memory := instruction.Operands[0].(asm.Memory)
	return memory
}

func isReservedR11(name string) bool {
	switch strings.ToUpper(name) {
	case "R11", "R11B", "R11W", "R11D":
		return true
	default:
		return false
	}
}

func textUsesReservedR11(text string) bool {
	for _, name := range [...]string{"R11", "R11B", "R11W", "R11D"} {
		if textUsesRegister(text, name) {
			return true
		}
	}
	return false
}

func textUsesRegister(text, register string) bool {
	for start := 0; start < len(text); {
		for start < len(text) && !registerChar(text[start]) {
			start++
		}
		end := start
		for end < len(text) && registerChar(text[end]) {
			end++
		}
		if strings.EqualFold(text[start:end], register) {
			return true
		}
		start = end
	}
	return false
}

func registerChar(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9' || char == '_'
}

func validateNativeControlFlow(funcs []*asm.Function, desc arch.Descriptor) error {
	symbols := make(map[string]bool, len(funcs))
	labels := make(map[string]bool)
	for _, function := range funcs {
		symbols[function.Name] = true
		for _, node := range function.Body {
			switch node := node.(type) {
			case *asm.LabelLine:
				labels[node.Name] = true
			case *asm.Inst:
				if node.Label != "" {
					labels[node.Label] = true
				}
			}
		}
	}
	for _, function := range funcs {
		for _, node := range function.Body {
			instruction, ok := node.(*asm.Inst)
			if !ok {
				continue
			}
			symbol, ok := nativeControlSymbol(instruction, desc)
			if !ok {
				continue
			}
			local := labels[symbol.Name]
			if len(instruction.Prefixes) != 0 {
				if !local {
					return fmt.Errorf("function %s line %d %q: prefixed native/non-local control transfer to %q is unsupported",
						function.Name, instruction.Line, instruction.Raw, symbol.Name)
				}
				if symbol.Offset != 0 {
					return fmt.Errorf("function %s line %d %q: prefixed local control target %q has unsupported offset %d",
						function.Name, instruction.Line, instruction.Raw, symbol.Name, symbol.Offset)
				}
				continue
			}
			if local {
				if symbol.Offset != 0 {
					return fmt.Errorf("function %s line %d %q: local control target %q has unsupported offset %d",
						function.Name, instruction.Line, instruction.Raw, symbol.Name, symbol.Offset)
				}
				continue
			}
			target := nativeSymbolName(symbol.Name)
			if !symbols[target] {
				return fmt.Errorf("function %s line %d %q: unresolved native control target %q",
					function.Name, instruction.Line, instruction.Raw, symbol.Name)
			}
			if symbol.Offset != 0 {
				return fmt.Errorf("function %s line %d %q: native control target %q has unsupported offset %d",
					function.Name, instruction.Line, instruction.Raw, symbol.Name, symbol.Offset)
			}
			switch instruction.Opcode {
			case "call", "bl", "b", "jmp":
			default:
				return fmt.Errorf("function %s line %d %q: conditional native tail branch %q is unsupported",
					function.Name, instruction.Line, instruction.Raw, instruction.Opcode)
			}
		}
	}
	return nil
}

func nativeControlSymbol(instruction *asm.Inst, desc arch.Descriptor) (asm.Symbol, bool) {
	switch {
	case instruction.Opcode == "call", instruction.Opcode == "bl":
		if len(instruction.Operands) != 1 {
			return asm.Symbol{}, false
		}
	case strings.HasPrefix(instruction.Opcode, "j"),
		desc.Name() == "arm64" && isArm64Branch(instruction.Opcode):
		if len(instruction.Operands) == 0 {
			return asm.Symbol{}, false
		}
	default:
		return asm.Symbol{}, false
	}
	symbol, ok := instruction.Operands[len(instruction.Operands)-1].(asm.Symbol)
	return symbol, ok
}

type nativeEdge struct {
	to   int
	line int
}

// cycleBreakingCallLines removes only DFS back edges from the direct native
// call graph. Every directed cycle contains a back edge, so the remaining
// direct graph is acyclic while non-cycle edges retain their single native
// CALL/JMP instruction.
func cycleBreakingCallLines(funcs []*asm.Function) map[int]bool {
	byName := make(map[string]int, len(funcs))
	for index, function := range funcs {
		byName[function.Name] = index
	}

	adjacency := make([][]nativeEdge, len(funcs))
	edgeCount := 0
	for from, function := range funcs {
		for _, node := range function.Body {
			instruction, ok := node.(*asm.Inst)
			if !ok {
				continue
			}
			target, ok := directNativeEdge(instruction)
			if !ok {
				continue
			}
			to, ok := byName[target]
			if !ok {
				continue
			}
			adjacency[from] = append(adjacency[from], nativeEdge{to: to, line: instruction.Line})
			edgeCount++
		}
	}
	if edgeCount == 0 {
		return nil
	}

	const (
		unvisited = iota
		visiting
		visited
	)
	state := make([]uint8, len(funcs))
	indirect := make(map[int]bool)
	var visit func(int)
	visit = func(function int) {
		state[function] = visiting
		for _, edge := range adjacency[function] {
			switch state[edge.to] {
			case unvisited:
				visit(edge.to)
			case visiting:
				indirect[edge.line] = true
			}
		}
		state[function] = visited
	}
	for function := range funcs {
		if state[function] == unvisited {
			visit(function)
		}
	}
	if len(indirect) == 0 {
		return nil
	}
	return indirect
}

func directNativeEdge(instruction *asm.Inst) (string, bool) {
	switch instruction.Opcode {
	case "call", "bl", "b", "jmp":
	default:
		return "", false
	}
	if len(instruction.Operands) != 1 {
		return "", false
	}
	symbol, ok := instruction.Operands[0].(asm.Symbol)
	if !ok {
		return "", false
	}
	return nativeSymbolName(symbol.Name), true
}

func isArm64Branch(op string) bool {
	switch op {
	case "cbz", "cbnz", "tbz", "tbnz":
		return true
	}
	// bl/blr/br are calls and indirect jumps, not label branches.
	return strings.HasPrefix(op, "b") && op != "bic" && op != "bl" && op != "blr" && op != "br"
}

// arm64TouchesSP reports compiler instructions whose SP semantics must remain
// opaque to cmd/asm. Rendering any of them as Plan 9 makes the linker derive a
// Go stack delta for a native C frame and reject valid deep call graphs.
func arm64TouchesSP(inst *asm.Inst) bool {
	for _, operand := range inst.Operands {
		switch operand := operand.(type) {
		case asm.Register:
			if operand.Name == "SP" || operand.Name == "RSP" {
				return true
			}
		case asm.Memory:
			if operand.Base == "SP" || operand.Base == "RSP" ||
				operand.Index == "SP" || operand.Index == "RSP" {
				return true
			}
		}
	}
	return false
}

// Encode renders one instruction as a Plan 9 line.
func (e *Encoder) Encode(inst *asm.Inst, desc arch.Descriptor) (string, error) {
	// Batch fast path: machine code already assembled for this function.
	if opcodes, ok := e.batch[inst.Line]; ok {
		if desc.Name() == "amd64" {
			// x86asm.GoSyntax is not a stable input dialect for cmd/asm:
			// widths, signed displacements, x87 names, REP prefixes, and
			// temporary PUSH/POP sequences can all be rejected or reinterpreted.
			// Batch bytes already preserve the exact compiler instruction.
			return commentLine(rawBytesLine(opcodes, desc), inst), nil
		}
		if arm64TouchesSP(inst) {
			return commentLine(rawBytesLine(opcodes, desc), inst), nil
		}
		if decoded, ok := decodeToPlan9(opcodes, desc); ok {
			return commentLine(decoded, inst), nil
		}
		return commentLine(rawBytesLine(opcodes, desc), inst), nil
	}

	// ① Direct mnemonics: jumps (label operand) and no-operand
	// instructions with a fixed Plan 9 spelling.
	if line, ok, err := e.encodeDirect(inst); ok || err != nil {
		return line, err
	}

	// ②③ Assemble the raw source text and decode or byte-encode it.
	opcodes, err := assembleOne(inst.Raw, desc)
	if err != nil {
		return "", fmt.Errorf("encode line %d %q: %w", inst.Line, inst.Raw, err)
	}
	if desc.Name() == "amd64" {
		return commentLine(rawBytesLine(opcodes, desc), inst), nil
	}
	if arm64TouchesSP(inst) {
		return commentLine(rawBytesLine(opcodes, desc), inst), nil
	}
	if decoded, ok := decodeToPlan9(opcodes, desc); ok {
		return commentLine(decoded, inst), nil
	}
	return commentLine(rawBytesLine(opcodes, desc), inst), nil
}

// encodeDirect handles jumps, calls, and trivial no-operand
// instructions.
func (e *Encoder) encodeDirect(inst *asm.Inst) (string, bool, error) {
	if len(inst.Prefixes) != 0 {
		if symbol, ok := nativeControlSymbol(inst, e.desc); ok {
			if !e.isLocalLabel(symbol.Name) {
				return "", true, fmt.Errorf("encode line %d %q: prefixed non-local control transfer to %q is unsupported",
					inst.Line, inst.Raw, symbol.Name)
			}
			if symbol.Offset != 0 {
				return "", true, fmt.Errorf("encode line %d %q: prefixed local control target %q has unsupported offset %d",
					inst.Line, inst.Raw, symbol.Name, symbol.Offset)
			}
			return "", true, fmt.Errorf("encode line %d %q: prefixed local control transfer requires function batch encoding",
				inst.Line, inst.Raw)
		}
		// Prefixes are encoding-significant. Keep non-native instructions on
		// the assembler/decoder path instead of emitting the bare mnemonic.
		return "", false, nil
	}
	op := inst.Opcode
	switch op {
	case "nop":
		return "    NOP", true, nil
	case "ret":
		return "    RET", true, nil
	case "call", "bl":
		return e.encodeCall(inst)
	case "blr", "br":
		if len(inst.Operands) != 1 {
			return "", true, fmt.Errorf("encode line %d %q: %s with %d operands", inst.Line, inst.Raw, op, len(inst.Operands))
		}
		line, err := e.encodeIndirectCall(inst, inst.Operands[0], op == "br")
		if err != nil {
			return "", true, err
		}
		return line, true, nil
	}
	if strings.HasPrefix(op, "j") || (e.desc.Name() == "arm64" && isArm64Branch(op)) {
		if len(inst.Operands) == 0 {
			return "", true, fmt.Errorf("encode line %d %q: branch %s without operands", inst.Line, inst.Raw, op)
		}
		last := inst.Operands[len(inst.Operands)-1]
		label, ok := last.(asm.Symbol)
		if !ok {
			if op != "jmp" {
				return "", false, nil
			}
			line, err := e.encodeIndirectCall(inst, last, true)
			if err != nil {
				return "", true, err
			}
			return line, true, nil
		}
		if !e.isLocalLabel(label.Name) {
			if label.Offset != 0 {
				return "", true, fmt.Errorf("encode line %d %q: native branch target %q has unsupported offset %d",
					inst.Line, inst.Raw, label.Name, label.Offset)
			}
			if op != "b" && op != "jmp" {
				return "", true, fmt.Errorf("encode line %d %q: conditional native tail branch %q is unsupported",
					inst.Line, inst.Raw, op)
			}
			target, resolved := e.resolveNativeSymbol(label.Name)
			if !resolved {
				return "", true, fmt.Errorf("encode line %d %q: branch to unresolved symbol %q (module native symbol missing)", inst.Line, inst.Raw, label.Name)
			}
			return e.encodeTailBranch(inst, target), true, nil
		}
		mapped := jumpMnemonic(op, e.desc)
		if op == "tbz" || op == "tbnz" {
			// Go asm: TBZ $imm, Rt, label (GNU: reg, #imm, label);
			// only 64-bit registers are accepted.
			if len(inst.Operands) != 3 {
				return "", true, fmt.Errorf("encode line %d %q: %s with %d operands", inst.Line, inst.Raw, op, len(inst.Operands))
			}
			reg := renderOperand(inst.Operands[0])
			if reg == "WZR" {
				reg = "ZR"
			} else if len(reg) > 1 && reg[0] == 'W' && reg[1] >= '0' && reg[1] <= '9' {
				reg = "R" + reg[1:]
			}
			imm := renderOperand(inst.Operands[1])
			return "    " + mapped + " " + imm + ", " + reg + ", " + label.Name, true, nil
		}
		prefix := ""
		for _, opnd := range inst.Operands[:len(inst.Operands)-1] {
			if reg, ok := opnd.(asm.Register); ok {
				switch {
				case reg.Name == "WZR":
					opnd = asm.Register{Name: "ZR"}
				case len(reg.Name) > 1 && reg.Name[0] == 'W' && reg.Name[1] >= '0' && reg.Name[1] <= '9':
					opnd = asm.Register{Name: "R" + reg.Name[1:]}
				}
			}
			prefix += renderOperand(opnd) + ", "
		}
		return "    " + mapped + " " + prefix + label.Name, true, nil
	}
	return "", false, nil
}

// isLocalLabel reports whether a branch target is a rewritten
// function-local label rather than a module symbol or tail-call target.
func (e *Encoder) isLocalLabel(name string) bool {
	return e.labels[name]
}

// jumpMnemonic maps a lowercase jump mnemonic to its Plan 9 spelling.
func jumpMnemonic(op string, desc arch.Descriptor) string {
	if mnemonic, ok := desc.JumpMnemonic(op); ok {
		return mnemonic
	}
	// arm64 conditional branches decode to B.HS etc.; the Go assembler
	// wants BHS.
	return strings.ReplaceAll(strings.ToUpper(op), ".", "")
}

// encodeCall resolves a direct module target or delegates non-symbol operands
// to native C-ABI indirect-call lowering. No ABI0 slot window is synthesized.
func (e *Encoder) encodeCall(inst *asm.Inst) (string, bool, error) {
	if len(inst.Operands) != 1 {
		return "", true, fmt.Errorf("encode line %d %q: call with %d operands", inst.Line, inst.Raw, len(inst.Operands))
	}
	sym, ok := inst.Operands[0].(asm.Symbol)
	if !ok {
		line, err := e.encodeIndirectCall(inst, inst.Operands[0], false)
		return line, true, err
	}
	if sym.Offset != 0 {
		return "", true, fmt.Errorf("encode line %d %q: native call target %q has unsupported offset %d",
			inst.Line, inst.Raw, sym.Name, sym.Offset)
	}
	target, resolved := e.resolveNativeSymbol(sym.Name)
	if !resolved {
		return "", true, fmt.Errorf("encode line %d %q: call to unresolved symbol %q (module native symbol missing)", inst.Line, inst.Raw, sym.Name)
	}
	if e.indirectLines[inst.Line] {
		return e.encodeNativeCall(target), true, nil
	}
	return "    CALL " + target, true, nil
}

func (e *Encoder) encodeIndirectCall(inst *asm.Inst, target asm.Operand, tail bool) (string, error) {
	operand := renderCallOperand(target)
	if operand == "" {
		return "", fmt.Errorf("encode line %d %q: indirect call operand not supported: %T", inst.Line, inst.Raw, target)
	}
	branch := "CALL"
	if tail {
		branch = "JMP"
	}
	if register, ok := target.(asm.Register); ok {
		if e.desc.Name() == "arm64" {
			return fmt.Sprintf("    %s (%s)", branch, register.Name), nil
		}
		return fmt.Sprintf("    %s %s", branch, register.Name), nil
	}

	// Go asm indirect branches accept a register target. Load memory targets
	// before branching without allocating temporary stack slots.
	scratch := "R16"
	mov := "MOVD"
	if e.desc.Name() == "amd64" {
		scratch = "R11"
		mov = "MOVQ"
	}
	callTarget := scratch
	if e.desc.Name() == "arm64" {
		callTarget = "(" + scratch + ")"
	}
	return fmt.Sprintf("    %s %s, %s\n    %s %s", mov, operand, scratch, branch, callTarget), nil
}

// stripSymbolMods removes linker decorations (@PLT, @GOT, ...).
func stripSymbolMods(name string) string {
	if idx := strings.Index(name, "@"); idx >= 0 {
		return name[:idx]
	}
	return name
}

// sanitizeSymbolPrefix mirrors asm's label sanitization.
func sanitizeSymbolPrefix(name string) string {
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

// resolveNativeSymbol verifies a raw module target and returns its reserved
// Plan 9 native C-ABI body symbol. Companion-only wrappers are absent from
// this set and therefore fail fast here.
func (e *Encoder) resolveNativeSymbol(name string) (string, bool) {
	target := nativeSymbolName(name)
	if target == "" || !e.symbols[target] {
		return "", false
	}
	return "·" + asm.NativeName(target) + "(SB)", true
}

func nativeSymbolName(name string) string {
	return sanitizeSymbolPrefix(strings.TrimPrefix(stripSymbolMods(name), "_"))
}

// encodeNativeCall hides a direct module call behind a register so generated
// native bodies do not expose a NOSPLIT call edge. The address load does not
// change SP; amd64 uses R11 to preserve the variadic vector count in AL.
func (e *Encoder) encodeNativeCall(target string) string {
	if e.desc.Name() == "amd64" {
		return fmt.Sprintf("    MOVQ $%s, R11\n    CALL R11", target)
	}
	return fmt.Sprintf("    MOVD $%s, R16\n    CALL (R16)", target)
}

func (e *Encoder) encodeNativeTail(target string) string {
	if e.desc.Name() == "amd64" {
		return fmt.Sprintf("    MOVQ $%s, R11\n    JMP R11", target)
	}
	return fmt.Sprintf("    MOVD $%s, R16\n    JMP (R16)", target)
}

// encodeTailBranch emits a real tail branch to a native C body. It preserves
// condition operands for the uncommon conditional-symbol form while never
// synthesizing a CALL/RET pair.
func (e *Encoder) encodeTailBranch(inst *asm.Inst, target string) string {
	if e.indirectLines[inst.Line] && len(inst.Operands) == 1 {
		return e.encodeNativeTail(target)
	}
	prefix := ""
	for _, opnd := range inst.Operands[:len(inst.Operands)-1] {
		if reg, ok := opnd.(asm.Register); ok {
			switch {
			case reg.Name == "WZR":
				opnd = asm.Register{Name: "ZR"}
			case len(reg.Name) > 1 && reg.Name[0] == 'W' && reg.Name[1] >= '0' && reg.Name[1] <= '9':
				opnd = asm.Register{Name: "R" + reg.Name[1:]}
			}
		}
		prefix += renderOperand(opnd) + ", "
	}
	return "    " + jumpMnemonic(inst.Opcode, e.desc) + " " + prefix + target
}

// renderCallOperand renders a register or memory operand for a CALL
// target in Go asm syntax.
func renderCallOperand(op asm.Operand) string {
	switch o := op.(type) {
	case asm.Register:
		return "(" + o.Name + ")"
	case asm.Memory:
		if o.Symbol != "" {
			return ""
		}
		base := o.Base
		sbRelative := base == "SB"
		if sbRelative {
			base = ""
		}
		var result string
		if o.Disp != 0 {
			result = fmt.Sprintf("%d", o.Disp)
		}
		if base != "" {
			result += "(" + base + ")"
		}
		if sbRelative {
			result += "()"
		}
		if o.Index != "" {
			scale := o.Scale
			if scale == 0 {
				scale = 1
			}
			if scale != 1 && scale != 2 && scale != 4 && scale != 8 {
				return ""
			}
			if result == "" {
				result = "0"
			}
			result += fmt.Sprintf("(%s*%d)", o.Index, scale)
		}
		if result == "" {
			return "()"
		}
		return result
	default:
		return ""
	}
}

// renderOperand renders a register or immediate operand in Plan 9
// syntax (used for branch operands).
func renderOperand(op asm.Operand) string {
	switch o := op.(type) {
	case asm.Register:
		return o.Name
	case asm.Immediate:
		return fmt.Sprintf("$%d", o.Value)
	default:
		return "?"
	}
}

// commentLine appends the original instruction as a comment when it is
// not already represented by the emitted text.
func commentLine(line string, inst *asm.Inst) string {
	if strings.TrimSpace(inst.Raw) == "" {
		return line
	}
	return line + " // " + inst.Raw
}
