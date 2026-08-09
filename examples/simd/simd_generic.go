//go:build nosimd

package simd

// Scalar fallback: builds with -tags=nosimd on CPUs or environments without
// SIMD support, using the same public API as the converted kernels.
func SumU32(input []uint32) uint64 {
	return sumU32Generic(input)
}

func AddU32(dst, a, b []uint32) {
	addU32Generic(dst, a, b)
}
