// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

// A failed sync of a rollback journal cuts it at the first byte written since
// its last good sync, and stops the File that synced it: SQLite must not roll
// back from a journal it may not read back as written. The journal opens
// again as it is on disk.
func TestJournalStops(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	// A journal takes the mode of its database, which must be there.
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	open := func() *File {
		t.Helper()
		f, _, rc := Open(db+"-journal", mainJrnl)
		if rc != OK {
			t.Fatalf("Open = %d", rc)
		}
		return f
	}
	a, b := open(), open()
	defer b.Close()
	p := []byte("0123456789")
	if rc := a.WriteAt(p, 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	if rc := a.Sync(SyncNormal); rc != OK {
		t.Fatalf("Sync = %d", rc)
	}
	if JournalsStopped() || JournalStopped(db) {
		t.Fatal("a journal is stopped after a good sync")
	}
	// Written after the good sync.
	a.WriteAt(p, 40)
	a.WriteAt(p, 25)

	SetFault(func(path string, op Op) int {
		if op == OpSync {
			return IOErrFsync
		}
		return OK
	})
	rc := a.Sync(SyncNormal)
	SetFault(nil)
	if rc != IOErrFsync {
		t.Fatalf("Sync = %d", rc)
	}
	if !JournalsStopped() || !JournalStopped(db) {
		t.Fatal("the journal is not stopped")
	}
	if JournalStopped(db + "2") {
		t.Fatal("the journal of another database is stopped")
	}
	if rc := a.ReadAt(p, 0); rc != IOErrRead {
		t.Errorf("ReadAt = %d", rc)
	}
	if rc := a.WriteAt(p, 0); rc != IOErrWrite {
		t.Errorf("WriteAt = %d", rc)
	}
	if rc := a.Truncate(0); rc != IOErrTruncate {
		t.Errorf("Truncate = %d", rc)
	}
	if rc := a.Sync(SyncNormal); rc != IOErrFsync {
		t.Errorf("Sync = %d", rc)
	}
	if _, rc := a.Size(); rc != IOErrFstat {
		t.Errorf("Size = %d", rc)
	}
	// Another File of the journal reads it as it is on disk.
	if rc := b.ReadAt(p, 0); rc != OK {
		t.Errorf("ReadAt of another File = %d", rc)
	}
	if size, rc := b.Size(); rc != OK || size != 25 {
		t.Errorf("Size = %d, %d, want the journal cut at 25", size, rc)
	}

	a.Close()
	a.Close()
	if JournalsStopped() || JournalStopped(db) {
		t.Fatal("the journal is still stopped once its File is closed")
	}
	c := open()
	defer c.Close()
	if rc := c.ReadAt(p, 0); rc != OK || string(p) != "0123456789" {
		t.Fatalf("ReadAt = %d, %q", rc, p)
	}
}

// A failed sync stops only a rollback journal: a database file goes on, as
// SQLite rolls its pages back from the journal.
func TestJournalStopsOnly(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db")
	f, _, rc := Open(name, mainDB)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
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
	if JournalsStopped() {
		t.Fatal("a journal is stopped")
	}
	if rc := f.WriteAt([]byte("x"), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
}

// A truncate counts as a write where the journal then ends: the cut never
// makes the journal longer.
func TestJournalCutAfterTruncate(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _, rc := Open(db+"-journal", mainJrnl)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	f.WriteAt(make([]byte, 100), 0)
	if rc := f.Sync(SyncNormal); rc != OK {
		t.Fatalf("Sync = %d", rc)
	}
	f.WriteAt(make([]byte, 10), 50)
	if rc := f.Truncate(30); rc != OK {
		t.Fatalf("Truncate = %d", rc)
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
	if st, err := os.Stat(db + "-journal"); err != nil || st.Size() != 30 {
		t.Fatalf("size %d, %v, want 30", st.Size(), err)
	}
}

// A journal with nothing written since its last good sync is not cut.
func TestJournalNotCutWhenClean(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _, rc := Open(db+"-journal", mainJrnl)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	f.WriteAt(make([]byte, 100), 0)
	if rc := f.Sync(SyncNormal); rc != OK {
		t.Fatalf("Sync = %d", rc)
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
	if st, err := os.Stat(db + "-journal"); err != nil || st.Size() != 100 {
		t.Fatalf("size %d, %v, want 100", st.Size(), err)
	}
	if !JournalStopped(db) {
		t.Fatal("the journal is not stopped")
	}
}

// failJournalSync opens the journal of db, syncs 100 bytes, writes 10 more
// and fails their sync, with the cut failing too. It returns the File, open
// and stopped.
func failJournalSync(t *testing.T, db string) *File {
	t.Helper()
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _, rc := Open(db+"-journal", mainJrnl)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	f.WriteAt(make([]byte, 100), 0)
	if rc := f.Sync(SyncNormal); rc != OK {
		t.Fatalf("Sync = %d", rc)
	}
	f.WriteAt(make([]byte, 10), 100)
	SetFault(func(path string, op Op) int {
		switch op {
		case OpSync:
			return IOErrFsync
		case OpTruncate:
			return IOErrTruncate
		}
		return OK
	})
	rc = f.Sync(SyncNormal)
	SetFault(nil)
	if rc != IOErrFsync {
		t.Fatalf("Sync = %d", rc)
	}
	return f
}

func journalSize(t *testing.T, db string) int64 {
	t.Helper()
	st, err := os.Stat(db + "-journal")
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// A journal whose cut failed does not open until the cut is made: SQLite
// must not roll back from what the failed sync may have left of it.
func TestJournalCutFails(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	failJournalSync(t, db).Close()
	if size := journalSize(t, db); size != 110 {
		t.Fatalf("size %d, want the journal not cut", size)
	}
	SetFault(func(path string, op Op) int {
		if op == OpTruncate {
			return IOErrTruncate
		}
		return OK
	})
	_, _, rc := Open(db+"-journal", mainJrnl)
	SetFault(nil)
	if rc != IOErr {
		t.Fatalf("Open while the cut fails = %d", rc)
	}
	// As SQLite opens a journal to see whether it must be rolled back.
	f, _, rc := Open(db+"-journal", OpenReadOnly|OpenMainJournal)
	if rc != OK {
		t.Fatalf("Open once the cut can be made = %d", rc)
	}
	defer f.Close()
	if size := journalSize(t, db); size != 100 {
		t.Fatalf("size %d, want the journal cut at 100", size)
	}
	// The cut is made once.
	if err := os.WriteFile(db+"-journal", make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	g, _, rc := Open(db+"-journal", mainJrnl)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	g.Close()
	if size := journalSize(t, db); size != 200 {
		t.Fatalf("size %d, want 200", size)
	}
}

// A journal whose cut failed and that another process wrote since, or
// deleted, is not cut: it is no longer the journal the sync failed on.
func TestJournalCutFailsThenChanges(t *testing.T) {
	t.Run("written", func(t *testing.T) {
		db := filepath.Join(t.TempDir(), "test.db")
		failJournalSync(t, db).Close()
		w, err := os.OpenFile(db+"-journal", os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.WriteAt(make([]byte, 50), 100); err != nil {
			t.Fatal(err)
		}
		w.Close()
		f, _, rc := Open(db+"-journal", mainJrnl)
		if rc != OK {
			t.Fatalf("Open = %d", rc)
		}
		f.Close()
		if size := journalSize(t, db); size != 150 {
			t.Fatalf("size %d, want 150", size)
		}
	})
	t.Run("deleted", func(t *testing.T) {
		db := filepath.Join(t.TempDir(), "test.db")
		failJournalSync(t, db).Close()
		if err := os.Remove(db + "-journal"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(db+"-journal", make([]byte, 300), 0o644); err != nil {
			t.Fatal(err)
		}
		f, _, rc := Open(db+"-journal", mainJrnl)
		if rc != OK {
			t.Fatalf("Open = %d", rc)
		}
		f.Close()
		if size := journalSize(t, db); size != 300 {
			t.Fatalf("size %d, want 300", size)
		}
	})
}
