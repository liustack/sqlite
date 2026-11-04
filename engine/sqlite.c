/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
/* The one file of SQLite the generator compiles: the compile options, the
   amalgamation as released (sqlite3.c is never edited, engine/vendor.sh
   --verify), then the VFS and mutexes that call the Go host (os.c). Each
   option is here for a reason written next to it. */

/* No operating system: the Go host gives the VFS (sqlite3_os_init). */
#define SQLITE_OS_OTHER 1
/* Connections run on many goroutines at once, each used by one at a time
   (database/sql never shares one), so the multi-thread mode. The mutexes come
   from the Go host (os.c installs them). */
#define SQLITE_THREADSAFE 2
/* Nothing to load a shared library with. */
#define SQLITE_OMIT_LOAD_EXTENSION 1
/* The options SQLite recommends for a new build
   (https://sqlite.org/compile.html#recommended_compile_time_options). */
#define SQLITE_DQS 0
#define SQLITE_DEFAULT_MEMSTATUS 0
#define SQLITE_OMIT_DEPRECATED 1
#define SQLITE_OMIT_SHARED_CACHE 1
/* The localtime and utc modifiers: localtime_r (os.c) asks the Go host,
   which uses time.Local. */
#define HAVE_LOCALTIME_R 1
/* What programs most often need and cannot add from Go: full-text search,
   the spatial index, the SQL math functions (sin, log, pow and the others,
   from musl in engine/libc/math, so every target gives the same bits) and
   median and percentile. */
#define SQLITE_ENABLE_FTS5 1
#define SQLITE_ENABLE_RTREE 1
#define SQLITE_ENABLE_MATH_FUNCTIONS 1
#define SQLITE_ENABLE_PERCENTILE 1

#include "sqlite3.c"
#include "os.c"
