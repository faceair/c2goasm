#!/bin/bash
# Convert the complete SQLite amalgamation and execute real SQL through cgo.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C2GOASM="${C2GOASM:-}"
if [ "$(go env GOOS)/$(go env GOARCH)" != "darwin/arm64" ]; then
	echo "sqlite-e2e requires darwin/arm64" >&2
	exit 1
fi
SQLITE_SOURCE_DIR="${SQLITE_SOURCE_DIR:-}"
if [ -z "$SQLITE_SOURCE_DIR" ]; then
	SQLITE_SOURCE_DIR="$("$ROOT/scripts/fetch-test-source.sh" sqlite)"
fi
SOURCE="$SQLITE_SOURCE_DIR/sqlite3.c"
if [ ! -f "$SOURCE" ]; then
	echo "SQLite source is missing sqlite3.c: $SQLITE_SOURCE_DIR" >&2
	exit 1
fi
if [ "$#" -gt 0 ]; then
	WORK="$1"
else
	WORK="$(mktemp -d "${TMPDIR:-/tmp}/c2goasm-sqlite-e2e.XXXXXX")"
	trap 'rm -rf "$WORK"' EXIT
fi
mkdir -p "$WORK/qmod/internal/native"
echo "workdir: $WORK"

if [ -n "$C2GOASM" ]; then
	if [ ! -x "$C2GOASM" ]; then
		echo "C2GOASM is not executable: $C2GOASM" >&2
		exit 1
	fi
else
	C2GOASM="$WORK/c2goasm"
	(cd "$ROOT" && go build -o "$C2GOASM" ./cmd/c2goasm)
fi

if ! grep -Fq '#define SQLITE_VERSION        "3.48.0"' "$SOURCE"; then
	echo "SQLite source is not version 3.48.0: $SOURCE" >&2
	exit 1
fi

cat > "$WORK/bridge.c" <<'EOF'
#include "memory.h"
#include "threads.h"
typedef long ssize_t;
typedef struct sqlite3 sqlite3;
typedef struct sqlite3_stmt sqlite3_stmt;

extern int sqlite3_open(const char *, sqlite3 **);
extern int sqlite3_threadsafe(void);
extern int sqlite3_exec(sqlite3 *, const char *, int (*)(void *, int, char **, char **), void *, char **);
extern int sqlite3_prepare_v2(sqlite3 *, const char *, int, sqlite3_stmt **, const char **);
extern int sqlite3_step(sqlite3_stmt *);
extern int sqlite3_column_int(sqlite3_stmt *, int);
extern int sqlite3_finalize(sqlite3_stmt *);
extern int sqlite3_close(sqlite3 *);

static const struct c2goasm_memory *system_memory;
static const struct c2goasm_threads *system_threads;

void c2goasm_install_memory(const struct c2goasm_memory *memory) {
    system_memory = memory;
}

void c2goasm_install_threads(const struct c2goasm_threads *threads) {
    system_threads = threads;
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

int pthread_mutexattr_init(void *attribute) {
    return system_threads->mutexattr_init(attribute);
}

int pthread_mutexattr_settype(void *attribute, int type) {
    return system_threads->mutexattr_settype(attribute, type);
}

int pthread_mutexattr_destroy(void *attribute) {
    return system_threads->mutexattr_destroy(attribute);
}

int pthread_mutex_init(void *mutex, const void *attribute) {
    return system_threads->mutex_init(mutex, attribute);
}

int pthread_mutex_destroy(void *mutex) {
    return system_threads->mutex_destroy(mutex);
}

int pthread_mutex_lock(void *mutex) {
    return system_threads->mutex_lock(mutex);
}

int pthread_mutex_trylock(void *mutex) {
    return system_threads->mutex_trylock(mutex);
}

int pthread_mutex_unlock(void *mutex) {
    return system_threads->mutex_unlock(mutex);
}

void *memcpy(void *dst, const void *src, size_t size) {
    unsigned char *d = dst;
    const unsigned char *s = src;
    for (size_t i = 0; i < size; i++)
        d[i] = s[i];
    return dst;
}

void *memmove(void *dst, const void *src, size_t size) {
    unsigned char *d = dst;
    const unsigned char *s = src;
    if (d <= s || d >= s + size) {
        for (size_t i = 0; i < size; i++)
            d[i] = s[i];
    } else {
        for (size_t i = size; i != 0; i--)
            d[i - 1] = s[i - 1];
    }
    return dst;
}

void *memset(void *dst, int value, size_t size) {
    unsigned char *d = dst;
    for (size_t i = 0; i < size; i++)
        d[i] = (unsigned char)value;
    return dst;
}

void bzero(void *dst, size_t size) { (void)memset(dst, 0, size); }

int memcmp(const void *left, const void *right, size_t size) {
    const unsigned char *a = left;
    const unsigned char *b = right;
    for (size_t i = 0; i < size; i++) {
        if (a[i] != b[i])
            return a[i] < b[i] ? -1 : 1;
    }
    return 0;
}

void *memchr(const void *source, int value, size_t size) {
    const unsigned char *s = source;
    for (size_t i = 0; i < size; i++) {
        if (s[i] == (unsigned char)value)
            return (void *)(s + i);
    }
    return (void *)0;
}

size_t strlen(const char *s) {
    size_t n = 0;
    while (s[n])
        n++;
    return n;
}

int strcmp(const char *a, const char *b) {
    while (*a && (unsigned char)*a == (unsigned char)*b) {
        a++;
        b++;
    }
    return (unsigned char)*a - (unsigned char)*b;
}

int strncmp(const char *a, const char *b, size_t size) {
    for (size_t i = 0; i < size; i++) {
        unsigned char x = (unsigned char)a[i];
        unsigned char y = (unsigned char)b[i];
        if (x != y)
            return x < y ? -1 : 1;
        if (x == 0)
            return 0;
    }
    return 0;
}

char *strrchr(const char *s, int value) {
    const char *last = (void *)0;
    do {
        if ((unsigned char)*s == (unsigned char)value)
            last = s;
    } while (*s++);
    return (char *)last;
}


char *strchr(const char *s, int value) {
    do {
        if ((unsigned char)*s == (unsigned char)value)
            return (char *)s;
    } while (*s++);
    return (void *)0;
}
size_t strspn(const char *s, const char *accept) {
    size_t n = 0;
    for (; s[n]; n++) {
        const char *p = accept;
        while (*p && *p != s[n])
            p++;
        if (!*p)
            break;
    }
    return n;
}

size_t strcspn(const char *s, const char *reject) {
    size_t n = 0;
    for (; s[n]; n++) {
        const char *p = reject;
        while (*p && *p != s[n])
            p++;
        if (*p)
            break;
    }
    return n;
}

int atoi(const char *s) {
    int sign = 1;
    int value = 0;
    while (*s == ' ' || *s == '\t' || *s == '\n')
        s++;
    if (*s == '-') {
        sign = -1;
        s++;
    } else if (*s == '+') {
        s++;
    }
    while (*s >= '0' && *s <= '9')
        value = value * 10 + (*s++ - '0');
    return sign * value;
}

void *__memcpy_chk(void *dst, const void *src, size_t size, size_t bound) {
    (void)bound;
    return memcpy(dst, src, size);
}

void *__memset_chk(void *dst, int value, size_t size, size_t bound) {
    (void)bound;
    return memset(dst, value, size);
}

static size_t bridge_strlcpy(char *dst, const char *src, size_t size) {
    size_t length = strlen(src);
    if (size) {
        size_t copied = length < size - 1 ? length : size - 1;
        memcpy(dst, src, copied);
        dst[copied] = 0;
    }
    return length;
}

static size_t bridge_strlcat(char *dst, const char *src, size_t size) {
    size_t used = strlen(dst);
    size_t added = strlen(src);
    if (used < size)
        bridge_strlcpy(dst + used, src, size - used);
    return used + added;
}

size_t __strlcpy_chk(char *dst, const char *src, size_t size, size_t bound) {
    (void)bound;
    return bridge_strlcpy(dst, src, size);
}

size_t __strlcat_chk(char *dst, const char *src, size_t size, size_t bound) {
    (void)bound;
    return bridge_strlcat(dst, src, size);
}

static size_t allocation_size(const void *ptr) {
    return system_memory->usable_size(ptr);
}

static size_t zone_size(void *zone, const void *ptr) {
    (void)zone;
    return allocation_size(ptr);
}

typedef struct {
    void *reserved0;
    void *reserved1;
    size_t (*size)(void *, const void *);
} FakeZone;

static FakeZone default_zone = {(void *)0, (void *)0, zone_size};

void *malloc_default_zone(void) { return &default_zone; }
void *malloc_create_zone(size_t start_size, unsigned flags) {
    (void)start_size;
    (void)flags;
    return &default_zone;
}
void malloc_set_zone_name(void *zone, const char *name) {
    (void)zone;
    (void)name;
}
void *malloc_zone_malloc(void *zone, size_t size) {
    (void)zone;
    return malloc(size);
}
void *malloc_zone_realloc(void *zone, void *ptr, size_t size) {
    (void)zone;
    return realloc(ptr, size);
}
void malloc_zone_free(void *zone, void *ptr) {
    (void)zone;
    free(ptr);
}

double fabs(double value) {
    return value < 0 ? -value : value;
}

static int bridge_errno;
void *__stderrp = (void *)0;
int *__error(void) { return &bridge_errno; }
char *getenv(const char *name) {
    (void)name;
    return (void *)0;
}
int getpid(void) { return 1; }
long sysconf(int name) {
    (void)name;
    return 4096;
}
size_t confstr(int name, char *buffer, size_t size) {
    (void)name;
    if (buffer && size)
        buffer[0] = 0;
    return 0;
}
int sysctlbyname(const char *name, void *old_value, size_t *old_size, const void *new_value, size_t new_size) {
    (void)name;
    (void)new_value;
    (void)new_size;
    if (old_value)
        *(int *)old_value = 2;
    if (old_size)
        *old_size = sizeof(int);
    return 0;
}
long random(void) {
    static unsigned state = 1;
    state = state * 1103515245u + 12345u;
    return (long)(state & 0x7fffffff);
}
void srandomdev(void) {}
long time(long *result) {
    long value = 1700000000;
    if (result)
        *result = value;
    return value;
}
int gettimeofday(void *raw, void *zone) {
    (void)zone;
    if (raw) {
        long *value = raw;
        value[0] = 1700000000;
        value[1] = 0;
    }
    return 0;
}
struct bridge_tm {
    int sec, min, hour, mday, mon, year, wday, yday, isdst;
    long gmtoff;
    const char *zone;
};
struct bridge_tm *localtime(const long *value) {
    static struct bridge_tm result;
    (void)value;
    return &result;
}
int nanosleep(const void *requested, void *remaining) {
    (void)requested;
    (void)remaining;
    return 0;
}
int gethostuuid(void *id, const void *timeout) {
    (void)id;
    (void)timeout;
    return -1;
}
char *strerror(int error) {
    (void)error;
    return "error";
}
int fprintf(void *stream, const char *format, ...) {
    (void)stream;
    (void)format;
    return 0;
}
void *dlopen(const char *path, int mode) {
    (void)path;
    (void)mode;
    return (void *)0;
}
void *dlsym(void *handle, const char *name) {
    (void)handle;
    (void)name;
    return (void *)0;
}
int dlclose(void *handle) {
    (void)handle;
    return 0;
}
char *dlerror(void) { return "dynamic loading disabled"; }

int open(const char *path, int flags, ...) {
    (void)path;
    (void)flags;
    bridge_errno = 2;
    return -1;
}
int access(const char *path, int mode) {
    (void)path;
    (void)mode;
    return -1;
}
int close(int fd) {
    (void)fd;
    return -1;
}
int fchmod(int fd, unsigned mode) {
    (void)fd;
    (void)mode;
    return -1;
}
int fchown(int fd, unsigned owner, unsigned group) {
    (void)fd;
    (void)owner;
    (void)group;
    return -1;
}
int fcntl(int fd, int command, ...) {
    (void)fd;
    (void)command;
    return -1;
}
int fstat(int fd, void *result) {
    (void)fd;
    (void)result;
    return -1;
}
int ftruncate(int fd, long length) {
    (void)fd;
    (void)length;
    return -1;
}
char *getcwd(char *buffer, size_t size) {
    (void)buffer;
    (void)size;
    return (void *)0;
}
unsigned geteuid(void) { return 0; }
int lstat(const char *path, void *result) {
    (void)path;
    (void)result;
    return -1;
}
int mkdir(const char *path, unsigned mode) {
    (void)path;
    (void)mode;
    return -1;
}
void *mmap(void *address, size_t length, int protection, int flags, int fd, long offset) {
    (void)address;
    (void)length;
    (void)protection;
    (void)flags;
    (void)fd;
    (void)offset;
    return (void *)-1;
}
int munmap(void *address, size_t length) {
    (void)address;
    (void)length;
    return -1;
}
ssize_t pread(int fd, void *buffer, size_t size, long offset) {
    (void)fd;
    (void)buffer;
    (void)size;
    (void)offset;
    return -1;
}
ssize_t pwrite(int fd, const void *buffer, size_t size, long offset) {
    (void)fd;
    (void)buffer;
    (void)size;
    (void)offset;
    return -1;
}
ssize_t read(int fd, void *buffer, size_t size) {
    (void)fd;
    (void)buffer;
    (void)size;
    return -1;
}
ssize_t readlink(const char *path, char *buffer, size_t size) {
    (void)path;
    (void)buffer;
    (void)size;
    return -1;
}
int rmdir(const char *path) {
    (void)path;
    return -1;
}
int stat(const char *path, void *result) {
    (void)path;
    (void)result;
    return -1;
}
int unlink(const char *path) {
    (void)path;
    return -1;
}
ssize_t write(int fd, const void *buffer, size_t size) {
    (void)fd;
    (void)buffer;
    (void)size;
    return -1;
}
int flock(int fd, int operation) {
    (void)fd;
    (void)operation;
    return -1;
}
int fsync(int fd) {
    (void)fd;
    return -1;
}
int fsctl(const char *path, unsigned command, void *data, unsigned options) {
    (void)path;
    (void)command;
    (void)data;
    (void)options;
    return -1;
}
int statfs(const char *path, void *result) {
    (void)path;
    (void)result;
    return -1;
}
int fstatfs(int fd, void *result) {
    (void)fd;
    (void)result;
    return -1;
}
int futimes(int fd, const void *times) {
    (void)fd;
    (void)times;
    return -1;
}
int utimes(const char *path, const void *times) {
    (void)path;
    (void)times;
    return -1;
}
int rename(const char *old_path, const char *new_path) {
    (void)old_path;
    (void)new_path;
    return -1;
}

int sqlite_e2e(void) {
    if (sqlite3_threadsafe() != 1)
        return -700;
    sqlite3 *database = (void *)0;
    sqlite3_stmt *statement = (void *)0;
    int rc = sqlite3_open(":memory:", &database);
    if (rc != 0)
        return -100 - rc;
    rc = sqlite3_exec(database,
        "CREATE TABLE values_table(value INTEGER);"
        "INSERT INTO values_table VALUES(42);",
        (void *)0, (void *)0, (void *)0);
    if (rc != 0) {
        sqlite3_close(database);
        return -200 - rc;
    }
    rc = sqlite3_prepare_v2(database, "SELECT value FROM values_table", -1, &statement, (void *)0);
    if (rc != 0) {
        sqlite3_close(database);
        return -300 - rc;
    }
    rc = sqlite3_step(statement);
    if (rc != 100) {
        sqlite3_finalize(statement);
        sqlite3_close(database);
        return -400 - rc;
    }
    int value = sqlite3_column_int(statement, 0);
    rc = sqlite3_finalize(statement);
    if (rc != 0) {
        sqlite3_close(database);
        return -500 - rc;
    }
    rc = sqlite3_close(database);
    if (rc != 0)
        return -600 - rc;
    return value;
}
EOF

COMMON_FLAGS=(
	-S -O2 -fwrapv -fno-builtin -fno-common -fomit-frame-pointer -fno-stack-protector
	-fno-jump-tables -fno-asynchronous-unwind-tables -fno-unwind-tables
	-fno-vectorize -fno-slp-vectorize -mno-outline
	-ffixed-x27 -ffixed-x28 --target=arm64-apple-darwin
)
clang "${COMMON_FLAGS[@]}" \
	-DSQLITE_THREADSAFE=1 -DSQLITE_MAX_WORKER_THREADS=0 \
	-o "$WORK/sqlite.s" "$SOURCE"
if ! grep -Eq '^[[:space:]]*\.globl[[:space:]]+_?sqlite3_open([[:space:]]|$)' "$WORK/sqlite.s"; then
	echo "SQLite compiler output is missing sqlite3_open" >&2
	exit 1
fi
if ! grep -Eq '^[[:space:]]*(stp|sub)[[:space:]].*(sp|wsp)' "$WORK/sqlite.s"; then
	echo "SQLite compiler output has no C stack-frame instructions" >&2
	exit 1
fi
clang "${COMMON_FLAGS[@]}" -I "$ROOT/nativecall" -o "$WORK/bridge.s" "$WORK/bridge.c"

# SQLite and the bridge are separate translation units. Namespace every bridge
# local symbol before concatenation so Mach-O local labels retain TU scope.
python3 - "$WORK/bridge.s" "$WORK/bridge.namespaced.s" <<'PY'
import re
import sys
from pathlib import Path

source, destination = map(Path, sys.argv[1:])
text = source.read_text()
lines = text.splitlines()
symbol_char = r"A-Za-z0-9_.$"
label_def = re.compile(rf"^([{symbol_char}]+):")
global_def = re.compile(rf"^\s*\.globl\s+([{symbol_char}]+)")
globals_ = {m.group(1) for line in lines if (m := global_def.match(line))}
labels = {m.group(1) for line in lines if (m := label_def.match(line))}
for line in lines:
    if re.match(r"^\s*\.zerofill\b", line):
        fields = [field.strip() for field in line.split(",")]
        if len(fields) >= 3:
            labels.add(fields[2])
labels.difference_update(globals_)
renamed = {
    label: "_c2goasm_sqlite_bridge_" + re.sub(r"[^A-Za-z0-9_]", "_", label)
    for label in labels
}
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
token = re.compile(rf"(?<![{symbol_char}])[A-Za-z_.$][{symbol_char}]*(?![{symbol_char}])")
text = token.sub(lambda match: renamed.get(match.group(0), match.group(0)), text)
destination.write_text(text)
PY

cat "$WORK/sqlite.s" "$WORK/bridge.namespaced.s" > "$WORK/sqlite-input.s"

cat > "$WORK/qmod/internal/native/sqlite.go" <<'EOF'
package native
EOF

"$C2GOASM" -t arm64 "$WORK/sqlite-input.s" "$WORK/qmod/internal/native/sqlite.s"

cat > "$WORK/qmod/internal/native/address.go" <<'EOF'
package native

func InstallMemoryAddress() uintptr
func InstallThreadsAddress() uintptr
func SQLiteAddress() uintptr
EOF

cat > "$WORK/qmod/internal/native/address.s" <<'EOF'
#include "textflag.h"

TEXT ·InstallMemoryAddress(SB), NOSPLIT, $0-8
    MOVD $·_c2goasm_native_c2goasm_install_memory(SB), R0
    MOVD R0, ret+0(FP)
    RET

TEXT ·InstallThreadsAddress(SB), NOSPLIT, $0-8
    MOVD $·_c2goasm_native_c2goasm_install_threads(SB), R0
    MOVD R0, ret+0(FP)
    RET

TEXT ·SQLiteAddress(SB), NOSPLIT, $0-8
    MOVD $·_c2goasm_native_sqlite_e2e(SB), R0
    MOVD R0, ret+0(FP)
    RET
EOF

cat > "$WORK/qmod/sqlite.go" <<'EOF'
package sqlite

import (
    "sync"

    "example.com/sqlite/internal/native"
    "github.com/faceair/c2goasm/nativecall"
)

var installHost sync.Once

func sqliteRun() int32 {
    installHost.Do(func() {
        nativecall.InstallMemory(native.InstallMemoryAddress())
        nativecall.InstallThreads(native.InstallThreadsAddress())
    })
    return nativecall.Call0(native.SQLiteAddress())
}
EOF

cat > "$WORK/qmod/sqlite_test.go" <<'EOF'
package sqlite

import (
    "sync"
    "testing"
)

func TestSQLiteInMemorySQL(t *testing.T) {
    if got := sqliteRun(); got != 42 {
        t.Fatalf("converted SQLite query result = %d, want 42", got)
    }
}

func TestSQLiteConcurrent(t *testing.T) {
    const workers = 4
    failures := make(chan int32, workers)
    var wait sync.WaitGroup
    wait.Add(workers)
    for range workers {
        go func() {
            defer wait.Done()
            if result := sqliteRun(); result != 42 {
                failures <- result
            }
        }()
    }
    wait.Wait()
    close(failures)
    for result := range failures {
        t.Errorf("concurrent SQLite result = %d, want 42", result)
    }
}
EOF

cat > "$WORK/qmod/go.mod" <<EOF
module example.com/sqlite

go 1.26.5

require github.com/faceair/c2goasm v0.0.0

replace github.com/faceair/c2goasm => $ROOT
EOF

(cd "$WORK/qmod" && CGO_ENABLED=1 GOARCH=arm64 go test -count=1 -v ./...)
