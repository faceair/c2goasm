package asm

import (
	"bytes"
	"strings"
	"testing"
)

func mustCollectTables(t *testing.T, prog *Program) []*ConstTable {
	t.Helper()
	tables, err := collectConstTables(prog)
	if err != nil {
		t.Fatal(err)
	}
	return tables
}

func TestCollectConstTablesNumericAndPadding(t *testing.T) {
	lines := []string{
		".section .rodata",
		"L0:",
		".byte 1",
		".byte 2",
		".short -2",
		".long 50462982",
		".p2align 3",
		"L1:",
		".quad 7",
		".space 2",
		".text",
	}
	tab := mustCollectTables(t, parseProgram(t, lines, "arm64"))[0]
	want := []byte{1, 2, 0xfe, 0xff, 0x06, 0x01, 0x02, 0x03, 7, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(tab.Data, want) {
		t.Fatalf("data = %x, want %x", tab.Data, want)
	}
	if tab.Labels["L0"] != 0 || tab.Labels["L1"] != 8 {
		t.Fatalf("labels = %+v", tab.Labels)
	}
}

func TestCollectConstTablesHexNumericLiterals(t *testing.T) {
	lines := []string{
		".section .rodata",
		"L0:",
		".quad 0x7ff0000000000000",
		".quad 0xffffffffffffffff",
		".long 0xdeadbeef",
		".text",
	}
	tab := mustCollectTables(t, parseProgram(t, lines, "arm64"))[0]
	want := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x7f,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xef, 0xbe, 0xad, 0xde,
	}
	if !bytes.Equal(tab.Data, want) {
		t.Fatalf("data = %x, want %x", tab.Data, want)
	}
}

func TestCollectConstTablesStringEscapes(t *testing.T) {
	lines := []string{
		".section __TEXT,__cstring",
		"S:",
		".ascii \"A\\x42\\t\\\\\", not-a-literal",
		".asciz \"Z\\000a b;c//d##e @ f\" ; trailing comment",
		".byte 9",
		".section __TEXT,__text",
		".asciz \"ignored\"",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "arm64"))
	if len(tables) != 1 {
		t.Fatalf("tables = %d, want one", len(tables))
	}
	want := []byte{'A', 'B', '\t', '\\', 'Z', 0, 'a', ' ', 'b', ';', 'c', '/', '/', 'd', '#', '#', 'e', ' ', '@', ' ', 'f', 0, 9}
	if !bytes.Equal(tables[0].Data, want) {
		t.Fatalf("data = %x, want %x", tables[0].Data, want)
	}
}

func TestCollectConstTablesMalformedNumericDirectiveFails(t *testing.T) {
	lines := []string{
		".section .rodata",
		".byte not-a-literal",
		".text",
	}
	if _, err := collectConstTables(parseProgram(t, lines, "arm64")); err == nil ||
		!strings.Contains(err.Error(), "unsupported relocation expression") {
		t.Fatalf("malformed numeric directive error = %v", err)
	}
}

func TestCollectZeroFillAsWritableBSS(t *testing.T) {
	lines := []string{
		".zerofill __DATA,__bss,_heap_used,8,3",
		".zerofill __DATA,__bss,_heap,16,4",
		".text",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "arm64"))
	if len(tables) != 1 {
		t.Fatalf("tables = %d, want one", len(tables))
	}
	tab := tables[0]
	if !tab.Writable || len(tab.Data) != 32 || tab.Labels["_heap_used"] != 0 || tab.Labels["_heap"] != 16 {
		t.Fatalf("table = %+v", tab)
	}
	out := strings.Join(EmitTables(tables), "\n")
	if out != "GLOBL LCDATA1<>(SB), 16, $32" {
		t.Fatalf("BSS output = %q", out)
	}
}

func TestCollectConstTablesDropsEmptySectionsAndFlushes(t *testing.T) {
	lines := []string{
		".section .rodata",
		".space 0",
		".text",
		".section __TEXT,__literal4",
		"L1:",
		".long 1",
		".section __TEXT,__text",
		".section .cstring",
		"L2:",
		".ascii \"x\"",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "arm64"))
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want 2", len(tables))
	}
	if tables[0].Name != "LCDATA1" || tables[1].Name != "LCDATA2" {
		t.Fatalf("table names = %s, %s", tables[0].Name, tables[1].Name)
	}
}

func TestCollectEmptyStringKeepsDataLabel(t *testing.T) {
	lines := []string{
		".section .rodata",
		".LC0:",
		`.string ""`,
		`.string ""`,
		".text",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "amd64"))
	if len(tables) != 1 {
		t.Fatalf("tables = %d, want one", len(tables))
	}
	if got := tables[0].Data; !bytes.Equal(got, []byte{0, 0}) {
		t.Fatalf("empty strings = %x, want 0000", got)
	}
	if offset, ok := tables[0].Labels[".LC0"]; !ok || offset != 0 {
		t.Fatalf("labels = %+v", tables[0].Labels)
	}
}

func TestCollectELFDataDirectives(t *testing.T) {
	lines := []string{
		".section .rodata",
		"head:",
		".byte 1, 2",
		".align 8",
		"payload:",
		`.base64 "AwQ="`,
		".value -2, 5",
		".set alias, payload",
		".section .bss",
		"counter:",
		".zero 4",
		".comm shared, 8, 16",
		".text",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "amd64"))
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want rodata and bss", len(tables))
	}
	wantRO := []byte{1, 2, 0, 0, 0, 0, 0, 0, 3, 4, 0xfe, 0xff, 5, 0}
	if !bytes.Equal(tables[0].Data, wantRO) {
		t.Fatalf("rodata = %x, want %x", tables[0].Data, wantRO)
	}
	if tables[0].Labels["payload"] != 8 || tables[0].Labels["alias"] != 8 {
		t.Fatalf("rodata labels = %+v", tables[0].Labels)
	}
	if !tables[1].Writable || len(tables[1].Data) != 24 ||
		tables[1].Labels["counter"] != 0 || tables[1].Labels["shared"] != 16 {
		t.Fatalf("bss table = %+v", tables[1])
	}
}

func TestEmitTablesRelocationBindingAndPadding(t *testing.T) {
	tables := []*ConstTable{
		{Name: "LCDATA1", Labels: map[string]uint{"target": 8}, Data: make([]byte, 16), Relocs: []Reloc{{Offset: 0, Symbol: "target"}}},
		{Name: "LCDATA2", Labels: map[string]uint{}, Data: []byte{1}},
	}
	out := strings.Join(EmitTables(tables), "\n")
	if !strings.Contains(out, "DATA LCDATA1<>+0x000(SB)/8, $LCDATA1<>+0x8(SB)") {
		t.Fatalf("relocation binding missing: %s", out)
	}
	if !strings.Contains(out, "GLOBL LCDATA1<>(SB), 24, $16") ||
		!strings.Contains(out, "GLOBL LCDATA2<>(SB), 24, $8") {
		t.Fatalf("table padding missing: %s", out)
	}
}

func TestCollectMachODataSymbolWithLeadingUnderscore(t *testing.T) {
	lines := []string{
		".section __TEXT,__const",
		".globl __pcre2_default_tables_8",
		"__pcre2_default_tables_8:",
		".byte 1",
		".quad __pcre2_default_tables_8",
		".text",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "arm64"))
	if len(tables) != 1 || len(tables[0].Relocs) != 1 {
		t.Fatalf("tables = %+v, want one table with one relocation", tables)
	}
	out := strings.Join(EmitTables(tables), "\n")
	if !strings.Contains(out, "DATA LCDATA1<>+0x001(SB)/8, $LCDATA1<>(SB)") {
		t.Fatalf("Mach-O data symbol did not bind to its table: %s", out)
	}
}

func TestConstHelpersRejectMalformedValues(t *testing.T) {
	if got := parseStringLiteral(nil); got != nil {
		t.Fatalf("nil string args = %x", got)
	}
	if isNumericLiteral("") || isNumericLiteral("12x") || isNumericLiteral("-") {
		t.Fatal("malformed numeric literal accepted")
	}
	if got := alignPad([]byte{1}, &Directive{Args: []string{"bad"}}); got != nil {
		t.Fatalf("invalid alignment produced padding %x", got)
	}
	if got := len(alignPad([]byte{1}, &Directive{Name: "align", Args: []string{"8"}})); got != 7 {
		t.Fatalf(".align padding = %d, want 7", got)
	}
	if got := len(alignPad([]byte{1}, &Directive{Name: "p2align", Args: []string{"3"}})); got != 7 {
		t.Fatalf(".p2align padding = %d, want 7", got)
	}
}

func TestCollectELFRelocationExpressions(t *testing.T) {
	lines := []string{
		".section .rodata",
		"base:",
		".long 1",
		"target:",
		".long target - base",
		".quad target + 8",
		".set alias, target + 8",
		".text",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "amd64"))
	if len(tables) != 1 {
		t.Fatalf("tables = %d, want one", len(tables))
	}
	table := tables[0]
	if got := table.Data[4:8]; !bytes.Equal(got, []byte{4, 0, 0, 0}) {
		t.Fatalf("label difference = %x, want 04000000", got)
	}
	if table.Labels["alias"] != 12 {
		t.Fatalf("alias offset = %d, want 12", table.Labels["alias"])
	}
	if len(table.Relocs) != 1 || table.Relocs[0].Width != 8 ||
		table.Relocs[0].Symbol != "target" || table.Relocs[0].Addend != 8 {
		t.Fatalf("relocs = %+v", table.Relocs)
	}
	out := strings.Join(EmitTables(tables), "\n")
	if !strings.Contains(out, "DATA LCDATA1<>+0x008(SB)/8, $LCDATA1<>+0xc(SB)") {
		t.Fatalf("relocation addend missing: %s", out)
	}
}

func TestCollectELFAbsoluteNarrowRelocationFails(t *testing.T) {
	lines := []string{
		".section .rodata",
		"table:",
		".long target",
		"target:",
		".long 7",
		".text",
	}
	if _, err := collectConstTables(parseProgram(t, lines, "amd64")); err == nil ||
		!strings.Contains(err.Error(), "unsupported 4-byte absolute relocation") {
		t.Fatalf("narrow relocation error = %v", err)
	}
}

func TestCollectELFWritableRelocationSection(t *testing.T) {
	lines := []string{
		`.section .data.rel.local,"aw",@progbits`,
		"slot:",
		".quad target",
		".text",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "amd64"))
	if len(tables) != 1 || !tables[0].Writable {
		t.Fatalf("writable ELF table = %+v", tables)
	}
	if out := strings.Join(EmitTables(tables), "\n"); !strings.Contains(out, "GLOBL LCDATA1<>(SB), 16, $8") {
		t.Fatalf("writable table flags missing: %s", out)
	}
}

func TestCollectMachOWritableDataSection(t *testing.T) {
	lines := []string{
		".section __DATA,__data",
		"_mutable:",
		".long 1",
		".section __TEXT,__const",
		"_constant:",
		".long 2",
		".text",
	}
	tables := mustCollectTables(t, parseProgram(t, lines, "arm64"))
	if len(tables) != 2 || !tables[0].Writable || tables[1].Writable {
		t.Fatalf("Mach-O table mutability = %+v", tables)
	}
	out := strings.Join(EmitTables(tables), "\n")
	if !strings.Contains(out, "GLOBL LCDATA1<>(SB), 16, $8") ||
		!strings.Contains(out, "GLOBL LCDATA2<>(SB), 24, $8") {
		t.Fatalf("Mach-O table flags missing: %s", out)
	}
}
