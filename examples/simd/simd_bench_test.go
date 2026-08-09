package simd

import "testing"

const benchSize = 1 << 20 // 4 MiB of uint32 input

// BenchmarkSumU32 and BenchmarkAddU32 run the scalar baseline first, then the
// converted SIMD kernel, and report the speedup as a metric. Run with
// `go test -bench=. -run=^$` on a SIMD build, or with `-tags=nosimd` to see
// the scalar path alone.
func BenchmarkSumU32(b *testing.B) {
	input := make([]uint32, benchSize)
	for i := range input {
		input[i] = uint32(i)
	}
	var genericNs float64
	b.Run("generic", func(b *testing.B) {
		b.SetBytes(4 * benchSize)
		var sink uint64
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sink += sumU32Generic(input)
		}
		genericNs = float64(b.Elapsed().Nanoseconds()) / float64(b.N)
		_ = sink
	})
	b.Run("simd", func(b *testing.B) {
		b.SetBytes(4 * benchSize)
		var sink uint64
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sink += SumU32(input)
		}
		if genericNs > 0 {
			b.ReportMetric(genericNs/(float64(b.Elapsed().Nanoseconds())/float64(b.N)), "speedup")
		}
		_ = sink
	})
}

func BenchmarkAddU32(b *testing.B) {
	a := make([]uint32, benchSize)
	bv := make([]uint32, benchSize)
	dst := make([]uint32, benchSize)
	for i := range a {
		a[i] = uint32(i)
		bv[i] = uint32(i * 3)
	}
	var genericNs float64
	b.Run("generic", func(b *testing.B) {
		b.SetBytes(12 * benchSize)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			addU32Generic(dst, a, bv)
		}
		genericNs = float64(b.Elapsed().Nanoseconds()) / float64(b.N)
	})
	b.Run("simd", func(b *testing.B) {
		b.SetBytes(12 * benchSize)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			AddU32(dst, a, bv)
		}
		if genericNs > 0 {
			b.ReportMetric(genericNs/(float64(b.Elapsed().Nanoseconds())/float64(b.N)), "speedup")
		}
	})
}
