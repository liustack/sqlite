// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func openWAL(t *testing.T, name string) (*File, int) {
	t.Helper()
	// A WAL takes the mode of its database, which must be there.
	db, err := os.OpenFile(strings.TrimSuffix(name, "-wal"), os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	f, _, rc := Open(name, OpenWAL|OpenReadWrite|OpenCreate)
	return f, rc
}

// A failed sync of a WAL cuts it at the first byte written since its last
// good sync, and stops every File of it. It opens again once all are
// closed.
func TestWALStops(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	a, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	b, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	if rc := a.WriteAt(bytes.Repeat([]byte{1}, 100), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	if rc := a.Sync(SyncNormal); rc != OK {
		t.Fatalf("Sync = %d", rc)
	}
	// Written after the good sync, the second through another File.
	a.WriteAt(bytes.Repeat([]byte{2}, 50), 150)
	b.WriteAt(bytes.Repeat([]byte{3}, 50), 120)

	SetFault(func(path string, op Op) int {
		if op == OpSync {
			return IOErrFsync
		}
		return OK
	})
	rc = a.Sync(SyncNormal)
	SetFault(nil)
	if rc != IOErrFsync {
		t.Fatalf("Sync = %d", rc)
	}
	if !WALsStopped() || !WALStopped(strings.TrimSuffix(name, "-wal")) {
		t.Fatal("the WAL is not stopped")
	}
	if size, _ := b.Size(); size != 120 {
		t.Fatalf("size %d, want the WAL cut at 120", size)
	}
	p := make([]byte, 10)
	for _, f := range []*File{a, b} {
		if rc := f.ReadAt(p, 0); rc != IOErrRead {
			t.Errorf("ReadAt = %d", rc)
		}
		if rc := f.WriteAt(p, 0); rc != IOErrWrite {
			t.Errorf("WriteAt = %d", rc)
		}
		if rc := f.Truncate(0); rc != IOErrTruncate {
			t.Errorf("Truncate = %d", rc)
		}
		if rc := f.Sync(SyncNormal); rc != IOErrFsync {
			t.Errorf("Sync = %d", rc)
		}
	}
	if _, rc := openWAL(t, name); rc != IOErr {
		t.Fatalf("Open of the stopped WAL = %d", rc)
	}
	a.Close()
	if _, rc := openWAL(t, name); rc != IOErr {
		t.Fatalf("Open while a File has the WAL open = %d", rc)
	}
	b.Close()
	if WALsStopped() {
		t.Fatal("the WAL is still stopped with no File open")
	}
	c, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open once all were closed = %d", rc)
	}
	defer c.Close()
	if WALsStopped() {
		t.Fatal("the WAL is still stopped once open again")
	}
	if rc := c.ReadAt(p, 0); rc != OK || p[0] != 1 {
		t.Fatalf("ReadAt = %d, %v", rc, p)
	}
}

// stopWAL opens the WAL name, writes to it and fails its sync, with the
// cut failing too when cutFails. It returns the File, still open.
func stopWAL(t *testing.T, name string, cutFails bool) *File {
	t.Helper()
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	f.WriteAt(make([]byte, 100), 0)
	SetFault(func(path string, op Op) int {
		switch {
		case op == OpSync:
			return IOErrFsync
		case op == OpTruncate && cutFails:
			return IOErrTruncate
		}
		return OK
	})
	defer SetFault(nil)
	if rc := f.Sync(SyncNormal); rc != IOErrFsync {
		t.Fatalf("Sync = %d", rc)
	}
	return f
}

// A write that comes while a sync runs waits for it, and is not taken as
// synced by it.
func TestWALWriteDuringSync(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	f.WriteAt(make([]byte, 100), 0)
	wrote := make(chan struct{})
	// The fault runs inside Sync, after the WAL took what it syncs.
	SetFault(func(path string, op Op) int {
		if op == OpSync {
			go func() {
				f.WriteAt(make([]byte, 50), 100)
				close(wrote)
			}()
			select {
			case <-wrote:
				t.Error("a write landed while a sync ran")
			case <-time.After(20 * time.Millisecond):
			}
		}
		return OK
	})
	rc = f.Sync(SyncNormal)
	SetFault(nil)
	if rc != OK {
		t.Fatalf("Sync = %d", rc)
	}
	<-wrote
	if d := f.wal.dirty.Load(); d != 100 {
		t.Fatalf("dirty from %d, want 100", d)
	}
}

// A WAL whose cut failed does not open again until the cut is made: each
// open tries it again.
func TestWALCutFailsThenWorks(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	f := stopWAL(t, name, true)
	failCut(t)
	f.Close()
	if _, rc := openWAL(t, name); rc != IOErr {
		t.Fatalf("Open = %d", rc)
	}
	SetFault(nil)
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open once the cut works = %d", rc)
	}
	defer f.Close()
	if n, rc := f.Size(); rc != OK || n != 0 {
		t.Fatalf("Size = %d, %d: the WAL was not cut", n, rc)
	}
}

// failCut fails every truncate until the test ends.
func failCut(t *testing.T) {
	SetFault(func(path string, op Op) int {
		if op == OpTruncate {
			return IOErrTruncate
		}
		return OK
	})
	t.Cleanup(func() { SetFault(nil) })
}

// A WAL whose cut fails does not open again, until it is deleted: a new
// WAL of that name opens.
func TestWALCutFails(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db-wal")
	f := stopWAL(t, name, true)
	failCut(t)
	f.Close()
	if _, rc := openWAL(t, name); rc != IOErr {
		t.Fatalf("Open = %d", rc)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	// The stopped WAL keeps its inode, so a new file cannot be given it
	// and be taken for the stopped one.
	var old *wal
	wals.Lock()
	for _, w := range wals.m {
		if w.name == name {
			old = w
		}
	}
	wals.Unlock()
	var st unix.Stat_t
	if old == nil || unix.Fstat(old.held, &st) != nil || (fileID{uint64(st.Dev), uint64(st.Ino)}) != old.id {
		t.Fatal("the stopped WAL does not hold its inode")
	}
	f, rc := openWAL(t, name)
	if rc != OK {
		t.Fatalf("Open of a new WAL = %d", rc)
	}
	f.Close()
	if WALsStopped() {
		t.Fatal("the deleted WAL is still stopped")
	}
}

// sharedWAL opens a database and maps its WAL index, as a connection in WAL
// mode has it, with idx written over the start of the -shm. It returns the
// database and the name of its WAL.
func sharedWAL(t *testing.T, idx []byte) (db *File, wal string) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "test.db")
	db = openT(t, name, mainDB)
	shmMapT(t, db, 0, true)
	writeAt(t, name+"-shm", idx, 0)
	return db, name + "-wal"
}

// writeAt writes b at off in the file name, which is there.
func writeAt(t *testing.T, name string, b []byte, off int64) {
	t.Helper()
	f, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatal(err)
	}
}

// sizeOf is the size of the file name.
func sizeOf(t *testing.T, name string) int64 {
	t.Helper()
	st, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// walHeader is the header of a WAL with pages of 512 bytes and the salts.
func walHeader(salts string) []byte {
	h := make([]byte, 32)
	binary.BigEndian.PutUint32(h, 0x377f0682)
	binary.BigEndian.PutUint32(h[8:], 512)
	copy(h[16:24], salts)
	return h
}

// walIndex is the start of a WAL index of the WAL with the header hdr, nil
// for a WAL of zeros: checkpoints copied nBackfill of its frames, and the
// last set out to copy attempted.
func walIndex(hdr []byte, nBackfill, attempted uint32) []byte {
	idx := make([]byte, 136)
	for _, h := range [][]byte{idx[:48], idx[48:96]} {
		h[12] = 1 // isInit
		if hdr != nil {
			copy(h[32:40], hdr[16:24])
		}
	}
	binary.NativeEndian.PutUint32(idx[96:], nBackfill)
	binary.NativeEndian.PutUint32(idx[128:], attempted)
	return idx
}

// A stopped WAL is not cut and does not open while another process has the
// -shm open: the WAL index there counts the frames. Once that process is
// gone, the WAL is cut and opens, and the -shm is emptied for the next
// process: by the last File of the database as it closes, or at the cut when
// the process has none.
func TestWALShmInUse(t *testing.T) {
	for _, mapped := range []bool{true, false} {
		t.Run(fmt.Sprint("mapped=", mapped), func(t *testing.T) {
			db, name := sharedWAL(t, walIndex(nil, 0, 0))
			shm := strings.TrimSuffix(name, "-wal") + "-shm"
			gone := otherProcess(t, strings.TrimSuffix(name, "-wal"))
			stopWAL(t, name, false).Close()
			if _, rc := openWAL(t, name); rc != IOErr {
				t.Fatalf("Open while the -shm is in use = %d", rc)
			}
			if n := sizeOf(t, name); n != 100 {
				t.Fatalf("the WAL is %d bytes while the -shm is in use, want 100", n)
			}
			if !mapped {
				db.ShmUnmap(false)
			}
			gone()
			f, rc := openWAL(t, name)
			if rc != OK {
				t.Fatalf("Open once the -shm is free = %d", rc)
			}
			defer f.Close()
			if n, rc := f.Size(); rc != OK || n != 0 {
				t.Fatalf("Size = %d, %d: the WAL was not cut", n, rc)
			}
			if mapped {
				// Until then no other process gets the -shm.
				fd, err := unix.Open(shm, unix.O_RDWR|unix.O_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd)
				var fl unix.Flock_t
				if err := setLock(fd, &fl, unix.F_RDLCK, shmDMS, 1); err == nil {
					t.Fatal("another process gets the DMS lock while the WAL index is mapped")
				}
				db.ShmUnmap(false)
			}
			if n := sizeOf(t, shm); n != 3 {
				t.Fatalf("the -shm is %d bytes, want 3", n)
			}
		})
	}
}

// When the -shm cannot be emptied as the node closes, the header of the WAL
// index is zeroed: a process that was about to take the DMS lock for reading
// then has SQLite build the index again, as from an empty -shm.
func TestWALShmWipeFails(t *testing.T) {
	db, name := sharedWAL(t, walIndex(nil, 0, 0))
	shm := strings.TrimSuffix(name, "-wal") + "-shm"
	stopWAL(t, name, false).Close()
	SetFault(func(path string, op Op) int {
		if op == OpTruncate && path == shm {
			return IOErrTruncate
		}
		return OK
	})
	defer SetFault(nil)
	db.ShmUnmap(false)
	b, err := os.ReadFile(shm)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 3 && !bytes.Equal(b[:136], make([]byte, 136)) {
		t.Fatalf("the -shm of %d bytes still has its WAL index: % x", len(b), b[:136])
	}
}

// A stopped WAL is not cut when anything was written while another process
// had the -shm open: the WAL, the database or the WAL index. That process
// went on over frames that may not be on disk, and no cut is sound.
func TestWALChangedByOther(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, db string)
	}{
		{"the WAL", func(t *testing.T, db string) { writeAt(t, db+"-wal", make([]byte, 50), 100) }},
		{"the database", func(t *testing.T, db string) { writeAt(t, db, []byte{1}, 0) }},
		{"a commit in the index", func(t *testing.T, db string) {
			writeAt(t, db+"-shm", []byte{7}, 16)
			writeAt(t, db+"-shm", []byte{7}, 48+16)
		}},
		{"frames copied", func(t *testing.T, db string) { writeAt(t, db+"-shm", []byte{1}, 96) }},
		{"frames to copy", func(t *testing.T, db string) { writeAt(t, db+"-shm", []byte{1}, 128) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, name := sharedWAL(t, walIndex(nil, 0, 0))
			gone := otherProcess(t, strings.TrimSuffix(name, "-wal"))
			stopWAL(t, name, false).Close()
			c.change(t, strings.TrimSuffix(name, "-wal"))
			want := sizeOf(t, name)
			gone()
			f, rc := openWAL(t, name)
			if rc != OK {
				t.Fatalf("Open = %d", rc)
			}
			defer f.Close()
			if n, rc := f.Size(); rc != OK || n != want {
				t.Fatalf("Size = %d, %d, want %d", n, rc, want)
			}
		})
	}
}

// The cut of a stopped WAL keeps the frames that a checkpoint may have
// copied into the database, as the WAL index tells, and it is made only
// when that can be told.
func TestWALCutKeepsCopied(t *testing.T) {
	const frame = 24 + 512
	hdr := walHeader("12345678")
	for _, c := range []struct {
		name  string
		index []byte
		// A connection of the process is at a checkpoint, and has marked
		// that it sets out to copy all three frames. With "lock" it took
		// the checkpoint lock, with "copy" it also copied a page, and with
		// "exclusive" it holds EXCLUSIVE on the database, as the last
		// connection that closes and one with locking_mode=EXCLUSIVE, whose
		// checkpoints take no lock on the WAL index. With "exclusive first"
		// it took EXCLUSIVE before it opened the index, as a connection
		// that leaves WAL mode without having read the WAL.
		own  string
		size int64 // of the WAL after the cut, -1 when it does not open
	}{
		{"none copied", walIndex(hdr, 0, 0), "", 32 + frame},
		{"two copied", walIndex(hdr, 2, 2), "", 32 + 2*frame},
		{"two copied in part", walIndex(hdr, 0, 2), "", 32 + 2*frame},
		{"all copied", walIndex(hdr, 3, 3), "", 32 + 3*frame},
		{"the index of another WAL", walIndex(walHeader("1235abcd"), 3, 3), "", 32 + frame},
		{"the index of no WAL yet", walIndex(nil, 0, 0), "", 32 + frame},
		{"its own checkpoint", walIndex(hdr, 0, 0), "lock", 32 + frame},
		{"its own checkpoint after two copied in part", walIndex(hdr, 0, 2), "lock", 32 + 2*frame},
		{"its own checkpoint that copied a page", walIndex(hdr, 0, 0), "copy", 32 + 3*frame},
		{"its own checkpoint with the database locked", walIndex(hdr, 0, 0), "exclusive", 32 + frame},
		{"the database locked after two copied in part", walIndex(hdr, 0, 2), "exclusive", 32 + 2*frame},
		{"the database locked before the index was open", walIndex(hdr, 0, 0), "exclusive first", 32 + frame},
		{"an index not built", make([]byte, 136), "", -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, name := sharedWAL(t, c.index)
			f, rc := openWAL(t, name)
			if rc != OK {
				t.Fatalf("Open = %d", rc)
			}
			// The header and one frame are synced, two more frames are not.
			f.WriteAt(hdr, 0)
			f.WriteAt(make([]byte, frame), 32)
			if rc := f.Sync(SyncNormal); rc != OK {
				t.Fatalf("Sync = %d", rc)
			}
			f.WriteAt(make([]byte, 2*frame), 32+frame)
			if c.own != "" {
				switch c.own {
				case "exclusive":
					lockT(t, "SHARED", db.Lock(LockShared), OK)
					lockT(t, "EXCLUSIVE", db.Lock(LockExclusive), OK)
				case "exclusive first":
					db.ShmUnmap(false)
					lockT(t, "SHARED", db.Lock(LockShared), OK)
					lockT(t, "EXCLUSIVE", db.Lock(LockExclusive), OK)
					// The first to open the -shm empties it.
					shmMapT(t, db, 0, true)
					writeAt(t, strings.TrimSuffix(name, "-wal")+"-shm", c.index, 0)
				default:
					lockT(t, "checkpoint lock", db.ShmLock(1, 1, ShmLock|ShmExclusive), OK)
				}
				mark := make([]byte, 4)
				binary.NativeEndian.PutUint32(mark, 3)
				writeAt(t, strings.TrimSuffix(name, "-wal")+"-shm", mark, 128)
				if c.own == "copy" {
					if rc := db.WriteAt(make([]byte, 512), 0); rc != OK {
						t.Fatalf("WriteAt = %d", rc)
					}
				}
			}
			SetFault(func(path string, op Op) int {
				if op == OpSync {
					return IOErrFsync
				}
				return OK
			})
			rc = f.Sync(SyncNormal)
			SetFault(nil)
			if rc != IOErrFsync {
				t.Fatalf("Sync = %d", rc)
			}
			// A checkpoint that had read a frame does not copy it now.
			if rc := db.WriteAt(make([]byte, 512), 0); rc != IOErrWrite {
				t.Errorf("WriteAt of the database = %d, want IOErrWrite", rc)
			}
			if rc := db.Truncate(0); rc != IOErrTruncate {
				t.Errorf("Truncate of the database = %d, want IOErrTruncate", rc)
			}
			f.Close()
			f, rc = openWAL(t, name)
			if c.size < 0 {
				if rc != IOErr {
					t.Fatalf("Open = %d, want IOErr", rc)
				}
				// So that the next test does not find a WAL stopped.
				os.Remove(name)
				if f, rc = openWAL(t, name); rc != OK {
					t.Fatalf("Open of a new WAL = %d", rc)
				}
				f.Close()
				return
			}
			if rc != OK {
				t.Fatalf("Open = %d", rc)
			}
			defer f.Close()
			if n, rc := f.Size(); rc != OK || n != c.size {
				t.Fatalf("Size = %d, %d, want %d", n, rc, c.size)
			}
		})
	}
}
