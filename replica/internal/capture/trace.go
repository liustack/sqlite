// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"os"
	"sync/atomic"
)

// ChangeKind is what a Change did.
type ChangeKind int

const (
	// ChangeCreate: the file was made, or emptied when it was there.
	ChangeCreate ChangeKind = iota
	// ChangeWritten: the file holds all its bytes. They are not synced.
	ChangeWritten
	// ChangeSync: the file was synced.
	ChangeSync
	// ChangeRename: the file Path got the name To, in place of the file
	// that had it.
	ChangeRename
	// ChangeRemove: the file was removed.
	ChangeRemove
	// ChangeDirSync: the folder Path was synced. The files made in it,
	// renamed in it and removed from it are on disk.
	ChangeDirSync
)

// Change is one thing the capturer did to a file of its staging folder, or
// to the folder (SetTrace).
type Change struct {
	Kind ChangeKind
	Path string
	To   string // for ChangeRename
}

// trace is the function SetTrace set, nil when none is.
var trace atomic.Pointer[func(Change)]

// SetTrace has the capturers tell f what they do to the staged files: each
// file made, written, synced, renamed and removed, and each sync of a
// folder, once it is done. It is for tests that play a power loss: a file
// is on disk as it was at its last sync, and has a name once the folder is
// synced after it got it. f is called from every goroutine that stages or
// removes a file. Not told: the folders made, the time set on a file, the
// lock file, snapshots and what a compaction writes. nil turns it off.
func SetTrace(f func(Change)) {
	if f == nil {
		trace.Store(nil)
		return
	}
	trace.Store(&f)
}

func tell(kind ChangeKind, path, to string) {
	if p := trace.Load(); p != nil {
		(*p)(Change{Kind: kind, Path: path, To: to})
	}
}

// remove removes the staged file name.
func remove(name string) error {
	err := os.Remove(name)
	if err == nil {
		tell(ChangeRemove, name, "")
	}
	return err
}

// rename moves a staged file to its name. syncDir makes it durable.
func rename(from, to string) error {
	err := renameFS(from, to)
	if err == nil {
		tell(ChangeRename, from, to)
	}
	return err
}

// syncDirFS is syncDir: it syncs the folder dir.
func syncDirFS(dir string) error {
	err := fsyncDir(dir)
	if err == nil {
		tell(ChangeDirSync, dir, "")
	}
	return err
}
