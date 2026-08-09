package asm

import (
	"strings"
	"testing"

	"github.com/faceair/c2goasm/arch"
)

func TestDirectWrapperArchitectureMatrix(t *testing.T) {
	entry := &directEntry{
		goName:     "_sum",
		nativeName: "sum",
		args: []directParam{
			{name: "left"},
			{name: "right"},
		},
		results: []directParam{{name: "result"}},
	}
	tests := []struct {
		name string
		desc arch.Descriptor
		want []string
	}{
		{
			name: "amd64",
			desc: arch.AMD64(),
			want: []string{
				"TEXT ·_sum(SB), 4, $0-24",
				"MOVQ left+0(FP), DI",
				"MOVQ right+8(FP), SI",
				"CALL ·_c2goasm_native_sum(SB)",
				"PXOR X15, X15",
				"MOVQ AX, result+16(FP)",
			},
		},
		{
			name: "arm64",
			desc: arch.ARM64(),
			want: []string{
				"TEXT ·_sum(SB), 4, $0-24",
				"MOVD left+0(FP), R0",
				"MOVD right+8(FP), R1",
				"CALL ·_c2goasm_native_sum(SB)",
				"MOVD R0, result+16(FP)",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			function := &Function{Name: "sum", direct: entry}
			joined := strings.Join(emitDirectWrapper(function, test.desc), "\n")
			for _, want := range test.want {
				if !strings.Contains(joined, want) {
					t.Errorf("wrapper missing %q:\n%s", want, joined)
				}
			}
		})
	}
}

func TestProcessDirectLeafRejectsUnprovableEntries(t *testing.T) {
	tests := []struct {
		name      string
		desc      arch.Descriptor
		lines     []string
		companion string
		want      string
	}{
		{
			name: "stack mutation",
			lines: []string{
				".globl foo", "foo:", "sub sp, sp, #16", "add sp, sp, #16", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "stack-pointer",
		},
		{
			name: "32-bit stack mutation",
			lines: []string{
				".globl foo", "foo:", "add wsp, wsp, #16", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "stack-pointer",
		},
		{
			name: "call",
			lines: []string{
				".globl foo", "foo:", "bl bar", "ret", ".size foo, .-foo",
				".globl bar", "bar:", "ret", ".size bar, .-bar",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "function calls are not allowed",
		},
		{
			name: "tail call",
			lines: []string{
				".globl foo", "foo:", "b bar", ".size foo, .-foo",
				".globl bar", "bar:", "ret", ".size bar, .-bar",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "non-local control target",
		},
		{
			name: "reserved register",
			lines: []string{
				".globl foo", "foo:", "mov x28, x0", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo(value uintptr) uintptr\n",
			want:      "reserved register R28",
		},
		{
			name: "arm64 platform register",
			lines: []string{
				".globl foo", "foo:", "mov x18, x0", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo(value uintptr) uintptr\n",
			want:      "reserved register R18",
		},
		{
			name: "amd64 TLS segment",
			desc: arch.AMD64(),
			lines: []string{
				".globl foo", "foo:", "mov rax, qword ptr fs:[0]", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo() uintptr\n",
			want:      "thread",
		},
		{
			name: "stack argument",
			lines: []string{
				".globl foo", "foo:", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo(a, b, c, d, e, f, g, h, i uintptr)\n",
			want:      "cannot pass 9 word arguments entirely in registers",
		},
		{
			name: "missing native",
			lines: []string{
				".globl foo", "foo:", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _missing()\n",
			want:      "native function \"missing\" not found",
		},
		{
			name: "static native",
			lines: []string{
				".type foo,@function", "foo:", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "is not source-exported",
		},
		{
			name: "amd64 implicit stack alias",
			desc: arch.AMD64(),
			lines: []string{
				".globl foo", "foo:", "pushfd", "popfd", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "implicit stack-pointer",
		},
		{
			name: "amd64 stack-relative memory",
			desc: arch.AMD64(),
			lines: []string{
				".globl foo", "foo:", "mov rax, QWORD PTR [rsp-8]", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo() uintptr\n",
			want:      "stack-relative memory",
		},
		{
			name: "amd64 far return",
			desc: arch.AMD64(),
			lines: []string{
				".globl foo", "foo:", "lretq", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "implicit stack-pointer",
		},
		{
			name: "amd64 TLS base",
			desc: arch.AMD64(),
			lines: []string{
				".globl foo", "foo:", "rdfsbase rax", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "thread/runtime register",
		},
		{
			name: "amd64 reserved register",
			desc: arch.AMD64(),
			lines: []string{
				".globl foo", "foo:", "mov r14, rdi", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo(value uintptr)\n",
			want:      "reserved register R14",
		},
		{
			name: "amd64 stack argument",
			desc: arch.AMD64(),
			lines: []string{
				".globl foo", "foo:", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo(a, b, c, d, e, f, g uintptr)\n",
			want:      "cannot pass 7 word arguments entirely in registers",
		},
		{
			name: "amd64 hardware transaction",
			desc: arch.AMD64(),
			lines: []string{
				".globl foo", "foo:", "xbegin Lfallback", "Lfallback:", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "hardware transaction control",
		},
		{
			name: "arm64 authenticated call",
			lines: []string{
				".globl foo", "foo:", "blraaz x0", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "function calls are not allowed",
		},
		{
			name: "arm64 authenticated tail call",
			lines: []string{
				".globl foo", "foo:", "braaz x0", "ret", ".size foo, .-foo",
			},
			companion: "package p\nfunc _foo()\n",
			want:      "indirect tail control transfer",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			desc := test.desc
			if desc.Name() == "" {
				desc = arch.ARM64()
			}
			_, err := Process(test.lines, []byte(test.companion), desc, &functionStateEncoder{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProcessDirectLeafAllowsLocalControlFlow(t *testing.T) {
	tests := []struct {
		name  string
		desc  arch.Descriptor
		lines []string
	}{
		{
			name: "arm64",
			desc: arch.ARM64(),
			lines: []string{
				".globl choose",
				"choose:",
				"cbz x0, Lzero",
				"add x0, x0, x1",
				"ret",
				"Lzero:",
				"mov x0, x1",
				"ret",
				".size choose, .-choose",
			},
		},
		{
			name: "amd64",
			desc: arch.AMD64(),
			lines: []string{
				".globl choose",
				"choose:",
				"test rdi, rdi",
				"je Lzero",
				"lea rax, [rdi+rsi]",
				"ret",
				"Lzero:",
				"mov rax, rsi",
				"ret",
				".size choose, .-choose",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := Process(
				test.lines,
				[]byte("package p\nfunc _choose(value, fallback uintptr) uintptr\n"),
				test.desc,
				&functionStateEncoder{},
			)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(out, "\n")
			if !strings.Contains(joined, "TEXT ·_choose(SB), 4, $0-24") {
				t.Fatalf("certified wrapper missing:\n%s", joined)
			}
		})
	}
}
