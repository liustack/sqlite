/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
/* The operating system of SQLite: a VFS and mutexes that call the Go host
   (host.h). Compiled after sqlite3.c in the same unit (sqlite.c), using only
   the public API. */
#include "host.h"

/* ---- mutexes ---- */

static int goMutexInit(void) { return SQLITE_OK; }
static int goMutexEnd(void) { return SQLITE_OK; }
static sqlite3_mutex *goMutexAlloc(int type) { return (sqlite3_mutex *)host_mutex_alloc(type); }
static void goMutexFree(sqlite3_mutex *m) { host_mutex_free((long)m); }
static void goMutexEnter(sqlite3_mutex *m) { host_mutex_enter((long)m); }
static int goMutexTry(sqlite3_mutex *m) { return host_mutex_try((long)m); }
static void goMutexLeave(sqlite3_mutex *m) { host_mutex_leave((long)m); }

static const sqlite3_mutex_methods goMutexMethods = {
    goMutexInit, goMutexEnd, goMutexAlloc, goMutexFree,
    goMutexEnter, goMutexTry, goMutexLeave, 0, 0,
};

/* go_init installs the mutexes and starts SQLite. The host calls it once,
   before any other function. */
int go_init(void) {
  int rc = sqlite3_config(SQLITE_CONFIG_MUTEX, &goMutexMethods);
  if (rc != SQLITE_OK) return rc;
  return sqlite3_initialize();
}

/* ---- local time ---- */

/* localtime_r is what the localtime and utc modifiers call
   (HAVE_LOCALTIME_R). The Go host knows the time zone. */
struct tm *localtime_r(const time_t *t, struct tm *tm) {
  int f[9];
  host_localtime(*t, f);
  tm->tm_sec = f[0];
  tm->tm_min = f[1];
  tm->tm_hour = f[2];
  tm->tm_mday = f[3];
  tm->tm_mon = f[4];
  tm->tm_year = f[5];
  tm->tm_wday = f[6];
  tm->tm_yday = f[7];
  tm->tm_isdst = f[8];
  return tm;
}

/* ---- functions written in Go ---- */

static void goFunc(sqlite3_context *ctx, int argc, sqlite3_value **argv) { host_func(ctx, argc, argv); }
static void goFuncDestroy(void *p) { host_func_destroy((long)p); }

/* go_create_function makes the Go function of handle h the SQL function
   name. SQLite calls host_func_destroy when it is replaced or the connection
   closes, and also when the call fails. */
int go_create_function(sqlite3 *db, const char *name, int nArg, int flags, long h) {
  return sqlite3_create_function_v2(db, name, nArg, flags, (void *)h, goFunc, 0, 0, goFuncDestroy);
}

static void goStep(sqlite3_context *ctx, int argc, sqlite3_value **argv) { host_step(ctx, argc, argv); }
static void goFinal(sqlite3_context *ctx) { host_final(ctx); }

/* go_create_aggregate is go_create_function for an aggregate: SQLite calls
   goStep for each row of a group and goFinal at its end, also when the
   statement stops early. */
int go_create_aggregate(sqlite3 *db, const char *name, int nArg, int flags, long h) {
  return sqlite3_create_function_v2(db, name, nArg, flags, (void *)h, 0, goStep, goFinal, goFuncDestroy);
}

static int goCollate(void *h, int n1, const void *p1, int n2, const void *p2) {
  return host_collate((long)h, n1, p1, n2, p2);
}
static void goCollateDestroy(void *h) { host_collate_destroy((long)h); }

/* go_create_collation makes the Go comparison of handle h the collation
   name. Unlike the functions, SQLite does not call goCollateDestroy when
   this fails: the caller drops h then. */
int go_create_collation(sqlite3 *db, const char *name, long h) {
  return sqlite3_create_collation_v2(db, name, SQLITE_UTF8, (void *)h, goCollate, goCollateDestroy);
}

/* ---- statements for group commit ---- */

/* goClassify is the authorizer the host installs on every connection. As a
   statement is prepared, it sorts it into *flags: GO_WRITES_MAIN when it
   changes a table of the main database, GO_PRIVATE when it is not plain DML
   or touches something only its own connection has (another schema such as
   temp, a write of a trigger, which may be a temporary one, or a function
   that reads the connection's state). The driver merges a statement into another
   connection's transaction only with GO_WRITES_MAIN and no GO_PRIVATE.
   GO_STATE marks a statement that changes the connection itself (a PRAGMA
   that sets something, ATTACH, DETACH, a temporary table, index, view or
   trigger): after one, the connection no longer runs as every other
   connection of its pool does, so the driver stops merging with it.

   goClassify also sees the statements SQLite prepares while it prepares the
   host's: R*Tree prepares writes of its shadow tables when a connection
   first uses one of its tables. What they do is not the statement's, so
   they never add GO_WRITES_MAIN. They may still add GO_PRIVATE and GO_STATE,
   which only stop merging. */
#define GO_WRITES_MAIN 1
#define GO_PRIVATE 2
#define GO_STATE 4

static int isMain(const char *db) { return db && strcmp(db, "main") == 0; }
static int isTemp(const char *db) { return db && strcmp(db, "temp") == 0; }

/* infoPragma says a PRAGMA with an argument only reads: the ones that take
   the name of a table or an index. */
static int infoPragma(const char *name) {
  static const char *const names[] = {"table_info",       "table_xinfo",     "index_info",
                                      "index_xinfo",      "index_list",      "foreign_key_list",
                                      "foreign_key_check", "integrity_check", "quick_check"};
  for (unsigned i = 0; i < sizeof names / sizeof names[0]; i++)
    if (sqlite3_stricmp(name, names[i]) == 0) return 1;
  return 0;
}

/* goClassifyWord is the word of the host's header goClassify writes to,
   with the connection it is installed on. */
struct goClassifyWord {
  int flags;
  sqlite3 *db;
};

/* goNested says the statement being parsed on db is one SQLite prepares
   while it prepares another. The body of a trigger is coded in a Parse of
   its own whose pToplevel is the statement it belongs to. Parse is internal
   to SQLite, as sqlite3OomFault is. */
static int goNested(sqlite3 *db) {
  Parse *p = db->pParse;
  if (p->pToplevel) p = p->pToplevel;
  return p->pOuterParse != 0;
}

static int goClassify(void *p, int op, const char *a, const char *b, const char *db, const char *trigger) {
  struct goClassifyWord *w = (struct goClassifyWord *)p;
  int f = 0, *flags = &f;
  (void)a;
  switch (op) {
  case SQLITE_INSERT:
  case SQLITE_UPDATE:
  case SQLITE_DELETE:
    /* trigger names the trigger a write comes from, which may be a
       temporary one. For a read it names a view or a common table
       expression. */
    *flags |= isMain(db) && !trigger ? GO_WRITES_MAIN : GO_PRIVATE;
    break;
  case SQLITE_READ:
    /* A read of a common table expression or a subquery has no schema. */
    if (db && !isMain(db)) *flags |= GO_PRIVATE;
    break;
  case SQLITE_SELECT:
  case SQLITE_RECURSIVE:
    break;
  case SQLITE_FUNCTION:
    if (sqlite3_stricmp(b, "last_insert_rowid") == 0 || sqlite3_stricmp(b, "changes") == 0 ||
        sqlite3_stricmp(b, "total_changes") == 0)
      *flags |= GO_PRIVATE;
    break;
  case SQLITE_PRAGMA:
    /* a is the name of the pragma, b its argument. Without an argument a
       pragma reads its setting. */
    *flags |= GO_PRIVATE;
    if (b && !infoPragma(a)) *flags |= GO_STATE;
    break;
  case SQLITE_ATTACH:
  case SQLITE_DETACH:
  case SQLITE_CREATE_TEMP_INDEX:
  case SQLITE_CREATE_TEMP_TABLE:
  case SQLITE_CREATE_TEMP_TRIGGER:
  case SQLITE_CREATE_TEMP_VIEW:
  case SQLITE_DROP_TEMP_INDEX:
  case SQLITE_DROP_TEMP_TABLE:
  case SQLITE_DROP_TEMP_TRIGGER:
  case SQLITE_DROP_TEMP_VIEW:
    *flags |= GO_PRIVATE | GO_STATE;
    break;
  case SQLITE_CREATE_VTABLE:
  case SQLITE_DROP_VTABLE:
    *flags |= GO_PRIVATE;
    if (isTemp(db)) *flags |= GO_STATE;
    break;
  default:
    *flags |= GO_PRIVATE;
  }
  if (goNested(w->db)) f &= ~GO_WRITES_MAIN;
  w->flags |= f;
  return SQLITE_OK;
}

/* go_classify installs goClassify on db with the word it writes to. */
int go_classify(sqlite3 *db, struct goClassifyWord *w) {
  w->db = db;
  return sqlite3_set_authorizer(db, goClassify, w);
}

/* goRollbackHook counts the transactions SQLite rolls back on db, in the word
   go_count_rollbacks gives it: a statement that writes and was running when
   one ended learns it lost what it wrote (Stmt.Step). */
static void goRollbackHook(void *n) { ++*(volatile unsigned *)n; }

/* go_count_rollbacks installs goRollbackHook on db with the word it counts in. */
int go_count_rollbacks(sqlite3 *db, unsigned *n) {
  sqlite3_rollback_hook(db, goRollbackHook, n);
  return SQLITE_OK;
}

/* go_collate_failed records the panic of a Go collation on db the way SQLite
   records an allocation that failed: the comparisons after the panic gave 0,
   so the statement must not finish. SQLite stops it with SQLITE_NOMEM and
   rolls it back, or the whole transaction, on the error path it tests for
   every allocation (commits, VACUUM and savepoints all take it). sqlite3OomFault
   is internal to SQLite: this file is compiled with sqlite3.c (sqlite.c). */
void go_collate_failed(sqlite3 *db) { sqlite3OomFault(db); }

/* go_vtabs_in_tx says virtual tables (FTS5, R*Tree) take part in the open
   transaction of db: one was written since it began. db->nVTrans is internal
   to SQLite, as sqlite3OomFault is. */
int go_vtabs_in_tx(sqlite3 *db) { return db->nVTrans > 0; }

/* ---- files ---- */

typedef struct GoFile {
  sqlite3_file base;
  long h;        /* the Go handle */
  int persistWal; /* SQLITE_FCNTL_PERSIST_WAL */
  int psow;       /* SQLITE_FCNTL_POWERSAFE_OVERWRITE */
  int iocap;      /* the rest of the SQLITE_IOCAP_* bits, from the Go side */
} GoFile;

#define FH(f) (((GoFile *)(f))->h)

static int goClose(sqlite3_file *f) { return host_close(FH(f)); }

static int goRead(sqlite3_file *f, void *buf, int n, sqlite3_int64 off) {
  return host_read(FH(f), buf, n, off);
}

static int goWrite(sqlite3_file *f, const void *buf, int n, sqlite3_int64 off) {
  return host_write(FH(f), buf, n, off);
}

static int goTruncate(sqlite3_file *f, sqlite3_int64 size) { return host_truncate(FH(f), size); }
static int goSync(sqlite3_file *f, int flags) { return host_sync(FH(f), flags); }

static int goFileSize(sqlite3_file *f, sqlite3_int64 *size) {
  long long s = 0;
  int rc = host_file_size(FH(f), &s);
  *size = s;
  return rc;
}

static int goLock(sqlite3_file *f, int level) { return host_lock(FH(f), level); }
static int goUnlock(sqlite3_file *f, int level) { return host_unlock(FH(f), level); }
static int goCheckReservedLock(sqlite3_file *f, int *res) { return host_check_reserved_lock(FH(f), res); }

/* modeBit reads (arg < 0) or sets a flag of the file, as os_unix does. */
static void modeBit(int *flag, int *arg) {
  if (*arg < 0) {
    *arg = *flag;
  } else {
    *flag = *arg != 0;
  }
}

static int goFileControl(sqlite3_file *f, int op, void *arg) {
  GoFile *g = (GoFile *)f;
  switch (op) {
  case SQLITE_FCNTL_LOCKSTATE:
    *(int *)arg = host_lock_state(g->h);
    return SQLITE_OK;
  case SQLITE_FCNTL_SIZE_HINT:
    return host_size_hint(g->h, *(sqlite3_int64 *)arg);
  case SQLITE_FCNTL_HAS_MOVED:
    *(int *)arg = host_has_moved(g->h);
    return SQLITE_OK;
  case SQLITE_FCNTL_PERSIST_WAL:
    modeBit(&g->persistWal, (int *)arg);
    return SQLITE_OK;
  case SQLITE_FCNTL_POWERSAFE_OVERWRITE:
    modeBit(&g->psow, (int *)arg);
    return SQLITE_OK;
  case SQLITE_FCNTL_VFSNAME:
    *(char **)arg = sqlite3_mprintf("%s", "go");
    return SQLITE_OK;
  case SQLITE_FCNTL_CHUNK_SIZE:
    host_chunk_size(g->h, *(int *)arg);
    return SQLITE_OK;
  }
  return SQLITE_NOTFOUND;
}

static int goSectorSize(sqlite3_file *f) {
  (void)f;
  return 4096;
}

static int goDeviceCharacteristics(sqlite3_file *f) {
  GoFile *g = (GoFile *)f;
  return g->iocap | (g->psow ? SQLITE_IOCAP_POWERSAFE_OVERWRITE : 0);
}

static int goShmMap(sqlite3_file *f, int region, int size, int extend, void volatile **p) {
  return host_shm_map(FH(f), region, size, extend, p);
}

static int goShmLock(sqlite3_file *f, int offset, int n, int flags) {
  return host_shm_lock(FH(f), offset, n, flags);
}

static void goShmBarrier(sqlite3_file *f) {
  (void)f;
  host_shm_barrier();
}

static int goShmUnmap(sqlite3_file *f, int deleteFlag) { return host_shm_unmap(FH(f), deleteFlag); }

static const sqlite3_io_methods goIoMethods = {
    2, goClose, goRead, goWrite, goTruncate, goSync, goFileSize,
    goLock, goUnlock, goCheckReservedLock, goFileControl, goSectorSize,
    goDeviceCharacteristics, goShmMap, goShmLock, goShmBarrier, goShmUnmap,
    0, 0,
};

/* ---- the VFS ---- */

/* internal/csqlite's host_open reads and writes these offsets. */
_Static_assert(__builtin_offsetof(struct host_open_args, modeof) == 0, "host_open_args.modeof");
_Static_assert(__builtin_offsetof(struct host_open_args, handle) == 8, "host_open_args.handle");
_Static_assert(__builtin_offsetof(struct host_open_args, readonly_shm) == 16, "host_open_args.readonly_shm");
_Static_assert(__builtin_offsetof(struct host_open_args, out_flags) == 20, "host_open_args.out_flags");
_Static_assert(__builtin_offsetof(struct host_open_args, iocap) == 24, "host_open_args.iocap");

static int goOpen(sqlite3_vfs *vfs, sqlite3_filename name, sqlite3_file *f, int flags, int *outFlags) {
  GoFile *g = (GoFile *)f;
  int out = 0;
  struct host_open_args a;
  int rc;
  (void)vfs;
  memset(g, 0, sizeof(*g));
  /* What os_unix also reads from the URI: the mode a new database is made
  ** with, and a -shm opened for reading only. */
  memset(&a, 0, sizeof(a));
  a.modeof = (flags & SQLITE_OPEN_URI) ? sqlite3_uri_parameter(name, "modeof") : 0;
  a.readonly_shm = sqlite3_uri_boolean(name, "readonly_shm", 0);
  rc = host_open(name, flags, &a);
  if (rc != SQLITE_OK) return rc;
  out = a.out_flags;
  g->h = a.handle;
  g->iocap = a.iocap;
  /* Powersafe overwrite is on unless the URI turns it off, as in os_unix. */
  g->psow = sqlite3_uri_boolean(name, "psow", SQLITE_POWERSAFE_OVERWRITE);
  g->base.pMethods = &goIoMethods;
  if (outFlags) *outFlags = out;
  return SQLITE_OK;
}

static int goDelete(sqlite3_vfs *vfs, const char *name, int syncDir) {
  (void)vfs;
  return host_delete(name, syncDir);
}

static int goAccess(sqlite3_vfs *vfs, const char *name, int flags, int *res) {
  (void)vfs;
  return host_access(name, flags, res);
}

static int goFullPathname(sqlite3_vfs *vfs, const char *name, int n, char *out) {
  (void)vfs;
  return host_full_pathname(name, n, out);
}

static int goRandomness(sqlite3_vfs *vfs, int n, char *out) {
  (void)vfs;
  host_randomness(out, n);
  return n;
}

static int goSleep(sqlite3_vfs *vfs, int us) {
  (void)vfs;
  host_sleep(us);
  return us;
}

/* The Julian day of the Unix epoch, in milliseconds. */
#define UNIX_EPOCH_JD_MS ((sqlite3_int64)24405875 * 8640000)

static int goCurrentTimeInt64(sqlite3_vfs *vfs, sqlite3_int64 *t) {
  (void)vfs;
  *t = UNIX_EPOCH_JD_MS + host_now_ms();
  return SQLITE_OK;
}

static int goCurrentTime(sqlite3_vfs *vfs, double *t) {
  sqlite3_int64 ms;
  goCurrentTimeInt64(vfs, &ms);
  *t = ms / 86400000.0;
  return SQLITE_OK;
}

static int goGetLastError(sqlite3_vfs *vfs, int n, char *out) {
  (void)vfs;
  (void)n;
  (void)out;
  return 0;
}

static sqlite3_vfs goVfs = {
    2, sizeof(GoFile), 512, 0, "go", 0,
    goOpen, goDelete, goAccess, goFullPathname,
    0, 0, 0, 0,
    goRandomness, goSleep, goCurrentTime, goGetLastError, goCurrentTimeInt64,
    0, 0, 0,
};

int sqlite3_os_init(void) { return sqlite3_vfs_register(&goVfs, 1); }
int sqlite3_os_end(void) { return SQLITE_OK; }
