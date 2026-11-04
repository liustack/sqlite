// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build windows && amd64

package vfs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// failedExclusiveFromShared is the lock left when SHARED -> EXCLUSIVE
// fails: os_win records PENDING, without the PENDING byte.
const failedExclusiveFromShared = LockPending

func TestMain(m *testing.M) {
	if name := os.Getenv(helperEnv); name != "" {
		os.Exit(helper(name))
	}
	os.Exit(m.Run())
}

func TestOpenCreate(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db")
	f, out, rc := Open(name, mainDB)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	if out != mainDB {
		t.Errorf("outFlags = %#x, want %#x", out, mainDB)
	}
	if rc := f.WriteAt([]byte("hello"), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	if size, rc := f.Size(); rc != OK || size != 5 {
		t.Errorf("Size = %d, %d", size, rc)
	}
	if rc := f.Sync(SyncFull); rc != OK {
		t.Errorf("Sync = %d", rc)
	}
}

func TestOpenReadOnlyFallback(t *testing.T) {
	name := filepath.Join(t.TempDir(), "ro.db")
	if err := os.WriteFile(name, []byte("data"), 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(name, 0644) })
	f, out, rc := Open(name, mainDB)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	if out&OpenReadOnly == 0 || out&(OpenReadWrite|OpenCreate) != 0 || out&OpenMainDB == 0 {
		t.Errorf("outFlags = %#x, want read-only main database", out)
	}
	buf := make([]byte, 4)
	if rc := f.ReadAt(buf, 0); rc != OK || string(buf) != "data" {
		t.Errorf("ReadAt = %d, %q", rc, buf)
	}
	if rc := f.WriteAt([]byte("x"), 0); rc != IOErrWrite {
		t.Errorf("WriteAt on read-only = %d, want IOErrWrite", rc)
	}
	lockT(t, "SHARED", f.Lock(LockShared), OK)
	lockT(t, "RESERVED", f.Lock(LockReserved), IOErrLock)

	// An exclusive create of a file that is there does not fall back.
	if _, _, rc := Open(name, mainDB|OpenExclusive); rc != CantOpen {
		t.Errorf("exclusive Open of a read-only file = %d, want CantOpen", rc)
	}
}

func TestTempFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMP", dir)
	f := openT(t, "", tempJrnl)
	data := []byte("temporary")
	if rc := f.WriteAt(data, 100); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	buf := make([]byte, len(data))
	if rc := f.ReadAt(buf, 100); rc != OK || !bytes.Equal(buf, data) {
		t.Fatalf("ReadAt = %d, %q", rc, buf)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), tempFilePrefix) {
		t.Errorf("temp dir holds %v, want one %s file", ents, tempFilePrefix)
	}
	if rc := f.Close(); rc != OK {
		t.Errorf("Close = %d", rc)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("temp dir holds %v after Close", ents)
	}
}

func TestOpenFileStaysPut(t *testing.T) {
	// As os_win, without FILE_SHARE_DELETE: a database in use cannot be
	// deleted or renamed, so it never moves.
	name := filepath.Join(t.TempDir(), "put.db")
	f := openT(t, name, mainDB)
	if err := os.Remove(name); err == nil {
		t.Error("an open database was deleted")
	}
	if err := os.Rename(name, name+"2"); err == nil {
		t.Error("an open database was renamed")
	}
	if f.HasMoved() {
		t.Error("HasMoved = true")
	}
}

func TestDeleteWaits(t *testing.T) {
	// Another program has the file open for a moment, as a virus scanner
	// does. Delete tries again until it is gone.
	name := filepath.Join(t.TempDir(), "busy")
	os.WriteFile(name, []byte("x"), 0644)
	p, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(60*time.Millisecond, func() { windows.CloseHandle(h) })
	if rc := Delete(name, false); rc != OK {
		t.Fatalf("Delete = %d", rc)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
}

func TestAccess(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	full := filepath.Join(dir, "full")
	ro := filepath.Join(dir, "ro")
	os.WriteFile(empty, nil, 0644)
	os.WriteFile(full, []byte("x"), 0644)
	os.WriteFile(ro, []byte("x"), 0444)
	t.Cleanup(func() { os.Chmod(ro, 0644) })

	for _, c := range []struct {
		name  string
		flags int
		want  bool
	}{
		{filepath.Join(dir, "missing"), AccessExists, false},
		{empty, AccessExists, false}, // an empty file counts as missing, as os_win
		{full, AccessExists, true},
		{dir, AccessExists, false}, // size 0, as os_win
		{full, AccessReadWrite, true},
		{ro, AccessReadWrite, false},
		{ro, AccessRead, true},
		{filepath.Join(dir, "missing"), AccessReadWrite, false},
		{filepath.Join(dir, "no", "such"), AccessRead, false},
	} {
		got, rc := Access(c.name, c.flags)
		if rc != OK || got != c.want {
			t.Errorf("Access(%s, %d) = %v, %d, want %v", c.name, c.flags, got, rc, c.want)
		}
	}
}

func TestFullPathname(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, c := range []struct{ in, want string }{
		{"test.db", dir + `\test.db`},
		{"./a/./b/../test.db", dir + `\a\test.db`},
		{`C:\x\y\..\z.db`, `C:\x\z.db`},
		{"/C:/x/y.db", `C:\x\y.db`}, // from a URI, as os_win
	} {
		got, rc := FullPathname(c.in)
		if rc != OK || !strings.EqualFold(got, c.want) {
			t.Errorf("FullPathname(%q) = %q, %d, want %q", c.in, got, rc, c.want)
		}
	}
	if _, rc := FullPathname(`C:\` + strings.Repeat("x", MaxPathname)); rc != CantOpen {
		t.Errorf("FullPathname of a long name = %d, want CantOpen", rc)
	}
}

// held tells how another handle sees n bytes from off of the file at name:
// whether it could lock them shared, and exclusive. It stands for SQLite
// built from C, in this process or another.
func held(t *testing.T, name string, off int64, n uint32) (canShare, canExcl bool) {
	t.Helper()
	p, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	try := func(flags uint32) bool {
		ov := windows.Overlapped{Offset: uint32(off), OffsetHigh: uint32(off >> 32)}
		if windows.LockFileEx(h, flags|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, n, 0, &ov) != nil {
			return false
		}
		ov = windows.Overlapped{Offset: uint32(off), OffsetHigh: uint32(off >> 32)}
		windows.UnlockFileEx(h, 0, n, 0, &ov)
		return true
	}
	return try(0), try(windows.LOCKFILE_EXCLUSIVE_LOCK)
}

func TestLockBytes(t *testing.T) {
	// The bytes and kinds of lock of os_win, seen from another handle.
	a, _, name := twoFiles(t)
	check := func(what string, off int64, n uint32, share, excl bool) {
		t.Helper()
		if s, e := held(t, name, off, n); s != share || e != excl {
			t.Errorf("%s: can lock shared %v, exclusive %v, want %v, %v", what, s, e, share, excl)
		}
	}
	check("nothing held", pendingByte, sharedFirst+sharedSize-pendingByte, true, true)

	lockT(t, "SHARED", a.Lock(LockShared), OK)
	check("SHARED: shared range", sharedFirst, sharedSize, true, false)
	check("SHARED: PENDING byte", pendingByte, 1, true, true)

	lockT(t, "RESERVED", a.Lock(LockReserved), OK)
	check("RESERVED: RESERVED byte", reservedByte, 1, false, false)
	check("RESERVED: shared range", sharedFirst, sharedSize, true, false)

	lockT(t, "EXCLUSIVE", a.Lock(LockExclusive), OK)
	check("EXCLUSIVE: PENDING byte", pendingByte, 1, false, false)
	check("EXCLUSIVE: shared range", sharedFirst, sharedSize, false, false)

	lockT(t, "unlock to SHARED", a.Unlock(LockShared), OK)
	check("back to SHARED: PENDING and RESERVED", pendingByte, 2, true, true)
	check("back to SHARED: shared range", sharedFirst, sharedSize, true, false)

	lockT(t, "unlock", a.Unlock(LockNone), OK)
	check("unlocked", pendingByte, sharedFirst+sharedSize-pendingByte, true, true)
}

func TestShmLockBytes(t *testing.T) {
	// The DMS byte and the slots of os_win on the -shm, seen from another
	// handle.
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	shm := name + "-shm"
	if s, e := held(t, shm, shmDMS, 1); !s || e {
		t.Errorf("DMS byte: can lock shared %v, exclusive %v, want true, false", s, e)
	}
	lockT(t, "a shared 3", a.ShmLock(3, 1, ShmLock|ShmShared), OK)
	if s, e := held(t, shm, shmBase+3, 1); !s || e {
		t.Errorf("slot 3 read: can lock shared %v, exclusive %v", s, e)
	}
	lockT(t, "b excl 0", b.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	if s, e := held(t, shm, shmBase, 1); s || e {
		t.Errorf("slot 0 written: can lock shared %v, exclusive %v", s, e)
	}
	// Unmapping releases everything, also a range taken at once.
	lockT(t, "a excl 4..7", a.ShmLock(4, 4, ShmLock|ShmExclusive), OK)
	a.ShmUnmap(false)
	b.ShmUnmap(false)
	if s, e := held(t, shm, shmBase, ShmNLock+1); !s || !e {
		t.Errorf("after unmap: can lock shared %v, exclusive %v", s, e)
	}
}

func TestTruncateWhileMapped(t *testing.T) {
	// SQLite built from C with mmap_size > 0 keeps the database mapped.
	// Shrinking it then fails with ERROR_USER_MAPPED_FILE, which os_win
	// takes as done: SQLite reads a file longer than its pages fine.
	name := filepath.Join(t.TempDir(), "mapped.db")
	f := openT(t, name, mainDB)
	if rc := f.WriteAt(make([]byte, 65536), 0); rc != OK {
		t.Fatal(rc)
	}
	p, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	m, err := windows.CreateFileMapping(h, nil, windows.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(m)
	v, err := windows.MapViewOfFile(m, windows.FILE_MAP_READ, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.UnmapViewOfFile(v)
	if rc := f.Truncate(4096); rc != OK {
		t.Errorf("Truncate under another's mapping = %d, want OK", rc)
	}
}

func TestShmSharedHandle(t *testing.T) {
	// On a network drive, as os_win, the Files of the process take their
	// slot locks on the node's one handle, and the node sorts them out.
	sharedShmForTest = true
	t.Cleanup(func() { sharedShmForTest = false })
	a, b, name := twoFiles(t)
	shmMapT(t, a, 0, true)
	shmMapT(t, b, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false); b.ShmUnmap(false) })
	if !a.shm.node.shared {
		t.Fatal("node does not use the shared handle")
	}
	shm := name + "-shm"
	const (
		lockShared = ShmLock | ShmShared
		lockExcl   = ShmLock | ShmExclusive
		unShared   = ShmUnlock | ShmShared
		unExcl     = ShmUnlock | ShmExclusive
	)
	lockT(t, "a shared 3", a.ShmLock(3, 1, lockShared), OK)
	lockT(t, "b shared 3", b.ShmLock(3, 1, lockShared), OK)
	lockT(t, "a unlock 3", a.ShmLock(3, 1, unShared), OK)
	// b still reads: the process keeps its lock.
	if s, e := held(t, shm, shmBase+3, 1); !s || e {
		t.Errorf("slot 3 with b reading: can lock shared %v, exclusive %v", s, e)
	}
	lockT(t, "a excl 3", a.ShmLock(3, 1, lockExcl), Busy)
	lockT(t, "b unlock 3", b.ShmLock(3, 1, unShared), OK)
	if s, e := held(t, shm, shmBase+3, 1); !s || !e {
		t.Errorf("slot 3 with no reader: can lock shared %v, exclusive %v", s, e)
	}
	lockT(t, "a excl 0", a.ShmLock(0, 1, lockExcl), OK)
	lockT(t, "b excl 0", b.ShmLock(0, 1, lockExcl), Busy)
	lockT(t, "b shared 0", b.ShmLock(0, 1, lockShared), Busy)
	lockT(t, "a unlock 0", a.ShmLock(0, 1, unExcl), OK)
	lockT(t, "b shared 0", b.ShmLock(0, 1, lockShared), OK)

	// Many readers come and go at once: the process's lock on the slot is
	// there exactly while one of them reads, and taken once.
	var wg sync.WaitGroup
	for range 4 {
		f := openT(t, name, mainDB)
		shmMapT(t, f, 0, false)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer f.ShmUnmap(false)
			for range 2000 {
				if rc := f.ShmLock(4, 1, lockShared); rc != OK {
					t.Errorf("shared 4 = %d", rc)
					return
				}
				if rc := f.ShmLock(4, 1, unShared); rc != OK {
					t.Errorf("unlock 4 = %d", rc)
					return
				}
			}
		}()
	}
	wg.Wait()
	if s, e := held(t, shm, shmBase+4, 1); !s || !e {
		t.Errorf("slot 4 after the readers left: can lock shared %v, exclusive %v", s, e)
	}
}
