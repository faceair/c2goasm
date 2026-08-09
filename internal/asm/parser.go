package asm

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseError reports a parse fault with source context.
type ParseError struct {
	Line int
	Raw  string
	Msg  string
	Err  error
}

func (e *ParseError) Error() string {
	line := fmt.Sprintf("line %d", e.Line)
	if e.Raw != "" {
		line += fmt.Sprintf(" %q", e.Raw)
	}
	if e.Err != nil {
		return fmt.Sprintf("parse %s: %s: %v", line, e.Msg, e.Err)
	}
	return fmt.Sprintf("parse %s: %s", line, e.Msg)
}

func (e *ParseError) Unwrap() error { return e.Err }

var amd64InstructionPrefixes = map[string]bool{
	"addr16":  true,
	"addr32":  true,
	"bnd":     true,
	"cs":      true,
	"data16":  true,
	"ds":      true,
	"es":      true,
	"fs":      true,
	"gs":      true,
	"lock":    true,
	"notrack": true,
	"rep":     true,
	"repe":    true,
	"repne":   true,
	"repnz":   true,
	"repz":    true,
	"ss":      true,
}

// ParseLine parses one assembly line into an Inst.
// The line must not contain a leading label (use ParseSource for that).
func ParseLine(line string, arch string, lineno int) (*Inst, error) {
	tokens, comment, err := ScanLine(line)
	if err != nil {
		return nil, &ParseError{Line: lineno, Raw: line, Msg: "scan failed", Err: err}
	}
	if len(tokens) == 0 {
		return nil, nil // blank or comment-only
	}
	if tokens[0].Kind != TokenIdentifier {
		return nil, &ParseError{Line: lineno, Raw: line, Msg: fmt.Sprintf("expected opcode at %d but got %q", tokens[0].Pos, tokens[0].Value)}
	}
	opcodeIndex := 0
	var prefixes []string
	if arch == "amd64" {
		for opcodeIndex < len(tokens)-1 &&
			tokens[opcodeIndex].Kind == TokenIdentifier &&
			amd64InstructionPrefixes[strings.ToLower(tokens[opcodeIndex].Value)] {
			prefixes = append(prefixes, strings.ToLower(tokens[opcodeIndex].Value))
			opcodeIndex++
		}
	}
	if tokens[opcodeIndex].Kind != TokenIdentifier {
		return nil, &ParseError{Line: lineno, Raw: line, Msg: fmt.Sprintf("expected opcode at %d but got %q", tokens[opcodeIndex].Pos, tokens[opcodeIndex].Value)}
	}
	inst := &Inst{
		Opcode:   strings.ToLower(tokens[opcodeIndex].Value),
		Prefixes: prefixes,
		Comment:  comment,
		Line:     lineno,
		Raw:      line,
		Label:    "",
	}
	operands, err := parseOperands(tokens[opcodeIndex+1:], arch)
	if err != nil {
		return nil, &ParseError{Line: lineno, Raw: line, Msg: "operands", Err: err}
	}
	inst.Operands = operands
	return inst, nil
}

func parseOperands(tokens []Token, arch string) ([]Operand, error) {
	groups := splitOperands(tokens)
	// arm64: "x8, lsl #3" is one operand (register + shift modifier).
	// Merge shift groups into the preceding group.
	if arch == "arm64" {
		groups = mergeArm64Shifts(groups)
	}
	operands := make([]Operand, 0, len(groups))
	for _, group := range groups {
		if len(group) == 0 {
			return nil, fmt.Errorf("empty operand near comma")
		}
		op, err := parseOperand(group, arch)
		if err != nil {
			return nil, err
		}
		operands = append(operands, op)
	}
	return operands, nil
}

var arm64ShiftOps = map[string]bool{
	"lsl": true, "lsr": true, "asr": true, "ror": true,
	"uxtb": true, "uxth": true, "uxtw": true, "uxtx": true,
	"sxtb": true, "sxth": true, "sxtw": true, "sxtx": true,
}

func mergeArm64Shifts(groups [][]Token) [][]Token {
	if len(groups) < 2 {
		return groups
	}
	merged := make([][]Token, 0, len(groups))
	for i, g := range groups {
		if i > 0 && len(g) > 0 && g[0].Kind == TokenIdentifier && arm64ShiftOps[strings.ToLower(g[0].Value)] {
			prev := merged[len(merged)-1]
			merged[len(merged)-1] = append(prev, g...)
			continue
		}
		merged = append(merged, g)
	}
	return merged
}

func parseOperand(tokens []Token, arch string) (Operand, error) {
	tokens = stripSizeQualifiers(tokens)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty operand")
	}

	// GCC Intel syntax places the displacement outside the brackets
	// (-1[rdi] == [rdi-1]): fold the leading disp into the bracket
	// expression as "[<disp>, <rest>]".
	for i, tok := range tokens {
		if tok.Kind != TokenPunctuation || tok.Value != "[" {
			continue
		}
		if i == 0 {
			break // normal [base+...] form
		}
		inner := append([]Token{}, tokens[:i]...)
		inner = append(inner, Token{Kind: TokenPunctuation, Value: ","})
		inner = append(inner, tokens[i+1:len(tokens)-1]...)
		tokens = append([]Token{{Kind: TokenPunctuation, Value: "["}}, inner...)
		tokens = append(tokens, Token{Kind: TokenPunctuation, Value: "]"})
		break
	}

	if tokens[0].Kind == TokenPunctuation && tokens[0].Value == "[" {
		writeback := false
		if tokens[len(tokens)-1].Kind == TokenPunctuation && tokens[len(tokens)-1].Value == "!" {
			writeback = true
			tokens = tokens[:len(tokens)-1]
		}
		if tokens[len(tokens)-1].Kind != TokenPunctuation || tokens[len(tokens)-1].Value != "]" {
			return nil, fmt.Errorf("missing ] to close memory operand")
		}
		inner := tokens[1 : len(tokens)-1]
		switch arch {
		case "amd64":
			return parseAMD64Memory(inner)
		case "arm64":
			return parseARM64Memory(inner, writeback)
		default:
			return nil, fmt.Errorf("unknown architecture %q", arch)
		}
	}

	if imm, ok, err := parseImmediate(tokens); ok {
		return imm, err
	}

	if len(tokens) >= 1 && tokens[0].Kind == TokenIdentifier {
		name := tokens[0].Value
		if isRegister(name, arch) {
			return Register{Name: normalizeRegName(name, arch)}, nil
		}
		if len(tokens) == 1 {
			// Strip Mach-O modifiers that sit inside the token
			// (scanner keeps @ as an ident char): sym@PAGE etc.
			return Symbol{Name: stripMachOMod(name)}, nil
		}
		// Symbol with Mach-O page modifier and/or offset:
		// sym@PAGE, sym@PAGEOFF, sym@PAGE+20.
		idx := 1
		if idx < len(tokens) && tokens[idx].Kind == TokenIdentifier &&
			(tokens[idx].Value == "@PAGE" || tokens[idx].Value == "@PAGEOFF" || tokens[idx].Value == "@GOTPAGE" || tokens[idx].Value == "@GOTPAGEOFF") {
			idx++
		}
		var off int64
		if idx+1 < len(tokens) && tokens[idx].Kind == TokenPunctuation && tokens[idx].Value == "+" && tokens[idx+1].Kind == TokenNumber {
			v, err := parseNum(tokens[idx+1].Value)
			if err != nil {
				return nil, fmt.Errorf("symbol offset parse: %v", err)
			}
			off = v
			idx += 2
		}
		if idx != len(tokens) {
			return nil, fmt.Errorf("cannot parse operand: %v", tokens)
		}
		return Symbol{Name: stripMachOMod(name), Offset: off}, nil
	}

	// Immediate, register, or bracket-memory operands are handled above;
	// this branch is unreachable but keeps the structure explicit.
	return nil, fmt.Errorf("cannot parse operand: %v", tokens)
}

// stripMachOMod removes a trailing Mach-O modifier from a symbol token.
func stripMachOMod(name string) string {
	for _, mod := range []string{"@GOTPAGEOFF", "@GOTPAGE", "@PAGEOFF", "@PAGE", "@PLT"} {
		if strings.HasSuffix(name, mod) {
			return name[:len(name)-len(mod)]
		}
	}
	return name
}

func isRegister(name string, arch string) bool {
	switch arch {
	case "amd64":
		return isAMD64Register(name)
	case "arm64":
		return isARM64Register(name)
	default:
		return false
	}
}

// normalizeRegName canonicalizes a register name to the spelling the
// Go assembler accepts. arm64: X0..X30 become R0..R30, XZR becomes ZR.
// amd64: the classic registers drop the R prefix (RCX -> CX); R8-R15
// keep it.
func normalizeRegName(name string, arch string) string {
	upper := strings.ToUpper(name)
	if arch == "amd64" {
		switch upper {
		case "RAX":
			return "AX"
		case "RBX":
			return "BX"
		case "RCX":
			return "CX"
		case "RDX":
			return "DX"
		case "RSI":
			return "SI"
		case "RDI":
			return "DI"
		case "RBP":
			return "BP"
		case "RSP":
			return "SP"
		}
		return upper
	}
	if upper == "XZR" {
		return "ZR"
	}
	if len(upper) > 1 && upper[0] == 'X' && upper[1] >= '0' && upper[1] <= '9' {
		return "R" + upper[1:]
	}
	return upper
}

func splitOperands(tokens []Token) [][]Token {
	if len(tokens) == 0 {
		return nil
	}
	var groups [][]Token
	depth := 0
	start := 0
	for i, tok := range tokens {
		if tok.Kind != TokenPunctuation {
			continue
		}
		switch tok.Value {
		case "[":
			depth++
		case "]":
			if depth > 0 {
				depth--
			}
		case ",":
			if depth == 0 {
				groups = append(groups, tokens[start:i])
				start = i + 1
			}
		}
	}
	groups = append(groups, tokens[start:])
	return groups
}

func stripSizeQualifiers(tokens []Token) []Token {
	for len(tokens) > 0 {
		switch strings.ToLower(tokens[0].Value) {
		case "byte", "word", "dword", "qword", "ptr", "xmmword", "ymmword", "zmmword":
			tokens = tokens[1:]
		default:
			return tokens
		}
	}
	return tokens
}

// parseNum parses an integer literal, accepting the full 64-bit range
// (clang emits negative values as unsigned hex like 0xfffffffffffffffc).
func parseNum(val string) (int64, error) {
	if v, err := strconv.ParseInt(val, 0, 64); err == nil {
		return v, nil
	}
	u, err := strconv.ParseUint(val, 0, 64)
	if err != nil {
		return 0, err
	}
	return int64(u), nil
}

func parseImmediate(tokens []Token) (Immediate, bool, error) {
	if len(tokens) == 0 {
		return Immediate{}, false, nil
	}
	idx := 0
	if tokens[idx].Kind == TokenPunctuation && tokens[idx].Value == "#" {
		idx++
	}
	sign := int64(1)
	if idx < len(tokens) && tokens[idx].Kind == TokenPunctuation && tokens[idx].Value == "-" {
		sign = -1
		idx++
	}
	if idx >= len(tokens) || tokens[idx].Kind != TokenNumber {
		return Immediate{}, false, nil
	}
	// arm64 shift modifiers may trail the literal (#64672, lsl #16).
	if idx != len(tokens)-1 && !isShiftTail(tokens[idx+1:]) {
		return Immediate{}, false, fmt.Errorf("immediate parsing failed, extra tokens: %v", tokens[idx+1:])
	}
	val, err := parseNum(tokens[idx].Value)
	if err != nil {
		// Float literal (e.g. fmov v0.2d, #1.00000000): accept it;
		// the encoder works from the raw source text.
		if f, ferr := strconv.ParseFloat(tokens[idx].Value, 64); ferr == nil {
			return Immediate{Value: int64(sign) * int64(f)}, true, nil
		}
		return Immediate{}, true, err
	}
	return Immediate{Value: sign * val}, true, nil
}

// isShiftTail reports whether the token tail is an arm64 shift
// modifier (lsl #16, uxtw, etc.).
func isShiftTail(tokens []Token) bool {
	if len(tokens) == 0 || tokens[0].Kind != TokenIdentifier {
		return false
	}
	if !arm64ShiftOps[strings.ToLower(tokens[0].Value)] {
		return false
	}
	// Optionally followed by #N.
	if len(tokens) == 1 {
		return true
	}
	if len(tokens) == 3 && tokens[1].Kind == TokenPunctuation && tokens[1].Value == "#" && tokens[2].Kind == TokenNumber {
		return true
	}
	if len(tokens) == 2 && tokens[1].Kind == TokenNumber {
		return true
	}
	return false
}

func parseAMD64Memory(tokens []Token) (Memory, error) {
	mem := Memory{}
	sign := int64(1)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch tok.Kind {
		case TokenPunctuation:
			switch tok.Value {
			case "+":
				sign = 1
			case "-":
				sign = -1
			case "*":
				if mem.Index == "" {
					return mem, fmt.Errorf("missing index register near %d", tok.Pos)
				}
				if i+1 >= len(tokens) || tokens[i+1].Kind != TokenNumber {
					return mem, fmt.Errorf("missing scale near %d", tok.Pos)
				}
				scale, err := parseNum(tokens[i+1].Value)
				if err != nil {
					return mem, err
				}
				mem.Scale = int(scale)
				i++
			}
		case TokenIdentifier:
			val := tok.Value
			// Size qualifiers inside the brackets (QWORD PTR, etc.).
			switch strings.ToLower(val) {
			case "byte", "word", "dword", "qword", "tbyte", "ptr", "xmmword", "ymmword", "zmmword", "oword", "yword", "fword":
				continue
			}
			reg := normalizeRegName(val, "amd64")
			if !isAMD64Register(reg) {
				if mem.Symbol != "" {
					return mem, fmt.Errorf("multiple symbols in memory operand")
				}
				mem.Symbol = tok.Value
				sign = 1
				continue
			}
			if mem.Base == "" {
				if i+1 < len(tokens) && tokens[i+1].Kind == TokenPunctuation && tokens[i+1].Value == "*" {
					mem.Index = reg
					if mem.Scale == 0 {
						mem.Scale = 1
					}
				} else {
					mem.Base = reg
				}
			} else if mem.Index == "" {
				mem.Index = reg
				if mem.Scale == 0 {
					mem.Scale = 1
				}
			} else {
				return mem, fmt.Errorf("too many registers in memory operand")
			}
		case TokenNumber:
			if sign == 1 && i+2 < len(tokens) && tokens[i+1].Kind == TokenPunctuation && tokens[i+1].Value == "*" && tokens[i+2].Kind == TokenIdentifier {
				reg := strings.ToUpper(tokens[i+2].Value)
				if !isAMD64Register(reg) {
					return mem, fmt.Errorf("unknown register %q", tokens[i+2].Value)
				}
				if mem.Index != "" {
					return mem, fmt.Errorf("too many registers in memory operand")
				}
				scale, err := parseNum(tok.Value)
				if err != nil {
					return mem, err
				}
				mem.Index = reg
				mem.Scale = int(scale)
				if mem.Scale == 0 {
					mem.Scale = 1
				}
				i += 2
				sign = 1
				continue
			}
			val, err := parseNum(tok.Value)
			if err != nil {
				return mem, err
			}
			mem.Disp += sign * val
			sign = 1
		default:
			return mem, fmt.Errorf("unsupported token %q", tok.Value)
		}
	}
	if mem.Symbol != "" {
		if mem.Base == "" || strings.EqualFold(mem.Base, "RIP") {
			mem.Base = "SB"
		}
	}
	return mem, nil
}

func parseARM64Memory(tokens []Token, writeback bool) (Memory, error) {
	mem := Memory{Writeback: writeback}
	sign := int64(1)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch tok.Kind {
		case TokenPunctuation:
			switch tok.Value {
			case "+":
				sign = 1
			case "-":
				sign = -1
			case ",":
				// Separator inside [base, #offset].
				continue
			case "#":
				// Immediate marker inside [base, #offset].
				continue
			default:
				return mem, fmt.Errorf("unsupported punctuation %q in arm64 memory operand", tok.Value)
			}
		case TokenIdentifier:
			val := tok.Value
			// Mach-O modifiers: sym@GOTPAGEOFF / sym@PAGEOFF.
			if strings.HasSuffix(val, "@GOTPAGEOFF") {
				val = strings.TrimSuffix(val, "@GOTPAGEOFF")
			} else if strings.HasSuffix(val, "@PAGEOFF") {
				val = strings.TrimSuffix(val, "@PAGEOFF")
			}
			if !isARM64Register(val) {
				if mem.Symbol != "" {
					return mem, fmt.Errorf("multiple symbols in memory operand")
				}
				mem.Symbol = val
				continue
			}
			reg := normalizeRegName(val, "arm64")
			if mem.Base == "" {
				mem.Base = reg
			} else if mem.Index == "" {
				// Register-offset addressing: [base, index].
				mem.Index = reg
				mem.Scale = 1
			} else {
				return mem, fmt.Errorf("too many registers in arm64 memory operand")
			}
		case TokenNumber:
			val, err := parseNum(tok.Value)
			if err != nil {
				return mem, err
			}
			mem.Disp += sign * val
			sign = 1
		default:
			return mem, fmt.Errorf("unsupported token %q", tok.Value)
		}
	}
	return mem, nil
}
