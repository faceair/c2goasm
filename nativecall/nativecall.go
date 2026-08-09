// Package nativecall enters c2goasm-generated C ABI functions through cgo.
package nativecall

/*
#cgo linux LDFLAGS: -pthread
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <pthread.h>
#if defined(__APPLE__)
#include <malloc/malloc.h>
#elif defined(__linux__)
#include <malloc.h>
#else
#error unsupported nativecall allocator platform
#endif
#include "memory.h"
#include "threads.h"

typedef int32_t (*c2goasm_call0_fn)(void);
typedef int32_t (*c2goasm_call_bytes_fn)(const unsigned char *, size_t);
typedef void (*c2goasm_install_memory_fn)(const struct c2goasm_memory *);
typedef void (*c2goasm_install_threads_fn)(const struct c2goasm_threads *);

static int32_t c2goasm_call0(uintptr_t address) {
	return ((c2goasm_call0_fn)address)();
}

static int32_t c2goasm_call_bytes(uintptr_t address, const unsigned char *value, size_t length) {
	return ((c2goasm_call_bytes_fn)address)(value, length);
}

static size_t c2goasm_malloc_usable_size(const void *pointer) {
	if (pointer == NULL)
		return 0;
#if defined(__APPLE__)
	return malloc_size(pointer);
#else
	return malloc_usable_size((void *)pointer);
#endif
}

static const struct c2goasm_memory c2goasm_system_memory = {
	malloc,
	free,
	realloc,
	c2goasm_malloc_usable_size,
};

static void c2goasm_install_memory(uintptr_t address) {
	((c2goasm_install_memory_fn)address)(&c2goasm_system_memory);
}

static int c2goasm_mutexattr_init(void *attribute) {
	return pthread_mutexattr_init((pthread_mutexattr_t *)attribute);
}

static int c2goasm_mutexattr_settype(void *attribute, int type) {
	return pthread_mutexattr_settype((pthread_mutexattr_t *)attribute, type);
}

static int c2goasm_mutexattr_destroy(void *attribute) {
	return pthread_mutexattr_destroy((pthread_mutexattr_t *)attribute);
}

static int c2goasm_mutex_init(void *mutex, const void *attribute) {
	return pthread_mutex_init(
		(pthread_mutex_t *)mutex,
		(const pthread_mutexattr_t *)attribute
	);
}

static int c2goasm_mutex_destroy(void *mutex) {
	return pthread_mutex_destroy((pthread_mutex_t *)mutex);
}

static int c2goasm_mutex_lock(void *mutex) {
	return pthread_mutex_lock((pthread_mutex_t *)mutex);
}

static int c2goasm_mutex_trylock(void *mutex) {
	return pthread_mutex_trylock((pthread_mutex_t *)mutex);
}

static int c2goasm_mutex_unlock(void *mutex) {
	return pthread_mutex_unlock((pthread_mutex_t *)mutex);
}

static const struct c2goasm_threads c2goasm_system_threads = {
	c2goasm_mutexattr_init,
	c2goasm_mutexattr_settype,
	c2goasm_mutexattr_destroy,
	c2goasm_mutex_init,
	c2goasm_mutex_destroy,
	c2goasm_mutex_lock,
	c2goasm_mutex_trylock,
	c2goasm_mutex_unlock,
};

static void c2goasm_install_threads(uintptr_t address) {
	((c2goasm_install_threads_fn)address)(&c2goasm_system_threads);
}
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// Call0 invokes a no-argument native function returning a C int32_t.
func Call0(address uintptr) int32 {
	if address == 0 {
		panic("nativecall: zero function address")
	}
	return int32(C.c2goasm_call0(C.uintptr_t(address)))
}

// InstallMemory passes the process system allocator to a native module. The
// target must accept a const struct c2goasm_memory pointer and retain it as
// read-only state.
func InstallMemory(address uintptr) {
	if address == 0 {
		panic("nativecall: zero memory installer address")
	}
	C.c2goasm_install_memory(C.uintptr_t(address))
}

// InstallThreads passes the process pthread mutex operations to a native
// module. The target must retain the table as read-only state.
func InstallThreads(address uintptr) {
	if address == 0 {
		panic("nativecall: zero thread installer address")
	}
	C.c2goasm_install_threads(C.uintptr_t(address))
}

// CallBytes invokes a native function receiving a byte pointer and length and
// returning a C int32_t. The native function must not retain value after it
// returns.
func CallBytes(address uintptr, value []byte) int32 {
	if address == 0 {
		panic("nativecall: zero function address")
	}
	var pointer *C.uchar
	if len(value) != 0 {
		pointer = (*C.uchar)(unsafe.Pointer(&value[0]))
	}
	result := int32(C.c2goasm_call_bytes(
		C.uintptr_t(address),
		pointer,
		C.size_t(len(value)),
	))
	runtime.KeepAlive(value)
	return result
}
