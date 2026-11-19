/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
/* diff runs the SQL of SQLite's fuzz cases (test/fuzzdata*.db) on the engine
   compiled natively by clang, and prints what each statement gave, for
   TestDiffC (internal/csqlite/diffc_test.go) to hold the engine in Go
   assembly to it. The two sides do the same calls in the same order: a
   change here goes there too.

     diff [-slice i/n] [-case ID] FILE...

   The cases are each script of xsql on each image of db (an empty database
   when there is none), or alone when the script carries its database (a
   dbsqlfuzz case), numbered over all the files in the order given. Each runs
   on a memory database. memdb is the default VFS, so every other file a case
   opens (ATTACH, VACUUM INTO, temporary files) is in memory too, and no file
   of the machine plays a part. The time is fixed (HOST_NOW_MS) and TZ is UTC.

   For each statement, a line:

     ID N prepare RC MSG           sqlite3_prepare_v2 failed
     ID N empty                    no statement (white space, comments)
     ID N RC COLS ROWS HASH CHANGES ROWID MSG
                                   it ran: the code of its last step, the
                                   count of columns and rows, an FNV-1a hash
                                   of the column names and of each value
                                   with its type, sqlite3_changes64,
                                   sqlite3_last_insert_rowid, the message of
                                   the error ("-" when it ended well)
     ID N interrupted              stopped after STMT_MS: the case ends

   and ID end after the last. A case also ends after CASE_MS. The first line
   of all is "now MS", the time the host gives (HOST_NOW_MS, set by diff.sh). */
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "sqlite3.h"

#define STMT_MS 1000
#define CASE_MS 10000
#define MX_FILE_SZ 10000000

struct goClassifyWord {
  int flags;
  int merging;
  sqlite3 *db;
  int guard;
};
int go_classify(sqlite3 *db, struct goClassifyWord *w);
int go_count_rollbacks(sqlite3 *db, unsigned *n);

static void die(const char *what, const char *msg) {
  fprintf(stderr, "diff: %s: %s\n", what, msg);
  exit(1);
}

static long long nowMs(void) {
  struct timespec t;
  clock_gettime(CLOCK_MONOTONIC, &t);
  return (long long)t.tv_sec * 1000 + t.tv_nsec / 1000000;
}

/* ---- the hash ---- */

static uint64_t hash;
static void hbytes(const void *p, int n) {
  const unsigned char *b = p;
  for (int i = 0; i < n; i++) {
    hash ^= b[i];
    hash *= 0x100000001b3ull;
  }
}
static void hu64(uint64_t v) {
  unsigned char b[8];
  for (int i = 0; i < 8; i++) b[i] = (unsigned char)(v >> (8 * i));
  hbytes(b, 8);
}
static void hblob(const void *p, int n) {
  hu64((uint64_t)n);
  if (n > 0) hbytes(p, n);
}

/* ---- the cases ---- */

struct blob {
  int id;
  int n;
  unsigned char *b;
};

static int readBlobs(sqlite3 *db, const char *sql, struct blob **out) {
  sqlite3_stmt *st;
  *out = 0;
  if (sqlite3_prepare_v2(db, sql, -1, &st, 0) != SQLITE_OK) {
    if (strstr(sqlite3_errmsg(db), "no such table")) return 0;
    die(sql, sqlite3_errmsg(db));
  }
  int n = 0, cap = 0;
  while (sqlite3_step(st) == SQLITE_ROW) {
    if (n == cap) {
      cap = cap ? 2 * cap : 64;
      *out = realloc(*out, cap * sizeof(**out));
      if (!*out) die("realloc", "out of memory");
    }
    struct blob *b = &(*out)[n++];
    b->id = sqlite3_column_int(st, 0);
    b->n = sqlite3_column_bytes(st, 1);
    b->b = malloc(b->n + 1);
    if (!b->b) die("malloc", "out of memory");
    if (b->n) memcpy(b->b, sqlite3_column_blob(st, 1), b->n);
    b->b[b->n] = 0;
  }
  if (sqlite3_finalize(st) != SQLITE_OK) die(sql, sqlite3_errmsg(db));
  return n;
}

static int isHex(unsigned char c) { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'); }
static int hexVal(unsigned char c) {
  if (c <= '9') return c - '0';
  if (c <= 'F') return c - 'A' + 10;
  return c - 'a' + 10;
}

/* isDbSql is fuzzcheck's: the script starts with a database image. */
static int isDbSql(const unsigned char *a, int n) {
  if (n > 4 && !memcmp(a, "\n--\n", 4)) return 1;
  while (n > 0 && (a[0] == ' ' || (a[0] >= '\t' && a[0] <= '\r'))) a++, n--;
  char buf[9];
  int k = 0;
  for (int i = 0; i < n && k < 8; i++)
    if (isHex(a[i])) buf[k++] = (char)a[i];
  buf[k] = 0;
  return !strcmp(buf, "53514c69");
}

/* decodeDatabase is fuzzcheck's: the image at the start of a dbsqlfuzz case,
   in hex with [offset] marks, up to "\n--\n". It returns 0 when there is
   none, and sets *img, *nImg (the image, from malloc) and *iSql. */
static int decodeDatabase(const unsigned char *in, int nIn, unsigned char **img, int *nImg, int *iSql) {
  if (nIn < 4) return 0;
  unsigned n = (unsigned)nIn, mx = 0, i, k = 0, len = 0;
  unsigned char *a = calloc(1, MX_FILE_SZ), b = 0;
  if (!a) die("calloc", "out of memory");
  for (i = 0; i < n; i++) {
    unsigned char c = in[i];
    if (isHex(c)) {
      k++;
      if (k & 1) {
        b = (unsigned char)(hexVal(c) * 16);
        continue;
      }
      b += (unsigned char)hexVal(c);
      unsigned j = k / 2 - 1;
      if (j >= MX_FILE_SZ) {
        free(a);
        return 0;
      }
      if (j >= mx) {
        mx = (j + 4095) & ~4095u;
        if (mx > MX_FILE_SZ) mx = MX_FILE_SZ;
      }
      if (j + 1 > len) len = j + 1;
      a[j] = b;
    } else if (c == '[' && i < n - 3) {
      unsigned off = 0, m = i + 1;
      for (; m < n && in[m] != ']'; m++) {
        if (!isHex(in[m])) break;
        off = off * 16 + (unsigned)hexVal(in[m]);
      }
      if (m < n && in[m] == ']') {
        k = 2 * off;
        i = m;
      }
    } else if (c == '\n' && i < n - 4 && !memcmp(in + i, "\n--\n", 4)) {
      i += 4;
      break;
    }
  }
  *img = a;
  *nImg = (int)mx;
  *iSql = (int)i;
  return 1;
}

/* The bits of a dbsqlfuzz image that turn a setting of sqlite3_db_config
   over, as fuzzcheck's runCombinedDbSqlInput reads them. */
static const struct {
  unsigned mask;
  int op;
} dbConfigBits[] = {
    {0x0001, SQLITE_DBCONFIG_ENABLE_FKEY},
    {0x0002, SQLITE_DBCONFIG_ENABLE_TRIGGER},
    {0x0004, SQLITE_DBCONFIG_ENABLE_VIEW},
    {0x0008, SQLITE_DBCONFIG_ENABLE_QPSG},
    {0x0010, SQLITE_DBCONFIG_TRIGGER_EQP},
    {0x0020, SQLITE_DBCONFIG_DEFENSIVE},
    {0x0040, SQLITE_DBCONFIG_WRITABLE_SCHEMA},
    {0x0080, SQLITE_DBCONFIG_LEGACY_ALTER_TABLE},
    {0x0100, SQLITE_DBCONFIG_STMT_SCANSTATUS},
    {0x0200, SQLITE_DBCONFIG_REVERSE_SCANORDER},
    {0x0800, SQLITE_DBCONFIG_DQS_DML},
    {0x1000, SQLITE_DBCONFIG_DQS_DDL},
    {0x2000, SQLITE_DBCONFIG_TRUSTED_SCHEMA},
};

static long long deadline;
static int cut;
static int progress(void *p) {
  (void)p;
  if (!cut && nowMs() >= deadline) cut = 1;
  return cut;
}

static void printMsg(sqlite3 *db) {
  for (const char *m = sqlite3_errmsg(db); *m; m++) {
    if (*m == '\n') fputs("\\n", stdout);
    else if (*m == '\r') fputs("\\r", stdout);
    else if (*m == '\\') fputs("\\\\", stdout);
    else putchar(*m);
  }
}

/* run runs the statements of sql, cut at its first NUL, on db. */
static void run(const char *id, sqlite3 *db, const char *sql) {
  long long caseEnd = nowMs() + CASE_MS;
  const char *z = sql;
  for (int n = 0; *z; n++) {
    if (nowMs() >= caseEnd) {
      printf("%s %d interrupted\n", id, n);
      return;
    }
    cut = 0;
    deadline = nowMs() + STMT_MS;
    sqlite3_stmt *st = 0;
    const char *tail = 0;
    int rc = sqlite3_prepare_v2(db, z, -1, &st, &tail);
    if (cut) {
      sqlite3_finalize(st);
      printf("%s %d interrupted\n", id, n);
      return;
    }
    if (rc != SQLITE_OK) {
      printf("%s %d prepare %d ", id, n, rc);
      printMsg(db);
      putchar('\n');
    } else if (!st) {
      printf("%s %d empty\n", id, n);
    } else {
      hash = 0xcbf29ce484222325ull;
      int cols = sqlite3_column_count(st);
      for (int i = 0; i < cols; i++) {
        const char *name = sqlite3_column_name(st, i);
        hblob(name, name ? (int)strlen(name) : -1);
      }
      int explain = sqlite3_stmt_isexplain(st) == 1;
      long long rows = 0;
      while ((rc = sqlite3_step(st)) == SQLITE_ROW) {
        rows++;
        for (int i = 0; i < cols; i++) {
          int t = sqlite3_column_type(st, i);
          unsigned char tb = (unsigned char)t;
          hbytes(&tb, 1);
          switch (t) {
          case SQLITE_INTEGER:
            hu64((uint64_t)sqlite3_column_int64(st, i));
            break;
          case SQLITE_FLOAT: {
            double d = sqlite3_column_double(st, i);
            uint64_t u;
            memcpy(&u, &d, 8);
            hu64(u);
            break;
          }
          case SQLITE_TEXT: {
            const unsigned char *p = sqlite3_column_text(st, i);
            int n = p ? sqlite3_column_bytes(st, i) : -1;
            /* EXPLAIN shows a virtual table by its address. */
            if (explain && i == 5 && n > 5 && !memcmp(p, "vtab:", 5)) n = 5;
            hblob(p, n);
            break;
          }
          case SQLITE_BLOB: {
            const void *p = sqlite3_column_blob(st, i);
            hblob(p, sqlite3_column_bytes(st, i));
            break;
          }
          }
        }
      }
      if (cut) {
        sqlite3_finalize(st);
        printf("%s %d interrupted\n", id, n);
        return;
      }
      printf("%s %d %d %d %lld %016llx %lld %lld ", id, n, rc, cols, rows, (unsigned long long)hash,
             (long long)sqlite3_changes64(db), (long long)sqlite3_last_insert_rowid(db));
      if (rc == SQLITE_DONE) putchar('-');
      else printMsg(db);
      putchar('\n');
      sqlite3_finalize(st);
    }
    if (!tail || tail == z) return;
    z = tail;
  }
}

static sqlite3 *openCase(const char *id) {
  sqlite3 *db;
  sqlite3_randomness(0, 0);
  /* PRAGMA soft_heap_limit and hard_heap_limit set them, and they are global. */
  sqlite3_soft_heap_limit64(0);
  sqlite3_hard_heap_limit64(0);
  if (sqlite3_open_v2(":memory:", &db, SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_URI | SQLITE_OPEN_EXRESCODE,
                      0) != SQLITE_OK)
    die(id, sqlite3_errmsg(db));
  static struct goClassifyWord w;
  static unsigned rollbacks;
  memset(&w, 0, sizeof w);
  if (go_classify(db, &w) != SQLITE_OK || go_count_rollbacks(db, &rollbacks) != SQLITE_OK) die(id, "open");
  sqlite3_progress_handler(db, 1000, progress, 0);
  return db;
}

/* deserialize puts img in main. It returns 0 when SQLite refuses it. */
static int deserialize(sqlite3 *db, const unsigned char *img, int n) {
  unsigned char *p = sqlite3_malloc64(n > 0 ? n : 1);
  if (!p) die("sqlite3_malloc64", "out of memory");
  if (n) memcpy(p, img, n);
  if (sqlite3_deserialize(db, "main", p, n, n, SQLITE_DESERIALIZE_RESIZEABLE | SQLITE_DESERIALIZE_FREEONCLOSE) != SQLITE_OK)
    return 0;
  sqlite3_int64 x = MX_FILE_SZ;
  sqlite3_file_control(db, "main", SQLITE_FCNTL_SIZE_LIMIT, &x);
  return 1;
}

static void closeCase(const char *id, sqlite3 *db) {
  if (sqlite3_close(db) != SQLITE_OK) die(id, "close");
  printf("%s end\n", id);
}

/* plain runs a script on an image, or on an empty database. */
static void plain(const char *id, const struct blob *sql, const struct blob *img) {
  sqlite3 *db = openCase(id);
  if (img && img->n > 0 && !deserialize(db, img->b, img->n)) {
    printf("%s 0 deserialize\n", id);
    closeCase(id, db);
    return;
  }
  sqlite3_limit(db, SQLITE_LIMIT_LENGTH, 10000000);
  sqlite3_limit(db, SQLITE_LIMIT_LIKE_PATTERN_LENGTH, 50);
  sqlite3_test_control(SQLITE_TESTCTRL_INTERNAL_FUNCTIONS, db);
  sqlite3_test_control(SQLITE_TESTCTRL_PRNG_SEED, 1, db);
  run(id, db, (const char *)sql->b);
  closeCase(id, db);
}

/* combined runs a dbsqlfuzz case: its image, the settings its header turns
   over, and its SQL. */
static void combined(const char *id, const struct blob *sql) {
  unsigned char *img;
  int nImg, iSql;
  if (sql->n < 10 || !decodeDatabase(sql->b, sql->n, &img, &nImg, &iSql)) return;
  unsigned dbFlags = 0, dbOpt = 0;
  if (nImg >= 76) dbFlags = (unsigned)img[72] << 24 | (unsigned)img[73] << 16 | (unsigned)img[74] << 8 | img[75];
  if (nImg >= 80) dbOpt = (unsigned)img[76] << 24 | (unsigned)img[77] << 16 | (unsigned)img[78] << 8 | img[79];
  if (nImg >= 20 && img[18] == 2 && img[19] == 2) img[18] = img[19] = 1;
  sqlite3 *db = openCase(id);
  sqlite3_test_control(SQLITE_TESTCTRL_OPTIMIZATIONS, db, dbOpt);
  for (size_t i = 0; i < sizeof dbConfigBits / sizeof dbConfigBits[0]; i++) {
    if (!(dbFlags & dbConfigBits[i].mask)) continue;
    int on = 0;
    sqlite3_db_config(db, dbConfigBits[i].op, -1, &on);
    sqlite3_db_config(db, dbConfigBits[i].op, !on, 0);
  }
  sqlite3_limit(db, SQLITE_LIMIT_VDBE_OP, 25000);
  sqlite3_limit(db, SQLITE_LIMIT_LENGTH, 1000000);
  sqlite3_limit(db, SQLITE_LIMIT_EXPR_DEPTH, 500);
  sqlite3_limit(db, SQLITE_LIMIT_LIKE_PATTERN_LENGTH, 100);
  int ok = deserialize(db, img, nImg);
  free(img);
  if (!ok) {
    printf("%s 0 deserialize\n", id);
    closeCase(id, db);
    return;
  }
  sqlite3_test_control(SQLITE_TESTCTRL_PRNG_SEED, 1, db);
  run(id, db, (const char *)sql->b + iSql);
  closeCase(id, db);
}

int main(int argc, char **argv) {
  int rem = 0, mod = 1, i = 1;
  const char *only = 0;
  for (; i + 1 < argc && argv[i][0] == '-'; i += 2) {
    if (!strcmp(argv[i], "-slice")) {
      if (sscanf(argv[i + 1], "%d/%d", &rem, &mod) != 2 || mod <= 0 || rem < 0 || rem >= mod) die("-slice", argv[i + 1]);
    } else if (!strcmp(argv[i], "-case")) {
      only = argv[i + 1];
    } else {
      die("unknown option", argv[i]);
    }
  }
  if (i >= argc) die("usage", "diff [-slice i/n] [-case ID] FILE...");
  setenv("TZ", "UTC", 1);
  tzset();
  sqlite3_vfs *memdb = sqlite3_vfs_find("memdb");
  if (!memdb || sqlite3_vfs_register(memdb, 1) != SQLITE_OK) die("memdb", "cannot make it the default VFS");
  printf("now %lld\n", (long long)HOST_NOW_MS);
  long k = 0;
  for (; i < argc; i++) {
    const char *path = argv[i];
    const char *name = strrchr(path, '/') ? strrchr(path, '/') + 1 : path;
    sqlite3 *src;
    if (sqlite3_open_v2(path, &src, SQLITE_OPEN_READONLY, "go") != SQLITE_OK) die(path, sqlite3_errmsg(src));
    struct blob *scripts, *images;
    int nScripts = readBlobs(src, "SELECT sqlid, sqltext FROM xsql", &scripts);
    int nImages = readBlobs(src, "SELECT dbid, dbcontent FROM db", &images);
    sqlite3_close(src);
    struct blob none = {1, 0, 0};
    if (nImages == 0) images = &none, nImages = 1;
    char id[300];
    for (int s = 0; s < nScripts; s++) {
      if (isDbSql(scripts[s].b, scripts[s].n)) {
        if (k++ % mod != rem) continue;
        snprintf(id, sizeof id, "%s:%d", name, scripts[s].id);
        if (!only || !strcmp(only, id)) combined(id, &scripts[s]);
        continue;
      }
      for (int d = 0; d < nImages; d++) {
        if (k++ % mod != rem) continue;
        snprintf(id, sizeof id, "%s:%d:%d", name, scripts[s].id, images[d].id);
        if (!only || !strcmp(only, id)) plain(id, &scripts[s], &images[d]);
      }
    }
    fflush(stdout);
  }
  return 0;
}
