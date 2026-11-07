/* Copyright (c) 2026 The liustack/sqlite Authors.
   SPDX-License-Identifier: MIT
   Use of this source code is governed by the MIT license in LICENSE at the
   root of this repository. */

/* engine FILE REPS: runs each block of engine.sql REPS times on SQLite in C
   and prints its name, the median time in ns and the value of its check,
   separated by tabs. ../main.go does the same through the driver. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include "sqlite3.h"

static double now(void) {
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1e9 + t.tv_nsec;
}

static int cmp(const void *a, const void *b) {
	double x = *(const double *)a, y = *(const double *)b;
	return x < y ? -1 : x > y;
}

static void must(sqlite3 *db, const char *name, const char *sql) {
	char *err = 0;
	if (sqlite3_exec(db, sql, 0, 0, &err) != SQLITE_OK) {
		fprintf(stderr, "%s: %s\n", name, err);
		exit(1);
	}
}

/* cut ends s at the line sep and returns what follows it. */
static char *cut(char *s, const char *sep, const char *name) {
	char *p = strstr(s, sep);
	if (!p) {
		fprintf(stderr, "%s: no %s\n", name, sep);
		exit(1);
	}
	*p = 0;
	return p + strlen(sep);
}

int main(int argc, char **argv) {
	if (argc != 3) {
		fprintf(stderr, "usage: engine FILE REPS\n");
		return 2;
	}
	int reps = atoi(argv[2]);
	if (reps < 1 || reps > 64) {
		fprintf(stderr, "REPS: 1 to 64\n");
		return 2;
	}
	FILE *f = fopen(argv[1], "rb");
	if (!f) {
		perror(argv[1]);
		return 1;
	}
	fseek(f, 0, SEEK_END);
	long n = ftell(f);
	rewind(f);
	char *src = malloc(n + 1);
	if (fread(src, 1, n, f) != (size_t)n) {
		perror(argv[1]);
		return 1;
	}
	src[n] = 0;
	fclose(f);
	char *p = strstr(src, "\n@@ ");
	while (p) {
		char *name = p + 4;
		char *next = strstr(name, "\n@@ ");
		if (next) *next = 0;
		char *setup = cut(name, "\n", name);
		char *run = cut(setup, "@@run\n", name);
		char *check = cut(run, "@@check\n", name);
		sqlite3 *db;
		if (sqlite3_open(":memory:", &db) != SQLITE_OK) return 1;
		must(db, name, setup);
		double t[64];
		for (int i = 0; i < reps; i++) {
			double s = now();
			must(db, name, run);
			t[i] = now() - s;
		}
		qsort(t, reps, sizeof t[0], cmp);
		sqlite3_stmt *st;
		if (sqlite3_prepare_v2(db, check, -1, &st, 0) != SQLITE_OK || sqlite3_step(st) != SQLITE_ROW) {
			fprintf(stderr, "%s: check: %s\n", name, sqlite3_errmsg(db));
			return 1;
		}
		printf("%s\t%.0f\t%s\n", name, t[reps / 2], sqlite3_column_text(st, 0));
		sqlite3_finalize(st);
		sqlite3_close(db);
		p = next;
	}
	return 0;
}
