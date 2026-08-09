# c2goasm

[English](README.md) | [简体中文](README.zh-CN.md)

[![CI](https://github.com/faceair/c2goasm/actions/workflows/ci.yml/badge.svg)](https://github.com/faceair/c2goasm/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

`c2goasm` converts compiler-generated 64-bit C/C++ assembly into Go Plan 9 assembly while preserving the platform C ABI. In plain terms: you write performance-critical code in C, let Clang or GCC handle instruction selection and vectorization, and c2goasm turns the compiler's assembly into Go assembly that builds straight into your program.

Depending on the shape of the function, there are two ways to wire it in:

- **Direct-safe leaf:** a pure-compute function with no stack frame and no calls gets a Go ABI wrapper after static certification, and runs with `CGO_ENABLED=0`.
- **Native graph:** complete C libraries like QuickJS, SQLite, or PCRE2 enter through a standard cgo trampoline; once inside, the whole internal call graph executes as native C ABI calls.

The two modes are deliberately distinct, with no hidden fallback: if a direct entry is requested but cannot be certified, conversion fails and names the function, the source line, and the reason.

## The headline use case: SIMD in Go

The Go compiler does not auto-vectorize today, so getting NEON/AVX performance in Go usually means picking between three uncomfortable options:

- **Writing Plan 9 assembly by hand:** a steep learning curve, plus you maintain register allocation, instruction encoding, and ABI details yourself;
- **Plain Go loops and hoping:** scalar execution, no SIMD speedup;
- **cgo into a C library:** per-call cross-language overhead, and the target machine needs a cgo toolchain.

`c2goasm` offers a fourth path: **write the kernel in C, let the C compiler generate SIMD assembly, then convert it to Go assembly.** C compilers have been optimizing SIMD for decades — auto-vectorization, intrinsics, and loop unrolling are all extremely mature — so you can focus on the algorithm instead of the plumbing.

The workflow looks like this:

```text
kernel.c（intrinsics 或普通循环）
   │  clang/gcc -O3
   ▼
kernel.s（C ABI 汇编）
   │  c2goasm
   ▼
kernel.s（Go Plan 9 汇编，编译进你的包）
```

### A minimal runnable example

The complete runnable example lives in [`examples/simd/`](examples/simd/) — converted assembly for both architectures is committed there, so you can `go test` it without a C toolchain. The code below is the same example, step by step.

Write two kernels in C: a sum (showing the return-value form) and an element-wise add (showing the output-pointer form):

```c
// simd.c
#include <stdint.h>

// Return-value form: uint64_t is a single word result, supported
// directly by the direct-safe certification.
uint64_t sum_u32(const uint32_t *input, uint64_t size) {
    uint64_t sum = 0;
    #pragma clang loop vectorize(enable) interleave(enable)
    for (uint64_t i = 0; i < size; i++) {
        sum += input[i];
    }
    return sum;
}

// Output-pointer form: writes back to dst, no return value, good for
// batch kernels.
void add_u32(const uint32_t *a, const uint32_t *b, uint32_t *dst, uint64_t size) {
    #pragma clang loop vectorize(enable) interleave(enable)
    for (uint64_t i = 0; i < size; i++) {
        dst[i] = a[i] + b[i];
    }
}
```

`#pragma clang loop vectorize(enable)` explicitly requests vectorization; both Clang and GCC honor it. Keep the source file in an `input/` subdirectory so the Go toolchain never treats the `.c`/`.s` inputs as package source.

The bodyless functions (`_sum_u32`, `_add_u32`) in the companion Go file are the conversion entry points: they explicitly ask c2goasm to generate a Go ABI wrapper for the corresponding C symbols.

```go
//go:build arm64 && !nosimd

package simd

import (
    "runtime"
    "unsafe"
)

func _sum_u32(input unsafe.Pointer, size uint64) uint64
func _add_u32(a unsafe.Pointer, b unsafe.Pointer, dst unsafe.Pointer, size uint64)

func SumU32(input []uint32) uint64 {
    if len(input) == 0 {
        return 0
    }
    result := _sum_u32(unsafe.Pointer(&input[0]), uint64(len(input)))
    runtime.KeepAlive(input)
    return result
}

func AddU32(dst, a, b []uint32) {
    _add_u32(unsafe.Pointer(&a[0]), unsafe.Pointer(&b[0]), unsafe.Pointer(&dst[0]), uint64(len(a)))
    runtime.KeepAlive(a)
    runtime.KeepAlive(b)
    runtime.KeepAlive(dst)
}
```

On AMD64 use the same companion signatures in a separate file with the build tag `//go:build amd64 && !nosimd`. Provide a plain scalar fallback for `-tags=nosimd` builds (again via build tags, e.g. a `generic.go` with `//go:build nosimd`), so CPUs without SIMD still work:

```go
//go:build nosimd

package simd

func SumU32(input []uint32) uint64 {
    var sum uint64
    for _, v := range input {
        sum += uint64(v)
    }
    return sum
}

func AddU32(dst, a, b []uint32) {
    for i, v := range a {
        dst[i] = v + b[i]
    }
}
```

Compile and convert (Darwin/arm64, NEON):

```bash
clang -S -O3 \
  -fomit-frame-pointer -fno-stack-protector \
  -fno-asynchronous-unwind-tables -fno-unwind-tables \
  -mno-outline -ffixed-x27 -ffixed-x28 \
  --target=arm64-apple-darwin \
  -o input/simd.s simd.c

c2goasm -t arm64 input/simd.s simd.s
CGO_ENABLED=0 go test ./...
```

Linux/amd64 (AVX2) works the same way:

```bash
gcc -S -O3 -mavx2 -masm=intel \
  -fomit-frame-pointer -fno-stack-protector -mno-red-zone \
  -fno-asynchronous-unwind-tables -fno-unwind-tables \
  -ffixed-rbp -ffixed-r14 -ffixed-r11 \
  -o input/simd.s simd.c

c2goasm -t amd64 input/simd.s simd.s
CGO_ENABLED=0 go test ./...
```

`-ffixed-x27 -ffixed-x28` (ARM64) and `-ffixed-rbp -ffixed-r14 -ffixed-r11` (AMD64) reserve the Go runtime's registers so the C compiler stays clear of them. The compiler must actually produce a frame-free leaf — optimization flags alone don't guarantee it, and the converter verifies the generated instructions one by one.

### How it performs

On an Apple M1 Pro (arm64/NEON) with 4 MiB inputs, `sum_u32` is roughly 6x faster than the plain Go scalar loop and `add_u32` about 3x, with zero allocations and `CGO_ENABLED=0` throughout. AMD64 (AVX2, GCC 15) shows a similar 2–3x improvement.

Exact numbers depend on the CPU microarchitecture and memory bandwidth: bandwidth-sensitive reductions gain more where bandwidth is plentiful, while element-wise ops are bounded by total bytes read and written. The benchmark in [`examples/simd/simd_bench_test.go`](examples/simd/simd_bench_test.go) pairs generic against SIMD and reports a speedup metric, so you can re-measure on your target hardware. This "scalar baseline + build-tag fallback + speedup comparison" layout is already in production use, e.g. in the GuanceDB executor.

## Direct-safe leaf: the boundary

The direct mode only accepts certified pure-compute leaves, and the wrapper is emitted only when certification passes:

- No stack-frame operations: must not read or write SP, or use stack memory or the red zone;
- No calls, indirect jumps, or control flow outside the function's own labels;
- No TLS, system registers, syscalls, or registers reserved by the Go runtime;
- Arguments must be integers or pointers (up to 6 on AMD64, 8 on ARM64), with at most one word-sized result of the same kind;
- The companion signature must match the C prototype exactly, and Go memory passed in must stay alive until return (use `runtime.KeepAlive` when in doubt).

Conversion fails outright when these conditions are not met. For floating-point arguments, aggregates, multiple results, allocators, or callbacks, use the native-graph mode below.

## Complete C libraries: native graph

Libraries like QuickJS, SQLite, and PCRE2 have dynamic stack frames, deep call graphs, allocators, and pthread state — they cannot run on a goroutine stack. Convert the whole module, then enter it through the small set of entry points in `github.com/faceair/c2goasm/nativecall`:

- `Call0(address uintptr) int32`, `CallBytes(address uintptr, value []byte) int32`
- `InstallMemory(address uintptr)`: inject the process allocator
- `InstallThreads(address uintptr)`: inject pthread mutex operations

Ready-to-run integrations:

- `scripts/quickjs-e2e.sh`, `scripts/pcre2-e2e.sh`, `scripts/sqlite-e2e.sh`, `scripts/amd64-cutils-e2e.sh`

The PCRE2 example converts all 31 core 8-bit translation units (Unicode on, JIT off) and exercises configuration, table generation, UTF/UCP matching, compile errors, and concurrent calls. These scripts are reference integrations — converting arbitrary C source still requires adapting its host boundary (libc, locale, threading model).

## Why c2goasm

- **Let the C compiler do what it does best:** instruction selection, register allocation, and vectorization stay with Clang/GCC instead of being hand-translated into Plan 9 assembly.
- **Reproducible assembly:** the same C source and flags always produce the same Plan 9 assembly — no maintenance drift from handwritten asm.
- **Pick the right runtime boundary per function:** small leaves run entirely without cgo; complete C libraries enter through standard cgo rather than pretending a goroutine stack is a C stack.
- **Fail fast:** unresolved relocations, unsupported instructions, symbol collisions, ABI overflows, and uncertifiable direct entries stop conversion with the function name, source line, and reason. No silent degradation.
- **Structured pipeline:** `parse → analyze → rewrite → encode → emit` works on an IR rather than regex substitution, which keeps it auditable and extensible.
- **No shared library to ship:** converted code links into the Go binary; native-graph mode needs cgo but never requires distributing a project `.so`/`.dylib`.
- **Two production architectures:** GCC Intel syntax (AMD64) and Clang GNU syntax (ARM64) are both first-class paths.

## Relationship to minio/c2goasm

This project continues the idea from the archived [minio/c2goasm](https://github.com/minio/c2goasm): compile C/C++ to assembly, then convert that assembly into something the Go assembler accepts. It is an independent rewrite, not a compatibility fork.

| Area | minio/c2goasm | github.com/faceair/c2goasm |
|---|---|---|
| Architecture | AMD64 | AMD64 and ARM64 |
| Compiler input | Primarily Clang Intel syntax | GCC Intel syntax on AMD64; Clang GNU syntax on ARM64 |
| Conversion model | Line-oriented conversion with asm2plan9s | Structured IR, program-wide label/relocation rewriting, three-stage encoding |
| Internal calls | Generally expected to be inlined/call-free | Full internal C call graphs in native-graph mode |
| Direct entry | C-like code emitted as a Go assembly function | Only explicitly requested leaves that pass static certification |
| Runtime boundary | Centered on direct wrappers | cgo-free leaf and standard-cgo native graph kept separate |
| Verification | Focused SIMD examples | Direct leaf, QuickJS, SQLite, PCRE2, cutils, concurrency, GC, stack-growth E2E |
| Failure model | Converts its documented subset | Contextual fail-fast; no legacy or unsafe fallback |
| License | Apache-2.0 | Apache-2.0 |

## c2goasm versus cgo

`c2goasm` is not a general replacement for cgo — they solve different problems.

| Property | Direct-safe leaf | Native graph | Direct cgo |
|---|---|---|---|
| `CGO_ENABLED=0` | Yes | No | No |
| Cross-language transition | None | Yes (`runtime.cgocall`) | Yes |
| Stack used by C code | Current goroutine stack (hence frame-free only) | cgo/system stack | cgo/system stack |
| C calls, TLS, errno, allocators, pthread | Rejected | Supported through the declared native boundary | Supported |
| Project shared library | Not needed | Not needed | Depends on linking |
| Best fit | Small bounded compute/scan | Converted engines and complete modules | Ordinary C interop |

One honest caveat: native-graph mode does not remove the Go→C transition cost (it still goes through `runtime.cgocall`). Its value is packaging converted code into the Go build and keeping the entire internal call graph native C ABI after a single entry. Direct cgo also calls C-to-C natively once inside C; this project does not claim otherwise.

## Supported toolchains

- Go 1.26
- `amd64`: System V AMD64 ABI, GCC Intel assembly
- `arm64`: AAPCS64, Clang GNU assembly
- Tested hosts: Linux/amd64 and Darwin/arm64
- Python 3 is used only by integration scripts that download and prepare upstream test sources

## Install

```bash
go install github.com/faceair/c2goasm/cmd/c2goasm@latest
```

Or build from a checkout:

```bash
git clone https://github.com/faceair/c2goasm.git
cd c2goasm
go build -o ./bin/c2goasm ./cmd/c2goasm
```

## CLI

```text
c2goasm [-t amd64|arm64] [-s] [-c] [-f] input.s output.s
```

| Option | Meaning |
|---|---|
| `-t` | Input architecture. Defaults to `amd64`; accepts `amd64`/`x86`/`x86_64`/`x86-64` and `arm64`/`aarch64`. |
| `-s` | Strip generated instruction comments. |
| `-c` | Compact adjacent byte literals. |
| `-f` | Run `asmfmt` from `PATH` on the output. |
| `-a` | Compatibility no-op; instruction encoding is always enabled. |

The output must end in `.s`, and a companion Go file with the same stem must sit next to it (e.g. `kernel.s` pairs with `kernel.go`). The companion supplies the Go declarations and makes every direct entry an explicit request.

## Input constraints

The accepted input is a compiler-generated 64-bit GNU/Intel assembly subset, not arbitrary handwritten assembly:

- Function boundaries must be visible through `.globl`, ELF `.size`, or Clang `Begin function` markers;
- Jump tables must be disabled; stack protectors, unwind tables, and compiler outlining should be disabled;
- ARM64 input must reserve X27/X28; AMD64 must reserve RBP/R14 (and R11 for complete graphs);
- `setjmp`/`longjmp`, C++ exceptions/unwind, Go callbacks, dynamic-loader semantics, and arbitrary TLS are unsupported;
- Every normalized Go/native symbol must be unique; collisions stop conversion.

Compiler flags are part of the ABI contract: start from the flags in the E2E scripts, and change them only with matching verification.

## Tests and CI

Fast local checks:

```bash
go test -count=1 ./...
go vet ./...
CGO_ENABLED=0 go build ./cmd/c2goasm
```

Native integration gates:

```bash
./scripts/direct-leaf-e2e.sh
./scripts/quickjs-e2e.sh
./scripts/pcre2-e2e.sh
./scripts/sqlite-e2e.sh          # Darwin/arm64
./scripts/amd64-cutils-e2e.sh    # Linux/amd64
```

Large third-party sources are not stored in the repository. Integration scripts download pinned official releases — QuickJS 2026-06-04, PCRE2 10.47, SQLite 3.48.0 — verify SHA-256, and extract them under:

```text
${C2GOASM_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/c2goasm}/sources
```

`QUICKJS_SOURCE_DIR`, `PCRE2_SOURCE_DIR`, and `SQLITE_SOURCE_DIR` can point at existing verified checkouts. Generated assembly and test modules live only in temporary directories. GitHub Actions runs the unit/build checks and the full native matrix on Linux/amd64 and Darwin/arm64.

## Repository layout

```text
arch/                 Platform C ABI and register descriptions
cmd/c2goasm/          CLI
examples/simd/        Runnable SIMD example (C kernels -> converted asm -> Go API)
internal/asm/         Parsing, IR, analysis, rewriting, direct certification, emission
internal/asm2plan9s/  Instruction assembly, decoding, and byte fallback
nativecall/           Standard-cgo native entry, allocator, and pthread boundary
scripts/              Reproducible direct and full-graph E2E gates
.github/workflows/    Linux/amd64 and Darwin/arm64 CI
```

## Contributing

Bug reports and focused pull requests are welcome. For conversion failures, include the compiler version, target architecture, the offending source instruction, and the smallest reproducible input. New instruction or relocation support should come with a behavioral or encoding regression test and must preserve fail-fast behavior.

## License

[Apache-2.0](LICENSE) © 2026 faceair.

Third-party sources downloaded by integration tests remain under their respective upstream licenses.
