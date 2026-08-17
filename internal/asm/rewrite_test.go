package asm

import (
	"strings"
	"testing"

	"github.com/faceair/c2goasm/arch"
)

func TestRewriteLabelsGlobal(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"cbz x2, .LBB0_3",
		".LBB0_3:",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := RewriteLabelsGlobal(funcs); err != nil {
		t.Fatal(err)
	}
	// Label definition renamed.
	var renamed bool
	for _, node := range funcs[0].Body {
		if l, ok := node.(*LabelLine); ok && l.Name == "foo_LBB0_3" {
			renamed = true
		}
	}
	if !renamed {
		t.Error("label definition not renamed")
	}
	for _, node := range funcs[0].Body {
		if inst, ok := node.(*Inst); ok {
			for _, op := range inst.Operands {
				if s, ok := op.(Symbol); ok && s.Name == "foo_LBB0_3" {
					if !strings.Contains(inst.Raw, "foo_LBB0_3") {
						t.Fatalf("raw branch reference not rewritten: %q", inst.Raw)
					}
					return // reference rewritten
				}
			}
		}
	}
	t.Errorf("branch reference not rewritten")
}

func TestReplaceAssemblySymbolHonorsTokenBoundaries(t *testing.T) {
	got := replaceAssemblySymbol("b .L1 // .L10 and x.L1", ".L1", "foo_L1")
	want := "b foo_L1 // .L10 and x.L1"
	if got != want {
		t.Fatalf("replaceAssemblySymbol = %q, want %q", got, want)
	}
}

func TestRewriteLabelsCrossFunction(t *testing.T) {
	// Cold-outline: fnA branches to a label defined in fnB.
	lines := []string{
		".globl fnA",
		"fnA:",
		"b LBB0_1",
		"ret",
		".size fnA, .-fnA",
		"fnB:",
		"LBB0_1:",
		"ret",
		".size fnB, .-fnB",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := RewriteLabelsGlobal(funcs); err != nil {
		t.Fatal(err)
	}
	// The label is renamed with the defining function's prefix and the
	// cross-function reference matches it.
	found := false
	for _, node := range funcs[0].Body {
		if inst, ok := node.(*Inst); ok {
			for _, op := range inst.Operands {
				if s, ok := op.(Symbol); ok && strings.HasPrefix(s.Name, "fnB_LBB0_1") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("cross-function label reference not rewritten")
	}
}

func TestRewriteLabelsScopesDuplicateNames(t *testing.T) {
	branch := func(line int) *Inst {
		return &Inst{Opcode: "b", Operands: []Operand{Symbol{Name: "Lshared"}}, Line: line}
	}
	funcs := []*Function{
		{Name: "first", Body: []Node{&LabelLine{Name: "Lshared"}, branch(1)}},
		{Name: "second", Body: []Node{&LabelLine{Name: "Lshared"}, branch(2)}},
	}
	if err := RewriteLabelsGlobal(funcs); err != nil {
		t.Fatal(err)
	}
	for index, want := range []string{"first_Lshared", "second_Lshared"} {
		label := funcs[index].Body[0].(*LabelLine)
		target := funcs[index].Body[1].(*Inst).Operands[0].(Symbol)
		if label.Name != want || target.Name != want {
			t.Errorf("function %d label=%q target=%q, want %q", index, label.Name, target.Name, want)
		}
	}
}

func TestRewriteLabelsRejectsAmbiguousCrossFunctionReference(t *testing.T) {
	funcs := []*Function{
		{Name: "first", Body: []Node{&LabelLine{Name: "Lshared"}}},
		{Name: "second", Body: []Node{&LabelLine{Name: "Lshared"}}},
		{Name: "caller", Body: []Node{&Inst{
			Opcode:   "b",
			Operands: []Operand{Symbol{Name: "Lshared"}},
			Line:     7,
		}}},
	}
	err := RewriteLabelsGlobal(funcs)
	if err == nil || !strings.Contains(err.Error(), "ambiguous cross-function label") {
		t.Fatalf("ambiguous label error = %v", err)
	}
}

func TestRewriteLabelsDataLabelsExcluded(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"adrp x8, l_.str.2@PAGE",
		"add x8, x8, l_.str.2@PAGEOFF",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := RewriteLabelsGlobal(funcs); err != nil {
		t.Fatal(err)
	}
	// The string label must not be renamed (it belongs to ConstTables).
	for _, node := range funcs[0].Body {
		if inst, ok := node.(*Inst); ok {
			for _, op := range inst.Operands {
				if s, ok := op.(Symbol); ok && strings.Contains(s.Name, "_l_.str") {
					t.Errorf("data label renamed: %s", s.Name)
				}
			}
		}
	}
}

func TestRewriteSymbolAddresses(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"adrp x8, _dbuf_default_realloc@PAGE",
		"_c2goasm_quickjs_Lloh0:",
		"add x8, x8, _dbuf_default_realloc@PAGEOFF",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := RewriteLabelsGlobal(funcs); err != nil {
		t.Fatal(err)
	}
	tables := mustCollectTables(t, prog)
	if err := rewriteSymbolAddresses(funcs[0], arch.ARM64(), tables, nil); err != nil {
		t.Fatal(err)
	}
	for _, node := range funcs[0].Body {
		if s, ok := node.(*SyntheticInst); ok && strings.Contains(s.Text, "·_c2goasm_native_dbuf_default_realloc(SB)") {
			return
		}
	}
	t.Error("adrp/add pair not rewritten")
}

func TestRewriteFunctionAddressSanitizesNativeSymbol(t *testing.T) {
	lines := []string{
		".globl owner",
		"owner:",
		"adrp x8, _foo.bar@PAGE",
		"add x8, x8, _foo.bar@PAGEOFF",
		"ret",
		".size owner, .-owner",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteSymbolAddresses(funcs[0], arch.ARM64(), nil, nil); err != nil {
		t.Fatal(err)
	}
	synthetic, ok := funcs[0].Body[0].(*SyntheticInst)
	if !ok || !strings.Contains(synthetic.Text, "·_c2goasm_native_foo_bar(SB)") {
		t.Fatalf("sanitized function address = %#v", funcs[0].Body[0])
	}
}

func TestRewriteFunctionAddressRejectsUnknown(t *testing.T) {
	lines := []string{
		".globl owner",
		"owner:",
		"adrp x8, _missing@PAGE",
		"add x8, x8, _missing@PAGEOFF",
		"ret",
		".size owner, .-owner",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	err = rewriteSymbolAddresses(funcs[0], arch.ARM64(), nil, map[string]bool{"owner": true})
	if err == nil || !strings.Contains(err.Error(), "unresolved ARM64 function address") {
		t.Fatalf("unknown function address error = %v", err)
	}
}

func TestRewriteSymbolAddressesDifferentDestinationRegister(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"adrp x8, _dbuf_default_realloc@PAGE",
		"Lloh0:",
		"add x9, x8, _dbuf_default_realloc@PAGEOFF",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteSymbolAddresses(funcs[0], arch.ARM64(), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, node := range funcs[0].Body {
		if s, ok := node.(*SyntheticInst); ok && strings.Contains(s.Text, "·_c2goasm_native_dbuf_default_realloc(SB), R9") {
			return
		}
	}
	t.Errorf("different-destination adrp/add pair was not rewritten: %+v", funcs[0].Body)
}

func TestRewriteSymbolAddressesKeepsPairOffset(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"adrp x8, _data@PAGE+16",
		"Lloh0:",
		"add x8, x8, _data@PAGEOFF+16",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	tables := []*ConstTable{{Name: "LCDATA1", Labels: map[string]uint{"_data": 0x20}}}
	if err := rewriteSymbolAddresses(funcs[0], arch.ARM64(), tables, nil); err != nil {
		t.Fatal(err)
	}
	for _, node := range funcs[0].Body {
		if s, ok := node.(*SyntheticInst); ok && strings.Contains(s.Text, "LCDATA1<>+0x30(SB)") {
			return
		}
	}
	t.Errorf("pair offset was dropped: %+v", funcs[0].Body)
}

func TestRewriteGotLoads(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"adrp x2, _lre_realloc@GOTPAGE",
		"Lloh1:",
		"ldr x2, [x2, _lre_realloc@GOTPAGEOFF]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteGotLoads(funcs[0], arch.ARM64(), nil, map[string]bool{"lre_realloc": true}); err != nil {
		t.Fatal(err)
	}
	for _, node := range funcs[0].Body {
		if s, ok := node.(*SyntheticInst); ok && strings.Contains(s.Text, "·_c2goasm_native_lre_realloc(SB)") {
			return
		}
	}
	t.Error("GOT load not rewritten")
}

func TestRewriteGotLoadsRejectsUnknownFunction(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"adrp x2, _missing@GOTPAGE",
		"ldr x2, [x2, _missing@GOTPAGEOFF]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	err = rewriteGotLoads(funcs[0], arch.ARM64(), nil, map[string]bool{"foo": true})
	if err == nil || !strings.Contains(err.Error(), "unresolved ARM64 GOT function address") {
		t.Fatalf("unknown GOT function address error = %v", err)
	}
}

func TestRewriteGotLoadsPreservesControlLabel(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"adrp x2, _target@GOTPAGE",
		"LBB0_1:",
		"ldr x2, [x2, _target@GOTPAGEOFF]",
		"b LBB0_1",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteGotLoads(funcs[0], arch.ARM64(), nil, map[string]bool{"target": true}); err != nil {
		t.Fatal(err)
	}
	var label, load bool
	for _, node := range funcs[0].Body {
		switch n := node.(type) {
		case *LabelLine:
			label = label || n.Name == "LBB0_1"
		case *Inst:
			if n.Opcode == "adrp" {
				t.Fatalf("unrelocated ADRP remained: %q", n.Raw)
			}
		case *SyntheticInst:
			load = load || strings.Contains(n.Text, "MOVD $·_c2goasm_native_target(SB), R2")
		}
	}
	if !label || !load {
		t.Fatalf("control label or GOT load lost: label=%v load=%v body=%+v", label, load, funcs[0].Body)
	}
}

func TestConstTablesStrings(t *testing.T) {
	lines := []string{
		".section __TEXT,__cstring",
		"l_.str.1:",
		".asciz \"hello\\n\"",
		"l_.str.2:",
		".asciz \"world\"",
		".text",
		".globl foo",
		"foo:",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	tables := mustCollectTables(t, prog)
	if len(tables) != 1 {
		t.Fatalf("got %d tables", len(tables))
	}
	tab := tables[0]
	if len(tab.Data) != 13 { // "hello\n\0" + "world\0"
		t.Errorf("data size = %d, want 13: %x", len(tab.Data), tab.Data)
	}
	if tab.Labels["l_.str.1"] != 0 || tab.Labels["l_.str.2"] != 7 {
		t.Errorf("labels: %+v", tab.Labels)
	}
	if len(tab.Relocs) != 0 {
		t.Errorf("unexpected relocs: %+v", tab.Relocs)
	}
}

func TestConstTablesReloc(t *testing.T) {
	lines := []string{
		".section .rodata",
		"LRE_TABLE:",
		".quad _unicode_prop",
		".quad 42",
		".text",
	}
	prog := parseProgram(t, lines, "arm64")
	tables := mustCollectTables(t, prog)
	if len(tables) != 1 {
		t.Fatalf("got %d tables", len(tables))
	}
	tab := tables[0]
	if len(tab.Relocs) != 1 || tab.Relocs[0].Symbol != "unicode_prop" || tab.Relocs[0].Offset != 0 {
		t.Errorf("relocs: %+v", tab.Relocs)
	}
	out := EmitTables(tables)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "$·_c2goasm_native_unicode_prop(SB)") {
		t.Errorf("reloc not emitted: %s", joined)
	}
}

func TestConstTablesMachOFunctionReloc(t *testing.T) {
	lines := []string{
		".section __DATA,__const",
		".p2align 3, 0x0",
		"_class_def:",
		".long 165",
		".space 4",
		".quad _finalizer",
		".quad _marker",
		".text",
	}
	prog := parseProgram(t, lines, "arm64")
	tables := mustCollectTables(t, prog)
	if len(tables) != 1 || len(tables[0].Relocs) != 2 {
		t.Fatalf("tables=%d relocs=%+v", len(tables), tables)
	}
	if tables[0].Relocs[0].Symbol != "finalizer" || tables[0].Relocs[1].Symbol != "marker" {
		t.Fatalf("relocs=%+v", tables[0].Relocs)
	}
}

func TestConstTablesUnalignedReloc(t *testing.T) {
	lines := []string{
		".section .rodata",
		".byte 1, 2",
		".long 165",
		".space 4",
		".quad _finalizer",
		".text",
	}
	prog := parseProgram(t, lines, "arm64")
	tables := mustCollectTables(t, prog)
	if len(tables) != 1 || len(tables[0].Relocs) != 1 || tables[0].Relocs[0].Offset != 10 {
		if len(tables) == 1 {
			t.Fatalf("data=%x relocs=%+v", tables[0].Data, tables[0].Relocs)
		}
		t.Fatalf("tables=%+v", tables)
	}
	joined := strings.Join(EmitTables(tables), "\n")
	if !strings.Contains(joined, "DATA LCDATA1<>+0x00a(SB)/8, $·_c2goasm_native_finalizer(SB)") {
		t.Fatalf("unaligned reloc missing: %s", joined)
	}
}

func TestConstTablesRelocDataTarget(t *testing.T) {
	lines := []string{
		".section .rodata",
		".quad _target",
		"_target:",
		".quad 7",
		".text",
	}
	prog := parseProgram(t, lines, "arm64")
	tables := mustCollectTables(t, prog)
	joined := strings.Join(EmitTables(tables), "\n")
	if !strings.Contains(joined, "$LCDATA1<>+0x8(SB)") {
		t.Errorf("data relocation not resolved: %s", joined)
	}
}

func TestRewriteSymbolAddressesConstTable(t *testing.T) {
	lines := []string{
		".section __TEXT,__cstring",
		"l_.str.1:",
		".asciz \"x\"",
		".text",
		".globl foo",
		"foo:",
		"adrp x8, l_.str.1@PAGE",
		"Lloh0:",
		"// keep relocation comment",
		"add x8, x8, l_.str.1@PAGEOFF",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	tables := mustCollectTables(t, prog)
	if err := rewriteSymbolAddresses(funcs[0], arch.ARM64(), tables, nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range funcs[0].Body {
		if s, ok := node.(*SyntheticInst); ok && strings.Contains(s.Text, "LCDATA1<>+0x0(SB)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("constant address not emitted: %#v", funcs[0].Body)
	}
}

func TestRewriteSymbolAddressesLeavesMismatchedPair(t *testing.T) {
	lines := []string{".globl foo", "foo:", "adrp x8, _a@PAGE", "add x8, x8, _b@PAGEOFF", "ret", ".size foo, .-foo"}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteSymbolAddresses(funcs[0], arch.ARM64(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(funcs[0].Body) < 2 {
		t.Fatal("mismatched address pair was removed")
	}
	if _, ok := funcs[0].Body[0].(*Inst); !ok {
		t.Fatalf("first instruction became %#v", funcs[0].Body[0])
	}
}

func TestRewriteFunctionRejectsUnconsumedARM64Relocation(t *testing.T) {
	lines := []string{".globl foo", "foo:", "adrp x8, _a@PAGE", "add x8, x8, _b@PAGEOFF", "ret", ".size foo, .-foo"}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	err = rewriteFunction(funcs[0], arch.ARM64(), nil, map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "unresolved page reference") {
		t.Fatalf("unconsumed relocation error = %v", err)
	}
}

func TestRewriteGotLoadsConstTable(t *testing.T) {
	lines := []string{".section .rodata", "_target:", ".quad 1", ".text", ".globl foo", "foo:", "adrp x2, _target@GOTPAGE", "ldr x2, [x2, _target@GOTPAGEOFF]", "ret", ".size foo, .-foo"}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteGotLoads(funcs[0], arch.ARM64(), mustCollectTables(t, prog), nil); err != nil {
		t.Fatal(err)
	}
	s, ok := funcs[0].Body[0].(*SyntheticInst)
	if !ok || !strings.Contains(s.Text, "LCDATA1<>+0x0(SB)") {
		t.Fatalf("GOT data address = %#v", funcs[0].Body[0])
	}
}

func TestRewritePageLoadKeepsDisplacement(t *testing.T) {
	lines := []string{
		".section __DATA,__data",
		"_target:",
		".space 128",
		".text",
		".globl foo",
		"foo:",
		"adrp x8, _target@PAGE",
		"ldr x9, [x8, _target@PAGEOFF+64]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteGotLoads(funcs[0], arch.ARM64(), mustCollectTables(t, prog), nil); err != nil {
		t.Fatal(err)
	}
	baseFound, displacementFound := false, false
	for _, node := range funcs[0].Body {
		switch node := node.(type) {
		case *SyntheticInst:
			baseFound = baseFound || strings.Contains(node.Text, "LCDATA1<>+0x0(SB)")
		case *Inst:
			displacementFound = displacementFound || strings.Contains(node.Raw, "#64")
		}
	}
	if !baseFound || !displacementFound {
		t.Fatalf("page-relative base/displacement = %#v", funcs[0].Body)
	}
}

func TestRewriteSharedPageBaseKeepsIndependentDisplacements(t *testing.T) {
	lines := []string{
		".section __DATA,__data",
		"_config:",
		".space 64",
		".text",
		".globl foo",
		"foo:",
		"adrp x8, _config@PAGE",
		"ldr w9, [x8, _config@PAGEOFF+16]",
		"ldr w10, [x8, _config@PAGEOFF+24]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteFunction(funcs[0], arch.ARM64(), mustCollectTables(t, prog), nil); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for _, node := range funcs[0].Body {
		switch node := node.(type) {
		case *SyntheticInst:
			body.WriteString(node.Text)
		case *Inst:
			body.WriteString(node.Raw)
		}
		body.WriteByte('\n')
	}
	got := body.String()
	for _, want := range []string{"LCDATA1<>+0x0(SB)", "#16", "#24"} {
		if !strings.Contains(got, want) {
			t.Fatalf("shared PAGEOFF rewrite missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "+0x10(SB)") || strings.Contains(got, "#40") {
		t.Fatalf("shared PAGEOFF displacement was folded twice:\n%s", got)
	}
}

func TestRewritePageBaseRewritesEveryUse(t *testing.T) {
	lines := []string{
		".section __DATA,__data",
		"_counter:",
		".long 0",
		".text",
		".globl foo",
		"foo:",
		"adrp x9, _counter@PAGE",
		"ldr w0, [x9, _counter@PAGEOFF]",
		"add w10, w0, #1",
		"str w10, [x9, _counter@PAGEOFF]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteFunction(funcs[0], arch.ARM64(), mustCollectTables(t, prog), nil); err != nil {
		t.Fatal(err)
	}
	for _, node := range funcs[0].Body {
		instruction, ok := node.(*Inst)
		if ok && strings.Contains(instruction.Raw, "@PAGE") {
			t.Fatalf("page relocation survived rewrite: %#v", instruction)
		}
	}
}

func TestRewriteSeparatedPageBaseAndLoad(t *testing.T) {
	lines := []string{
		".section __DATA,__data",
		"_vfs:",
		".space 64",
		".text",
		".globl foo",
		"foo:",
		"adrp x21, _vfs@PAGE",
		"mov x0, x1",
		"ldr x9, [x21, _vfs@PAGEOFF+16]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteGotLoads(funcs[0], arch.ARM64(), mustCollectTables(t, prog), nil); err != nil {
		t.Fatal(err)
	}
	var address bool
	for _, node := range funcs[0].Body {
		if synthetic, ok := node.(*SyntheticInst); ok &&
			strings.Contains(synthetic.Text, "MOVD $LCDATA1<>+0x0(SB), R21") {
			address = true
		}
		if inst, ok := node.(*Inst); ok && inst.Opcode == "ldr" {
			memory, ok := inst.Operands[1].(Memory)
			if !ok || memory.Symbol != "" || memory.Disp != 16 {
				t.Fatalf("PAGEOFF load = %#v, want base+16", inst.Operands[1])
			}
		}
	}
	if !address {
		t.Fatalf("separated ADRP was not rewritten: %#v", funcs[0].Body)
	}
}

func TestRewritePageBaseAcrossOtherRelocations(t *testing.T) {
	lines := []string{
		".section __DATA,__data",
		"_config:",
		".space 128",
		"_stats:",
		".space 128",
		".text",
		".globl foo",
		"foo:",
		"adrp x24, _config@PAGE+56",
		"Lloh0:",
		"adrp x25, _stats@PAGE",
		"Lloh1:",
		"add x25, x25, _stats@PAGEOFF",
		"b LBB0_1",
		"LBB0_1:",
		"adrp x8, _config@PAGE",
		"Lloh2:",
		"ldr w8, [x8, _config@PAGEOFF]",
		"ldr x8, [x24, _config@PAGEOFF+56]",
		"adrp x24, _config@PAGE+56",
		"ldr x9, [x24, _config@PAGEOFF+56]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	tables := mustCollectTables(t, prog)
	if err := rewriteSymbolAddresses(funcs[0], arch.ARM64(), tables, nil); err != nil {
		t.Fatal(err)
	}
	if err := rewriteGotLoads(funcs[0], arch.ARM64(), tables, nil); err != nil {
		t.Fatal(err)
	}
	var addresses, loads int
	for _, node := range funcs[0].Body {
		if synthetic, ok := node.(*SyntheticInst); ok &&
			strings.Contains(synthetic.Text, "LCDATA1<>") &&
			strings.HasSuffix(synthetic.Text, ", R24") {
			addresses++
		}
		if inst, ok := node.(*Inst); ok && inst.Opcode == "ldr" {
			memory, ok := inst.Operands[1].(Memory)
			if ok && memory.Base == "R24" && memory.Symbol == "" &&
				(memory.Disp == 0 || memory.Disp == 56) {
				if strings.Contains(inst.Raw, "@PAGEOFF") {
					t.Fatalf("rewritten load retained relocation syntax: %q", inst.Raw)
				}
				loads++
			}
		}
	}
	if addresses != 2 || loads != 2 {
		t.Fatalf("reused page base rewrite: addresses=%d loads=%d", addresses, loads)
	}
}

func TestRewritePageBaseWithRegisterOffsets(t *testing.T) {
	lines := []string{
		".section __DATA,__data",
		"_config:",
		".space 128",
		".text",
		".globl foo",
		"foo:",
		"adrp x25, _config@PAGE+40",
		"str w0, [x25, x1, lsl #2]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteGotLoads(funcs[0], arch.ARM64(), mustCollectTables(t, prog), nil); err != nil {
		t.Fatal(err)
	}
	for _, node := range funcs[0].Body {
		if synthetic, ok := node.(*SyntheticInst); ok &&
			strings.Contains(synthetic.Text, "MOVD $LCDATA1<>+0x28(SB), R25") {
			return
		}
	}
	t.Fatalf("ADRP addend was not materialized: %#v", funcs[0].Body)
}

func TestRewriteLabelsGlobalSanitizesFunctionPrefix(t *testing.T) {
	lines := []string{".globl foo.bar", "foo.bar:", "b .LBB0_1", ".LBB0_1:", "ret", ".size foo.bar, .-foo.bar"}
	prog := parseProgram(t, lines, "arm64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}

	if err := RewriteLabelsGlobal(funcs); err != nil {
		t.Fatal(err)
	}
	for _, node := range funcs[0].Body {
		if l, ok := node.(*LabelLine); ok && l.Name == "foo_bar_LBB0_1" {
			return
		}
	}
	t.Fatal("label did not use sanitized function prefix")
}

func TestRewriteAMD64PreservesHighByteRegister(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"mov BYTE PTR payload[rip], ah",
		"ret",
		".size foo, .-foo",
		".section .data",
		"payload:",
		".byte 0",
	}
	prog := parseProgram(t, lines, "amd64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	tables := mustCollectTables(t, prog)
	if err := rewriteAMD64SymbolReferences(funcs[0], tables, nil); err != nil {
		t.Fatal(err)
	}
	synthetic, ok := funcs[0].Body[0].(*SyntheticInst)
	if !ok || !strings.Contains(synthetic.Text, "MOVB AH,") {
		t.Fatalf("high-byte symbolic store = %#v", funcs[0].Body[0])
	}
}

func TestRewriteAMD64RIPRelativeReferences(t *testing.T) {
	lines := []string{
		".section .text",
		".globl foo",
		".type foo, @function",
		"foo:",
		"lea rax, payload[rip]",
		"mov eax, DWORD PTR counter[rip]",
		"mov DWORD PTR counter[rip], edx",
		"movsd xmm0, QWORD PTR payload[rip]",
		"mov rdx, QWORD PTR bar@GOTPCREL[rip]",
		"movzx eax, BYTE PTR payload[rip]",
		"movsx r8, WORD PTR payload[rip]",
		"movsxd r9, DWORD PTR payload[rip]",
		"add eax, DWORD PTR payload[rip]",
		"movdqa xmm1, XMMWORD PTR payload[rip]",
		"cmp DWORD PTR payload[rip], eax",
		"vpbroadcastq ymm3, QWORD PTR payload[rip]",
		"vbroadcastsd ymm3, QWORD PTR payload[rip]",
		"vmovddup xmm2, QWORD PTR payload[rip]",
		"vmovsd xmm0, QWORD PTR payload[rip]",
		"vmovdqa ymm1, YMMWORD PTR payload[rip]",
		"ret",
		".size foo, .-foo",
		".section .rodata",
		".byte 9",
		".align 8",
		"payload:",
		".quad 0",
		".section .bss",
		"counter:",
		".zero 4",
		".text",
	}
	prog := parseProgram(t, lines, "amd64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	tables := mustCollectTables(t, prog)
	if err := rewriteAMD64SymbolReferences(funcs[0], tables, map[string]bool{"bar": true}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, node := range funcs[0].Body {
		if synthetic, ok := node.(*SyntheticInst); ok {
			got = append(got, strings.TrimSpace(synthetic.Text))
		}
	}
	want := []string{
		"LEAQ LCDATA1<>+0x8(SB), AX",
		"MOVL LCDATA2<>+0x0(SB), AX",
		"MOVL DX, LCDATA2<>+0x0(SB)",
		"MOVSD LCDATA1<>+0x8(SB), X0",
		"LEAQ ·_c2goasm_native_bar(SB), DX",
		"MOVBLZX LCDATA1<>+0x8(SB), AX",
		"MOVWQSX LCDATA1<>+0x8(SB), R8",
		"MOVLQSX LCDATA1<>+0x8(SB), R9",
		"ADDL LCDATA1<>+0x8(SB), AX",
		"MOVO LCDATA1<>+0x8(SB), X1",
		"CMPL AX, LCDATA1<>+0x8(SB)",
		"VPBROADCASTQ LCDATA1<>+0x8(SB), Y3",
		"VBROADCASTSD LCDATA1<>+0x8(SB), Y3",
		"VMOVDDUP LCDATA1<>+0x8(SB), X2",
		"VMOVSD LCDATA1<>+0x8(SB), X0",
		"VMOVDQA LCDATA1<>+0x8(SB), Y1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rewritten references:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRewriteAMD64RejectsNonLoadGOTPCREL(t *testing.T) {
	lines := []string{
		".globl foo",
		".type foo, @function",
		"foo:",
		"cmp rax, QWORD PTR bar@GOTPCREL[rip]",
		"ret",
		".size foo, .-foo",
	}
	prog := parseProgram(t, lines, "amd64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	err = rewriteAMD64SymbolReferences(funcs[0], nil, map[string]bool{"bar": true})
	if err == nil || !strings.Contains(err.Error(), `unsupported GOTPCREL opcode "cmp"`) {
		t.Fatalf("non-load GOTPCREL error = %v", err)
	}
}

func TestRewriteAMD64RIPRelativeUnknownFails(t *testing.T) {
	lines := []string{".globl foo", ".type foo, @function", "foo:", "lea rax, missing[rip]", "ret", ".size foo, .-foo"}
	prog := parseProgram(t, lines, "amd64")
	funcs, err := analyze(prog)
	if err != nil {
		t.Fatal(err)
	}
	err = rewriteAMD64SymbolReferences(funcs[0], nil, nil)
	if err == nil || !strings.Contains(err.Error(), `unresolved RIP-relative symbol "missing"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestRewriteLabelsGlobalUpdatesMemorySymbols(t *testing.T) {
	fn := &Function{
		Name: "foo",
		Body: []Node{
			&Inst{Opcode: "lea", Operands: []Operand{Register{Name: "AX"}, Memory{Base: "SB", Symbol: ".Llocal"}}},
			&LabelLine{Name: ".Llocal"},
			&Inst{Opcode: "ret"},
		},
	}
	if err := RewriteLabelsGlobal([]*Function{fn}); err != nil {
		t.Fatal(err)
	}
	memory := fn.Body[0].(*Inst).Operands[1].(Memory)
	if memory.Symbol != "foo_Llocal" {
		t.Fatalf("memory symbol = %q", memory.Symbol)
	}
}
