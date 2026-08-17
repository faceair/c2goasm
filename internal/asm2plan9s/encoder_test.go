package asm2plan9s

import (
	"strings"
	"testing"

	"github.com/faceair/c2goasm/arch"
	"github.com/faceair/c2goasm/internal/asm"

	"golang.org/x/arch/arm64/arm64asm"
)

func encodeLine(t *testing.T, line, target string) string {
	t.Helper()
	desc, err := arch.Resolve(target)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := asm.ParseLine(line, target, 1)
	if err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	enc := NewEncoder(desc)
	// Local-label set covers any .Lxxx references in the line.
	var labels []string
	for _, op := range inst.Operands {
		if s, ok := op.(asm.Symbol); ok && strings.Contains(s.Name, "LBB") {
			labels = append(labels, s.Name)
		}
	}
	if err := enc.BeginProgram(nil, labels, desc); err != nil {
		t.Fatal(err)
	}
	fn := &asm.Function{Name: "foo"}
	_ = enc.BeginFunction(fn, desc)
	out, err := enc.Encode(inst, desc)
	if err != nil {
		t.Fatalf("encode %q: %v", line, err)
	}
	return out
}

func TestEncodeDirectMnemonics(t *testing.T) {
	for _, tc := range []struct {
		target, line, want string
	}{
		{"arm64", "nop", "NOP"},
		{"amd64", "ret", "RET"},
		{"amd64", "je .LBB0_1", "JEQ"},
		{"arm64", "cbnz wzr, .LBB0_4", "CBNZ ZR"},
		{"arm64", "cbz x2, .LBB0_3", "CBZ"},
		{"arm64", "mov w19, w2", "MOVWU R2, R19"},
	} {
		out := encodeLine(t, tc.line, tc.target)
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s %q: %q missing %q", tc.target, tc.line, out, tc.want)
		}
	}
}

func assertNoCallMarshalling(t *testing.T, out string) {
	t.Helper()
	for _, forbidden := range []string{"SUB", "ADD", "0(SP)", "8(SP)", "0(RSP)", "8(RSP)"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("native call unexpectedly contains %q: %q", forbidden, out)
		}
	}
}

func TestEncodeDirectCallsUseNativeABI(t *testing.T) {
	tests := []struct {
		target string
		opcode string
		want   string
	}{
		{target: "arm64", opcode: "bl", want: "CALL ·_c2goasm_native_strlen(SB)"},
		{target: "amd64", opcode: "call", want: "CALL ·_c2goasm_native_strlen(SB)"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			desc, err := arch.Resolve(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			enc := NewEncoder(desc)
			if err := enc.BeginProgram([]string{"strlen"}, nil, desc); err != nil {
				t.Fatal(err)
			}
			inst := &asm.Inst{Opcode: tc.opcode, Operands: []asm.Operand{asm.Symbol{Name: "strlen"}}, Line: 1}
			out, err := enc.Encode(inst, desc)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.want) || strings.Contains(out, "R16") || strings.Contains(out, "R11") {
				t.Fatalf("direct %s call = %q", tc.target, out)
			}
			assertNoCallMarshalling(t, out)
		})
	}
}

func TestCycleBreakingNativeEdgesUseIndirection(t *testing.T) {
	call := func(opcode, target string, line int) *asm.Inst {
		return &asm.Inst{
			Opcode:   opcode,
			Operands: []asm.Operand{asm.Symbol{Name: target}},
			Line:     line,
		}
	}
	funcs := []*asm.Function{
		{Name: "foo", Body: []asm.Node{call("bl", "bar", 11), call("b", "bar", 12)}},
		{Name: "bar", Body: []asm.Node{call("bl", "foo", 13)}},
		{Name: "caller", Body: []asm.Node{call("bl", "foo", 14)}},
		{Name: "self", Body: []asm.Node{call("bl", "self", 15)}},
	}
	indirect := cycleBreakingCallLines(funcs)
	for _, line := range []int{13, 15} {
		if !indirect[line] {
			t.Errorf("DFS back edge at line %d was not selected for indirection", line)
		}
	}
	for _, line := range []int{11, 12, 14} {
		if indirect[line] {
			t.Errorf("non-back edge at line %d was selected for indirection", line)
		}
	}

	tests := []struct {
		target    string
		opcode    string
		line      int
		want      string
		forbidden string
	}{
		{target: "arm64", opcode: "bl", line: 13, want: "MOVD $·_c2goasm_native_foo(SB), R16\n    CALL (R16)", forbidden: "CALL ·"},
		{target: "amd64", opcode: "call", line: 13, want: "MOVQ $·_c2goasm_native_foo(SB), R11\n    CALL R11", forbidden: "CALL ·"},
	}
	for _, test := range tests {
		t.Run(test.target, func(t *testing.T) {
			desc, err := arch.Resolve(test.target)
			if err != nil {
				t.Fatal(err)
			}
			encoder := NewEncoder(desc)
			if err := encoder.BeginProgram([]string{"foo"}, nil, desc); err != nil {
				t.Fatal(err)
			}
			encoder.indirectLines = indirect
			out, err := encoder.Encode(call(test.opcode, "foo", test.line), desc)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, test.want) || strings.Contains(out, test.forbidden) {
				t.Fatalf("recursive %s call = %q", test.target, out)
			}
			assertNoCallMarshalling(t, out)
		})
	}
}

func TestProgramBatchModelsCycleLoweringGeometry(t *testing.T) {
	desc := arch.ARM64()
	address, err := asm.ParseLine("adr x0, Lafter", "arm64", 21)
	if err != nil {
		t.Fatal(err)
	}
	call, err := asm.ParseLine("bl self", "arm64", 22)
	if err != nil {
		t.Fatal(err)
	}
	function := &asm.Function{
		Name: "self",
		Body: []asm.Node{address, call, &asm.LabelLine{Name: "Lafter"}},
	}
	encoder := NewEncoder(desc)
	if err := encoder.BeginProgram([]string{"self"}, []string{"Lafter"}, desc); err != nil {
		t.Fatal(err)
	}
	if err := encoder.BeginFunctions([]*asm.Function{function}, desc); err != nil {
		t.Fatal(err)
	}
	expected, err := assembleText(".text\nadr x0, Lafter\nnop\nnop\nnop\nLafter:\n", desc)
	if err != nil {
		t.Fatal(err)
	}
	if got := encoder.programBatch[21]; string(got) != string(expected[:4]) {
		t.Fatalf("cycle-spanning ADR bytes = %x, want %x", got, expected[:4])
	}
}

func TestCycleLoweringAMD64UsesImm64CallGeometry(t *testing.T) {
	instruction, err := asm.ParseLine("call self", "amd64", 22)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := batchInstructionLines(instruction, arch.AMD64(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 13 {
		t.Fatalf("amd64 cycle geometry has %d bytes, want 13", len(lines))
	}
}

func TestBatchGeometryModelsLoweredIndirectMemoryCall(t *testing.T) {
	instruction := &asm.Inst{
		Opcode:   "call",
		Operands: []asm.Operand{asm.Memory{Base: "BX", Index: "CX", Scale: 4, Disp: 8}},
		Raw:      "call qword ptr [rbx+rcx*4+8]",
		Line:     23,
	}
	lines, err := batchInstructionLines(instruction, arch.AMD64(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].text != "mov r11, qword ptr [rbx+rcx*4+8]" || lines[1].text != "call r11" {
		t.Fatalf("indirect-call geometry = %#v", lines)
	}
}

func TestAMD64SyntheticGeometryDoesNotRequireLiftedDataLabel(t *testing.T) {
	lines, err := syntheticBatchLines(&asm.SyntheticInst{
		Raw:  "movsd xmm0, QWORD PTR .LC74[rip]",
		Line: 24,
	}, arch.AMD64())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || strings.Contains(lines[0], ".LC74") ||
		!strings.Contains(lines[0], "c2goasm_geometry_symbol[rip]") {
		t.Fatalf("synthetic geometry = %#v", lines)
	}
}

func TestInstLenTreatsEndbrAsOneInstruction(t *testing.T) {
	length, err := instLen([]byte{0xf3, 0x0f, 0x1e, 0xfa, 0x48, 0x85, 0xd2}, arch.AMD64())
	if err != nil {
		t.Fatal(err)
	}
	if length != 4 {
		t.Fatalf("ENDBR64 length = %d, want 4", length)
	}
}

func TestBeginFunctionsRejectsAMD64ReservedScratch(t *testing.T) {
	tests := []struct {
		name string
		node asm.Node
	}{
		{name: "full", node: &asm.Inst{Raw: "mov r11, rax", Operands: []asm.Operand{asm.Register{Name: "R11"}}, Line: 27}},
		{name: "subregister", node: &asm.Inst{Raw: "mov r11d, eax", Operands: []asm.Operand{asm.Register{Name: "R11D"}}, Line: 27}},
		{name: "rewritten", node: &asm.SyntheticInst{Text: "    LEAQ symbol(SB), R11W", Line: 27}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			function := &asm.Function{Name: "bad", Body: []asm.Node{
				test.node,
				&asm.Inst{
					Opcode:   "call",
					Operands: []asm.Operand{asm.Symbol{Name: "bar"}},
					Line:     28,
				},
			}}
			err := NewEncoder(arch.AMD64()).BeginFunctions([]*asm.Function{function}, arch.AMD64())
			if err == nil || !strings.Contains(err.Error(), "-ffixed-r11") ||
				!strings.Contains(err.Error(), "function bad line 27") {
				t.Fatalf("reserved scratch error = %v", err)
			}
		})
	}
	if textUsesReservedR11("MOVQ R110, AX") {
		t.Error("R110 was mistaken for the reserved R11 register")
	}
}

func TestBeginFunctionsAllowsR11InDirectOnlyLeaf(t *testing.T) {
	function := &asm.Function{
		Name: "leaf",
		Body: []asm.Node{
			&asm.Inst{
				Raw:      "mov r11, rax",
				Operands: []asm.Operand{asm.Register{Name: "R11"}, asm.Register{Name: "RAX"}},
				Line:     27,
			},
			&asm.Inst{
				Opcode:   "jmp",
				Operands: []asm.Operand{asm.Symbol{Name: "leaf_local"}},
				Line:     28,
			},
			&asm.LabelLine{Name: "leaf_local"},
			&asm.Inst{Opcode: "ret", Raw: "ret", Line: 29},
		},
	}
	if err := validateReservedScratch([]*asm.Function{function}, arch.AMD64()); err != nil {
		t.Fatalf("direct-only leaf rejected: %v", err)
	}
}

func TestBeginFunctionsRejectsR11WithMemoryIndirectCall(t *testing.T) {
	function := &asm.Function{
		Name: "caller",
		Body: []asm.Node{
			&asm.Inst{
				Raw:      "mov r11, rax",
				Operands: []asm.Operand{asm.Register{Name: "R11"}, asm.Register{Name: "RAX"}},
				Line:     27,
			},
			&asm.Inst{
				Opcode:   "call",
				Operands: []asm.Operand{asm.Memory{Base: "R12"}},
				Line:     28,
			},
		},
	}
	if err := validateReservedScratch([]*asm.Function{function}, arch.AMD64()); err == nil ||
		!strings.Contains(err.Error(), "-ffixed-r11") {
		t.Fatalf("memory-indirect caller error = %v", err)
	}
}

func TestBeginFunctionsRejectsUnsupportedNativeControlFlow(t *testing.T) {
	tests := []struct {
		name string
		inst *asm.Inst
		want string
	}{
		{name: "prefix", inst: &asm.Inst{Opcode: "call", Prefixes: []string{"notrack"}, Operands: []asm.Operand{asm.Symbol{Name: "bar"}}, Line: 31}, want: "prefixed native"},
		{name: "conditional", inst: &asm.Inst{Opcode: "jne", Operands: []asm.Operand{asm.Symbol{Name: "bar"}}, Line: 32}, want: "conditional native"},
		{name: "offset", inst: &asm.Inst{Opcode: "call", Operands: []asm.Operand{asm.Symbol{Name: "bar", Offset: 4}}, Line: 33}, want: "unsupported offset 4"},
		{name: "unresolved-prefix", inst: &asm.Inst{Opcode: "call", Prefixes: []string{"bnd"}, Operands: []asm.Operand{asm.Symbol{Name: "missing"}}, Line: 34}, want: "prefixed native/non-local"},
		{name: "unresolved", inst: &asm.Inst{Opcode: "call", Operands: []asm.Operand{asm.Symbol{Name: "missing"}}, Line: 35}, want: "unresolved native control"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoder := NewEncoder(arch.AMD64())
			if err := encoder.BeginProgram([]string{"foo", "bar"}, nil, arch.AMD64()); err != nil {
				t.Fatal(err)
			}
			function := &asm.Function{Name: "foo", Body: []asm.Node{test.inst}}
			target := &asm.Function{Name: "bar"}
			err := encoder.BeginFunctions([]*asm.Function{function, target}, arch.AMD64())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("native control error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEncodeRejectsPrefixedUnresolvedControlSymbol(t *testing.T) {
	desc := arch.AMD64()
	encoder := NewEncoder(desc)
	if err := encoder.BeginProgram(nil, nil, desc); err != nil {
		t.Fatal(err)
	}
	instruction := &asm.Inst{
		Opcode:   "call",
		Prefixes: []string{"notrack"},
		Operands: []asm.Operand{asm.Symbol{Name: "missing"}},
		Raw:      "notrack call missing",
		Line:     41,
	}
	_, err := encoder.Encode(instruction, desc)
	if err == nil || !strings.Contains(err.Error(), "prefixed non-local") ||
		!strings.Contains(err.Error(), "missing") {
		t.Fatalf("prefixed unresolved control error = %v", err)
	}
}

func TestEncodeRequiresBatchForPrefixedLocalControl(t *testing.T) {
	desc := arch.AMD64()
	encoder := NewEncoder(desc)
	if err := encoder.BeginProgram(nil, []string{"local"}, desc); err != nil {
		t.Fatal(err)
	}
	instruction := &asm.Inst{
		Opcode:   "jmp",
		Prefixes: []string{"data16"},
		Operands: []asm.Operand{asm.Symbol{Name: "local"}},
		Raw:      "data16 jmp local",
		Line:     42,
	}
	if directlyMapped(instruction, desc) {
		t.Fatal("prefixed local branch was discarded as a direct placeholder")
	}
	_, err := encoder.Encode(instruction, desc)
	if err == nil || !strings.Contains(err.Error(), "requires function batch") {
		t.Fatalf("prefixed local batch error = %v", err)
	}
}

func TestEncodeBranchAndTailCall(t *testing.T) {
	tests := []struct {
		target string
		opcode string
		want   string
	}{
		{target: "arm64", opcode: "b", want: "B ·_c2goasm_native_realloc(SB)"},
		{target: "amd64", opcode: "jmp", want: "JMP ·_c2goasm_native_realloc(SB)"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			desc, err := arch.Resolve(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			enc := NewEncoder(desc)
			if err := enc.BeginProgram([]string{"realloc"}, []string{"foo_LBB0_1"}, desc); err != nil {
				t.Fatal(err)
			}
			// Local label branches remain ordinary control flow.
			local := &asm.Inst{Opcode: map[string]string{"arm64": "b", "amd64": "jmp"}[tc.target], Operands: []asm.Operand{asm.Symbol{Name: "foo_LBB0_1"}}, Line: 1}
			out, err := enc.Encode(local, desc)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "foo_LBB0_1") || strings.Contains(out, "c2goasm_native") {
				t.Fatalf("local branch: %q", out)
			}
			tail := &asm.Inst{Opcode: tc.opcode, Operands: []asm.Operand{asm.Symbol{Name: "realloc"}}, Line: 2}
			out, err = enc.Encode(tail, desc)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.want) || strings.Contains(out, "CALL") || strings.Contains(out, "RET") {
				t.Fatalf("direct tail %s: %q", tc.target, out)
			}
			assertNoCallMarshalling(t, out)
		})
	}
}

func TestEncodeIndirectCallsAndTails(t *testing.T) {
	tests := []struct {
		name     string
		desc     arch.Descriptor
		call     *asm.Inst
		tail     *asm.Inst
		callWant string
		tailWant string
		memory   bool
	}{
		{
			name:     "arm64-register",
			desc:     arch.ARM64(),
			call:     &asm.Inst{Opcode: "blr", Operands: []asm.Operand{asm.Register{Name: "R9"}}, Line: 1},
			tail:     &asm.Inst{Opcode: "br", Operands: []asm.Operand{asm.Register{Name: "R2"}}, Line: 2},
			callWant: "CALL (R9)",
			tailWant: "JMP (R2)",
		},
		{
			name:     "amd64-register",
			desc:     arch.AMD64(),
			call:     &asm.Inst{Opcode: "call", Operands: []asm.Operand{asm.Register{Name: "R10"}}, Line: 3},
			tail:     &asm.Inst{Opcode: "jmp", Operands: []asm.Operand{asm.Register{Name: "AX"}}, Line: 4},
			callWant: "CALL R10",
			tailWant: "JMP AX",
		},
		{
			name:     "arm64-memory",
			desc:     arch.ARM64(),
			call:     &asm.Inst{Opcode: "bl", Operands: []asm.Operand{asm.Memory{Base: "R12"}}, Line: 5},
			tail:     &asm.Inst{Opcode: "jmp", Operands: []asm.Operand{asm.Memory{Base: "R13"}}, Line: 6},
			callWant: "MOVD (R12), R16\n    CALL (R16)",
			tailWant: "MOVD (R13), R16\n    JMP (R16)",
			memory:   true,
		},
		{
			name:     "amd64-memory",
			desc:     arch.AMD64(),
			call:     &asm.Inst{Opcode: "call", Operands: []asm.Operand{asm.Memory{Base: "R12"}}, Line: 7},
			tail:     &asm.Inst{Opcode: "jmp", Operands: []asm.Operand{asm.Memory{Base: "R13"}}, Line: 8},
			callWant: "MOVQ (R12), R11\n    CALL R11",
			tailWant: "MOVQ (R13), R11\n    JMP R11",
			memory:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enc := NewEncoder(tc.desc)
			if err := enc.BeginProgram(nil, nil, tc.desc); err != nil {
				t.Fatal(err)
			}
			out, err := enc.Encode(tc.call, tc.desc)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.callWant) || strings.Contains(out, "RET") {
				t.Fatalf("indirect call: %q", out)
			}
			assertNoCallMarshalling(t, out)
			out, err = enc.Encode(tc.tail, tc.desc)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.tailWant) || strings.Contains(out, "CALL") || strings.Contains(out, "RET") {
				t.Fatalf("indirect tail: %q", out)
			}
			assertNoCallMarshalling(t, out)
			if tc.memory && strings.Contains(out, "SP") {
				t.Fatalf("memory target unexpectedly depends on SP: %q", out)
			}
		})
	}
}

func TestUnsupportedSyntaxFilters(t *testing.T) {
	cases := []struct {
		target, syntax string
		unsupported    bool
	}{
		{"amd64", "MOVZX AX, BX", true},
		{"amd64", "MOVDQU (DI), X0", true},
		{"amd64", "SETE AL", true},
		{"amd64", "CMOVE AX, BX", true},
		{"amd64", "TESTL AL, AL", true},
		{"amd64", "CMPL DL, $61", true},
		{"amd64", "TZCNT DI, R10", true},
		{"amd64", "SHLDQ CL, AX, DX", true},
		{"amd64", "CVTSI2SDQ AX, X0", true},
		{"amd64", "CVTSI2SDL AX, X0", true},
		{"amd64", "CVTTSD2SIL X0, AX", true},
		{"amd64", "CVTTSD2SIQ X0, AX", true},
		{"amd64", "MOVSD_XMM X0, X1", true},
		{"amd64", "CMPSD_XMM $1, X0, X1", true},
		{"amd64", "BSWAP AX", true},
		{"amd64", "FLD (SP)", true},
		{"amd64", "FSTP (SP)", true},
		{"amd64", "DS", true},
		{"amd64", "MOVQ AX, BX", false},
		{"amd64", "ADDQ $16, DI", false},
		{"arm64", "LDPSW 40(RSP), R8, R20", true},
		{"arm64", "ADD R1, R0, R0", false},
		{"arm64", "MOVD ZR, R8", false},
	}
	for _, tc := range cases {
		var got bool
		if tc.target == "amd64" {
			got = unsupportedAMD64Syntax(tc.syntax)
		} else {
			got = unsupportedARM64Syntax(tc.syntax)
		}
		if got != tc.unsupported {
			t.Errorf("%s %q: unsupported=%v, want %v", tc.target, tc.syntax, got, tc.unsupported)
		}
	}
}

func TestUnsupportedARM64Instructions(t *testing.T) {
	for _, test := range []struct {
		op          arm64asm.Op
		unsupported bool
	}{
		{op: arm64asm.ADDP, unsupported: true},
		{op: arm64asm.MOVI, unsupported: true},
		{op: arm64asm.ADD, unsupported: false},
	} {
		if got := unsupportedARM64(arm64asm.Inst{Op: test.op}); got != test.unsupported {
			t.Errorf("%s: unsupported=%v, want %v", test.op, got, test.unsupported)
		}
	}
}

func TestStripARM64InlineComment(t *testing.T) {
	if got := stripArm64InlineComment(" add x0, x0, #1 ; annotation"); got != "add x0, x0, #1" {
		t.Fatalf("commented instruction = %q", got)
	}
	if got := stripArm64InlineComment("  ret  "); got != "ret" {
		t.Fatalf("plain instruction = %q", got)
	}
}

func TestSanitizeSymbolPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"re_string_find2.cold.1", "re_string_find2_cold_1"},
		{"simple", "simple"},
		{"123abc", "L123abc"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := sanitizeSymbolPrefix(tc.in); got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripSymbolMods(t *testing.T) {
	cases := []struct{ in, want string }{
		{"realloc@PLT", "realloc"},
		{"foo@GOT", "foo"},
		{"plain", "plain"},
	}
	for _, tc := range cases {
		if got := stripSymbolMods(tc.in); got != tc.want {
			t.Errorf("stripSymbolMods(%q) = %q", tc.in, got)
		}
	}
}

func TestNormalizeGCCIntel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mov rax, QWORD PTR 32[rcx]", "mov rax, QWORD PTR [rcx+32]"},
		{"mov cl, BYTE PTR -1[rdi]", "mov cl, BYTE PTR [rdi-1]"},
		{"add rax, rbx", "add rax, rbx"},
		{"movsx rsi, esi", "movsxd rsi, esi"},
		{"movsx r8, DWORD PTR [rax]", "movsxd r8, DWORD PTR [rax]"},
		{"movsx rax, WORD PTR [rax]", "movsx rax, WORD PTR [rax]"},
	}
	for _, tc := range cases {
		if got := normalizeGCCIntel(tc.in); got != tc.want {
			t.Errorf("normalizeGCCIntel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEncodeARM64StackOperandsAsOpaqueWords(t *testing.T) {
	tests := []struct {
		line    string
		opcodes []byte
		want    string
	}{
		{line: "stp x29, x30, [sp, #-16]!", opcodes: []byte{0xfd, 0x7b, 0xbf, 0xa9}, want: "WORD $0xa9bf7bfd"},
		{line: "sub sp, sp, #16", opcodes: []byte{0xff, 0x43, 0x00, 0xd1}, want: "WORD $0xd10043ff"},
		{line: "ldr x0, [sp, #8]", opcodes: []byte{0xe0, 0x07, 0x40, 0xf9}, want: "WORD $0xf94007e0"},
	}
	for i, test := range tests {
		inst, err := asm.ParseLine(test.line, "arm64", i+1)
		if err != nil {
			t.Fatal(err)
		}
		enc := NewEncoder(arch.ARM64())
		enc.batch = map[int][]byte{inst.Line: test.opcodes}
		out, err := enc.Encode(inst, arch.ARM64())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, test.want) {
			t.Errorf("%q = %q, want opaque %q", test.line, out, test.want)
		}
	}
}

func TestRawBytesLine(t *testing.T) {
	amd := rawBytesLine([]byte{0x78, 0x56, 0x34, 0x12}, arch.AMD64())
	if !strings.Contains(amd, "LONG $0x12345678") {
		t.Errorf("amd64 bytes: %q", amd)
	}
	arm := rawBytesLine([]byte{0x40, 0x00, 0x00, 0x37}, arch.ARM64())
	if !strings.Contains(arm, "WORD $0x37000040") {
		t.Errorf("arm64 bytes: %q", arm)
	}
}

func TestEncodeAMD64RawBytePath(t *testing.T) {
	desc := arch.AMD64()
	enc := NewEncoder(desc)
	if err := enc.BeginProgram(nil, nil, desc); err != nil {
		t.Fatal(err)
	}
	inst, err := asm.ParseLine("add rax, rbx", "amd64", 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.BeginFunction(&asm.Function{Name: "syntax"}, desc); err != nil {
		t.Fatal(err)
	}
	out, err := enc.Encode(inst, desc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "WORD $0x0148") || !strings.Contains(out, "BYTE $0xd8") ||
		!strings.Contains(out, "add rax, rbx") {
		t.Fatalf("amd64 raw-byte output = %q", out)
	}
}

func TestEncodeCallBlacklistError(t *testing.T) {
	desc := arch.AMD64()
	enc := NewEncoder(desc)
	_ = enc.BeginProgram(nil, nil, desc)
	inst := &asm.Inst{Opcode: "call", Raw: "call missing@PLT", Line: 11, Operands: []asm.Operand{asm.Symbol{Name: "missing@PLT"}}}
	if _, err := enc.Encode(inst, desc); err == nil || !strings.Contains(err.Error(), "unresolved symbol") {
		t.Fatalf("unresolved call error = %v", err)
	}
	bad := &asm.Inst{Opcode: "call", Raw: "call", Line: 12}
	if _, err := enc.Encode(bad, desc); err == nil || !strings.Contains(err.Error(), "with 0 operands") {
		t.Fatalf("bad call error = %v", err)
	}
}

func TestRawBytesLineAllLiteralWidths(t *testing.T) {
	got := rawBytesLine([]byte{1, 2, 3, 4, 5, 6}, arch.AMD64())
	if !strings.Contains(got, "LONG $0x04030201") || !strings.Contains(got, "WORD $0x0605") {
		t.Fatalf("amd64 mixed literals = %q", got)
	}
	got = rawBytesLine([]byte{0xab}, arch.ARM64())
	if !strings.Contains(got, "BYTE $0xab") {
		t.Fatalf("arm64 byte literal = %q", got)
	}
}

func TestBeginFunctionBatchPath(t *testing.T) {
	desc := arch.AMD64()
	enc := NewEncoder(desc)
	_ = enc.BeginProgram(nil, nil, desc)
	inst := &asm.Inst{Opcode: "add", Raw: "add rax, rbx", Line: 21}
	fn := &asm.Function{Name: "batched", Body: []asm.Node{inst}}
	if err := enc.BeginFunction(fn, desc); err != nil {
		t.Fatal(err)
	}
	if len(enc.batch) == 0 {
		t.Fatal("batch encoding was not populated")
	}
	out, err := enc.Encode(inst, desc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "WORD $0x0148") || !strings.Contains(out, "BYTE $0xd8") {
		t.Fatalf("batch output = %q", out)
	}
}

func TestEncodeRawByteFallback(t *testing.T) {
	desc := arch.AMD64()
	enc := NewEncoder(desc)
	_ = enc.BeginProgram(nil, nil, desc)
	inst, err := asm.ParseLine("movdqu xmm0, xmmword ptr [rdi]", "amd64", 31)
	if err != nil {
		t.Fatal(err)
	}
	out, err := enc.Encode(inst, desc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "LONG $") && !strings.Contains(out, "BYTE $") && !strings.Contains(out, "WORD $") {
		t.Fatalf("raw byte fallback output = %q", out)
	}
}

func TestBeginFunctionsProgramBatchNamespacesLabels(t *testing.T) {
	desc := arch.ARM64()
	firstAdd, err := asm.ParseLine("add x0, x0, #1", "arm64", 101)
	if err != nil {
		t.Fatal(err)
	}
	firstBranch, err := asm.ParseLine("b LBB0_1", "arm64", 102)
	if err != nil {
		t.Fatal(err)
	}
	secondSub, err := asm.ParseLine("sub x1, x1, #1", "arm64", 201)
	if err != nil {
		t.Fatal(err)
	}
	secondBranch, err := asm.ParseLine("b LBB0_1", "arm64", 202)
	if err != nil {
		t.Fatal(err)
	}
	funcs := []*asm.Function{
		{
			Name: "first",
			Body: []asm.Node{
				&asm.LabelLine{Name: "LBB0_1"},
				firstAdd,
				firstBranch,
			},
		},
		{
			Name: "second",
			Body: []asm.Node{
				&asm.LabelLine{Name: "LBB0_1"},
				secondSub,
				secondBranch,
			},
		},
	}
	if err := asm.RewriteLabelsGlobal(funcs); err != nil {
		t.Fatal(err)
	}

	enc := NewEncoder(desc)
	if err := enc.BeginFunctions(funcs, desc); err != nil {
		t.Fatal(err)
	}
	if len(enc.programBatch[101]) != 4 || len(enc.programBatch[201]) != 4 {
		t.Fatalf("program batch missing function instructions: %#v", enc.programBatch)
	}
	for _, test := range []struct {
		fn   *asm.Function
		inst *asm.Inst
		want string
	}{
		{fn: funcs[0], inst: firstAdd, want: "ADD"},
		{fn: funcs[1], inst: secondSub, want: "SUB"},
	} {
		if err := enc.BeginFunction(test.fn, desc); err != nil {
			t.Fatal(err)
		}
		got, err := enc.Encode(test.inst, desc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, test.want) {
			t.Fatalf("%s batch output = %q", test.fn.Name, got)
		}
	}
}

func TestBeginFunctionsBatchesRewrittenPCRelativeLabel(t *testing.T) {
	desc := arch.ARM64()
	address, err := asm.ParseLine("adr x0, .Ltarget", "arm64", 251)
	if err != nil {
		t.Fatal(err)
	}
	nop, err := asm.ParseLine("nop", "arm64", 252)
	if err != nil {
		t.Fatal(err)
	}
	fn := &asm.Function{
		Name: "owner",
		Body: []asm.Node{
			address,
			nop,
			&asm.SyntheticInst{Text: "    MOVD $·data(SB), R1", Line: 253},
			&asm.LabelLine{Name: ".Ltarget"},
		},
	}
	if err := asm.RewriteLabelsGlobal([]*asm.Function{fn}); err != nil {
		t.Fatal(err)
	}
	enc := NewEncoder(desc)
	if err := enc.BeginFunctions([]*asm.Function{fn}, desc); err != nil {
		t.Fatal(err)
	}
	if len(enc.programBatch[251]) != 4 {
		t.Fatalf("rewritten PC-relative instruction missed program batch: raw=%q batch=%#v",
			address.Raw, enc.programBatch)
	}
	expected, err := assembleText(".text\nadr x0, Ltarget\nnop\nnop\nnop\nLtarget:\n", desc)
	if err != nil {
		t.Fatal(err)
	}
	if got := enc.programBatch[251]; string(got) != string(expected[:4]) {
		t.Fatalf("PC-relative bytes = %x, want %x for final synthetic geometry", got, expected[:4])
	}
}

func TestBeginFunctionsAMD64UsesProgramBatch(t *testing.T) {
	desc := arch.AMD64()
	add, err := asm.ParseLine("add rax, rbx", "amd64", 261)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := asm.ParseLine("sub rcx, rdx", "amd64", 271)
	if err != nil {
		t.Fatal(err)
	}
	funcs := []*asm.Function{
		{Name: "add", Body: []asm.Node{add}},
		{Name: "sub", Body: []asm.Node{sub}},
	}
	enc := NewEncoder(desc)
	if err := enc.BeginFunctions(funcs, desc); err != nil {
		t.Fatal(err)
	}
	if len(enc.programBatch[261]) == 0 || len(enc.programBatch[271]) == 0 {
		t.Fatalf("amd64 program batch missing: program=%#v perFunction=%#v",
			enc.programBatch, enc.batchByFn)
	}
}

func TestBeginFunctionsFallsBackPerFunction(t *testing.T) {
	desc := arch.AMD64()
	valid, err := asm.ParseLine("add rax, rbx", "amd64", 301)
	if err != nil {
		t.Fatal(err)
	}
	invalid := &asm.Inst{Opcode: "invalid", Raw: "this_is_not_an_instruction", Line: 401}
	funcs := []*asm.Function{
		{Name: "valid", Body: []asm.Node{valid}},
		{Name: "invalid", Body: []asm.Node{invalid}},
	}

	enc := NewEncoder(desc)
	if err := enc.BeginFunctions(funcs, desc); err != nil {
		t.Fatal(err)
	}
	if enc.programBatch != nil {
		t.Fatal("invalid function unexpectedly produced a program batch")
	}
	if len(enc.batchByFn["valid"]) == 0 {
		t.Fatalf("valid fallback batch missing: %#v", enc.batchByFn)
	}
	if _, ok := enc.batchByFn["invalid"]; ok {
		t.Fatalf("invalid fallback batch retained: %#v", enc.batchByFn)
	}

	if err := enc.BeginFunction(funcs[0], desc); err != nil {
		t.Fatal(err)
	}
	got, err := enc.Encode(valid, desc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "WORD $0x0148") || !strings.Contains(got, "BYTE $0xd8") {
		t.Fatalf("valid fallback output = %q", got)
	}
	if err := enc.BeginFunction(funcs[1], desc); err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Encode(invalid, desc); err == nil ||
		!strings.Contains(err.Error(), "this_is_not_an_instruction") {
		t.Fatalf("invalid fallback error = %v", err)
	}
}

func TestBeginFunctionsEmptyProgram(t *testing.T) {
	enc := NewEncoder(arch.ARM64())
	if err := enc.BeginFunctions(nil, arch.ARM64()); err != nil {
		t.Fatal(err)
	}
	if enc.programBatch != nil || enc.batchByFn != nil {
		t.Fatalf("empty program initialized batches: %#v %#v", enc.programBatch, enc.batchByFn)
	}
}

func TestRenderCallOperands(t *testing.T) {
	tests := []struct {
		name string
		op   asm.Operand
		want string
	}{
		{name: "register", op: asm.Register{Name: "R12"}, want: "(R12)"},
		{name: "base", op: asm.Memory{Base: "RAX"}, want: "(RAX)"},
		{name: "displacement", op: asm.Memory{Base: "RAX", Disp: 24}, want: "24(RAX)"},
		{name: "sb", op: asm.Memory{Base: "SB", Disp: 8}, want: "8()"},
		{name: "indexed", op: asm.Memory{Base: "RAX", Index: "RCX", Scale: 4, Disp: 8}, want: "8(RAX)(RCX*4)"},
		{name: "unsupported", op: asm.Symbol{Name: "target"}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := renderCallOperand(test.op); got != test.want {
				t.Fatalf("renderCallOperand(%#v) = %q, want %q", test.op, got, test.want)
			}
		})
	}
	if got := renderOperand(asm.Immediate{Value: -7}); got != "$-7" {
		t.Fatalf("immediate operand = %q", got)
	}
	if got := renderOperand(asm.Memory{Base: "RAX"}); got != "?" {
		t.Fatalf("unsupported branch operand = %q", got)
	}
}

func TestEncodeAMD64IndexedIndirectCall(t *testing.T) {
	inst, err := asm.ParseLine("call [QWORD PTR 104[rsp+r13]]", "amd64", 1)
	if err != nil {
		t.Fatal(err)
	}
	enc := NewEncoder(arch.AMD64())
	out, handled, err := enc.encodeCall(inst)
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !strings.Contains(out, "MOVQ 104(SP)(R13*1), R11") ||
		!strings.Contains(out, "CALL R11") || strings.Contains(out, "SUBQ") ||
		strings.Contains(out, "MOVQ R11, 0(SP)") || strings.Contains(out, "AX") {
		t.Fatalf("indexed indirect call = %q", out)
	}
	assertNoCallMarshalling(t, out)
}

func TestEncodeAMD64SimpleMemoryIndirectCallLoadsPointer(t *testing.T) {
	inst, err := asm.ParseLine("call [r12]", "amd64", 1)
	if err != nil {
		t.Fatal(err)
	}
	enc := NewEncoder(arch.AMD64())
	out, handled, err := enc.encodeCall(inst)
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !strings.Contains(out, "MOVQ (R12), R11") ||
		!strings.Contains(out, "CALL R11") || strings.Contains(out, "SUBQ") ||
		strings.Contains(out, "MOVQ R11, 0(SP)") || strings.Contains(out, "AX") {
		t.Fatalf("simple memory-indirect call = %q", out)
	}
	assertNoCallMarshalling(t, out)
}

func TestAMD64SymbolReferenceBypassesBatchEncoding(t *testing.T) {
	inst, err := asm.ParseLine("movsd xmm0, QWORD PTR value[rip]", "amd64", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isAddressPair(inst, arch.AMD64()) {
		t.Fatal("RIP-relative symbol would enter relocation-blind batch encoding")
	}
	if isAddressPair(inst, arch.ARM64()) {
		t.Fatal("amd64 symbolic memory was classified for arm64")
	}
}

func TestAMD64BitScanIsNotArm64Branch(t *testing.T) {
	inst, err := asm.ParseLine("bsr ecx, r9d", "amd64", 1)
	if err != nil {
		t.Fatal(err)
	}
	enc := NewEncoder(arch.AMD64())
	if _, direct, err := enc.encodeDirect(inst); err != nil || direct {
		t.Fatalf("BSR classified as branch: direct=%v err=%v", direct, err)
	}
}

func TestDecodeAMD64Plan9(t *testing.T) {
	got, ok := decodeToPlan9([]byte{0x48, 0x01, 0xd8, 0x90, 0xc3}, arch.AMD64())
	if !ok {
		t.Fatal("valid amd64 instruction stream was rejected")
	}
	for _, want := range []string{"ADDQ BX, AX", "NOP", "RET"} {
		if !strings.Contains(got, want) {
			t.Errorf("decode output %q missing %q", got, want)
		}
	}
}

func TestDecodeAMD64RejectsInvalidOrRelocatableBytes(t *testing.T) {
	cases := [][]byte{
		nil,
		{0x0f},
		{0x48, 0x8b, 0x05, 0x00, 0x00, 0x00, 0x00},
	}
	for _, opcodes := range cases {
		if got, ok := decodeAMD64(opcodes); ok {
			t.Errorf("decodeAMD64(%x) = %q, want rejection", opcodes, got)
		}
	}
}

func TestNormalizeAMD64Regs(t *testing.T) {
	got := normalizeAMD64Regs("MOVQ 8(RSP)(RCX*8), RAX")
	if want := "MOVQ 8(SP)(CX*8), AX"; got != want {
		t.Fatalf("normalizeAMD64Regs = %q, want %q", got, want)
	}
}
