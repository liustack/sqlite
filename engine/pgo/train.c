/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
/* train FILE DIR: runs the sections of FILE (train.sql, which says how it is
   written) on SQLite, with database files in DIR, the way the driver uses the
   C API: statements prepared once and run many times with bound values, each
   column read by its type, writes merged as group commit merges them. For
   each section it prints the name, the statements run, the rows read, the
   statements a constraint refused and a sum of every value read, separated
   by tabs. Any other error stops it. */
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "sqlite3.h"

static const char *section = "";
static const char *dir;
static sqlite3 *db, *other;
static char dbPath[512];
static long long nRun, nRows, nRefused;
static uint64_t sum;

static void die(const char *what, const char *sql) {
  fprintf(stderr, "train: %s: %s", section, what);
  if (sql) fprintf(stderr, ": %.200s", sql);
  fprintf(stderr, "\n");
  exit(1);
}

static void mix(const void *p, size_t n) {
  const unsigned char *b = p;
  for (size_t i = 0; i < n; i++) sum = (sum ^ b[i]) * 0x100000001b3ull;
}

static uint64_t hash(uint64_t x) {
  x += 0x9e3779b97f4a7c15ull;
  x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9ull;
  x = (x ^ (x >> 27)) * 0x94d049bb133111ebull;
  return x ^ (x >> 31);
}

static const char *words[] = {
    "amber",  "basalt", "cedar",   "delta",  "ember",  "fjord",  "granite", "harbor", "indigo", "juniper", "kelp",
    "lagoon", "meadow", "nectar",  "orchid", "pebble", "quartz", "river",   "summit", "tundra", "umber",   "valley",
    "willow", "xenon",  "yarrow",  "zephyr", "anchor", "bridge", "candle",  "drift",  "engine", "forest",  "garden",
    "hollow", "island", "journey", "kettle", "ladder", "market", "needle",  "orange", "packet", "quiver",  "ribbon",
    "signal", "thread", "update",  "vector", "window", "yellow",
};
#define NWORDS (int)(sizeof words / sizeof words[0])

/* bindAll gives the parameters of st their values for turn i, by name:
     :i  the turn, from 1
     :r  a number below 2^31 that jumps from turn to turn
     :q  another such number
     :s  a short text of :r, one of 250007
     :t  a line of 5 to 16 words
     :f  a float
     :b  a blob of 8 to 71 bytes
     :n  NULL */
static void bindAll(sqlite3_stmt *st, long long i) {
  int n = sqlite3_bind_parameter_count(st);
  for (int k = 1; k <= n; k++) {
    const char *name = sqlite3_bind_parameter_name(st, k);
    uint64_t r = hash(i) >> 33, q = hash(i * 2654435761u + 17) >> 33;
    char buf[256];
    int rc = SQLITE_OK;
    if (!name || name[0] != ':' || !name[1] || name[2]) die("a parameter that is not one of :i :r :q :s :t :f :b :n", sqlite3_sql(st));
    switch (name[1]) {
    case 'i':
      rc = sqlite3_bind_int64(st, k, i);
      break;
    case 'r':
      rc = sqlite3_bind_int64(st, k, (sqlite3_int64)r);
      break;
    case 'q':
      rc = sqlite3_bind_int64(st, k, (sqlite3_int64)q);
      break;
    case 's':
      snprintf(buf, sizeof buf, "%s-%06d", words[r % NWORDS], (int)(r % 250007));
      rc = sqlite3_bind_text(st, k, buf, -1, SQLITE_TRANSIENT);
      break;
    case 't': {
      int len = 0, nw = 5 + (int)(q % 12);
      uint64_t h = r;
      for (int w = 0; w < nw; w++) {
        h = hash(h + w);
        len += snprintf(buf + len, sizeof buf - len, "%s%s", w ? " " : "", words[h % NWORDS]);
      }
      rc = sqlite3_bind_text(st, k, buf, len, SQLITE_TRANSIENT);
      break;
    }
    case 'f':
      rc = sqlite3_bind_double(st, k, (double)(r % 1000000) / 7.0);
      break;
    case 'b': {
      int len = 8 + (int)(r % 64);
      uint64_t h = q;
      for (int j = 0; j < len; j++) {
        if (j % 8 == 0) h = hash(h);
        buf[j] = (char)(h >> (8 * (j % 8)));
      }
      rc = sqlite3_bind_blob(st, k, buf, len, SQLITE_TRANSIENT);
      break;
    }
    case 'n':
      rc = sqlite3_bind_null(st, k);
      break;
    default:
      die("a parameter that is not one of :i :r :q :s :t :f :b :n", sqlite3_sql(st));
    }
    if (rc != SQLITE_OK) die(sqlite3_errmsg(sqlite3_db_handle(st)), sqlite3_sql(st));
  }
}

/* run steps st to its end and reads every column of every row by its type,
   as the driver does. It returns 0, or 1 when a constraint refused the
   statement and refusedOK lets that pass. */
static int run(sqlite3_stmt *st, int refusedOK) {
  int rc, cols = sqlite3_column_count(st);
  while ((rc = sqlite3_step(st)) == SQLITE_ROW) {
    nRows++;
    for (int c = 0; c < cols; c++) {
      int t = sqlite3_column_type(st, c);
      mix(&t, sizeof t);
      switch (t) {
      case SQLITE_INTEGER: {
        sqlite3_int64 v = sqlite3_column_int64(st, c);
        mix(&v, sizeof v);
        break;
      }
      case SQLITE_FLOAT: {
        double v = sqlite3_column_double(st, c);
        mix(&v, sizeof v);
        break;
      }
      case SQLITE_TEXT: {
        const unsigned char *v = sqlite3_column_text(st, c);
        mix(v, (size_t)sqlite3_column_bytes(st, c));
        break;
      }
      case SQLITE_BLOB: {
        const void *v = sqlite3_column_blob(st, c);
        mix(v, (size_t)sqlite3_column_bytes(st, c));
        break;
      }
      }
    }
  }
  nRun++;
  if (rc == SQLITE_DONE) {
    if (!sqlite3_stmt_readonly(st)) {
      sqlite3_int64 ch = sqlite3_changes64(sqlite3_db_handle(st)), id = sqlite3_last_insert_rowid(sqlite3_db_handle(st));
      mix(&ch, sizeof ch);
      mix(&id, sizeof id);
    }
    sqlite3_reset(st);
    return 0;
  }
  if (refusedOK && (rc & 0xff) == SQLITE_CONSTRAINT) {
    nRefused++;
    sqlite3_reset(st);
    return 1;
  }
  die(sqlite3_errmsg(sqlite3_db_handle(st)), sqlite3_sql(st));
  return 0;
}

/* prepare compiles the first statement of *sql and moves *sql past it. It
   returns 0 at the end of the text. */
static sqlite3_stmt *prepare(sqlite3 *d, const char **sql) {
  for (;;) {
    sqlite3_stmt *st = 0;
    const char *tail = 0;
    if (!**sql) return 0;
    if (sqlite3_prepare_v2(d, *sql, -1, &st, &tail) != SQLITE_OK) die(sqlite3_errmsg(d), *sql);
    *sql = tail;
    if (st) return st;
  }
}

/* script runs every statement of sql once, each prepared, run with the
   values of turn i and finalized. */
static void script(sqlite3 *d, const char *sql, long long i) {
  sqlite3_stmt *st;
  while ((st = prepare(d, &sql))) {
    bindAll(st, i);
    run(st, 0);
    sqlite3_finalize(st);
  }
}

static sqlite3_stmt *one(const char *sql) {
  sqlite3_stmt *st = prepare(db, &sql);
  if (!st) die("no statement", sql);
  return st;
}

/* loop prepares the statements of sql once and runs them in turn n times.
   With per > 0 it runs them as group commit does: per turns in one
   transaction, each statement in a SAVEPOINT of its own, which a statement
   that a constraint refused rolls back. Without it each statement commits
   by itself, and a constraint may refuse it. */
static void loop(const char *sql, long long n, long long per) {
  sqlite3_stmt *st[64], *begin = 0, *commit = 0, *save = 0, *release = 0, *back = 0;
  int ns = 0;
  while (ns < 64 && (st[ns] = prepare(db, &sql))) ns++;
  if (ns == 64 && *sql) die("more than 64 statements in a block", 0);
  if (per > 0) {
    begin = one("BEGIN IMMEDIATE");
    commit = one("COMMIT");
    save = one("SAVEPOINT merge");
    release = one("RELEASE merge");
    back = one("ROLLBACK TO merge");
  }
  for (long long i = 1; i <= n; i++) {
    if (per > 0 && (i - 1) % per == 0) run(begin, 0);
    for (int k = 0; k < ns; k++) {
      bindAll(st[k], i);
      if (per > 0) run(save, 0);
      if (run(st[k], 1) && per > 0) run(back, 0);
      if (per > 0) run(release, 0);
    }
    if (per > 0 && (i % per == 0 || i == n)) run(commit, 0);
  }
  for (int k = 0; k < ns; k++) sqlite3_finalize(st[k]);
  sqlite3_finalize(begin);
  sqlite3_finalize(commit);
  sqlite3_finalize(save);
  sqlite3_finalize(release);
  sqlite3_finalize(back);
}

static sqlite3 *openDB(void) {
  sqlite3 *d;
  if (sqlite3_open_v2(dbPath, &d, SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_URI, 0) != SQLITE_OK)
    die("open", dbPath);
  sqlite3_busy_timeout(d, 0);
  return d;
}

static void closeAll(void) {
  if (other && sqlite3_close(other) != SQLITE_OK) die("close of the second connection", 0);
  if (db && sqlite3_close(db) != SQLITE_OK) die("close", 0);
  db = other = 0;
}

static void report(void) {
  if (!*section) return;
  closeAll();
  printf("%s\t%lld\t%lld\t%lld\t%016llx\n", section, nRun, nRows, nRefused, (unsigned long long)sum);
  fflush(stdout);
}

int main(int argc, char **argv) {
  if (argc != 3) {
    fprintf(stderr, "usage: train FILE DIR\n");
    return 2;
  }
  dir = argv[2];
  FILE *f = fopen(argv[1], "rb");
  if (!f) {
    perror(argv[1]);
    return 1;
  }
  fseek(f, 0, SEEK_END);
  long n = ftell(f);
  rewind(f);
  char *src = malloc(n + 2);
  if (!src || fread(src + 1, 1, n, f) != (size_t)n) {
    perror(argv[1]);
    return 1;
  }
  src[0] = '\n';
  src[n + 1] = 0;
  fclose(f);
  /* A block is a line that starts with @@ and the SQL up to the next one. */
  char *p = strstr(src, "\n@@");
  while (p) {
    char *head = p + 3;
    char *next = strstr(head, "\n@@");
    if (next) *next = 0;
    char *sql = strchr(head, '\n');
    if (sql) *sql++ = 0;
    else sql = head + strlen(head);
    char word[64] = "", arg[64] = "";
    long long a = 0, b = 0;
    if (head[0] == ' ') {
      /* @@ NAME [memory]: a new section on a new database. */
      report();
      if (sscanf(head, " %63s %63s", word, arg) < 1) die("a section without a name", head);
      section = strdup(word);
      nRun = nRows = nRefused = 0;
      sum = 0xcbf29ce484222325ull;
      if (!strcmp(arg, "memory")) {
        snprintf(dbPath, sizeof dbPath, ":memory:");
      } else {
        char path[600];
        if (*arg) die("after the name of a section comes memory or nothing", head);
        snprintf(dbPath, sizeof dbPath, "%s/%s.db", dir, section);
        static const char *ext[] = {"", "-wal", "-shm", "-journal"};
        for (int i = 0; i < 4; i++) {
          snprintf(path, sizeof path, "%s%s", dbPath, ext[i]);
          remove(path);
        }
      }
      db = openDB();
      script(db, sql, 0);
    } else if (!db) {
      die("a block before the first section", head);
    } else if (sscanf(head, "%63s %lld %lld", word, &a, &b) >= 1 && !strcmp(word, "run")) {
      /* @@run: each statement once. */
      script(db, sql, 0);
    } else if (!strcmp(word, "bind") && a > 0) {
      /* @@bind N: prepared once, run N times, each statement a commit. */
      loop(sql, a, 0);
    } else if (!strcmp(word, "merge") && a > 0 && b > 0) {
      /* @@merge N K: prepared once, run N times, K turns a transaction. */
      loop(sql, a, b);
    } else if (!strcmp(word, "prepare") && a > 0) {
      /* @@prepare N: every statement prepared again each of N times. */
      for (long long i = 1; i <= a; i++) script(db, sql, i);
    } else if (!strcmp(word, "open") && a > 0) {
      /* @@open N: N times a new connection that runs the statements. */
      for (long long i = 1; i <= a; i++) {
        sqlite3 *d = openDB();
        script(d, sql, i);
        if (sqlite3_close(d) != SQLITE_OK) die("close", 0);
      }
    } else if (!strcmp(word, "other")) {
      /* @@other: on a second connection to the database of the section. */
      if (!other) other = openDB();
      script(other, sql, 0);
    } else {
      die("a block that is not run, bind N, merge N K, prepare N, open N or other", head);
    }
    p = next;
    if (p) *p = '\n';
  }
  report();
  return 0;
}
