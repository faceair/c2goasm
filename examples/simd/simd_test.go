package simd

import "testing"

func TestSumU32(t *testing.T) {
	input := []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}
	var want uint64
	for _, v := range input {
		want += uint64(v)
	}
	if got := SumU32(input); got != want {
		t.Fatalf("SumU32 = %d, want %d", got, want)
	}
}

func TestAddU32(t *testing.T) {
	a := []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	b := []uint32{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	dst := make([]uint32, len(a))
	AddU32(dst, a, b)
	for i := range dst {
		if dst[i] != a[i]+b[i] {
			t.Fatalf("AddU32[%d] = %d, want %d", i, dst[i], a[i]+b[i])
		}
	}
}
