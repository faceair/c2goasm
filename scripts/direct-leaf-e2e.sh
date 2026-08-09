#!/bin/bash
# Compile a frameless C leaf, convert it, and call the generated Go ABI0 entry
# without cgo. Run natively on Darwin/arm64 or Linux/amd64.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
if [ -n "${1:-}" ]; then
	WORK=$1
	mkdir -p "$WORK"
else
	WORK=$(mktemp -d "${TMPDIR:-/tmp}/c2goasm-direct-leaf.XXXXXX")
	trap 'rm -rf "$WORK"' EXIT
fi
mkdir -p "$WORK/qmod"

echo "workdir: $WORK"
TARGET=$(go env GOARCH)
case "$(go env GOOS)/$TARGET" in
	darwin/arm64)
		CC=${CC:-clang}
		CFLAGS=(
			-S -O2 -fomit-frame-pointer -fno-stack-protector
			-fno-asynchronous-unwind-tables -fno-unwind-tables
			-fno-vectorize -fno-slp-vectorize -mno-outline
			-ffixed-x27 -ffixed-x28 --target=arm64-apple-darwin
		)
		;;
	linux/amd64)
		CC=${CC:-gcc}
		CFLAGS=(
			-S -O2 -masm=intel -fomit-frame-pointer -fno-stack-protector
			-fno-asynchronous-unwind-tables -fno-unwind-tables
			-fno-tree-vectorize -fno-tree-slp-vectorize -fno-optimize-sibling-calls
			-fno-reorder-blocks-and-partition -mno-red-zone
			-ffixed-rbp -ffixed-r14 -ffixed-r11
		)
		;;
	*)
		echo "direct-leaf-e2e supports native Darwin/arm64 and Linux/amd64" >&2
		exit 1
		;;
esac

cat > "$WORK/leaf.c" <<'EOF'
#include <stdint.h>

uintptr_t c2goasm_leaf_sum(uintptr_t left, uintptr_t right) {
    return (left ^ UINT64_C(0x9e3779b97f4a7c15)) + right;
}

uintptr_t c2goasm_leaf_choose(uintptr_t value, uintptr_t fallback) {
    return value != 0 ? value + 7 : fallback;
}

uintptr_t c2goasm_leaf_xor(const uintptr_t *values, uintptr_t count) {
    uintptr_t result = 0;
    for (uintptr_t index = 0; index < count; index++)
        result ^= values[index] + index;
    return result;
}

void *c2goasm_leaf_identity(void *value) {
    return value;
}
EOF
"$CC" "${CFLAGS[@]}" -o "$WORK/leaf-input.s" "$WORK/leaf.c"

cat > "$WORK/qmod/leaf.go" <<'EOF'
package directleaf

import (
	"runtime"
	"unsafe"
)

func _c2goasm_leaf_sum(left, right uintptr) uintptr
func _c2goasm_leaf_choose(value, fallback uintptr) uintptr
func _c2goasm_leaf_xor(values unsafe.Pointer, count uintptr) uintptr
func _c2goasm_leaf_identity(value unsafe.Pointer) unsafe.Pointer

func Sum(left, right uintptr) uintptr {
	return _c2goasm_leaf_sum(left, right)
}

func Choose(value, fallback uintptr) uintptr {
	return _c2goasm_leaf_choose(value, fallback)
}

func Xor(values []uintptr) uintptr {
	if len(values) == 0 {
		return _c2goasm_leaf_xor(nil, 0)
	}
	result := _c2goasm_leaf_xor(unsafe.Pointer(&values[0]), uintptr(len(values)))
	runtime.KeepAlive(values)
	return result
}

func Identity(value unsafe.Pointer) unsafe.Pointer {
	result := _c2goasm_leaf_identity(value)
	runtime.KeepAlive(value)
	return result
}
EOF

if [ -n "${C2GOASM:-}" ]; then
	if [ ! -x "$C2GOASM" ]; then
		echo "C2GOASM is not executable: $C2GOASM" >&2
		exit 1
	fi
else
	C2GOASM=$WORK/c2goasm
	(cd "$ROOT" && go build -o "$C2GOASM" ./cmd/c2goasm)
fi
"$C2GOASM" -t "$TARGET" "$WORK/leaf-input.s" "$WORK/qmod/leaf.s"

cat > "$WORK/qmod/go.mod" <<'EOF'
module example.com/directleaf

go 1.26.0
EOF
cat > "$WORK/qmod/leaf_test.go" <<'EOF'
package directleaf

import (
	"runtime"
	"sync"
	"testing"
	"unsafe"
)

func xorAfterStackGrowth(values []uintptr, depth int) uintptr {
	var pad [256]byte
	pad[0] = byte(depth)
	if depth == 0 {
		result := Xor(values)
		runtime.KeepAlive(pad)
		return result
	}
	result := xorAfterStackGrowth(values, depth-1)
	runtime.KeepAlive(pad)
	return result
}

func TestDirectLeaf(t *testing.T) {
	const mask = uintptr(0x9e3779b97f4a7c15)
	if got, want := Sum(11, 31), (uintptr(11)^mask)+31; got != want {
		t.Fatalf("Sum(11, 31) = %#x, want %#x", got, want)
	}
	if got := Choose(0, 42); got != 42 {
		t.Fatalf("Choose(0, 42) = %d, want 42", got)
	}
	if got := Choose(35, 99); got != 42 {
		t.Fatalf("Choose(35, 99) = %d, want 42", got)
	}
	values := []uintptr{3, 5, 8, 13, 21}
	var wantXor uintptr
	for index, value := range values {
		wantXor ^= value + uintptr(index)
	}
	if got := Xor(values); got != wantXor {
		t.Fatalf("Xor(%v) = %d, want %d", values, got, wantXor)
	}
	stress := make([]uintptr, 1<<18)
	var stressWant uintptr
	for index := range stress {
		stress[index] = uintptr(index*17 + 3)
		stressWant ^= stress[index] + uintptr(index)
	}
	gcDone := make(chan struct{})
	if got := Identity(unsafe.Pointer(&values[2])); got != unsafe.Pointer(&values[2]) {
		t.Fatalf("Identity returned %p, want %p", got, &values[2])
	}
	go func() {
		defer close(gcDone)
		for iteration := 0; iteration < 20; iteration++ {
			runtime.GC()
		}
	}()
	for iteration := 0; iteration < 20; iteration++ {
		if got := xorAfterStackGrowth(stress, 64); got != stressWant {
			t.Fatalf("Xor under GC = %d, want %d", got, stressWant)
		}
	}
	<-gcDone

	var wait sync.WaitGroup
	for worker := 0; worker < 10; worker++ {
		wait.Add(1)
		go func(seed uintptr) {
			defer wait.Done()
			for iteration := uintptr(0); iteration < 10000; iteration++ {
				value := seed + iteration
				if got, want := Sum(value, iteration), (value^mask)+iteration; got != want {
					t.Errorf("concurrent Sum(%d, %d) = %#x, want %#x", value, iteration, got, want)
					return
				}
			}
		}(uintptr(worker))
	}
	runtime.GC()
	wait.Wait()
}
EOF

(cd "$WORK/qmod" && CGO_ENABLED=0 go test -count=1 -v ./...)
