// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"math"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// When a sync fails, Linux marks the pages it could not write clean: the
// page cache still shows them, a later sync succeeds, and once the kernel
// evicts them they read back as the disk holds them. Under
// synchronous=NORMAL the WAL is synced only by a checkpoint, so after its
// sync fails the next checkpoint would copy frames that may not be on disk
// into the database, and the database is damaged once they are evicted.
//
// So the first failed sync of a WAL stops it in the process: the WAL is cut
// at the first byte written since its last good sync, which drops the frames
// that may not be on disk, from the page cache too. Every File of the WAL then
// fails each read, write, truncate and sync, and no new File of it opens.
// Under synchronous=FULL a failed commit never made its frames part of the
// WAL, and the cut drops only them.
//
// The WAL opens again once no File of the process has it open and no other
// process has the -shm open: the next connection then finds the WAL index
// left by no one, and SQLite rebuilds it from the frames on disk. While
// another process has the -shm open, the index there still counts the frames
// cut, and a writer would go on past them. When the cut itself fails, nothing
// can make the WAL sound, and it never opens again in the process.

// wal is a WAL file of one or more Files of the process.
type wal struct {
	id   fileID
	name string // the name the first File opened it by
	nRef int    // Files of the WAL, under wals

	// io is held shared by each write and truncate, from its check that the
	// WAL is not stopped to its end, and alone by each sync. So a sync knows
	// every byte written before it, and no write lands after a cut.
	io sync.RWMutex
	// dirty is the first byte written since the last good sync, clean when
	// none was.
	dirty atomic.Int64
	// failed says a sync failed. It is set with io held alone, under wals.
	failed atomic.Bool
	// cut says the WAL was cut on disk after the failure. Under wals.
	cut bool
}

const clean = math.MaxInt64

// wals are the WALs open in the process, and those a failed sync stopped
// until they open again. The mutex is held to find, make, count and drop
// them.
var wals struct {
	sync.Mutex
	m map[fileID]*wal
}

// attachWAL gives f, a WAL just opened, the wal of its file. A WAL that a
// failed sync stopped opens only when it can again.
func (f *File) attachWAL() int {
	if err := unix.Fstat(f.fd, &f.st); err != nil {
		return IOErrFstat
	}
	id := fileID{uint64(f.st.Dev), uint64(f.st.Ino)}
	wals.Lock()
	defer wals.Unlock()
	w := wals.m[id]
	if w != nil && w.failed.Load() {
		if !w.lift() {
			return IOErr
		}
		w = nil
	}
	if w == nil {
		// A stopped WAL that is no longer the file of this name was deleted:
		// SQLite does that once no connection of any process has the
		// database open, and then no one reads its frames again.
		for old, w := range wals.m {
			if w.name == f.path && w.nRef == 0 && w.failed.Load() {
				delete(wals.m, old)
				stoppedWALs.Add(-1)
			}
		}
		w = &wal{id: id, name: f.path}
		w.dirty.Store(clean)
		if wals.m == nil {
			wals.m = make(map[fileID]*wal)
		}
		wals.m[id] = w
	}
	w.nRef++
	f.wal = w
	return OK
}

// shmInUse says another process may have the -shm of the WAL name open: it
// holds the DMS lock (see lockDMS). This process holds it only while a File
// of the database maps the -shm, and SQLite unmaps it before it closes the
// WAL.
func shmInUse(name string) bool {
	fd, err := openFD(strings.TrimSuffix(name, "-wal")+"-shm", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return false
	}
	if err != nil {
		return true
	}
	defer unix.Close(fd)
	var fl unix.Flock_t
	typ, err := testLock(fd, &fl, shmDMS, 1)
	return err != nil || typ != unix.F_UNLCK
}

// WALStopped says a failed sync stopped the WAL of the database name, the
// name SQLite opened it by, and it cannot open yet.
func WALStopped(name string) bool {
	if !WALsStopped() {
		return false
	}
	wals.Lock()
	defer wals.Unlock()
	for _, w := range wals.m {
		if w.name == name+"-wal" && w.failed.Load() {
			return true
		}
	}
	return false
}

// lift lets the stopped WAL w open again when it can: no File of the process
// has it open, the cut reached the disk, and no other process maps the
// -shm. Under wals.
func (w *wal) lift() bool {
	if w.nRef > 0 || !w.cut || shmInUse(w.name) {
		return false
	}
	delete(wals.m, w.id)
	stoppedWALs.Add(-1)
	return true
}

// detachWAL drops f from its wal. A WAL a failed sync stopped stays until
// it can open again: see lift.
func (f *File) detachWAL() {
	w := f.wal
	f.wal = nil
	wals.Lock()
	defer wals.Unlock()
	w.nRef--
	switch {
	case w.nRef > 0:
	case w.failed.Load():
		w.lift()
	default:
		delete(wals.m, w.id)
	}
}

// wrote notes that bytes from off on are about to be written. w.io is held
// shared.
func (w *wal) wrote(off int64) {
	for {
		d := w.dirty.Load()
		if d <= off || w.dirty.CompareAndSwap(d, off) {
			return
		}
	}
}

// fail stops the WAL after a sync of f failed, dirty being the first byte
// written since the last good sync, and cuts it there. w.io is held alone.
func (w *wal) fail(f *File, dirty int64) {
	wals.Lock()
	w.failed.Store(true)
	stoppedWALs.Add(1)
	wals.Unlock()
	cut := dirty == clean
	if !cut && f.faultOf(OpTruncate) == OK {
		// As the sync that failed, the cut may not reach the disk.
		cut = retry(func() error { return unix.Ftruncate(f.fd, dirty) }) == nil && fullFsync(f.fd, true) == nil
	}
	wals.Lock()
	w.cut = cut
	wals.Unlock()
}
