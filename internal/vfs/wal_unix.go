// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"math"
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
// fails each read, write, truncate and sync, and no new File of it opens,
// until all of them are closed. A connection opened again finds the WAL
// index left by no one and rebuilds it from the frames on disk. Under
// synchronous=FULL a failed commit never made its frames part of the WAL, and
// the cut drops only them.
//
// This holds for the Files of one process. Another process that has the
// database open does not know the WAL stopped.

// wal is a WAL file of one or more Files of the process.
type wal struct {
	id   fileID
	name string // the name the first File opened it by
	nRef int    // Files of the WAL, under wals

	// dirty is the first byte written since the last good sync, clean when
	// none was.
	dirty  atomic.Int64
	failed atomic.Bool // set under wals
}

const clean = math.MaxInt64

// wals are the WALs open in the process. The mutex is held to find, make,
// count and drop them.
var wals struct {
	sync.Mutex
	m map[fileID]*wal
}

// attachWAL gives f, a WAL just opened, the wal of its file. A WAL that a
// failed sync stopped does not open.
func (f *File) attachWAL() int {
	if err := unix.Fstat(f.fd, &f.st); err != nil {
		return IOErrFstat
	}
	id := fileID{uint64(f.st.Dev), uint64(f.st.Ino)}
	wals.Lock()
	defer wals.Unlock()
	w := wals.m[id]
	if w == nil {
		w = &wal{id: id, name: f.path}
		w.dirty.Store(clean)
		if wals.m == nil {
			wals.m = make(map[fileID]*wal)
		}
		wals.m[id] = w
	} else if w.failed.Load() {
		return IOErr
	}
	w.nRef++
	f.wal = w
	return OK
}

// WALStopped says a failed sync stopped the WAL of the database name, the
// name SQLite opened it by, and a File of the process still has it open.
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

// detachWAL drops f from its wal. The last File to go lets the WAL open
// again.
func (f *File) detachWAL() {
	w := f.wal
	f.wal = nil
	wals.Lock()
	defer wals.Unlock()
	w.nRef--
	if w.nRef == 0 {
		delete(wals.m, w.id)
		if w.failed.Load() {
			stoppedWALs.Add(-1)
		}
	}
}

// wrote notes that bytes from off on are about to be written.
func (w *wal) wrote(off int64) {
	for {
		d := w.dirty.Load()
		if d <= off || w.dirty.CompareAndSwap(d, off) {
			return
		}
	}
}

// fail stops the WAL after a sync of f failed, dirty being what the sync
// was to write from, and cuts it at the first byte not known to be on disk.
func (w *wal) fail(f *File, dirty int64) {
	w.wrote(dirty)
	wals.Lock()
	first := !w.failed.Swap(true)
	if first {
		stoppedWALs.Add(1)
	}
	wals.Unlock()
	if !first {
		return
	}
	if cut := w.dirty.Load(); cut != clean {
		// As the sync that failed, the cut may not reach the disk. Then
		// nothing can make the WAL sound, but the frames past the cut no
		// longer show from the page cache.
		if retry(func() error { return unix.Ftruncate(f.fd, cut) }) == nil {
			fullFsync(f.fd, false)
		}
	}
}
