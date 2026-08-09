#ifndef C2GOASM_NATIVECALL_THREADS_H
#define C2GOASM_NATIVECALL_THREADS_H

struct c2goasm_threads {
  int (*mutexattr_init)(void *);
  int (*mutexattr_settype)(void *, int);
  int (*mutexattr_destroy)(void *);
  int (*mutex_init)(void *, const void *);
  int (*mutex_destroy)(void *);
  int (*mutex_lock)(void *);
  int (*mutex_trylock)(void *);
  int (*mutex_unlock)(void *);
};

#endif
