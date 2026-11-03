// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"crypto/rand"
	"time"

	"github.com/liustack/sqlite/internal/vfs"
)

// The VFS callbacks of engine/os.c. Each turns the words of the C into Go
// values and calls internal/vfs. An int of the C arrives in a word whose
// upper bits are not set: int32 cuts it.

var files table[vfs.File]

func i32(w uintptr) int { return int(int32(w)) }

func rc(r int) uintptr { return uintptr(int32(r)) }

func putInt(p uintptr, v int) { *(*int32)(at(p)) = int32(v) }

func host_open(sp, name, flags, outFlags, handle uintptr) uintptr {
	f, out, r := vfs.Open(cstring(name), i32(flags))
	if r != sqliteOK {
		return rc(r)
	}
	putInt(outFlags, out)
	*(*int64)(at(handle)) = int64(files.add(f))
	return sqliteOK
}

func host_delete(sp, name, syncDir uintptr) uintptr {
	return rc(vfs.Delete(cstring(name), i32(syncDir) != 0))
}

func host_access(sp, name, flags, res uintptr) uintptr {
	ok, r := vfs.Access(cstring(name), i32(flags))
	v := 0
	if ok {
		v = 1
	}
	putInt(res, v)
	return rc(r)
}

func host_full_pathname(sp, name, n, out uintptr) uintptr {
	p, r := vfs.FullPathname(cstring(name))
	if r != vfs.OK && r != vfs.OKSymlink {
		return rc(r)
	}
	if len(p)+1 > i32(n) {
		return sqliteCantOpen
	}
	b := bytesAt(out, len(p)+1)
	copy(b, p)
	b[len(p)] = 0
	return rc(r)
}

func host_randomness(sp, out, n uintptr) {
	rand.Read(bytesAt(out, i32(n)))
}

func host_sleep(sp, us uintptr) {
	time.Sleep(time.Duration(i32(us)) * time.Microsecond)
}

func host_now_ms(sp uintptr) uintptr {
	return uintptr(time.Now().UnixMilli())
}

func host_close(sp, f uintptr) uintptr {
	r := files.get(f).Close()
	files.remove(f)
	return rc(r)
}

func host_read(sp, f, buf, n, off uintptr) uintptr {
	return rc(files.get(f).ReadAt(bytesAt(buf, i32(n)), int64(off)))
}

func host_write(sp, f, buf, n, off uintptr) uintptr {
	return rc(files.get(f).WriteAt(bytesAt(buf, i32(n)), int64(off)))
}

func host_truncate(sp, f, size uintptr) uintptr {
	return rc(files.get(f).Truncate(int64(size)))
}

func host_sync(sp, f, flags uintptr) uintptr {
	return rc(files.get(f).Sync(i32(flags)))
}

func host_file_size(sp, f, size uintptr) uintptr {
	s, r := files.get(f).Size()
	*(*int64)(at(size)) = s
	return rc(r)
}

func host_lock(sp, f, level uintptr) uintptr {
	return rc(files.get(f).Lock(i32(level)))
}

func host_unlock(sp, f, level uintptr) uintptr {
	return rc(files.get(f).Unlock(i32(level)))
}

func host_check_reserved_lock(sp, f, res uintptr) uintptr {
	ok, r := files.get(f).CheckReservedLock()
	v := 0
	if ok {
		v = 1
	}
	putInt(res, v)
	return rc(r)
}

func host_lock_state(sp, f uintptr) uintptr {
	return rc(files.get(f).LockState())
}

func host_size_hint(sp, f, size uintptr) uintptr {
	return rc(files.get(f).SizeHint(int64(size)))
}

func host_chunk_size(sp, f, n uintptr) {
	files.get(f).SetChunkSize(i32(n))
}

func host_has_moved(sp, f uintptr) uintptr {
	if files.get(f).HasMoved() {
		return 1
	}
	return 0
}

func host_shm_map(sp, f, region, size, extend, p uintptr) uintptr {
	a, r := files.get(f).ShmMap(i32(region), i32(size), i32(extend) != 0)
	*(*uintptr)(at(p)) = a
	return rc(r)
}

func host_shm_lock(sp, f, offset, n, flags uintptr) uintptr {
	return rc(files.get(f).ShmLock(i32(offset), i32(n), i32(flags)))
}

func host_shm_barrier(sp uintptr) { vfs.ShmBarrier() }

func host_shm_unmap(sp, f, del uintptr) uintptr {
	return rc(files.get(f).ShmUnmap(i32(del) != 0))
}

const sqliteCantOpen = 14
