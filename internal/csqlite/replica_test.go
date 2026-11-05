// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package csqlite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func exec(t *testing.T, c *Conn, sql string) {
	t.Helper()
	if err := c.Exec(sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// beginRead opens a read transaction on c.
func beginRead(t *testing.T, c *Conn) {
	t.Helper()
	exec(t, c, "BEGIN")
	s, _, err := c.Prepare("SELECT count(*) FROM sqlite_schema")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Step(); err != nil {
		t.Fatal(err)
	}
	s.Finalize()
}

func walDB(t *testing.T) (w, r *Conn, name string) {
	name = filepath.Join(t.TempDir(), "x.db")
	w = open(t, name)
	exec(t, w, "PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE t(x)")
	for range 20 {
		exec(t, w, "INSERT INTO t VALUES (randomblob(3000))")
	}
	return w, open(t, name), name
}

func TestWALHeader(t *testing.T) {
	_, r, name := walDB(t)
	if _, err := r.WALHeader(); err == nil {
		t.Fatal("a header with no read transaction")
	}
	beginRead(t, r)
	h, err := r.WALHeader()
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(name + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if h.PageSize != 4096 || h.MaxFrame == 0 || h.BigEndian != (wal[3] == 0x83) {
		t.Fatalf("header %+v", h)
	}
	if !bytes.Equal(h.Salt[:], wal[16:24]) {
		t.Errorf("salts %x, the WAL has %x", h.Salt, wal[16:24])
	}
	frame := 32 + int(h.MaxFrame-1)*(24+h.PageSize)
	be := binary.BigEndian
	if got := [2]uint32{be.Uint32(wal[frame+16:]), be.Uint32(wal[frame+20:])}; got != h.Checksum {
		t.Errorf("checksum %x, the last frame has %x", h.Checksum, got)
	}
	if n := be.Uint32(wal[frame+4:]); n != h.Pages {
		t.Errorf("%d pages, the last frame says %d", h.Pages, n)
	}
	n, err := r.PageCount()
	if err != nil || n != h.Pages {
		t.Fatalf("PageCount %d %v, header %d", n, err, h.Pages)
	}
	exec(t, r, "COMMIT")
}

func TestReadPage(t *testing.T) {
	w, r, name := walDB(t)
	if _, _, err := w.Checkpoint(CheckpointTruncate); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	beginRead(t, r)
	n, err := r.PageCount()
	if err != nil || int(n)*4096 != len(file) {
		t.Fatalf("%d pages %v, file of %d bytes", n, err, len(file))
	}
	page := make([]byte, 4096)
	for p := uint32(1); p <= n; p++ {
		if err := r.ReadPage(p, page); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(page, file[(p-1)*4096:p*4096]) {
			t.Fatalf("page %d differs from the file", p)
		}
	}
	if err := r.ReadPage(n+1, page); err == nil {
		t.Error("read a page past the end")
	}
	if err := r.ReadPage(1, page[:100]); err == nil {
		t.Error("read a page into a short buffer")
	}
	exec(t, r, "COMMIT")
	// A page written after the read transaction began is not seen.
	beginRead(t, r)
	before := make([]byte, 4096)
	if err := r.ReadPage(2, before); err != nil {
		t.Fatal(err)
	}
	exec(t, w, "UPDATE t SET x = randomblob(3000)")
	if err := r.ReadPage(2, page); err != nil || !bytes.Equal(page, before) {
		t.Fatalf("page 2 changed in the read transaction (%v)", err)
	}
	exec(t, r, "COMMIT")
	if err := r.ReadPage(2, page); err == nil {
		t.Error("read a page with no read transaction")
	}
}

func TestCheckpointTruncate(t *testing.T) {
	w, _, name := walDB(t)
	log, ckpt, err := w.Checkpoint(CheckpointPassive)
	if err != nil || log == 0 || ckpt != log {
		t.Fatalf("passive: %d %d %v", log, ckpt, err)
	}
	if _, _, err := w.Checkpoint(CheckpointTruncate); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(name + "-wal"); err != nil || st.Size() != 0 {
		t.Fatalf("WAL after TRUNCATE: %v %v", st, err)
	}
}

func TestGuard(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name)
	// same is the main database again, under another name.
	exec(t, c, "PRAGMA journal_mode=WAL; ATTACH ':memory:' AS aux; ATTACH '"+name+"' AS same")
	c.SetGuard(true)
	for _, sql := range []string{
		"PRAGMA wal_checkpoint", "PRAGMA wal_checkpoint(TRUNCATE)", "PRAGMA main.wal_checkpoint",
		"PRAGMA wal_autocheckpoint=100", "PRAGMA journal_mode=DELETE", "PRAGMA main.journal_mode=memory",
		"PRAGMA locking_mode=EXCLUSIVE",
		// wal_autocheckpoint is of the connection, whatever the schema.
		"PRAGMA temp.wal_autocheckpoint=100", "PRAGMA aux.wal_autocheckpoint=100",
		"PRAGMA same.wal_checkpoint(TRUNCATE)", "PRAGMA same.journal_mode=DELETE", "PRAGMA same.locking_mode=EXCLUSIVE",
	} {
		if s, _, err := c.Prepare(sql); !errors.Is(err, ErrGuarded) {
			if s != nil {
				s.Finalize()
			}
			t.Errorf("Prepare %s: %v", sql, err)
		}
		if err := c.Exec(sql); !errors.Is(err, ErrGuarded) {
			t.Errorf("Exec %s: %v", sql, err)
		}
	}
	for _, sql := range []string{
		"PRAGMA journal_mode=wal", "PRAGMA journal_mode", "PRAGMA wal_autocheckpoint", "PRAGMA locking_mode",
		"PRAGMA locking_mode=normal", "PRAGMA aux.journal_mode=memory", "PRAGMA aux.wal_checkpoint",
		"PRAGMA synchronous=NORMAL", "PRAGMA temp.journal_mode=memory", "PRAGMA same.journal_mode",
	} {
		if err := c.Exec(sql); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
	c.SetGuard(false)
	exec(t, c, "PRAGMA main.wal_checkpoint; PRAGMA wal_autocheckpoint=100")
}

// The replicator counts on Change: one more for each commit, of any
// connection, kept when the WAL starts over.
func TestWALHeaderChange(t *testing.T) {
	w, r, _ := walDB(t)
	header := func() WALHeader {
		t.Helper()
		beginRead(t, r)
		defer exec(t, r, "COMMIT")
		h, err := r.WALHeader()
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	h0 := header()
	exec(t, w, "INSERT INTO t VALUES (1); INSERT INTO t VALUES (2)")
	exec(t, w, "BEGIN; INSERT INTO t VALUES (3); INSERT INTO t VALUES (4); COMMIT")
	exec(t, w, "BEGIN; INSERT INTO t VALUES (5); ROLLBACK")
	h1 := header()
	if h1.Change-h0.Change != 3 || h1.Salt != h0.Salt {
		t.Fatalf("3 commits: change %d to %d", h0.Change, h1.Change)
	}
	if _, _, err := w.Checkpoint(CheckpointTruncate); err != nil {
		t.Fatal(err)
	}
	h2 := header()
	if h2.Change != h1.Change || h2.MaxFrame != 0 || h2.Salt == h1.Salt {
		t.Fatalf("after TRUNCATE: %+v, before %+v", h2, h1)
	}
	exec(t, w, "INSERT INTO t VALUES (6)")
	if h3 := header(); h3.Change != h2.Change+1 || h3.Salt != h2.Salt || h3.MaxFrame == 0 {
		t.Fatalf("a commit after TRUNCATE: %+v, before %+v", h3, h2)
	}
}

// WALFrames is the length of the WAL a commit left, with no transaction
// open, also once the WAL started over.
func TestWALFrames(t *testing.T) {
	w, r, _ := walDB(t)
	if n := r.WALFrames(); n != 0 {
		t.Fatalf("%d frames before the connection read the WAL", n)
	}
	beginRead(t, r)
	h, err := r.WALHeader()
	if err != nil {
		t.Fatal(err)
	}
	exec(t, r, "COMMIT")
	if n := w.WALFrames(); n != h.MaxFrame || n == 0 {
		t.Fatalf("%d frames after the commits, the WAL has %d", n, h.MaxFrame)
	}
	exec(t, w, "PRAGMA wal_checkpoint(PASSIVE)")
	exec(t, w, "INSERT INTO t VALUES (1)")
	if n := w.WALFrames(); n != 1 {
		t.Fatalf("%d frames after the WAL started over with one", n)
	}
	mem := open(t, ":memory:")
	exec(t, mem, "CREATE TABLE t(x); INSERT INTO t VALUES (1)")
	if n := mem.WALFrames(); n != 0 {
		t.Fatalf("%d frames without a WAL", n)
	}
}
