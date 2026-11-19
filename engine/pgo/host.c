/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
/* The host callbacks of engine/host.h in plain C, for the training binaries
   of engine/pgo/train.sh and the C side of engine/diff: the engine is compiled as the generator compiles it
   (SQLITE_OS_OTHER, os.c, engine/libc) and runs natively with these in the
   place of the Go host. One process and one thread: files with the locks of
   SQLite between the connections of the process, shared memory for WAL on
   the heap, no sync. Functions, collations and virtual tables written in Go
   have no counterpart here, and a call of one stops the training. */
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>
#ifdef __APPLE__
#include <malloc/malloc.h>
#else
#include <malloc.h>
#endif

#include "sqlite3.h"
#include "host.h"

int go_init(void);

__attribute__((constructor)) static void start(void) {
  if (go_init() != SQLITE_OK) {
    fprintf(stderr, "training host: go_init failed\n");
    exit(1);
  }
}

#ifdef __APPLE__
size_t malloc_usable_size(void *p) { return p ? malloc_size(p) : 0; }
#endif

static void unused(const char *what) {
  fprintf(stderr, "training host: %s is not implemented\n", what);
  abort();
}

static void *must(void *p) {
  if (!p) {
    fprintf(stderr, "training host: out of memory\n");
    abort();
  }
  return p;
}

/* ---- mutexes ---- */

struct hmutex {
  pthread_mutex_t m;
};
#define NSTATIC 16
static struct hmutex statics[NSTATIC];
static int staticsReady;

static void initMutex(struct hmutex *x, int recursive) {
  pthread_mutexattr_t a;
  pthread_mutexattr_init(&a);
  if (recursive) pthread_mutexattr_settype(&a, PTHREAD_MUTEX_RECURSIVE);
  pthread_mutex_init(&x->m, &a);
  pthread_mutexattr_destroy(&a);
}

long host_mutex_alloc(int type) {
  if (type >= 2) {
    if (type - 2 >= NSTATIC) unused("a static mutex past the table");
    if (!staticsReady) {
      for (int i = 0; i < NSTATIC; i++) initMutex(&statics[i], 0);
      staticsReady = 1;
    }
    return (long)&statics[type - 2];
  }
  struct hmutex *x = must(calloc(1, sizeof(*x)));
  initMutex(x, type == SQLITE_MUTEX_RECURSIVE);
  return (long)x;
}
void host_mutex_free(long m) {
  struct hmutex *x = (struct hmutex *)m;
  pthread_mutex_destroy(&x->m);
  free(x);
}
void host_mutex_enter(long m) { pthread_mutex_lock(&((struct hmutex *)m)->m); }
int host_mutex_try(long m) {
  return pthread_mutex_trylock(&((struct hmutex *)m)->m) == 0 ? SQLITE_OK : SQLITE_BUSY;
}
void host_mutex_leave(long m) { pthread_mutex_unlock(&((struct hmutex *)m)->m); }

/* ---- files ---- */

struct hfile;

/* node is what the open files of one name share: the locks of SQLite on the
   file, and the shared memory of its WAL with the locks on that. */
struct node {
  char *name;
  int nref;
  int shared; /* files that hold SHARED or more */
  struct hfile *reserved, *pending, *exclusive;
  void **region;
  int nregion;
  int shmRef; /* files that use the shared memory */
  int shmShared[SQLITE_SHM_NLOCK];
  struct hfile *shmExcl[SQLITE_SHM_NLOCK];
  struct node *next;
};
static struct node *nodes;

struct hfile {
  int fd;
  struct node *n;
  int level;                   /* SQLITE_LOCK_* */
  int shm;                     /* this file uses the shared memory */
  unsigned shmShared, shmExcl; /* the shm locks this file holds, a bit each */
};

static struct node *nodeOf(const char *name) {
  for (struct node *n = nodes; n; n = n->next)
    if (!strcmp(n->name, name)) {
      n->nref++;
      return n;
    }
  struct node *n = must(calloc(1, sizeof(*n)));
  n->name = must(strdup(name));
  n->nref = 1;
  n->next = nodes;
  nodes = n;
  return n;
}

static void nodeDrop(struct node *n) {
  if (--n->nref > 0) return;
  for (struct node **p = &nodes; *p; p = &(*p)->next)
    if (*p == n) {
      *p = n->next;
      break;
    }
  for (int i = 0; i < n->nregion; i++) free(n->region[i]);
  free(n->region);
  free(n->name);
  free(n);
}

int host_open(const char *name, int flags, struct host_open_args *a) {
  char tmp[96];
  int del = (flags & SQLITE_OPEN_DELETEONCLOSE) != 0;
  if (!name) {
    static unsigned long seq;
    const char *dir = getenv("TMPDIR");
    snprintf(tmp, sizeof tmp, "%s/pgo-train-%d-%lu", dir && strlen(dir) < 48 ? dir : "/tmp", (int)getpid(), ++seq);
    name = tmp;
    del = 1;
  }
  int o = (flags & SQLITE_OPEN_READWRITE) ? O_RDWR : O_RDONLY;
  if (flags & SQLITE_OPEN_CREATE) o |= O_CREAT;
  if (flags & SQLITE_OPEN_EXCLUSIVE) o |= O_EXCL;
  int fd = open(name, o, 0644);
  if (fd < 0) return SQLITE_CANTOPEN;
  if (del) unlink(name);
  struct hfile *f = must(calloc(1, sizeof(*f)));
  f->fd = fd;
  f->n = nodeOf(name);
  a->handle = (long)f;
  a->out_flags = flags;
  a->iocap = 0;
  return SQLITE_OK;
}
int host_delete(const char *name, int sync_dir) {
  (void)sync_dir;
  if (unlink(name) != 0 && errno != ENOENT) return SQLITE_IOERR_DELETE;
  return SQLITE_OK;
}
int host_access(const char *name, int flags, int *res) {
  (void)flags;
  *res = access(name, F_OK) == 0;
  return SQLITE_OK;
}
int host_full_pathname(const char *name, int n, char *out) {
  if ((int)strlen(name) + 1 > n) return SQLITE_CANTOPEN;
  strcpy(out, name);
  return SQLITE_OK;
}
/* The same bytes on every run, so that two trainings do the same work. */
void host_randomness(char *out, int n) {
  static unsigned long long s = 0x9e3779b97f4a7c15ull;
  for (int i = 0; i < n; i++) {
    s ^= s << 13;
    s ^= s >> 7;
    s ^= s << 17;
    out[i] = (char)(s >> 24);
  }
}
void host_sleep(int us) { usleep(us); }
long long host_now_ms(void) {
#ifdef HOST_NOW_MS
  /* engine/diff/diff.c: the same time in C and in Go. */
  return HOST_NOW_MS;
#endif
  struct timespec t;
  clock_gettime(CLOCK_REALTIME, &t);
  return (long long)t.tv_sec * 1000 + t.tv_nsec / 1000000;
}
/* localtime, not localtime_r: os.c defines a localtime_r of its own, which
   calls this function. */
void host_localtime(long long t, int *out) {
  time_t x = (time_t)t;
  struct tm *tm = localtime(&x);
  if (!tm) unused("localtime of this time");
  out[0] = tm->tm_sec;
  out[1] = tm->tm_min;
  out[2] = tm->tm_hour;
  out[3] = tm->tm_mday;
  out[4] = tm->tm_mon;
  out[5] = tm->tm_year;
  out[6] = tm->tm_wday;
  out[7] = tm->tm_yday;
  out[8] = tm->tm_isdst > 0;
}

/* shmUse counts h as a user of the shared memory of its file. */
static void shmUse(struct hfile *h) {
  if (!h->shm) {
    h->shm = 1;
    h->n->shmRef++;
  }
}

/* shmRelease ends the use of the shared memory by h: its locks go, and when
   it was the last user the memory goes too, so that the next user starts
   with none, as the first user of a -shm file truncates it. */
static void shmRelease(struct hfile *h) {
  struct node *n = h->n;
  for (int i = 0; i < SQLITE_SHM_NLOCK; i++) {
    if (h->shmShared & (1u << i)) n->shmShared[i]--;
    if (h->shmExcl & (1u << i)) n->shmExcl[i] = 0;
  }
  h->shmShared = h->shmExcl = 0;
  if (!h->shm) return;
  h->shm = 0;
  if (--n->shmRef > 0) return;
  for (int i = 0; i < n->nregion; i++) free(n->region[i]);
  free(n->region);
  n->region = 0;
  n->nregion = 0;
}

int host_close(long f) {
  struct hfile *h = (struct hfile *)f;
  host_unlock(f, SQLITE_LOCK_NONE);
  shmRelease(h);
  close(h->fd);
  nodeDrop(h->n);
  free(h);
  return SQLITE_OK;
}
int host_read(long f, void *buf, int n, long long off) {
  struct hfile *h = (struct hfile *)f;
  ssize_t got = pread(h->fd, buf, n, off);
  if (got < 0) return SQLITE_IOERR_READ;
  if (got < n) {
    memset((char *)buf + got, 0, n - got);
    return SQLITE_IOERR_SHORT_READ;
  }
  return SQLITE_OK;
}
int host_write(long f, const void *buf, int n, long long off) {
  struct hfile *h = (struct hfile *)f;
  if (pwrite(h->fd, buf, n, off) != n) return SQLITE_IOERR_WRITE;
  return SQLITE_OK;
}
int host_truncate(long f, long long size) {
  return ftruncate(((struct hfile *)f)->fd, size) == 0 ? SQLITE_OK : SQLITE_IOERR_TRUNCATE;
}
int host_sync(long f, int flags) {
  (void)f, (void)flags;
  return SQLITE_OK;
}
int host_file_size(long f, long long *size) {
  struct stat st;
  if (fstat(((struct hfile *)f)->fd, &st) != 0) return SQLITE_IOERR_FSTAT;
  *size = st.st_size;
  return SQLITE_OK;
}

/* The locks of SQLite on a file, between the files of this process that
   have the same name, as internal/vfs keeps them. */
int host_lock(long f, int level) {
  struct hfile *h = (struct hfile *)f;
  struct node *n = h->n;
  if (level <= h->level) return SQLITE_OK;
  if (level == SQLITE_LOCK_SHARED) {
    if ((n->pending && n->pending != h) || (n->exclusive && n->exclusive != h)) return SQLITE_BUSY;
    n->shared++;
    h->level = SQLITE_LOCK_SHARED;
    return SQLITE_OK;
  }
  if (level == SQLITE_LOCK_RESERVED) {
    if (n->reserved && n->reserved != h) return SQLITE_BUSY;
    n->reserved = h;
    h->level = SQLITE_LOCK_RESERVED;
    return SQLITE_OK;
  }
  if ((n->reserved && n->reserved != h) || (n->pending && n->pending != h)) return SQLITE_BUSY;
  n->pending = h;
  if (h->level < SQLITE_LOCK_PENDING) h->level = SQLITE_LOCK_PENDING;
  if (n->shared > 1) return SQLITE_BUSY;
  n->exclusive = h;
  h->level = SQLITE_LOCK_EXCLUSIVE;
  return SQLITE_OK;
}
int host_unlock(long f, int level) {
  struct hfile *h = (struct hfile *)f;
  struct node *n = h->n;
  if (h->level <= level) return SQLITE_OK;
  if (n->reserved == h) n->reserved = 0;
  if (n->pending == h) n->pending = 0;
  if (n->exclusive == h) n->exclusive = 0;
  if (level == SQLITE_LOCK_NONE) n->shared--;
  h->level = level;
  return SQLITE_OK;
}
int host_check_reserved_lock(long f, int *res) {
  *res = ((struct hfile *)f)->n->reserved != 0;
  return SQLITE_OK;
}
int host_lock_state(long f) { return ((struct hfile *)f)->level; }
int host_size_hint(long f, long long size) {
  (void)f, (void)size;
  return SQLITE_OK;
}
void host_chunk_size(long f, int n) { (void)f, (void)n; }
int host_has_moved(long f) {
  (void)f;
  return 0;
}

/* The shared memory of a WAL: regions on the heap, one set for each name. */
int host_shm_map(long f, int region, int size, int extend, void volatile **p) {
  struct node *n = ((struct hfile *)f)->n;
  shmUse((struct hfile *)f);
  *p = 0;
  if (region >= n->nregion) {
    if (!extend) return SQLITE_OK;
    n->region = must(realloc(n->region, (region + 1) * sizeof(void *)));
    for (int i = n->nregion; i <= region; i++) n->region[i] = must(calloc(1, size));
    n->nregion = region + 1;
  }
  *p = n->region[region];
  return SQLITE_OK;
}
int host_shm_lock(long f, int offset, int cnt, int flags) {
  struct hfile *h = (struct hfile *)f;
  struct node *n = h->n;
  unsigned mask = ((1u << cnt) - 1) << offset;
  shmUse(h);
  if (flags & SQLITE_SHM_UNLOCK) {
    for (int i = offset; i < offset + cnt; i++) {
      if ((flags & SQLITE_SHM_SHARED) && (h->shmShared & (1u << i))) n->shmShared[i]--;
      if ((flags & SQLITE_SHM_EXCLUSIVE) && (h->shmExcl & (1u << i))) n->shmExcl[i] = 0;
    }
    if (flags & SQLITE_SHM_SHARED) h->shmShared &= ~mask;
    else h->shmExcl &= ~mask;
    return SQLITE_OK;
  }
  if (flags & SQLITE_SHM_SHARED) {
    for (int i = offset; i < offset + cnt; i++)
      if (n->shmExcl[i] && n->shmExcl[i] != h) return SQLITE_BUSY;
    for (int i = offset; i < offset + cnt; i++)
      if (!(h->shmShared & (1u << i))) n->shmShared[i]++;
    h->shmShared |= mask;
    return SQLITE_OK;
  }
  for (int i = offset; i < offset + cnt; i++) {
    int own = (h->shmShared >> i) & 1;
    if ((n->shmExcl[i] && n->shmExcl[i] != h) || n->shmShared[i] > own) return SQLITE_BUSY;
  }
  for (int i = offset; i < offset + cnt; i++) n->shmExcl[i] = h;
  h->shmExcl |= mask;
  return SQLITE_OK;
}
void host_shm_barrier(void) { __sync_synchronize(); }
int host_shm_unmap(long f, int delete_flag) {
  (void)delete_flag;
  shmRelease((struct hfile *)f);
  return SQLITE_OK;
}

/* ---- what only a program with Go functions calls ---- */

void host_func(sqlite3_context *ctx, int argc, sqlite3_value **argv) {
  (void)ctx, (void)argc, (void)argv;
  unused("host_func");
}
void host_step(sqlite3_context *ctx, int argc, sqlite3_value **argv) {
  (void)ctx, (void)argc, (void)argv;
  unused("host_step");
}
void host_final(sqlite3_context *ctx) {
  (void)ctx;
  unused("host_final");
}
void host_value(sqlite3_context *ctx) {
  (void)ctx;
  unused("host_value");
}
void host_inverse(sqlite3_context *ctx, int argc, sqlite3_value **argv) {
  (void)ctx, (void)argc, (void)argv;
  unused("host_inverse");
}
void host_func_destroy(long h) { (void)h; }
int host_collate(long h, int n1, const void *p1, int n2, const void *p2) {
  (void)h, (void)n1, (void)p1, (void)n2, (void)p2;
  unused("host_collate");
  return 0;
}
void host_collate_destroy(long h) { (void)h; }
int host_vtab_connect(struct host_vtab_connect_args *a) {
  (void)a;
  unused("host_vtab_connect");
  return SQLITE_ERROR;
}
int host_vtab_best_index(sqlite3_vtab *v, long t, sqlite3_index_info *i) {
  (void)v, (void)t, (void)i;
  unused("host_vtab_best_index");
  return SQLITE_ERROR;
}
int host_vtab_disconnect(long t) {
  (void)t;
  unused("host_vtab_disconnect");
  return SQLITE_ERROR;
}
int host_vtab_open(sqlite3_vtab *v, long t, long *c) {
  (void)v, (void)t, (void)c;
  unused("host_vtab_open");
  return SQLITE_ERROR;
}
int host_vtab_close(long c) {
  (void)c;
  unused("host_vtab_close");
  return SQLITE_ERROR;
}
int host_vtab_filter(sqlite3_vtab_cursor *c, int n, const char *s, int argc, sqlite3_value **argv) {
  (void)c, (void)n, (void)s, (void)argc, (void)argv;
  unused("host_vtab_filter");
  return SQLITE_ERROR;
}
int host_vtab_next(sqlite3_vtab_cursor *c) {
  (void)c;
  unused("host_vtab_next");
  return SQLITE_ERROR;
}
int host_vtab_column(sqlite3_vtab_cursor *c, sqlite3_context *x, int i) {
  (void)c, (void)x, (void)i;
  unused("host_vtab_column");
  return SQLITE_ERROR;
}
int host_vtab_rowid(sqlite3_vtab_cursor *c, sqlite3_int64 *r) {
  (void)c, (void)r;
  unused("host_vtab_rowid");
  return SQLITE_ERROR;
}
int host_vtab_update(sqlite3_vtab *v, long t, int argc, sqlite3_value **argv, sqlite3_int64 *r) {
  (void)v, (void)t, (void)argc, (void)argv, (void)r;
  unused("host_vtab_update");
  return SQLITE_ERROR;
}
int host_vtab_tx(sqlite3_vtab *v, long t, int op, int n) {
  (void)v, (void)t, (void)op, (void)n;
  unused("host_vtab_tx");
  return SQLITE_ERROR;
}
void host_module_destroy(long h) { (void)h; }
