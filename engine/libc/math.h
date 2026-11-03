/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
#ifndef LIBC_MATH_H
#define LIBC_MATH_H

/* SQLite is built without its math functions (SQLITE_ENABLE_MATH_FUNCTIONS):
   it only needs these two, which the compiler makes inline. */
#define INFINITY __builtin_inff()
#define fabs(x) __builtin_fabs(x)

#endif
