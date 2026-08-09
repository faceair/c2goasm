# SIMD example: C kernels converted to Go assembly

This is the complete, runnable version of the [SIMD workflow](../..#典型场景在-go-里用上-simd) described in the project README: kernels written in C, vectorized by the C compiler, converted to Go Plan 9 assembly by c2goasm, and called directly from Go with `CGO_ENABLED=0`.

Two kernels are included, showing the two direct-safe shapes:

| Function | Shape | Instruction |
|---|---|---|
| `SumU32` | return value (`uint64_t`) | NEON `uaddlv`-style reduction / AVX2 `vpaddq` |
| `AddU32` | output pointer, no return | element-wise `add` |

## Layout

```text
c/simd.c           C source (in a subdirectory so Go never scans it)
simd.go            scalar baseline, shared by both builds
simd_arm64.go      arm64 companion + dispatch  (//go:build arm64 && !nosimd)
simd_amd64.go      amd64 companion + dispatch  (//go:build amd64 && !nosimd)
simd_generic.go    scalar public API          (//go:build nosimd)
simd_arm64.s       converted NEON assembly (generated, committed)
simd_amd64.s       converted AVX2 assembly (generated, committed)
simd_test.go       correctness tests
simd_bench_test.go generic-vs-SIMD speedup benchmark
generate.sh        regenerate the .s files
```

The converted `.s` files are committed, so the example builds and tests out of
the box without a C compiler. `generate.sh` regenerates them when the C source
or the converter changes.

This directory is its own Go module (`example.com/simd`), nested under the
main repository. That keeps the main module's `go vet ./...` from tripping
over the converted native bodies: a converted `_c2goasm_native_*` symbol is
defined in assembly with a C ABI frame and has no Go declaration, which
`asmdecl` would otherwise report. The full-graph E2E modules work the same
way.

## Run

```bash
go test ./...                  # correctness, on the SIMD build
go test -bench=. -run=^$       # scalar baseline + SIMD, reports speedup
go test -tags=nosimd ./...     # scalar fallback path
```

The benchmark runs the scalar baseline first and reports the SIMD speedup as
a metric, e.g.:

```text
BenchmarkSumU32/generic-10   3520   342617 ns/op   12241.96 MB/s
BenchmarkSumU32/simd-10     22992    51905 ns/op   80807.66 MB/s  speedup=6.60
```

Exact numbers depend on the CPU microarchitecture and memory bandwidth —
re-run on your target hardware.

## Regenerate

With `c2goasm` on `PATH` (`go install github.com/faceair/c2goasm/cmd/c2goasm@latest`):

```bash
C2GOASM_TARGET=arm64 ./generate.sh   # needs clang
C2GOASM_TARGET=amd64 ./generate.sh   # needs gcc, Linux/amd64
```

The generated `.s` files pair with the companion files by stem
(`simd_arm64.s` ↔ `simd_arm64.go`), which is how direct entries are
requested — see the project README for the direct-safe certification rules.
