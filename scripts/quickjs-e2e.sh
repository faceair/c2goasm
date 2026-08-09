#!/bin/bash
# QuickJS end-to-end: compile the complete engine core, convert it to Go
# Plan 9 assembly, link it into a Go package, and execute JavaScript.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
QJS="${QUICKJS_SOURCE_DIR:-}"
if [ -z "$QJS" ]; then
  QJS="$("$ROOT/scripts/fetch-test-source.sh" quickjs)"
fi
if [ ! -f "$QJS/VERSION" ]; then
  echo "QuickJS source is missing VERSION: $QJS" >&2
  exit 1
fi
if [ "$#" -gt 0 ]; then
  WORK="$1"
else
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/c2goasm-quickjs.XXXXXX")"
  trap 'rm -rf "$WORK"' EXIT
fi
mkdir -p "$WORK/qmod/internal/native"
echo "workdir: $WORK"

TARGET="${C2GOASM_TARGET:-$(go env GOARCH)}"
if [ -n "${C2GOASM:-}" ]; then
  if [ ! -x "$C2GOASM" ]; then
    echo "C2GOASM is not executable: $C2GOASM" >&2
    exit 1
  fi
else
  C2GOASM="$WORK/c2goasm"
  (cd "$ROOT" && go build -o "$C2GOASM" ./cmd/c2goasm)
fi

VERSION="$(tr -d '\r\n' < "$QJS/VERSION")"
case "$TARGET" in
arm64)
  CC="${CC:-clang}"
  COMMON_FLAGS=(
    -S -O2 -fwrapv -D_GNU_SOURCE "-DCONFIG_VERSION=\"$VERSION\"" -DNDEBUG -I "$QJS"
    -fomit-frame-pointer -fno-stack-protector -fno-jump-tables
    -fno-asynchronous-unwind-tables -fno-unwind-tables
    -fno-vectorize -fno-slp-vectorize -mno-outline
    -ffixed-x27 -ffixed-x28 --target=arm64-apple-darwin
  )
  ;;
amd64)
  if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
    echo "QuickJS amd64 E2E requires Linux x86_64" >&2
    exit 1
  fi
  CC="${CC:-gcc}"
  COMMON_FLAGS=(
    -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=0
    -S -O2 -fwrapv -fno-builtin -D_GNU_SOURCE "-DCONFIG_VERSION=\"$VERSION\"" -DNDEBUG -I "$QJS"
    -masm=intel -fomit-frame-pointer -fno-stack-protector -fno-jump-tables
    -fno-asynchronous-unwind-tables -fno-unwind-tables
    -fno-tree-vectorize -fno-tree-slp-vectorize -fno-optimize-sibling-calls
    -fno-reorder-blocks-and-partition -fno-common -mno-red-zone
    -ffixed-rbp -ffixed-r14 -ffixed-r11
  )
  ;;
*)
  echo "unsupported C2GOASM_TARGET: $TARGET" >&2
  exit 1
  ;;
esac
QUICKJS_FLAGS=("${COMMON_FLAGS[@]}" -D__EMSCRIPTEN__)

for src in cutils dtoa libregexp libunicode; do
  "$CC" "${COMMON_FLAGS[@]}" -o "$WORK/$src.s" "$QJS/$src.c"
done
"$CC" "${QUICKJS_FLAGS[@]}" -o "$WORK/quickjs.s" "$QJS/quickjs.c"

cat > "$WORK/bridge.c" <<'EOF'
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include "memory.h"

static const struct c2goasm_memory *system_memory;

void c2goasm_install_memory(const struct c2goasm_memory *memory) {
    system_memory = memory;
}

void *malloc(size_t size) {
    return system_memory->allocate(size);
}

void free(void *ptr) {
    system_memory->release(ptr);
}

void *realloc(void *ptr, size_t size) {
    return system_memory->resize(ptr, size);
}

size_t malloc_size(const void *ptr) {
    return system_memory->usable_size(ptr);
}

__attribute__((noinline)) void __chkstk_darwin(void) {}

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
        for (size_t i = n; i != 0; i--) d[i - 1] = s[i - 1];
    }
    return dst;
}

void *memset(void *dst, int value, size_t n) {
    unsigned char *d = dst;
    for (size_t i = 0; i < n; i++) d[i] = (unsigned char)value;
    return dst;
}

void bzero(void *dst, size_t n) { memset(dst, 0, n); }

int memcmp(const void *a, const void *b, size_t n) {
    const unsigned char *x = a, *y = b;
    for (size_t i = 0; i < n; i++) {
        if (x[i] != y[i]) return x[i] < y[i] ? -1 : 1;
    }
    return 0;
}

size_t strlen(const char *s) {
    size_t n = 0;
    while (s[n]) n++;
    return n;
}

int strcmp(const char *a, const char *b) {
    while (*a && *a == *b) { a++; b++; }
    return (unsigned char)*a - (unsigned char)*b;
}

const char *strchr(const char *s, int c) {
    for (;; s++) {
        if ((unsigned char)*s == (unsigned char)c) return s;
        if (*s == 0) return NULL;
    }
}

const char *strrchr(const char *s, int c) {
    const char *last = NULL;
    for (;; s++) {
        if ((unsigned char)*s == (unsigned char)c) last = s;
        if (*s == 0) return last;
    }
}

void *memchr(const void *s, int c, size_t n) {
    const unsigned char *p = s;
    for (size_t i = 0; i < n; i++) if (p[i] == (unsigned char)c) return (void *)(p + i);
    return NULL;
}

// These definitions are the narrow C ABI surface required by the selected
// QuickJS objects; unsupported symbols remain unresolved and fail conversion.
int snprintf(char *dst, size_t size, const char *fmt, ...) {
    (void)fmt;
    if (size) dst[0] = 0;
    return 0;
}
int vsnprintf(char *dst, size_t size, const char *fmt, va_list ap) {
    (void)fmt; (void)ap;
    if (size) dst[0] = 0;
    return 0;
}
int __snprintf_chk(char *dst, size_t size, int flag, size_t dst_size, const char *fmt, ...) {
    (void)flag; (void)dst_size;
    return snprintf(dst, size, fmt);
}
int __vsnprintf_chk(char *dst, size_t size, int flag, size_t dst_size, const char *fmt, va_list ap) {
    (void)flag; (void)dst_size;
    return vsnprintf(dst, size, fmt, ap);
}
int fprintf(void *stream, const char *fmt, ...) { (void)stream; (void)fmt; return 0; }
int __fprintf_chk(void *stream, int flag, const char *fmt, ...) {
    (void)flag;
    return fprintf(stream, fmt);
}
int fputc(int c, void *stream) { (void)stream; return c; }
size_t fwrite(const void *p, size_t size, size_t n, void *stream) {
    (void)p; (void)size; (void)stream;
    return n;
}
int abort(void) { __builtin_trap(); }


int gettimeofday(void *tv, void *tz) { (void)tv; (void)tz; return -1; }
int clock_gettime(int clock_id, void *ts) { (void)clock_id; (void)ts; return -1; }
void *localtime_r(const void *timep, void *result) { (void)timep; (void)result; return NULL; }

double fmod(double x, double y) { (void)y; return x; }
double pow(double x, double y) { (void)y; return x; }
double hypot(double x, double y) { (void)y; return x; }
double modf(double x, double *iptr) { if (iptr) *iptr = x; return 0; }
double fabs(double x) { return x < 0 ? -x : x; }
int abs(int x) { return x < 0 ? -x : x; }
double fmax(double x, double y) { return x > y ? x : y; }
double fmin(double x, double y) { return x < y ? x : y; }
long lrint(double x) { return (long)x; }
double round(double x) { return x < 0 ? (double)(long long)(x - 0.5) : (double)(long long)(x + 0.5); }
double floor(double x) { return x; }
double ceil(double x) { return x; }
double sqrt(double x) { return x; }
double acos(double x) { return x; }
double asin(double x) { return x; }
double atan(double x) { return x; }
double atan2(double x, double y) { (void)y; return x; }
double cos(double x) { return x; }
double exp(double x) { return x; }
double log(double x) { return x; }
double sin(double x) { return x; }
double tan(double x) { return x; }
double trunc(double x) { return x; }
double cosh(double x) { return x; }
double sinh(double x) { return x; }
double tanh(double x) { return x; }
double acosh(double x) { return x; }
double asinh(double x) { return x; }
double atanh(double x) { return x; }
double expm1(double x) { return x; }
double log1p(double x) { return x; }
double log2(double x) { return x; }
double log10(double x) { return x; }
double cbrt(double x) { return x; }
unsigned __int128 __udivti3(unsigned __int128 a, unsigned __int128 b) { (void)b; return a; }
unsigned __int128 __udivmodti4(unsigned __int128 a, unsigned __int128 b, unsigned __int128 *rem) {
    (void)b;
    if (rem) *rem = 0;
    return a;
}
EOF
"$CC" "${COMMON_FLAGS[@]}" -fno-builtin -I "$ROOT/nativecall" -o "$WORK/bridge.s" "$WORK/bridge.c"

cat > "$WORK/entry.c" <<'EOF'
#include <stddef.h>
#include <stdint.h>
#include "quickjs.h"


int qjs_run(const char *source, size_t length) {
    JSRuntime *rt = JS_NewRuntime();
    if (!rt)
        return -10;
    JSContext *ctx = JS_NewContext(rt);
    if (!ctx) {
        JS_FreeRuntime(rt);
        return -11;
    }
    JSValue value = JS_Eval(ctx, source, length, "<e2e>", JS_EVAL_TYPE_GLOBAL);
    int result = -1;
    if (!JS_IsException(value)) {
        int32_t number;
        if (JS_ToInt32(ctx, &number, value) == 0)
            result = number;
        else
            result = -12;
        JS_FreeValue(ctx, value);
    }
    JS_FreeContext(ctx);
    JS_FreeRuntime(rt);
    return result;
}
EOF
"$CC" "${COMMON_FLAGS[@]}" -o "$WORK/entry.s" "$WORK/entry.c"

if ! grep -Eq '^[[:space:]]*\.globl[[:space:]]+_?JS_Eval([[:space:]]|$)' "$WORK/quickjs.s"; then
  echo "QuickJS compiler output is missing JS_Eval" >&2
  exit 1
fi
if [ "$TARGET" = arm64 ]; then
  FRAME_PATTERN='^[[:space:]]*(stp|sub)[[:space:]].*(sp|wsp)'
else
  FRAME_PATTERN='^[[:space:]]*(push|sub[[:space:]]+rsp)'
fi
if ! grep -Eq "$FRAME_PATTERN" "$WORK/quickjs.s"; then
  echo "QuickJS compiler output has no C stack-frame instructions" >&2
  exit 1
fi

# Assembly-local symbols are scoped to one compiler output. Preserve that
# scope before concatenating translation units; otherwise branch labels,
# literal strings, and private tables silently bind to another source file.
python3 - "$WORK" <<'PYEOF_LABELS'
import re
import sys
from pathlib import Path

work = Path(sys.argv[1])
sources = ("cutils", "dtoa", "libregexp", "libunicode", "quickjs", "bridge", "entry")
symbol_char = r"A-Za-z0-9_.$"
label_def = re.compile(rf"^([{symbol_char}]+):")
global_def = re.compile(rf"^\s*\.globl\s+([{symbol_char}]+)")
set_def = re.compile(rf"^\s*\.set\s+([{symbol_char}]+)\s*,\s*([{symbol_char}]+)")

for source in sources:
    path = work / f"{source}.s"
    text = path.read_text()
    lines = text.splitlines()
    globals_ = {match.group(1) for line in lines
                if (match := global_def.match(line))}
    labels = {
        match.group(1)
        for line in lines
        if (match := label_def.match(line))
    }
    for line in lines:
        if re.match(r"^\s*\.zerofill\b", line):
            fields = [field.strip() for field in line.split(",")]
            if len(fields) >= 3:
                labels.add(fields[2])
    labels.difference_update(globals_)
    aliases = {
        match.group(1): match.group(2)
        for line in lines
        if (match := set_def.match(line))
    }
    renamed = {
        label: f"_c2goasm_{source}_{re.sub(r'[^A-Za-z0-9_]', '_', label)}"
        for label in labels
    }
    replacements = dict(renamed)
    for alias, target in aliases.items():
        if alias not in globals_ and (target in labels or target in globals_):
            replacements[alias] = renamed.get(target, target)
    if renamed:
        # Clang's Mach-O function marker omits the platform underscore.
        # Keep it synchronized when a static function label is namespaced.
        begin_names = {
            label[1:]: replacement[1:]
            for label, replacement in renamed.items()
            if label.startswith("_")
        }
        begin_marker = re.compile(r"(Begin function )([A-Za-z0-9_.$]+)")
        text = begin_marker.sub(
            lambda match: match.group(1) + begin_names.get(match.group(2), match.group(2)),
            text,
        )
        token = re.compile(
            rf"(?<![{symbol_char}])[A-Za-z_.$][{symbol_char}]*(?![{symbol_char}])"
        )
        text = token.sub(lambda match: replacements.get(match.group(0), match.group(0)), text)
    (work / f"{source}.namespaced.s").write_text(text)
PYEOF_LABELS


cat "$WORK/cutils.namespaced.s" "$WORK/dtoa.namespaced.s" \
  "$WORK/libregexp.namespaced.s" "$WORK/libunicode.namespaced.s" \
  "$WORK/quickjs.namespaced.s" "$WORK/bridge.namespaced.s" \
  "$WORK/entry.namespaced.s" > "$WORK/quickjs-input.s"

cat > "$WORK/qmod/internal/native/quickjs.go" <<'EOF'
package native
EOF

"$C2GOASM" -t "$TARGET" "$WORK/quickjs-input.s" "$WORK/qmod/internal/native/quickjs.s"

cat > "$WORK/qmod/internal/native/address.go" <<'EOF'
package native

func InstallMemoryAddress() uintptr
func QuickJSAddress() uintptr
EOF

if [ "$TARGET" = arm64 ]; then
cat > "$WORK/qmod/internal/native/address.s" <<'EOF'
#include "textflag.h"

TEXT ·InstallMemoryAddress(SB), NOSPLIT, $0-8
    MOVD $·_c2goasm_native_c2goasm_install_memory(SB), R0
    MOVD R0, ret+0(FP)
    RET

TEXT ·QuickJSAddress(SB), NOSPLIT, $0-8
    MOVD $·_c2goasm_native_qjs_run(SB), R0
    MOVD R0, ret+0(FP)
    RET
EOF
else
cat > "$WORK/qmod/internal/native/address.s" <<'EOF'
#include "textflag.h"

TEXT ·InstallMemoryAddress(SB), NOSPLIT, $0-8
    LEAQ ·_c2goasm_native_c2goasm_install_memory(SB), AX
    MOVQ AX, ret+0(FP)
    RET

TEXT ·QuickJSAddress(SB), NOSPLIT, $0-8
    LEAQ ·_c2goasm_native_qjs_run(SB), AX
    MOVQ AX, ret+0(FP)
    RET
EOF
fi

cat > "$WORK/qmod/quickjs.go" <<'EOF'
package quickjs

import (
    "sync"

    "example.com/quickjs/internal/native"
    "github.com/faceair/c2goasm/nativecall"
)

var installMemory sync.Once

func qjsRun(source []byte) int32 {
    if len(source) == 0 || source[len(source)-1] != 0 {
        panic("quickjs source is not NUL-terminated")
    }
    installMemory.Do(func() {
        nativecall.InstallMemory(native.InstallMemoryAddress())
    })
    return nativecall.CallBytes(native.QuickJSAddress(), source[:len(source)-1])
}
EOF

cat > "$WORK/qmod/go.mod" <<EOF
module example.com/quickjs

go 1.26.5

require github.com/faceair/c2goasm v0.0.0

replace github.com/faceair/c2goasm => $ROOT
EOF
cat > "$WORK/qmod/qjs_test.go" <<'EOF'
package quickjs

import (
    "sync"
    "testing"
)

func quickJSScript() []byte {
    return append([]byte(`(() => {
  const values = [1, 2, 3, 4];
  const sum = values.map(x => x * x).reduce((a, b) => a + b, 0);
  const regexp = /a+b/.test("xxaaabyy");
  const bigint = 2n ** 10n;
  if (sum !== 30 || !regexp || bigint !== 1024n) throw new Error("assertion");
  return sum;
})()`), 0)
}

func TestQuickJSJavaScript(t *testing.T) {
    script := quickJSScript()
    for i := 0; i < 3; i++ {
        if got := qjsRun(script); got != 30 {
            t.Fatalf("QuickJS result at run %d = %d, want 30", i, got)
        }
    }
}

func TestQuickJSConcurrent(t *testing.T) {
    const workers = 4
    failures := make(chan int32, workers)
    var wait sync.WaitGroup
    wait.Add(workers)
    for range workers {
        go func() {
            defer wait.Done()
            if result := qjsRun(quickJSScript()); result != 30 {
                failures <- result
            }
        }()
    }
    wait.Wait()
    close(failures)
    for result := range failures {
        t.Errorf("concurrent QuickJS result = %d, want 30", result)
    }
}
EOF

(cd "$WORK/qmod" && CGO_ENABLED=1 GOARCH="$TARGET" go test -count=1 -run 'TestQuickJS(JavaScript|Concurrent)' -v ./...)
