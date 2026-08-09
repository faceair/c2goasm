package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgsPreservesCLIContract(t *testing.T) {
	opts, args, err := parseArgs([]string{"-a", "-c", "-f", "-s", "-t", "arm64", "input.s", "output.s"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Assemble || !opts.Compact || !opts.Format || !opts.Strip || opts.Target != "arm64" {
		t.Fatalf("options = %+v", opts)
	}
	if strings.Join(args, ",") != "input.s,output.s" {
		t.Fatalf("args = %q", args)
	}
}

func TestCompactOpcodesPreservesBytesAcrossLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code.s")
	input := strings.Join([]string{
		"TEXT ·f(SB), $0-0",
		"    LONG $0x04030201; WORD $0x0605; BYTE $0x07 // mov x0, #0xdead",
		"    LONG $0x0b0a0908 // another instruction",
		"    RET",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := compactOpcodes(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"TEXT ·f(SB), $0-0",
		"    QUAD $0x0807060504030201",
		"    WORD $0x0a09; BYTE $0x0b",
		"    RET",
		"",
	}, "\n")
	if string(data) != want {
		t.Fatalf("compacted output:\n%s\nwant:\n%s", data, want)
	}
}

func TestCompactOpcodesRejectsMalformedLiteral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.s")
	if err := os.WriteFile(path, []byte("    LONG $0xnothex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := compactOpcodes(path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("error = %v, want source line context", err)
	}
}

func TestFormatAssemblyInvokesAsmfmt(t *testing.T) {
	dir := t.TempDir()
	asmfmt := filepath.Join(dir, "asmfmt")
	script := "#!/bin/sh\nprintf '\\n// formatted\\n' >> \"$2\"\n"
	if err := os.WriteFile(asmfmt, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	path := filepath.Join(dir, "code.s")
	if err := os.WriteFile(path, []byte("RET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := formatAssembly(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "// formatted") {
		t.Fatalf("asmfmt did not update file: %q", data)
	}
}

func TestRunUsesBodylessCompanionAsDirectLeafContract(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "input.s")
	destination := filepath.Join(dir, "leaf.s")
	companion := filepath.Join(dir, "leaf.go")
	if err := os.WriteFile(source, []byte(strings.Join([]string{
		".globl foo",
		"foo:",
		"ret",
		".size foo, .-foo",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		companion,
		[]byte("package leaf\nfunc _foo(value uintptr) uintptr\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := Run(Options{Target: "arm64"}, []string{source, destination}); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(output)
	for _, want := range []string{
		"TEXT ·_c2goasm_native_foo(SB), 516, $0-0",
		"TEXT ·_foo(SB), 4, $0-16",
		"MOVD value+0(FP), R0",
		"CALL ·_c2goasm_native_foo(SB)",
		"MOVD R0, ret+8(FP)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("output missing %q:\n%s", want, joined)
		}
	}
}

func TestRunRejectsUnsafeDirectLeafCompanion(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "input.s")
	destination := filepath.Join(dir, "leaf.s")
	if err := os.WriteFile(source, []byte(strings.Join([]string{
		".globl foo",
		"foo:",
		"sub sp, sp, #16",
		"add sp, sp, #16",
		"ret",
		".size foo, .-foo",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "leaf.go"),
		[]byte("package leaf\nfunc _foo()\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	err := Run(Options{Target: "arm64"}, []string{source, destination})
	if err == nil || !strings.Contains(err.Error(), "stack-pointer") {
		t.Fatalf("error = %v, want direct-leaf stack rejection", err)
	}
}
