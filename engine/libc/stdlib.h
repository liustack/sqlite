/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
#ifndef LIBC_STDLIB_H
#define LIBC_STDLIB_H

#include <stddef.h>

void abort(void) __attribute__((noreturn));

/* The memory of SQLite comes from the Go host. */
void *malloc(size_t n);
void *realloc(void *p, size_t n);
void free(void *p);

#endif
