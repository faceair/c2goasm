#!/bin/bash
# amd64 Linux end-to-end gate: GCC Intel input -> c2goasm -> Go runtime.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
    echo "amd64-cutils-e2e requires Linux x86_64" >&2
    exit 1
fi
for tool in gcc go python3; do
    command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 1; }
done

if [[ $# -gt 0 ]]; then
    WORK=$1
    mkdir -p "$WORK"
else
    WORK=$(mktemp -d "${TMPDIR:-/tmp}/c2goasm-amd64-cutils.XXXXXX")
    trap 'rm -rf "$WORK"' EXIT
fi
mkdir -p "$WORK/qmod/internal/native"

echo "workdir: $WORK"
(cd "$ROOT" && go build -o "$WORK/c2goasm" ./cmd/c2goasm)
QJS="${QUICKJS_SOURCE_DIR:-}"
if [ -z "$QJS" ]; then
    QJS="$("$ROOT/scripts/fetch-test-source.sh" quickjs)"
fi
if [ ! -f "$QJS/cutils.c" ]; then
    echo "QuickJS source is missing cutils.c: $QJS" >&2
    exit 1
fi

cat > "$WORK/combined.c" <<EOF
#include "$ROOT/nativecall/memory.h"
#include "$QJS/cutils.c"

static const struct c2goasm_memory *system_memory;

void c2goasm_install_memory(const struct c2goasm_memory *memory) {
    system_memory = memory;
}

void *malloc(size_t size) {
    return system_memory->allocate(size);
}

void free(void *pointer) {
    system_memory->release(pointer);
}

void *realloc(void *pointer, size_t size) {
    return system_memory->resize(pointer, size);
}

size_t malloc_usable_size(const void *pointer) {
    return system_memory->usable_size(pointer);
}

size_t strlen(const char *s) {
    size_t n = 0;
    while (s[n] != 0) n++;
    return n;
}

int memcmp(const void *a, const void *b, size_t n) {
    const unsigned char *x = a;
    const unsigned char *y = b;
    for (size_t i = 0; i < n; i++) {
        if (x[i] != y[i]) return (int)x[i] - (int)y[i];
    }
    return 0;
}

void *memcpy(void *dst, const void *src, size_t n) {
    unsigned char *d = dst;
    const unsigned char *s = src;
    for (size_t i = 0; i < n; i++) d[i] = s[i];
    return dst;
}

void *memmove(void *dst, const void *src, size_t n) {
    unsigned char *d = dst;
    const unsigned char *s = src;
    if (d < s) {
        for (size_t i = 0; i < n; i++) d[i] = s[i];
    } else if (d > s) {
        for (size_t i = n; i > 0; i--) d[i - 1] = s[i - 1];
    }
    return dst;
}

void *memset(void *dst, int value, size_t n) {
    unsigned char *d = dst;
    for (size_t i = 0; i < n; i++) d[i] = (unsigned char)value;
    return dst;
}

int __vsnprintf_chk(char *dst, size_t size, int flag, size_t dst_size,
                    const char *format, __builtin_va_list args) {
    (void)dst; (void)size; (void)flag; (void)dst_size; (void)format; (void)args;
    return 0;
}

unsigned __int128 __udivti3(unsigned __int128 a, unsigned __int128 b) {
    (void)b;
    return a;
}
EOF

gcc -S -O2 -fno-builtin -masm=intel -fwrapv -D_GNU_SOURCE -DNDEBUG \
    -I"$QJS" -fomit-frame-pointer -fno-stack-protector \
    -fno-jump-tables -fno-asynchronous-unwind-tables -fno-unwind-tables \
    -fno-tree-vectorize -fno-tree-slp-vectorize -fno-optimize-sibling-calls \
    -mno-red-zone -ffixed-rbp -ffixed-r14 -ffixed-r11 \
    -o "$WORK/combined.s" "$WORK/combined.c"
if ! grep -Eq '^[[:space:]]*\.globl[[:space:]]+pstrcpy([[:space:]]|$)' "$WORK/combined.s"; then
    echo "cutils compiler output is missing pstrcpy" >&2
    exit 1
fi
if ! grep -Eq '^[[:space:]]*(push|sub[[:space:]]+rsp)' "$WORK/combined.s"; then
    echo "cutils compiler output has no C stack-frame instructions" >&2
    exit 1
fi

cat > "$WORK/qmod/internal/native/cutils.go" <<'EOF'
package native
EOF

"$WORK/c2goasm" -t amd64 "$WORK/combined.s" "$WORK/qmod/internal/native/cutils.s"

cat > "$WORK/qmod/internal/native/address.go" <<'EOF'
package native

func InstallMemoryAddress() uintptr
func HasSuffixAddress() uintptr
func PstrcpyAddress() uintptr
EOF

cat > "$WORK/qmod/internal/native/address.s" <<'EOF'
#include "textflag.h"
TEXT ·InstallMemoryAddress(SB), NOSPLIT, $0-8
    LEAQ ·_c2goasm_native_c2goasm_install_memory(SB), AX
    MOVQ AX, ret+0(FP)
    RET


TEXT ·HasSuffixAddress(SB), NOSPLIT, $0-8
    LEAQ ·_c2goasm_native_has_suffix(SB), AX
    MOVQ AX, ret+0(FP)
    RET

TEXT ·PstrcpyAddress(SB), NOSPLIT, $0-8
    LEAQ ·_c2goasm_native_pstrcpy(SB), AX
    MOVQ AX, ret+0(FP)
    RET
EOF

cat > "$WORK/qmod/cutils.go" <<'EOF'
package cutils

/*
#include <stddef.h>
#include <stdint.h>

typedef void (*c2goasm_pstrcpy_fn)(unsigned char *, int, const unsigned char *);
typedef int32_t (*c2goasm_has_suffix_fn)(const unsigned char *, const unsigned char *);

static void c2goasm_call_pstrcpy(
    uintptr_t address,
    unsigned char *destination,
    int size,
    const unsigned char *source
) {
    ((c2goasm_pstrcpy_fn)address)(destination, size, source);
}

static int32_t c2goasm_call_has_suffix(
    uintptr_t address,
    const unsigned char *value,
    const unsigned char *suffix
) {
    return ((c2goasm_has_suffix_fn)address)(value, suffix);
}
*/
import "C"

import (
    "runtime"
    "sync"
    "unsafe"

    "example.com/cutils/internal/native"
    "github.com/faceair/c2goasm/nativecall"
)

var installMemory sync.Once

func pstrcpy(destination, source []byte) {
    if len(destination) == 0 || len(source) == 0 {
        panic("pstrcpy requires non-empty buffers")
    }
    installMemory.Do(func() {
        nativecall.InstallMemory(native.InstallMemoryAddress())
    })
    C.c2goasm_call_pstrcpy(
        C.uintptr_t(native.PstrcpyAddress()),
        (*C.uchar)(unsafe.Pointer(&destination[0])),
        C.int(len(destination)),
        (*C.uchar)(unsafe.Pointer(&source[0])),
    )
    runtime.KeepAlive(destination)
    runtime.KeepAlive(source)
}

func hasSuffix(value, suffix []byte) int32 {
    if len(value) == 0 || len(suffix) == 0 {
        panic("hasSuffix requires non-empty strings")
    }
    installMemory.Do(func() {
        nativecall.InstallMemory(native.InstallMemoryAddress())
    })
    result := int32(C.c2goasm_call_has_suffix(
        C.uintptr_t(native.HasSuffixAddress()),
        (*C.uchar)(unsafe.Pointer(&value[0])),
        (*C.uchar)(unsafe.Pointer(&suffix[0])),
    ))
    runtime.KeepAlive(value)
    runtime.KeepAlive(suffix)
    return result
}
EOF

cat > "$WORK/qmod/go.mod" <<EOF
module example.com/cutils

go 1.26.5

require github.com/faceair/c2goasm v0.0.0

replace github.com/faceair/c2goasm => $ROOT
EOF
cat > "$WORK/qmod/cutils_test.go" <<'EOF'
package cutils

import "testing"

func TestPstrcpy(t *testing.T) {
    dst := make([]byte, 16)
    src := []byte("hello\x00")
    pstrcpy(dst, src)
    if got := string(dst[:5]); got != "hello" {
        t.Fatalf("pstrcpy result = %q", got)
    }
}

func TestHasSuffix(t *testing.T) {
    value := []byte("hello world\x00")
    suffix := []byte("world\x00")
    got := hasSuffix(value, suffix)
    if got == 0 {
        t.Fatal("has_suffix should be true")
    }
}
EOF

(cd "$WORK/qmod" && CGO_ENABLED=1 go test -count=1 -run 'TestPstrcpy|TestHasSuffix' -v ./...)
