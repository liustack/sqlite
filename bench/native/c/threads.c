/* Copyright (c) 2026 The liustack/sqlite Authors.
   SPDX-License-Identifier: MIT
   Use of this source code is governed by the MIT license in LICENSE at the
   root of this repository. */

/* threads MODE N SECS SYNC CACHE PATH: N threads on one database file in
   WAL for SECS seconds, each with a connection of its own, on SQLite in C.
   MODE is read (N readers), write (N writers) or mixed (N of each). A read
   is one row of 100k by a random key, a write inserts one row. Writes queue
   on a mutex of the process, which is faster than SQLite's busy handler.
   SYNC is the synchronous setting, CACHE the cache_size (0: SQLite's).
   Prints reads/s, writes/s and errors as ../main.go does. */
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include "sqlite3.h"

static volatile int stop;
static int ready, started; /* threads ready, and the start of the timed part */
static pthread_mutex_t wmu = PTHREAD_MUTEX_INITIALIZER;
static const char *path, *syncmode, *cache;
static char payload[101];

static void must(sqlite3 *db, const char *sql) {
	char *err = 0;
	if (sqlite3_exec(db, sql, 0, 0, &err) != SQLITE_OK) {
		fprintf(stderr, "%s: %s\n", sql, err);
		exit(1);
	}
}

static sqlite3 *openc(void) {
	sqlite3 *db;
	if (sqlite3_open_v2(path, &db, SQLITE_OPEN_READWRITE | SQLITE_OPEN_NOMUTEX, 0) != SQLITE_OK) {
		fprintf(stderr, "%s: %s\n", path, sqlite3_errmsg(db));
		exit(1);
	}
	sqlite3_busy_timeout(db, 5000);
	char q[64];
	snprintf(q, sizeof q, "PRAGMA synchronous=%s", syncmode);
	must(db, q);
	if (strcmp(cache, "0") != 0) {
		snprintf(q, sizeof q, "PRAGMA cache_size=%s", cache);
		must(db, q);
	}
	return db;
}

/* wait_start counts the thread as ready, its connection open and its
   statement prepared, and waits for the timed part to start. */
static void wait_start(void) {
	__atomic_add_fetch(&ready, 1, __ATOMIC_SEQ_CST);
	while (!__atomic_load_n(&started, __ATOMIC_ACQUIRE)) usleep(100);
}

typedef struct {
	long ops, errs;
	unsigned seed;
} arg;

static void *writer(void *p) {
	arg *a = p;
	sqlite3 *db = openc();
	sqlite3_stmt *st;
	if (sqlite3_prepare_v2(db, "INSERT INTO w(v) VALUES(?1)", -1, &st, 0) != SQLITE_OK) exit(1);
	wait_start();
	while (!stop) {
		pthread_mutex_lock(&wmu);
		sqlite3_bind_text(st, 1, payload, 100, SQLITE_STATIC);
		int rc = sqlite3_step(st);
		sqlite3_reset(st);
		pthread_mutex_unlock(&wmu);
		if (rc == SQLITE_DONE) a->ops++;
		else a->errs++;
	}
	sqlite3_finalize(st);
	sqlite3_close(db);
	return 0;
}

static void *reader(void *p) {
	arg *a = p;
	sqlite3 *db = openc();
	sqlite3_stmt *st;
	if (sqlite3_prepare_v2(db, "SELECT v FROM t WHERE id=?1", -1, &st, 0) != SQLITE_OK) exit(1);
	wait_start();
	while (!stop) {
		sqlite3_bind_int(st, 1, rand_r(&a->seed) % 100000 + 1);
		if (sqlite3_step(st) == SQLITE_ROW && sqlite3_column_bytes(st, 0) == 100) a->ops++;
		else a->errs++;
		sqlite3_reset(st);
	}
	sqlite3_finalize(st);
	sqlite3_close(db);
	return 0;
}

int main(int argc, char **argv) {
	if (argc != 7) {
		fprintf(stderr, "usage: threads MODE N SECS SYNC CACHE PATH\n");
		return 2;
	}
	const char *mode = argv[1];
	int n = atoi(argv[2]), secs = atoi(argv[3]);
	syncmode = argv[4];
	cache = argv[5];
	path = argv[6];
	int nr = strcmp(mode, "write") ? n : 0, nw = strcmp(mode, "read") ? n : 0;
	if (n < 1 || n > 128 || secs < 1 || (strcmp(mode, "read") && strcmp(mode, "write") && strcmp(mode, "mixed"))) {
		fprintf(stderr, "bad arguments\n");
		return 2;
	}
	memset(payload, 'x', 100);
	char f[1024];
	unlink(path);
	snprintf(f, sizeof f, "%s-wal", path);
	unlink(f);
	snprintf(f, sizeof f, "%s-shm", path);
	unlink(f);
	sqlite3 *db;
	if (sqlite3_open(path, &db) != SQLITE_OK) return 1;
	must(db, "PRAGMA journal_mode=WAL; CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT); CREATE TABLE w(id INTEGER PRIMARY KEY, v TEXT);"
	         "WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 100000) INSERT INTO t SELECT i, printf('%.100c', 'v') FROM r;");
	sqlite3_close(db);
	pthread_t th[256];
	arg args[256] = {0};
	for (int i = 0; i < nr; i++) {
		args[i].seed = i + 1;
		pthread_create(&th[i], 0, reader, &args[i]);
	}
	for (int i = 0; i < nw; i++) pthread_create(&th[nr + i], 0, writer, &args[nr + i]);
	while (__atomic_load_n(&ready, __ATOMIC_SEQ_CST) < nr + nw) usleep(100);
	__atomic_store_n(&started, 1, __ATOMIC_RELEASE);
	sleep(secs);
	stop = 1;
	long r = 0, w = 0, e = 0;
	for (int i = 0; i < nr + nw; i++) {
		pthread_join(th[i], 0);
		e += args[i].errs;
		if (i < nr) r += args[i].ops;
		else w += args[i].ops;
	}
	printf("reads/s %.0f writes/s %.0f errors %ld\n", (double)r / secs, (double)w / secs, e);
	return 0;
}
