package asm

import (
	"reflect"
	"strings"
	"testing"
)

func parseProgram(t *testing.T, lines []string, target string) *Program {
	t.Helper()
	program, err := ParseSource(lines, target)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func TestFindFunctionsELF(t *testing.T) {
	lines := []string{
		".text",
		".globl pstrcpy",
		"pstrcpy:",
		"mov rdi, rsi",
		"ret",
		".size pstrcpy, .-pstrcpy",
		".globl dbuf_claim",
		"dbuf_claim:",
		"add rdi, rsi",
		"ret",
		".size dbuf_claim, .-dbuf_claim",
	}
	functions := findFunctions(parseProgram(t, lines, "amd64").Nodes)
	if len(functions) != 2 {
		t.Fatalf("got %d functions", len(functions))
	}
	if functions[0].name != "pstrcpy" || !functions[0].export {
		t.Errorf("functions[0]: %+v", functions[0])
	}
	if functions[1].name != "dbuf_claim" || !functions[1].export {
		t.Errorf("functions[1]: %+v", functions[1])
	}
	if functions[0].labelIndex >= functions[1].labelIndex {
		t.Errorf("label order wrong: %d vs %d", functions[0].labelIndex, functions[1].labelIndex)
	}
}

func TestFindFunctionsExcludesELFObjectsAndBoundsBody(t *testing.T) {
	lines := []string{
		".text",
		".globl decode",
		".type decode,@function",
		"decode:",
		"add rax, rsi",
		"ret",
		".size decode, .-decode",
		".section .rodata",
		".globl utf8_min_code",
		".type utf8_min_code, @object",
		".size utf8_min_code, 20",
		"utf8_min_code:",
		".long 128",
		"0:",
		".long 2048",
	}
	program := parseProgram(t, lines, "amd64")
	found := findFunctions(program.Nodes)
	if len(found) != 1 || found[0].name != "decode" {
		t.Fatalf("functions = %+v, want decode only", found)
	}
	if found[0].endIndex <= found[0].labelIndex {
		t.Fatalf("function bounds = %+v", found[0])
	}
	functions, err := analyze(program)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range functions[0].Body {
		if label, ok := node.(*LabelLine); ok && (label.Name == "utf8_min_code" || label.Name == "0") {
			t.Fatalf("data label %q leaked into function body", label.Name)
		}
	}
}

func TestFindFunctionsBeginComments(t *testing.T) {
	lines := []string{
		".globl _pstrcpy ; -- Begin function pstrcpy",
		"_pstrcpy:",
		"ret",
		".p2align 2 ; -- Begin function __dbuf_put_u16",
		"___dbuf_put_u16:",
		"ret",
	}
	functions := findFunctions(parseProgram(t, lines, "arm64").Nodes)
	if len(functions) != 2 {
		t.Fatalf("got %d functions", len(functions))
	}
	if functions[0].name != "_pstrcpy" || !functions[0].export {
		t.Errorf("functions[0]: %+v", functions[0])
	}
	if functions[1].name != "___dbuf_put_u16" || functions[1].export {
		t.Errorf("functions[1]: %+v", functions[1])
	}
}

func TestFindFunctionsBeginCommentsWithUnprefixedELFLabels(t *testing.T) {
	lines := []string{
		".globl int64_neon_sum // -- Begin function int64_neon_sum",
		"int64_neon_sum:",
		"add x0, x0, x1",
		"ret",
		".size int64_neon_sum, .-int64_neon_sum",
		".globl int64_neon_min // -- Begin function int64_neon_min",
		"int64_neon_min:",
		"sub x0, x0, x1",
		"ret",
		".size int64_neon_min, .-int64_neon_min",
	}
	functions, err := analyze(parseProgram(t, lines, "arm64"))
	if err != nil {
		t.Fatal(err)
	}
	if len(functions) != 2 {
		t.Fatalf("got %d functions, want 2", len(functions))
	}
	if got := instructionTexts(functions[0].Body); !reflect.DeepEqual(got, []string{"add x0, x0, x1", "ret"}) {
		t.Fatalf("first function body = %q", got)
	}
	if got := instructionTexts(functions[1].Body); !reflect.DeepEqual(got, []string{"sub x0, x0, x1", "ret"}) {
		t.Fatalf("second function body = %q", got)
	}
}

func TestFindFunctionsDedupePrefixedBeginCommentAndELFSize(t *testing.T) {
	lines := []string{
		".globl _foo // -- Begin function _foo",
		"_foo:",
		"add x0, x0, x1",
		"ret",
		".size _foo, .-_foo",
	}
	functions, err := analyze(parseProgram(t, lines, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if len(functions) != 1 || functions[0].Name != "_foo" {
		t.Fatalf("functions = %+v, want one _foo function", functions)
	}
	if got := instructionTexts(functions[0].Body); !reflect.DeepEqual(got, []string{"add x0, x0, x1", "ret"}) {
		t.Fatalf("function body = %q", got)
	}
}

func TestFindFunctionsDedupe(t *testing.T) {
	lines := []string{
		".globl foo",
		"// -- Begin function foo",
		"foo:",
		"ret",
		".size foo, .-foo",
	}
	functions := findFunctions(parseProgram(t, lines, "amd64").Nodes)
	if len(functions) != 1 {
		t.Fatalf("got %d functions, want 1", len(functions))
	}
}

func TestAnalyzePreservesNativeInstructions(t *testing.T) {
	tests := []struct {
		name   string
		target string
		lines  []string
		want   []string
	}{
		{
			name:   "amd64-frame-and-cet",
			target: "amd64",
			lines: []string{
				".globl foo",
				"foo:",
				"endbr64",
				"push rbp",
				"mov rbp, rsp",
				"sub rsp, 32",
				"add rax, rsi",
				"add rsp, 32",
				"pop rbp",
				"rep ret",
				".size foo, .-foo",
			},
			want: []string{
				"endbr64",
				"push rbp",
				"mov rbp, rsp",
				"sub rsp, 32",
				"add rax, rsi",
				"add rsp, 32",
				"pop rbp",
				"rep ret",
			},
		},
		{
			name:   "arm64-multi-pair-frame",
			target: "arm64",
			lines: []string{
				".globl foo",
				"foo:",
				"stp x22, x21, [sp, #-48]!",
				"stp x20, x19, [sp, #16]",
				"stp x29, x30, [sp, #32]",
				"mov x29, sp",
				"sub sp, sp, #64",
				"mov x19, x1",
				"add sp, sp, #64",
				"ldp x29, x30, [sp, #32]",
				"ldp x20, x19, [sp, #16]",
				"ldp x22, x21, [sp], #48",
				"ret",
				".size foo, .-foo",
			},
			want: []string{
				"stp x22, x21, [sp, #-48]!",
				"stp x20, x19, [sp, #16]",
				"stp x29, x30, [sp, #32]",
				"mov x29, sp",
				"sub sp, sp, #64",
				"mov x19, x1",
				"add sp, sp, #64",
				"ldp x29, x30, [sp, #32]",
				"ldp x20, x19, [sp, #16]",
				"ldp x22, x21, [sp], #48",
				"ret",
			},
		},
		{
			name:   "control-dependent-adjustment",
			target: "arm64",
			lines: []string{
				".globl foo",
				"foo:",
				"cbz x0, .Lreturn",
				"sub sp, sp, #32",
				"add x0, x0, x1",
				"add sp, sp, #32",
				".Lreturn:",
				"ret",
				".size foo, .-foo",
			},
			want: []string{
				"cbz x0, .Lreturn",
				"sub sp, sp, #32",
				"add x0, x0, x1",
				"add sp, sp, #32",
				"ret",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			functions, err := analyze(parseProgram(t, test.lines, test.target))
			if err != nil {
				t.Fatal(err)
			}
			if len(functions) != 1 {
				t.Fatalf("got %d functions, want 1", len(functions))
			}
			if got := instructionTexts(functions[0].Body); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("native instructions changed:\n got: %q\nwant: %q", got, test.want)
			}
		})
	}
}

func TestAnalyzeExportsAndInternal(t *testing.T) {
	lines := []string{
		".globl exported_fn",
		"exported_fn:",
		"add x0, x0, x1",
		"ret",
		".size exported_fn, .-exported_fn",
		"static_fn:",
		"sub x0, x0, x1",
		"ret",
		".size static_fn, .-static_fn",
	}
	functions, err := analyze(parseProgram(t, lines, "arm64"))
	if err != nil {
		t.Fatal(err)
	}
	if len(functions) != 2 {
		t.Fatalf("got %d functions", len(functions))
	}
	if !functions[0].Export || functions[0].Name != "exported_fn" {
		t.Errorf("functions[0]: export=%v name=%s", functions[0].Export, functions[0].Name)
	}
	if functions[1].Export || functions[1].Name != "static_fn" {
		t.Errorf("functions[1]: export=%v name=%s", functions[1].Export, functions[1].Name)
	}
}

func TestAnalyzeRejectsGoSymbolCollision(t *testing.T) {
	lines := []string{
		".globl foo.bar",
		"foo.bar:",
		"ret",
		".size foo.bar, .-foo.bar",
		".globl foo_bar",
		"foo_bar:",
		"ret",
		".size foo_bar, .-foo_bar",
	}
	_, err := analyze(parseProgram(t, lines, "amd64"))
	if err == nil || !strings.Contains(err.Error(), "both map to Go assembly symbol \"foo_bar\"") {
		t.Fatalf("collision error = %v", err)
	}
}

func TestAnalyzeRejectsNativeSymbolCollision(t *testing.T) {
	lines := []string{
		".globl foo",
		"foo:",
		"ret",
		".size foo, .-foo",
		".globl __c2goasm_native_foo",
		"__c2goasm_native_foo:",
		"ret",
		".size __c2goasm_native_foo, .-__c2goasm_native_foo",
	}
	_, err := analyze(parseProgram(t, lines, "amd64"))
	if err == nil || !strings.Contains(err.Error(), "both map to native Go assembly symbol \"_c2goasm_native_foo\"") {
		t.Fatalf("native collision error = %v", err)
	}
}

func instructionTexts(nodes []Node) []string {
	instructions := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if instruction, ok := node.(*Inst); ok {
			instructions = append(instructions, strings.TrimSpace(instruction.Raw))
		}
	}
	return instructions
}
