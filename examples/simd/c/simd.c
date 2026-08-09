// SIMD kernels written in C. The compiler (Clang on arm64, GCC on amd64)
// vectorizes these loops with -O3; c2goasm then converts the generated
// assembly into Go Plan 9 assembly. See README.md in this directory.
#include <stdint.h>

// Return-value form: uint64_t is a single word result, supported directly
// by the direct-safe certification.
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
