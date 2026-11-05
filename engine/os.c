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

static void goValue(sqlite3_context *ctx) { host_value(ctx); }
static void goInverse(sqlite3_context *ctx, int argc, sqlite3_value **argv) { host_inverse(ctx, argc, argv); }

/* go_create_window is go_create_aggregate for an aggregate window function:
   goValue gives the result for the current frame, and goInverse takes out
   a row that left it. */
int go_create_window(sqlite3 *db, const char *name, int nArg, int flags, long h) {
  return sqlite3_create_window_function(db, name, nArg, flags, (void *)h, goStep, goFinal, goValue, goInverse,
                                        goFuncDestroy);
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

/* ---- virtual tables written in Go ---- */

/* A table and a cursor of a Go module are handles of the Go tables, kept
   after SQLite's own struct. The Go side answers xEof ahead: Filter and Next
   write eof, so that xEof does not leave C. */
typedef struct GoVtab {
  sqlite3_vtab base;
  long h;
} GoVtab;

typedef struct GoVcursor {
  sqlite3_vtab_cursor base;
  long h;
  int eof;
} GoVcursor;

/* internal/csqlite reads and writes these offsets. */
_Static_assert(__builtin_offsetof(GoVcursor, base.pVtab) == 0, "GoVcursor.pVtab");
_Static_assert(__builtin_offsetof(GoVcursor, h) == 8, "GoVcursor.h");
_Static_assert(__builtin_offsetof(GoVcursor, eof) == 16, "GoVcursor.eof");
_Static_assert(__builtin_offsetof(struct host_vtab_connect_args, db) == 0, "connect.db");
_Static_assert(__builtin_offsetof(struct host_vtab_connect_args, module) == 8, "connect.module");
_Static_assert(__builtin_offsetof(struct host_vtab_connect_args, argv) == 16, "connect.argv");
_Static_assert(__builtin_offsetof(struct host_vtab_connect_args, argc) == 24, "connect.argc");
_Static_assert(__builtin_offsetof(struct host_vtab_connect_args, config) == 28, "connect.config");
_Static_assert(__builtin_offsetof(struct host_vtab_connect_args, table) == 32, "connect.table");
_Static_assert(__builtin_offsetof(struct host_vtab_connect_args, err) == 40, "connect.err");
_Static_assert(__builtin_offsetof(sqlite3_index_info, nConstraint) == 0, "index_info.nConstraint");
_Static_assert(__builtin_offsetof(sqlite3_index_info, aConstraint) == 8, "index_info.aConstraint");
_Static_assert(__builtin_offsetof(sqlite3_index_info, nOrderBy) == 16, "index_info.nOrderBy");
_Static_assert(__builtin_offsetof(sqlite3_index_info, aOrderBy) == 24, "index_info.aOrderBy");
_Static_assert(__builtin_offsetof(sqlite3_index_info, aConstraintUsage) == 32, "index_info.aConstraintUsage");
_Static_assert(__builtin_offsetof(sqlite3_index_info, idxNum) == 40, "index_info.idxNum");
_Static_assert(__builtin_offsetof(sqlite3_index_info, idxStr) == 48, "index_info.idxStr");
_Static_assert(__builtin_offsetof(sqlite3_index_info, needToFreeIdxStr) == 56, "index_info.needToFreeIdxStr");
_Static_assert(__builtin_offsetof(sqlite3_index_info, orderByConsumed) == 60, "index_info.orderByConsumed");
_Static_assert(__builtin_offsetof(sqlite3_index_info, estimatedCost) == 64, "index_info.estimatedCost");
_Static_assert(__builtin_offsetof(sqlite3_index_info, estimatedRows) == 72, "index_info.estimatedRows");
_Static_assert(__builtin_offsetof(sqlite3_index_info, idxFlags) == 80, "index_info.idxFlags");
_Static_assert(__builtin_offsetof(sqlite3_index_info, colUsed) == 88, "index_info.colUsed");
_Static_assert(sizeof(struct sqlite3_index_constraint) == 12, "index_constraint");
_Static_assert(__builtin_offsetof(struct sqlite3_index_constraint, op) == 4, "index_constraint.op");
_Static_assert(__builtin_offsetof(struct sqlite3_index_constraint, usable) == 5, "index_constraint.usable");
_Static_assert(sizeof(struct sqlite3_index_orderby) == 8, "index_orderby");
_Static_assert(__builtin_offsetof(struct sqlite3_index_orderby, desc) == 4, "index_orderby.desc");
_Static_assert(sizeof(struct sqlite3_index_constraint_usage) == 8, "index_constraint_usage");
_Static_assert(__builtin_offsetof(struct sqlite3_index_constraint_usage, omit) == 4, "index_constraint_usage.omit");

static int goVtabConnect(sqlite3 *db, void *module, int argc, const char *const *argv, sqlite3_vtab **pp,
                         char **pzErr) {
  struct host_vtab_connect_args a;
  GoVtab *t;
  int rc;
  memset(&a, 0, sizeof(a));
  a.db = db;
  a.module = (long)module;
  a.argc = argc;
  a.argv = argv;
  rc = host_vtab_connect(&a);
  if (rc != SQLITE_OK) {
    *pzErr = a.err;
    return rc;
  }
  if (a.config & GO_VTAB_DIRECTONLY) sqlite3_vtab_config(db, SQLITE_VTAB_DIRECTONLY);
  if (a.config & GO_VTAB_INNOCUOUS) sqlite3_vtab_config(db, SQLITE_VTAB_INNOCUOUS);
  t = sqlite3_malloc(sizeof(*t));
  if (!t) {
    host_vtab_disconnect(a.table);
    return SQLITE_NOMEM;
  }
  memset(t, 0, sizeof(*t));
  t->h = a.table;
  *pp = &t->base;
  return SQLITE_OK;
}

static int goVtabBestIndex(sqlite3_vtab *v, sqlite3_index_info *info) {
  return host_vtab_best_index(v, ((GoVtab *)v)->h, info);
}

static int goVtabDisconnect(sqlite3_vtab *v) {
  GoVtab *t = (GoVtab *)v;
  int rc = host_vtab_disconnect(t->h);
  sqlite3_free(t->base.zErrMsg);
  sqlite3_free(t);
  return rc;
}

static int goVtabOpen(sqlite3_vtab *v, sqlite3_vtab_cursor **pp) {
  GoVcursor *c = sqlite3_malloc(sizeof(*c));
  int rc;
  if (!c) return SQLITE_NOMEM;
  memset(c, 0, sizeof(*c));
  rc = host_vtab_open(v, ((GoVtab *)v)->h, &c->h);
  if (rc != SQLITE_OK) {
    sqlite3_free(c);
    return rc;
  }
  c->eof = 1;
  *pp = &c->base;
  return SQLITE_OK;
}

static int goVtabClose(sqlite3_vtab_cursor *p) {
  GoVcursor *c = (GoVcursor *)p;
  int rc = host_vtab_close(c->h);
  sqlite3_free(c);
  return rc;
}

static int goVtabFilter(sqlite3_vtab_cursor *c, int idxNum, const char *idxStr, int argc, sqlite3_value **argv) {
  return host_vtab_filter(c, idxNum, idxStr, argc, argv);
}

static int goVtabNext(sqlite3_vtab_cursor *c) { return host_vtab_next(c); }

static int goVtabEof(sqlite3_vtab_cursor *c) { return ((GoVcursor *)c)->eof; }

static int goVtabColumn(sqlite3_vtab_cursor *c, sqlite3_context *ctx, int i) {
  return host_vtab_column(c, ctx, i);
}

static int goVtabRowid(sqlite3_vtab_cursor *c, sqlite3_int64 *rowid) { return host_vtab_rowid(c, rowid); }

/* xCreate does what xConnect does, but must be another function: SQLite
   makes the name of a module whose xCreate is its xConnect a table too. */
static int goVtabCreate(sqlite3 *db, void *module, int argc, const char *const *argv, sqlite3_vtab **pp,
                        char **pzErr) {
  return goVtabConnect(db, module, argc, argv, pp, pzErr);
}

static int goVtabBegin(sqlite3_vtab *v);
static int goVtabRollback(sqlite3_vtab *v);
static int goVtabSavepoint(sqlite3_vtab *v, int n);

/* goWritableCreate also begins the new table's transaction: SQLite puts a
   table it creates in the transaction without calling xBegin, and calls its
   xSync and xCommit, or xRollback, when the transaction ends. Inside a
   savepoint it also does what sqlite3VtabBegin does: it marks the table as
   part of the savepoints open now and calls xSavepoint. Without that,
   ROLLBACK TO would not reach the table, and its writes would be committed
   after the savepoint that made it was rolled back. db->pVtabCtx is
   SQLite's own, as Parse is (goNested). */
static int goWritableCreate(sqlite3 *db, void *module, int argc, const char *const *argv, sqlite3_vtab **pp,
                            char **pzErr) {
  int rc = goVtabConnect(db, module, argc, argv, pp, pzErr);
  int n = db->nStatement + db->nSavepoint;
  if (rc != SQLITE_OK) return rc;
  rc = goVtabBegin(*pp);
  if (rc == SQLITE_OK && n) {
    db->pVtabCtx->pVTable->iSavepoint = n;
    rc = goVtabSavepoint(*pp, n - 1);
    if (rc != SQLITE_OK) {
      char *msg = (*pp)->zErrMsg;
      (*pp)->zErrMsg = 0;
      goVtabRollback(*pp);
      sqlite3_free((*pp)->zErrMsg);
      (*pp)->zErrMsg = msg;
    }
  }
  if (rc != SQLITE_OK) {
    *pzErr = (*pp)->zErrMsg;
    (*pp)->zErrMsg = 0;
    goVtabDisconnect(*pp);
    *pp = 0;
  }
  return rc;
}

static int goVtabUpdate(sqlite3_vtab *v, int argc, sqlite3_value **argv, sqlite3_int64 *rowid) {
  return host_vtab_update(v, ((GoVtab *)v)->h, argc, argv, rowid);
}

static int goVtabBegin(sqlite3_vtab *v) { return host_vtab_tx(v, ((GoVtab *)v)->h, GO_TX_BEGIN, 0); }
static int goVtabSync(sqlite3_vtab *v) { return host_vtab_tx(v, ((GoVtab *)v)->h, GO_TX_SYNC, 0); }
static int goVtabCommit(sqlite3_vtab *v) { return host_vtab_tx(v, ((GoVtab *)v)->h, GO_TX_COMMIT, 0); }
static int goVtabRollback(sqlite3_vtab *v) { return host_vtab_tx(v, ((GoVtab *)v)->h, GO_TX_ROLLBACK, 0); }
static int goVtabSavepoint(sqlite3_vtab *v, int n) { return host_vtab_tx(v, ((GoVtab *)v)->h, GO_TX_SAVEPOINT, n); }
static int goVtabRelease(sqlite3_vtab *v, int n) { return host_vtab_tx(v, ((GoVtab *)v)->h, GO_TX_RELEASE, n); }
static int goVtabRollbackTo(sqlite3_vtab *v, int n) {
  return host_vtab_tx(v, ((GoVtab *)v)->h, GO_TX_ROLLBACK_TO, n);
}

/* goModule serves CREATE VIRTUAL TABLE. goEponymousModule serves only the
   name of the module used as a table (a table-valued function): it has no
   xCreate. Neither writes: no xUpdate, so SQLite refuses INSERT, UPDATE and
   DELETE on their tables. goWritableModule is goModule with writes and the
   transaction methods, and goClassify keeps its writes out of group commit. */
static const sqlite3_module goModule = {
    0, goVtabCreate, goVtabConnect, goVtabBestIndex, goVtabDisconnect, goVtabDisconnect,
    goVtabOpen, goVtabClose, goVtabFilter, goVtabNext, goVtabEof, goVtabColumn, goVtabRowid,
};

static const sqlite3_module goEponymousModule = {
    0, 0, goVtabConnect, goVtabBestIndex, goVtabDisconnect, goVtabDisconnect,
    goVtabOpen, goVtabClose, goVtabFilter, goVtabNext, goVtabEof, goVtabColumn, goVtabRowid,
};

static const sqlite3_module goWritableModule = {
    2,           goWritableCreate, goVtabConnect,  goVtabBestIndex, goVtabDisconnect, goVtabDisconnect,
    goVtabOpen,  goVtabClose,     goVtabFilter,   goVtabNext,      goVtabEof,        goVtabColumn,
    goVtabRowid, goVtabUpdate,    goVtabBegin,    goVtabSync,      goVtabCommit,     goVtabRollback,
    0,           0,               goVtabSavepoint, goVtabRelease,  goVtabRollbackTo,
};

/* goWritesGoTable says whether table of schema is a table of the writable
   Go module on db. It reads SQLite's own structs (Table, Module), as this
   file is compiled with sqlite3.c. */
static int goWritesGoTable(sqlite3 *db, const char *table, const char *schema) {
  Table *t;
  Module *m;
  if (!table) return 0;
  t = sqlite3FindTable(db, table, schema);
  if (!t || !IsVirtual(t) || t->u.vtab.nArg < 1) return 0;
  m = (Module *)sqlite3HashFind(&db->aModule, t->u.vtab.azArg[0]);
  return m && m->pModule == &goWritableModule;
}

static void goModuleDestroy(void *h) { host_module_destroy((long)h); }

/* go_create_module makes the Go module of handle h the module name. SQLite
   calls host_module_destroy when it is replaced or the connection closes,
   and also when the call fails. */
int go_create_module(sqlite3 *db, const char *name, long h, int kind) {
  const sqlite3_module *m = kind == GO_MODULE_EPONYMOUS ? &goEponymousModule
                            : kind == GO_MODULE_WRITABLE ? &goWritableModule
                                                         : &goModule;
  return sqlite3_create_module_v2(db, name, m, (void *)h,
                                  goModuleDestroy);
}

/* go_vtab_error makes z the error message of the table v, for SQLite to
   report when a method returns an error. */
void go_vtab_error(sqlite3_vtab *v, const char *z) {
  sqlite3_free(v->zErrMsg);
  v->zErrMsg = sqlite3_mprintf("%s", z);
}

/* go_strdup copies z into memory of sqlite3_malloc, for SQLite to free. */
char *go_strdup(const char *z) { return sqlite3_mprintf("%s", z); }

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
/* GO_GUARDED: goClassify refused a PRAGMA that would take the checkpoints
   of a replicated database from the replicator (goGuarded). */
#define GO_GUARDED 8

static int isMain(const char *db) { return db && strcmp(db, "main") == 0; }
static int isTemp(const char *db) { return db && strcmp(db, "temp") == 0; }

/* isMainFile says schema db of connection conn is the main database: main,
   or the same file attached under another name. */
static int isMainFile(sqlite3 *conn, const char *db) {
  if (isMain(db)) return 1;
  const char *f = sqlite3_db_filename(conn, db), *m = sqlite3_db_filename(conn, "main");
  return f && m && *m && strcmp(f, m) == 0;
}

/* goGuarded says a PRAGMA of name with argument arg (NULL for none) on schema
   db (NULL for every one) of connection conn would checkpoint the main
   database, turn the automatic checkpoints back on, take the main database
   out of WAL mode, or lock it for one connection: on a replicated database
   the replicator runs every checkpoint, after it has read the frames
   (replica/internal/capture). wal_autocheckpoint is of the connection,
   whatever schema it names. */
static int goGuarded(sqlite3 *conn, const char *name, const char *arg, const char *db) {
  if (arg && sqlite3_stricmp(name, "wal_autocheckpoint") == 0) return 1;
  if (db && !isMainFile(conn, db)) return 0;
  if (sqlite3_stricmp(name, "wal_checkpoint") == 0) return 1;
  if (!arg) return 0;
  if (sqlite3_stricmp(name, "journal_mode") == 0) return sqlite3_stricmp(arg, "wal") != 0;
  if (sqlite3_stricmp(name, "locking_mode") == 0) return sqlite3_stricmp(arg, "normal") != 0;
  return 0;
}

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
   with the connection it is installed on. merging is GO_MERGING while the
   connection runs a statement of another connection's caller, and
   goClassify makes it GO_REFUSED when it refuses one (goClassify). guard is
   not 0 on a connection to a replicated database (goGuarded). */
struct goClassifyWord {
  int flags;
  int merging;
  sqlite3 *db;
  int guard;
};
_Static_assert(__builtin_offsetof(struct goClassifyWord, merging) == 4, "goClassifyWord.merging");
_Static_assert(__builtin_offsetof(struct goClassifyWord, db) == 8, "goClassifyWord.db");
_Static_assert(__builtin_offsetof(struct goClassifyWord, guard) == 16, "goClassifyWord.guard");
#define GO_MERGING 1
#define GO_REFUSED 2

/* goNested says the statement being parsed on db is one SQLite prepares
   while it prepares another. The body of a trigger is coded in a Parse of
   its own whose pToplevel is the statement it belongs to. Parse is internal
   to SQLite, as sqlite3OomFault is. */
static int goNested(sqlite3 *db) {
  Parse *p = db->pParse;
  if (p->pToplevel) p = p->pToplevel;
  return p->pOuterParse != 0;
}

/* goReprepare says the statement being parsed on db, or the one it is
   parsed for, is one SQLite prepares again in sqlite3_step after a change
   of the schema (sqlite3Reprepare). */
static int goReprepare(sqlite3 *db) {
  Parse *p = db->pParse;
  for (;;) {
    if (p->pToplevel) p = p->pToplevel;
    if (!p->pOuterParse) return p->pReprepare != 0;
    p = p->pOuterParse;
  }
}

static int goClassify(void *p, int op, const char *a, const char *b, const char *db, const char *trigger) {
  struct goClassifyWord *w = (struct goClassifyWord *)p;
  int f = 0, *flags = &f;
  switch (op) {
  case SQLITE_INSERT:
  case SQLITE_UPDATE:
  case SQLITE_DELETE:
    /* trigger names the trigger a write comes from, which may be a
       temporary one. For a read it names a view or a common table
       expression. */
    /* A write to a table of the writable Go module never merges: its
       transaction methods run in Go, outside what a batch can undo. */
    *flags |= isMain(db) && !trigger && !goWritesGoTable(w->db, a, db) ? GO_WRITES_MAIN : GO_PRIVATE;
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
    if (w->guard && goGuarded(w->db, a, b, db)) {
      w->flags |= GO_GUARDED;
      return SQLITE_DENY;
    }
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
  /* A statement the driver sorted as mergeable is prepared again when the
     schema changed after it was prepared, and may be no longer mergeable.
     sqlite3_step finds the change before the statement does anything, so
     while the connection runs another caller's statement, the new program is
     refused: the step fails with nothing done, and the driver runs the
     statement on its caller's connection. */
  if (w->merging && (f & (GO_PRIVATE | GO_STATE)) && goReprepare(w->db)) {
    w->merging = GO_REFUSED;
    return SQLITE_DENY;
  }
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

/* ---- replication ---- */

/* The replicator reads a database as the read transaction open on its
   connection sees it: the header of the WAL that transaction reads, and its
   pages. Pager, Wal and WalIndexHdr are internal to SQLite, as Parse is.
   WalIndexHdr is also the header of the -shm file, a format every SQLite
   since 3.7.0 shares. */
_Static_assert(sizeof(WalIndexHdr) == 48, "WalIndexHdr");

/* goReadPager is the pager of the main database of db when a read
   transaction is open on it, or NULL. */
static Pager *goReadPager(sqlite3 *db) {
  if (!db->aDb[0].pBt || sqlite3_txn_state(db, "main") < SQLITE_TXN_READ) return 0;
  return sqlite3BtreePager(db->aDb[0].pBt);
}

/* go_wal_header copies the WAL header the read transaction of db reads
   (struct WalIndexHdr) to out: its mxFrame is the last frame of the last
   transaction that transaction sees. */
int go_wal_header(sqlite3 *db, void *out) {
  Pager *p = goReadPager(db);
  if (!p || !p->pWal || p->pWal->readLock < 0) return SQLITE_ERROR;
  memcpy(out, &p->pWal->hdr, sizeof(WalIndexHdr));
  return SQLITE_OK;
}

/* go_page_count is the size of the main database of db in pages, as its
   read transaction sees it. */
int go_page_count(sqlite3 *db, unsigned *n) {
  if (!goReadPager(db)) return SQLITE_ERROR;
  *n = sqlite3BtreeLastPage(db->aDb[0].pBt);
  return SQLITE_OK;
}

/* go_read_page copies page pgno of the main database of db, as its read
   transaction sees it, to out, which holds n bytes, the page size. The lock
   page cannot be read. */
int go_read_page(sqlite3 *db, unsigned pgno, void *out, int n) {
  Pager *p = goReadPager(db);
  DbPage *pg;
  int rc;
  if (!p || pgno == 0 || pgno > sqlite3BtreeLastPage(db->aDb[0].pBt) ||
      n != sqlite3BtreeGetPageSize(db->aDb[0].pBt))
    return SQLITE_ERROR;
  rc = sqlite3PagerGet(p, pgno, &pg, 0);
  if (rc != SQLITE_OK) return rc;
  memcpy(out, sqlite3PagerGetData(pg), n);
  sqlite3PagerUnref(pg);
  return SQLITE_OK;
}

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
