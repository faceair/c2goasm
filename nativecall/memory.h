#ifndef C2GOASM_NATIVECALL_MEMORY_H
#define C2GOASM_NATIVECALL_MEMORY_H

#include <stddef.h>

struct c2goasm_memory {
  void *(*allocate)(size_t);
  void (*release)(void *);
  void *(*resize)(void *, size_t);
  size_t (*usable_size)(const void *);
};

#endif
