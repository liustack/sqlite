/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
#ifndef LIBC_TIME_H
#define LIBC_TIME_H

/* What SQLite's localtime and utc modifiers use (HAVE_LOCALTIME_R).
   localtime_r is in os.c and asks the Go host. */
typedef long long time_t;

struct tm {
  int tm_sec;
  int tm_min;
  int tm_hour;
  int tm_mday;
  int tm_mon;  /* 0 to 11 */
  int tm_year; /* years since 1900 */
  int tm_wday;
  int tm_yday;
  int tm_isdst;
};

struct tm *localtime_r(const time_t *t, struct tm *tm);

#endif
