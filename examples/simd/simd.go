// Generic scalar implementations. These are the fallback used by the
// -tags=nosimd build and the baseline that the benchmark compares the
// converted SIMD kernels against. The file has no build tag on purpose:
// both the SIMD and the nosimd variants call into it.
package simd

func sumU32Generic(input []uint32) uint64 {
	var sum uint64
	for _, v := range input {
		sum += uint64(v)
	}
	return sum
}

func addU32Generic(dst, a, b []uint32) {
	for i, v := range a {
		dst[i] = v + b[i]
	}
}
