/*
 * Copyright (c) 2026 The liustack/sqlite Authors.
 * SPDX-License-Identifier: MIT
 * Use of this source code is governed by the MIT license in LICENSE at the
 * root of this repository.
 */
/* The Go callbacks of the host (internal/csqlite). Every name starts with
   host_, which is how the generator knows them: a call to one becomes a call
   to the Go function of the same name, with the shadow-stack pointer first.
   Arguments and results are words: an int arrives with its upper bits unset
   and Go cuts it to 32 bits. */
#ifndef HOST_H
#define HOST_H

/* Mutexes (sqlite3_mutex_methods). A mutex is a handle of the Go table. */
long host_mutex_alloc(int type);
void host_mutex_free(long m);
void host_mutex_enter(long m);
int host_mutex_try(long m);
void host_mutex_leave(long m);

/* The VFS. A file is a handle of the Go table. Each returns a SQLite result
   code unless it says otherwise. */
int host_open(const char *name, int flags, int *out_flags, long *handle);
int host_delete(const char *name, int sync_dir);
int host_access(const char *name, int flags, int *res);
int host_full_pathname(const char *name, int n, char *out);
void host_randomness(char *out, int n);
void host_sleep(int us);
long long host_now_ms(void); /* Unix time in milliseconds */
/* The local time of Unix time t, by time.Local: sec, min, hour, day of the
   month, month (0 to 11), years since 1900, day of the week, day of the year
   and summer time (0 or 1) into out[0..8]. */
void host_localtime(long long t, int *out);

/* Functions written in Go. A function is a handle of the Go table, the user
   data of its sqlite3_create_function_v2. host_func runs it: it reads the
   arguments and sets the result through the C API, from the shadow pointer
   it was called with. */
void host_func(sqlite3_context *ctx, int argc, sqlite3_value **argv);
void host_func_destroy(long h);

int host_close(long f);
int host_read(long f, void *buf, int n, long long off);
int host_write(long f, const void *buf, int n, long long off);
int host_truncate(long f, long long size);
int host_sync(long f, int flags);
int host_file_size(long f, long long *size);
int host_lock(long f, int level);
int host_unlock(long f, int level);
int host_check_reserved_lock(long f, int *res);
int host_lock_state(long f); /* the level */
int host_size_hint(long f, long long size);
void host_chunk_size(long f, int n);
int host_has_moved(long f); /* 0 or 1 */
int host_shm_map(long f, int region, int size, int extend, void volatile **p);
int host_shm_lock(long f, int offset, int n, int flags);
void host_shm_barrier(void);
int host_shm_unmap(long f, int delete_flag);

#endif
