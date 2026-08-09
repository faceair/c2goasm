#!/bin/bash
# PCRE2 end-to-end: compile the 8-bit interpreter core, convert it to Go
# Plan 9 assembly, link it into a Go package, and execute regular expressions.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PCRE2="${PCRE2_SOURCE_DIR:-}"
if [ -z "$PCRE2" ]; then
  PCRE2="$("$ROOT/scripts/fetch-test-source.sh" pcre2)"
fi
if [ ! -f "$PCRE2/src/pcre2_compile.c" ]; then
  echo "PCRE2 source is missing src/pcre2_compile.c: $PCRE2" >&2
  exit 1
fi
if ! grep -Fq 'm4_define(pcre2_major, [10])' "$PCRE2/configure.ac" ||
   ! grep -Fq 'm4_define(pcre2_minor, [47])' "$PCRE2/configure.ac" ||
   ! grep -Fq 'm4_define(pcre2_date, [2025-10-21])' "$PCRE2/configure.ac"; then
  echo "PCRE2 source is not release 10.47 (2025-10-21): $PCRE2" >&2
  exit 1
fi
if [ "$#" -gt 0 ]; then
  WORK="$1"
else
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/c2goasm-pcre2.XXXXXX")"
  trap 'rm -rf "$WORK"' EXIT
fi
mkdir -p "$WORK/include" "$WORK/qmod/internal/native"
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

case "$TARGET" in
arm64)
  CC="${CC:-clang}"
  COMMON_FLAGS=(
    -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=0
    -S -O2 -std=c99 -fwrapv -fno-builtin -DNDEBUG
    -fomit-frame-pointer -fno-stack-protector -fno-jump-tables
    -fno-asynchronous-unwind-tables -fno-unwind-tables
    -fno-vectorize -fno-slp-vectorize -mno-outline
    -fno-optimize-sibling-calls -ffixed-x27 -ffixed-x28 --target=arm64-apple-darwin
  )
  FRAME_PATTERN='^[[:space:]]*(stp|sub)[[:space:]].*(sp|wsp)'
  ;;
amd64)
  if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
    echo "PCRE2 amd64 E2E requires Linux x86_64" >&2
    exit 1
  fi
  CC="${CC:-gcc}"
  COMMON_FLAGS=(
    -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=0
    -S -O2 -std=c99 -fwrapv -fno-builtin -DNDEBUG
    -masm=intel -fomit-frame-pointer -fno-stack-protector -fno-jump-tables
    -fno-asynchronous-unwind-tables -fno-unwind-tables
    -fno-tree-vectorize -fno-tree-slp-vectorize -fno-optimize-sibling-calls
    -fno-reorder-blocks-and-partition -fno-common -mno-red-zone
    -fno-stack-clash-protection -ffixed-rbp -ffixed-r14 -ffixed-r11
  )
  FRAME_PATTERN='^[[:space:]]*(push|sub)[[:space:]].*(rsp|esp)'
  ;;
*)
  echo "unsupported C2GOASM_TARGET: $TARGET" >&2
  exit 1
  ;;
esac

cp "$PCRE2/src/config.h.generic" "$WORK/include/config.h"
cp "$PCRE2/src/pcre2.h.generic" "$WORK/include/pcre2.h"
cp "$PCRE2/src/pcre2_chartables.c.dist" "$WORK/pcre2_chartables.c"
PCRE2_FLAGS=(
  "${COMMON_FLAGS[@]}"
  -DHAVE_CONFIG_H -DPCRE2_STATIC=1 -DPCRE2_CODE_UNIT_WIDTH=8
  -DSUPPORT_PCRE2_8=1 -DSUPPORT_UNICODE=1
  -DHAVE_ASSERT_H=1 -DHAVE_LIMITS_H=1 -DHAVE_STDINT_H=1
  -DHAVE_STDLIB_H=1 -DHAVE_STRING_H=1 -DHAVE_SYS_TYPES_H=1
  -I "$WORK/include" -I "$PCRE2/src"
)

# The distributed chartables keep the default build deterministic. Include
# pcre2_maketables as well so callers can explicitly request locale tables.
SOURCES=(
  pcre2_auto_possess
  pcre2_chartables
  pcre2_chkdint
  pcre2_compile
  pcre2_compile_cgroup
  pcre2_compile_class
  pcre2_config
  pcre2_context
  pcre2_convert
  pcre2_dfa_match
  pcre2_error
  pcre2_extuni
  pcre2_find_bracket
  pcre2_jit_compile
  pcre2_maketables
  pcre2_match
  pcre2_match_data
  pcre2_match_next
  pcre2_newline
  pcre2_ord2utf
  pcre2_pattern_info
  pcre2_script_run
  pcre2_serialize
  pcre2_string_utils
  pcre2_study
  pcre2_substitute
  pcre2_substring
  pcre2_tables
  pcre2_ucd
  pcre2_valid_utf
  pcre2_xclass
)

cat > "$WORK/pcre2_maketables_wrapper.c" <<'EOF'
#include "pcre2_internal.h"

int c2goasm_ascii_tolower(int);
int c2goasm_ascii_toupper(int);
int c2goasm_ascii_isalnum(int);
int c2goasm_ascii_isalpha(int);
int c2goasm_ascii_iscntrl(int);
int c2goasm_ascii_isdigit(int);
int c2goasm_ascii_isgraph(int);
int c2goasm_ascii_islower(int);
int c2goasm_ascii_isprint(int);
int c2goasm_ascii_ispunct(int);
int c2goasm_ascii_isspace(int);
int c2goasm_ascii_isupper(int);
int c2goasm_ascii_isxdigit(int);

#undef tolower
#undef toupper
#undef isalnum
#undef isalpha
#undef iscntrl
#undef isdigit
#undef isgraph
#undef islower
#undef isprint
#undef ispunct
#undef isspace
#undef isupper
#undef isxdigit
#define tolower c2goasm_ascii_tolower
#define toupper c2goasm_ascii_toupper
#define isalnum c2goasm_ascii_isalnum
#define isalpha c2goasm_ascii_isalpha
#define iscntrl c2goasm_ascii_iscntrl
#define isdigit c2goasm_ascii_isdigit
#define isgraph c2goasm_ascii_isgraph
#define islower c2goasm_ascii_islower
#define isprint c2goasm_ascii_isprint
#define ispunct c2goasm_ascii_ispunct
#define isspace c2goasm_ascii_isspace
#define isupper c2goasm_ascii_isupper
#define isxdigit c2goasm_ascii_isxdigit
#include <pcre2_maketables.c>
EOF


for source in "${SOURCES[@]}"; do
  input="$PCRE2/src/$source.c"
  case "$source" in
  pcre2_chartables) input="$WORK/pcre2_chartables.c" ;;
  pcre2_maketables) input="$WORK/pcre2_maketables_wrapper.c" ;;
  esac
  "$CC" "${PCRE2_FLAGS[@]}" -o "$WORK/$source.s" "$input"
done

cat > "$WORK/bridge.c" <<'EOF'
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

void free(void *pointer) {
    system_memory->release(pointer);
}

void *memcpy(void *destination, const void *source, size_t length) {
    unsigned char *output = destination;
    const unsigned char *input = source;
    for (size_t index = 0; index < length; index++) output[index] = input[index];
    return destination;
}

void *memmove(void *destination, const void *source, size_t length) {
    unsigned char *output = destination;
    const unsigned char *input = source;
    if (output < input) {
        for (size_t index = 0; index < length; index++) output[index] = input[index];
    } else if (output > input) {
        for (size_t index = length; index != 0; index--) output[index - 1] = input[index - 1];
    }
    return destination;
}

void *memset(void *destination, int value, size_t length) {
    unsigned char *output = destination;
    for (size_t index = 0; index < length; index++) output[index] = (unsigned char)value;
    return destination;
}

void bzero(void *destination, size_t length) {
    memset(destination, 0, length);
}

int memcmp(const void *left, const void *right, size_t length) {
    const unsigned char *a = left;
    const unsigned char *b = right;
    for (size_t index = 0; index < length; index++) {
        if (a[index] != b[index]) return a[index] < b[index] ? -1 : 1;
    }
    return 0;
}

void *memchr(const void *value, int character, size_t length) {
    const unsigned char *input = value;
    for (size_t index = 0; index < length; index++) {
        if (input[index] == (unsigned char)character) return (void *)(input + index);
    }
    return NULL;
}

size_t strlen(const char *value) {
    size_t length = 0;
    while (value[length] != 0) length++;
    return length;
}

const char *strchr(const char *value, int character) {
    for (;;) {
        if ((unsigned char)*value == (unsigned char)character) return value;
        if (*value == 0) return NULL;
        value++;
    }
}

int c2goasm_ascii_islower(int value) {
    return value >= 'a' && value <= 'z';
}

int c2goasm_ascii_isupper(int value) {
    return value >= 'A' && value <= 'Z';
}

int c2goasm_ascii_isalpha(int value) {
    return c2goasm_ascii_islower(value) || c2goasm_ascii_isupper(value);
}

int c2goasm_ascii_isdigit(int value) {
    return value >= '0' && value <= '9';
}

int c2goasm_ascii_isalnum(int value) {
    return c2goasm_ascii_isalpha(value) || c2goasm_ascii_isdigit(value);
}

int c2goasm_ascii_iscntrl(int value) {
    return (value >= 0 && value < 32) || value == 127;
}

int c2goasm_ascii_isprint(int value) {
    return value >= 32 && value <= 126;
}

int c2goasm_ascii_isgraph(int value) {
    return value >= 33 && value <= 126;
}

int c2goasm_ascii_isspace(int value) {
    return value == ' ' || (value >= '\t' && value <= '\r');
}

int c2goasm_ascii_isxdigit(int value) {
    return c2goasm_ascii_isdigit(value) ||
        (value >= 'a' && value <= 'f') ||
        (value >= 'A' && value <= 'F');
}

int c2goasm_ascii_ispunct(int value) {
    return c2goasm_ascii_isgraph(value) && !c2goasm_ascii_isalnum(value);
}

int c2goasm_ascii_tolower(int value) {
    return c2goasm_ascii_isupper(value) ? value + ('a' - 'A') : value;
}

int c2goasm_ascii_toupper(int value) {
    return c2goasm_ascii_islower(value) ? value - ('a' - 'A') : value;
}

__attribute__((noinline)) void __chkstk_darwin(void) {}
EOF
"$CC" "${COMMON_FLAGS[@]}" -I "$ROOT/nativecall" -o "$WORK/bridge.s" "$WORK/bridge.c"

cat > "$WORK/entry.c" <<'EOF'
#include <stddef.h>
#include <stdint.h>
#define PCRE2_CODE_UNIT_WIDTH 8
#include "pcre2.h"

int32_t pcre2_example_find(const unsigned char *input, size_t length) {
    size_t pattern_length = 0;
    while (pattern_length < length && input[pattern_length] != 0) pattern_length++;
    if (pattern_length == length) return -2;

    uint32_t compiled_widths = 0;
    uint32_t unicode = 0;
    uint32_t jit = 1;
    if (pcre2_config(PCRE2_CONFIG_COMPILED_WIDTHS, &compiled_widths) != 0 ||
        pcre2_config(PCRE2_CONFIG_UNICODE, &unicode) != 0 ||
        pcre2_config(PCRE2_CONFIG_JIT, &jit) != 0 ||
        compiled_widths != 1 || unicode != 1 || jit != 0) {
        return -5000;
    }

    const uint8_t *locale_tables = pcre2_maketables(NULL);
    if (locale_tables == NULL) return -4000;
    pcre2_maketables_free(NULL, locale_tables);

    const unsigned char *subject = input + pattern_length + 1;
    size_t subject_length = length - pattern_length - 1;
    int error_code = 0;
    PCRE2_SIZE error_offset = 0;
    pcre2_code *code = pcre2_compile(
        input,
        pattern_length,
        PCRE2_UTF | PCRE2_UCP,
        &error_code,
        &error_offset,
        NULL
    );
    if (code == NULL) return -1000 - error_code;

    pcre2_match_data *match_data = pcre2_match_data_create_from_pattern(code, NULL);
    if (match_data == NULL) {
        pcre2_code_free(code);
        return -3000;
    }

    int result = pcre2_match(code, subject, subject_length, 0, 0, match_data, NULL);
    if (result == PCRE2_ERROR_NOMATCH) {
        pcre2_match_data_free(match_data);
        pcre2_code_free(code);
        return -1;
    }
    if (result < 0) {
        pcre2_match_data_free(match_data);
        pcre2_code_free(code);
        return -2000 + result;
    }

    PCRE2_SIZE *ovector = pcre2_get_ovector_pointer(match_data);
    PCRE2_SIZE start = ovector[0];
    PCRE2_SIZE end = ovector[1];
    pcre2_match_data_free(match_data);
    pcre2_code_free(code);
    if (start > 0x7fff || end > 0xffff) return -3;
    return (int32_t)((start << 16) | end);
}
EOF
"$CC" "${PCRE2_FLAGS[@]}" -I "$ROOT/nativecall" -o "$WORK/entry.s" "$WORK/entry.c"
SOURCES+=(bridge entry)

if ! grep -Eq '^[[:space:]]*\.globl[[:space:]]+_?pcre2_compile_8([[:space:]]|$)' "$WORK/pcre2_compile.s"; then
  echo "PCRE2 compiler output is missing pcre2_compile_8" >&2
  exit 1
fi
if ! grep -Eq "$FRAME_PATTERN" "$WORK/pcre2_match.s"; then
  echo "PCRE2 compiler output has no C stack-frame instructions" >&2
  exit 1
fi

# Assembly-local symbols are scoped to one compiler output. Namespace them
# before concatenating translation units so private labels and tables cannot
# bind to another source file with the same compiler-generated name.
python3 - "$WORK" "${SOURCES[@]}" <<'PYEOF_LABELS'
import re
import sys
from pathlib import Path

work = Path(sys.argv[1])
sources = sys.argv[2:]
symbol_char = r"A-Za-z0-9_.$"
label_def = re.compile(rf"^([{symbol_char}]+):")
global_def = re.compile(rf"^\s*\.globl\s+([{symbol_char}]+)")
local_def = re.compile(rf"^\s*\.local\s+([{symbol_char}]+)")
lcomm_def = re.compile(rf"^\s*\.lcomm\s+([{symbol_char}]+)")

for source in sources:
    path = work / f"{source}.s"
    text = path.read_text()
    lines = text.splitlines()
    globals_ = {
        match.group(1)
        for line in lines
        if (match := global_def.match(line))
    }
    labels = {
        match.group(1)
        for line in lines
        if (match := label_def.match(line))
    }
    labels.update(
        match.group(1)
        for line in lines
        if (match := local_def.match(line))
    )
    labels.update(
        match.group(1)
        for line in lines
        if (match := lcomm_def.match(line))
    )
    for line in lines:
        if re.match(r"^\s*\.zerofill\b", line):
            fields = [field.strip() for field in line.split(",")]
            if len(fields) >= 3:
                labels.add(fields[2])
    labels.difference_update(globals_)
    renamed = {
        label: f"_c2goasm_{source}_{re.sub(r'[^A-Za-z0-9_]', '_', label)}"
        for label in labels
    }
    if renamed:
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
        text = token.sub(lambda match: renamed.get(match.group(0), match.group(0)), text)
    (work / f"{source}.namespaced.s").write_text(text)
PYEOF_LABELS

: > "$WORK/pcre2-input.s"
for source in "${SOURCES[@]}"; do
  cat "$WORK/$source.namespaced.s" >> "$WORK/pcre2-input.s"
done

cat > "$WORK/qmod/internal/native/pcre2.go" <<'EOF'
package native
EOF
"$C2GOASM" -t "$TARGET" "$WORK/pcre2-input.s" "$WORK/qmod/internal/native/pcre2.s"

cat > "$WORK/qmod/internal/native/address.go" <<'EOF'
package native

func InstallMemoryAddress() uintptr
func FindAddress() uintptr
EOF
if [ "$TARGET" = arm64 ]; then
cat > "$WORK/qmod/internal/native/address.s" <<'EOF'
#include "textflag.h"

TEXT ·InstallMemoryAddress(SB), NOSPLIT, $0-8
    MOVD $·_c2goasm_native_c2goasm_install_memory(SB), R0
    MOVD R0, ret+0(FP)
    RET

TEXT ·FindAddress(SB), NOSPLIT, $0-8
    MOVD $·_c2goasm_native_pcre2_example_find(SB), R0
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

TEXT ·FindAddress(SB), NOSPLIT, $0-8
    LEAQ ·_c2goasm_native_pcre2_example_find(SB), AX
    MOVQ AX, ret+0(FP)
    RET
EOF
fi

cat > "$WORK/qmod/pcre2.go" <<'EOF'
package pcre2

import (
    "fmt"
    "strings"
    "sync"

    "example.com/pcre2/internal/native"
    "github.com/faceair/c2goasm/nativecall"
)

var installMemory sync.Once

// Find returns the byte offsets of the first PCRE2 match in subject.
func Find(pattern, subject string) (start, end int, matched bool, err error) {
    if strings.IndexByte(pattern, 0) >= 0 {
        return 0, 0, false, fmt.Errorf("pcre2: pattern contains NUL")
    }
    if len(subject) > 0x7fff {
        return 0, 0, false, fmt.Errorf("pcre2: subject is too large: %d bytes", len(subject))
    }
    input := make([]byte, len(pattern)+1+len(subject))
    copy(input, pattern)
    copy(input[len(pattern)+1:], subject)
    installMemory.Do(func() {
        nativecall.InstallMemory(native.InstallMemoryAddress())
    })
    result := int32(nativecall.CallBytes(native.FindAddress(), input))
    if result == -1 {
        return 0, 0, false, nil
    }
    if result < 0 {
        return 0, 0, false, fmt.Errorf("pcre2: native find failed: %d", result)
    }
    return int(result >> 16), int(result & 0xffff), true, nil
}
EOF

cat > "$WORK/qmod/go.mod" <<EOF
module example.com/pcre2

go 1.26.5

require github.com/faceair/c2goasm v0.0.0

replace github.com/faceair/c2goasm => $ROOT
EOF

cat > "$WORK/qmod/pcre2_test.go" <<'EOF'
package pcre2

import (
    "strings"
    "sync"
    "testing"
)

func TestPCRE2UnicodeMatch(t *testing.T) {
    const subject = "学习 Gopher 与 Go"
    start, end, matched, err := Find(`(?i)\b(gopher|go)\b`, subject)
    if err != nil {
        t.Fatal(err)
    }
    if !matched {
        t.Fatal("pattern did not match")
    }
    wantStart := strings.Index(subject, "Gopher")
    wantEnd := wantStart + len("Gopher")
    if start != wantStart || end != wantEnd {
        t.Fatalf("match offsets = [%d,%d), want [%d,%d)", start, end, wantStart, wantEnd)
    }
    if got := subject[start:end]; got != "Gopher" {
        t.Fatalf("matched text = %q, want Gopher", got)
    }
}

func TestPCRE2NoMatchAndCompileError(t *testing.T) {
    if _, _, matched, err := Find(`^rust$`, "gopher"); err != nil || matched {
        t.Fatalf("no-match result = matched %v, err %v", matched, err)
    }
    if _, _, _, err := Find(`(`, "subject"); err == nil {
        t.Fatal("invalid pattern succeeded")
    }
}

func TestPCRE2RejectsUnrepresentableSubject(t *testing.T) {
    if _, _, _, err := Find(`a`, strings.Repeat("a", 0x8000)); err == nil {
        t.Fatal("oversized subject succeeded")
    }
}

func TestPCRE2Concurrent(t *testing.T) {
    const workers = 8
    var wait sync.WaitGroup
    failures := make(chan string, workers)
    wait.Add(workers)
    for worker := 0; worker < workers; worker++ {
        go func() {
            defer wait.Done()
            for iteration := 0; iteration < 20; iteration++ {
                start, end, matched, err := Find(`\d{3}-\d{2}-\d{4}`, "id=123-45-6789")
                if err != nil || !matched || start != 3 || end != 14 {
                    failures <- "unexpected concurrent match result"
                    return
                }
            }
        }()
    }
    wait.Wait()
    close(failures)
    for failure := range failures {
        t.Fatal(failure)
    }
}
EOF

(cd "$WORK/qmod" && CGO_ENABLED=1 GOARCH="$TARGET" go test -count=1 -v ./...)
