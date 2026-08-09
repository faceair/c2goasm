package asm

import (
	"strings"
	"testing"
)

func TestParseAmd64Basic(t *testing.T) {
	cases := []struct {
		line   string
		opcode string
	}{
		{"mov rax, rbx", "mov"},
		{"lea rdi, [rsi+8]", "lea"},
		{"vmovups ymm0, ymmword ptr [rdi]", "vmovups"},
		{"add rax, 42", "add"},
		{"pxor xmm0, xmm0", "pxor"},
	}
	for _, tc := range cases {
		inst, err := ParseLine(tc.line, "amd64", 1)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if inst.Opcode != tc.opcode {
			t.Errorf("%q: opcode %q, want %q", tc.line, inst.Opcode, tc.opcode)
		}
	}
}

func TestParseAmd64InstructionPrefixes(t *testing.T) {
	cases := []struct {
		line     string
		opcode   string
		prefixes string
	}{
		{"rep bsf edi, r10d", "bsf", "rep"},
		{"lock add QWORD PTR [rax], 1", "add", "lock"},
		{"data16 cs nop", "nop", "data16,cs"},
		{"rep ret", "ret", "rep"},
	}
	for _, test := range cases {
		inst, err := ParseLine(test.line, "amd64", 1)
		if err != nil {
			t.Fatalf("%q: %v", test.line, err)
		}
		if inst.Opcode != test.opcode || strings.Join(inst.Prefixes, ",") != test.prefixes {
			t.Errorf("%q: opcode=%q prefixes=%v", test.line, inst.Opcode, inst.Prefixes)
		}
	}
}

func TestParseAmd64Memory(t *testing.T) {
	cases := []struct {
		line   string
		base   string
		index  string
		scale  int
		disp   int64
		symbol string
	}{
		{"mov rax, [rbp+8]", "BP", "", 0, 8, ""},
		{"mov rax, [rdi+rcx*4+8]", "DI", "CX", 4, 8, ""},
		{"mov rax, [rsp-16]", "SP", "", 0, -16, ""},
		{"mov rax, [rip + foo]", "SB", "", 0, 0, "foo"},
		{"mov rax, QWORD PTR -1[rdi]", "DI", "", 0, -1, ""},
		{"mov rax, QWORD PTR 32[rcx]", "CX", "", 0, 32, ""},
		{"mov rax, TBYTE PTR 8[rsp]", "SP", "", 0, 8, ""},
	}
	for _, tc := range cases {
		inst, err := ParseLine(tc.line, "amd64", 1)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		mem, ok := inst.Operands[1].(Memory)
		if !ok {
			t.Fatalf("%q: operand 1 not memory: %T", tc.line, inst.Operands[1])
		}
		if mem.Base != tc.base || mem.Index != tc.index || mem.Scale != tc.scale ||
			mem.Disp != tc.disp || mem.Symbol != tc.symbol {
			t.Errorf("%q: got base=%s idx=%s scale=%d disp=%d sym=%s; want %+v",
				tc.line, mem.Base, mem.Index, mem.Scale, mem.Disp, mem.Symbol, tc)
		}
	}
}

func TestParseArm64(t *testing.T) {
	cases := []struct {
		line   string
		opcode string
		ops    int
	}{
		{"add x0, x0, x1", "add", 3},
		{"ldp q2, q3, [x9, #-16]", "ldp", 3},
		{"ldrb w15, [x15, x13]", "ldrb", 2},
		{"cbz x2, .LBB0_3", "cbz", 2},
		{"tbnz w9, #0, .LBB0_3", "tbnz", 3},
		{"movk w9, #64672, lsl #16", "movk", 2},
		{"fmov v0.2d, #1.00000000", "fmov", 2},
	}
	for _, tc := range cases {
		inst, err := ParseLine(tc.line, "arm64", 1)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if inst.Opcode != tc.opcode || len(inst.Operands) != tc.ops {
			t.Errorf("%q: opcode=%q ops=%d; want %q/%d", tc.line, inst.Opcode, len(inst.Operands), tc.opcode, tc.ops)
		}
	}
}

func TestRegNormalization(t *testing.T) {
	// arm64 X registers become R; amd64 classic registers drop the R.
	if got := normalizeRegName("x2", "arm64"); got != "R2" {
		t.Errorf("x2 -> %s", got)
	}
	if got := normalizeRegName("xzr", "arm64"); got != "ZR" {
		t.Errorf("xzr -> %s", got)
	}
	if got := normalizeRegName("rcx", "amd64"); got != "CX" {
		t.Errorf("rcx -> %s", got)
	}
	if got := normalizeRegName("r8", "amd64"); got != "R8" {
		t.Errorf("r8 -> %s", got)
	}
	for _, name := range []string{"r8b", "r10w", "r12d", "r15d"} {
		if !isAMD64Register(name) {
			t.Errorf("%s was not recognized as an amd64 register", name)
		}
	}
}

func TestParseSymbolModifiers(t *testing.T) {
	cases := []struct {
		line   string
		name   string
		offset int64
	}{
		{"adrp x8, _foo@PAGE", "_foo", 0},
		{"adrp x8, l_.str.44@PAGE+20", "l_.str.44", 20},
		{"bl _bar@PLT", "_bar", 0},
	}
	for _, tc := range cases {
		inst, err := ParseLine(tc.line, "arm64", 1)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if len(inst.Operands) < 1 {
			t.Fatalf("%q: expected operands", tc.line)
		}
		oi := 1
		if len(inst.Operands) == 1 {
			oi = 0
		}
		sym, ok := inst.Operands[oi].(Symbol)
		if !ok {
			t.Fatalf("%q: operand 1 not symbol: %T", tc.line, inst.Operands[1])
		}
		if sym.Name != tc.name || sym.Offset != tc.offset {
			t.Errorf("%q: got %q+%d; want %q+%d", tc.line, sym.Name, sym.Offset, tc.name, tc.offset)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"mov rax, [rdi+rcx+rbx]",
		"mov rax, [rdi*rcx]",
		"123 invalid",
		"",
	}
	for _, line := range cases {
		if _, err := ParseLine(line, "amd64", 1); err == nil && line != "" {
			t.Errorf("%q: expected error", line)
		}
	}
	// Empty/comment lines return nil without error.
	if inst, err := ParseLine("", "amd64", 1); err != nil || inst != nil {
		t.Errorf("empty line: inst=%v err=%v", inst, err)
	}
}

func TestParseSourceClassification(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"\tadd x0, x0, x1 ; comment",
		"\tret",
		"// standalone comment",
		"",
	}
	prog, err := ParseSource(lines, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if len(prog.Nodes) != 6 {
		t.Fatalf("got %d nodes", len(prog.Nodes))
	}
	if _, ok := prog.Nodes[0].(*Directive); !ok {
		t.Errorf("node 0 not directive")
	}
	if l, ok := prog.Nodes[1].(*LabelLine); !ok || l.Name != "foo" {
		t.Errorf("node 1 not label foo: %#v", prog.Nodes[1])
	}
	inst := prog.Nodes[2].(*Inst)
	if inst.Opcode != "add" || inst.Comment != "comment" {
		t.Errorf("node 2: opcode=%q comment=%q", inst.Opcode, inst.Comment)
	}
	if _, ok := prog.Nodes[3].(*Inst); !ok {
		t.Errorf("node 3 not inst")
	}
	if _, ok := prog.Nodes[4].(*CommentLine); !ok {
		t.Errorf("node 4 not comment")
	}
	if _, ok := prog.Nodes[5].(*BlankLine); !ok {
		t.Errorf("node 5 not blank")
	}
}

func TestParseSourcePreservesQuotedCommentMarkers(t *testing.T) {
	prog, err := ParseSource([]string{`.asciz "a b;c//d##e @ f" ; real comment`}, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	directive, ok := prog.Nodes[0].(*Directive)
	if !ok {
		t.Fatalf("node = %T, want directive", prog.Nodes[0])
	}
	if len(directive.Args) != 1 || directive.Args[0] != `"a b;c//d##e @ f"` {
		t.Fatalf("args = %#v", directive.Args)
	}
	if directive.Comment != "real comment" {
		t.Fatalf("comment = %q", directive.Comment)
	}
}

func TestParseSourceLabelWithInstruction(t *testing.T) {
	prog, err := ParseSource([]string{"foo: add x0, x0, x1"}, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := prog.Nodes[0].(*Inst)
	if !ok || inst.Label != "foo" || inst.Opcode != "add" {
		t.Errorf("label+inst: %#v", prog.Nodes[0])
	}
}

func TestDemangle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"_ZN14MultiplyAndAddEPfS1_S1_S1_", "MultiplyAndAdd"},
		{"pstrcpy", "pstrcpy"},
		{"_pstrcpy", "_pstrcpy"},
	}
	for _, tc := range cases {
		got, err := Demangle(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("Demangle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := Demangle("_ZN"); err == nil {
		t.Error("Demangle(_ZN): expected error")
	}
}

func TestParseCompanionDirectLeaves(t *testing.T) {
	entries, err := parseCompanion([]byte(
		"package p\n" +
			"func helper() {}\n" +
			"func _entry(source, length uintptr) uintptr\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("direct entries = %d, want 1", len(entries))
	}
	entry := entries["entry"]
	if entry == nil || entry.goName != "_entry" || len(entry.args) != 2 || len(entry.results) != 1 {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.args[0].name != "source" || entry.args[1].name != "length" || entry.results[0].name != "ret" {
		t.Fatalf("slots = args %#v results %#v", entry.args, entry.results)
	}
	entries, err = parseCompanion([]byte(
		"package p\n" +
			"import \"unsafe\"\n" +
			"func _pointer(value unsafe.Pointer) unsafe.Pointer\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if entries["pointer"] == nil {
		t.Fatal("unsafe.Pointer direct entry was not parsed")
	}
	entries, err = parseCompanion([]byte("package p\nfunc _anonymous(uintptr, uintptr) uintptr\n"))
	if err != nil {
		t.Fatal(err)
	}
	entry = entries["anonymous"]
	if entry == nil || len(entry.args) != 2 || len(entry.results) != 1 ||
		entry.args[0].name != "arg" || entry.args[1].name != "arg1" || entry.results[0].name != "ret" {
		t.Fatalf("anonymous slots = %#v", entry)
	}

	cases := []struct {
		name string
		src  string
		want string
	}{
		{name: "syntax", src: "package", want: "companion parse"},
		{name: "prefix", src: "package p\nfunc entry()\n", want: "_<c-function>"},
		{name: "type", src: "package p\nfunc _entry(value []byte)\n", want: "not a supported 64-bit"},
		{name: "shadowed", src: "package p\ntype uintptr struct{}\nfunc _entry(value uintptr)\n", want: "uintptr is shadowed"},
		{name: "fake unsafe", src: "package p\ntype unsafe struct{ Pointer int }\nfunc _entry(value unsafe.Pointer)\n", want: "requires exactly one"},
		{name: "linkname", src: "package p\nimport _ \"unsafe\"\n//go:linkname _entry runtime.somewhere\nfunc _entry(value uintptr) uintptr\n", want: "must not use //go:linkname"},
		{name: "missing unsafe import", src: "package p\nfunc _entry(value unsafe.Pointer)\n", want: "requires exactly one"},
		{name: "multiple results", src: "package p\nfunc _entry() (uintptr, uintptr)\n", want: "at most one result"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCompanion([]byte(test.src))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
