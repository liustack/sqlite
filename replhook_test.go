// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
	"github.com/liustack/sqlite/internal/replhook"
)

// realName is the name SQLite gives the file name, the one replhook takes:
// with symbolic links resolved on unix, with the folder as the system spells
// it on windows (where a short 8.3 name stays short).
func realName(t *testing.T, name string) string {
	t.Helper()
	c, err := csqlite.Open(name, csqlite.OpenReadWrite|csqlite.OpenCreate|csqlite.OpenNoMutex, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.Filename("main")
}

func TestAttach(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	path := realName(t, name)
	detach, err := replhook.Attach(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replhook.Attach(path, nil); err == nil {
		t.Fatal("attached twice")
	}
	db := openDB(t, name)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("PRAGMA wal_autocheckpoint").Scan(&n); err != nil || n != 0 {
		t.Fatalf("wal_autocheckpoint %d %v", n, err)
	}
	for _, q := range []string{"PRAGMA wal_checkpoint", "PRAGMA wal_autocheckpoint=1000", "PRAGMA journal_mode=DELETE"} {
		if _, err := db.Exec(q); !errors.Is(err, csqlite.ErrGuarded) {
			t.Errorf("%s: %v", q, err)
		}
	}
	// The PRAGMAs of the connector are guarded too.
	other, err := sql.Open("sqlite", name+"?_pragma=wal_autocheckpoint(1000)")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Ping(); !errors.Is(err, csqlite.ErrGuarded) {
		t.Errorf("a connector that sets wal_autocheckpoint: %v", err)
	}
	other.Close()

	// The write lock holds the writers of the process off.
	unlock, err := replhook.Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() {
		_, err := db.Exec("INSERT INTO t VALUES (1)")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("a write ran while the lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	detach()
	if _, err := replhook.Lock(context.Background(), path); err == nil {
		t.Error("locked a file no longer attached")
	}
	// Connections opened after detach are as before.
	after := openDB(t, name)
	if _, err := after.Exec("PRAGMA wal_checkpoint"); err != nil {
		t.Errorf("a connection opened after detach: %v", err)
	}
}

func TestAttachOpen(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	db := openDB(t, name)
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := replhook.Attach(realName(t, name), nil); err == nil {
		t.Fatal("attached a file the driver has open")
	}
	db.Close()
	detach, err := replhook.Attach(realName(t, name), nil)
	if err != nil {
		t.Fatalf("attach once closed: %v", err)
	}
	detach()
}

// One file spelled two ways (case on a file system that ignores it, a short
// 8.3 name on windows) is one file to the driver: attached under one name,
// its connections under the other are guarded, and attaching it under the
// other name while open fails.
func TestAttachAlias(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "x.db")
	alias := filepath.Join(dir, "X.DB")
	path := realName(t, name)
	a, errA := os.Stat(name)
	b, errB := os.Stat(alias)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skip("the file system tells case apart")
	}
	detach, err := replhook.Attach(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	db := openDB(t, alias)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint"); !errors.Is(err, csqlite.ErrGuarded) {
		t.Errorf("a connection under the other name: %v", err)
	}
	if _, err := replhook.Attach(realName(t, alias), nil); err == nil {
		t.Error("attached under the other name")
	}
	db.Close()
	detach()

	db = openDB(t, alias)
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := replhook.Attach(path, nil); err == nil {
		t.Error("attached a file open under the other name")
	}
}

// committed is called after each write, holding the write lock, with the
// frames of the WAL the write left.
func TestAttachCommitted(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	path := realName(t, name)
	var calls []uint32
	var lockErr error
	detach, err := replhook.Attach(path, func(frames, salt uint32, limit int64) {
		calls = append(calls, frames)
		if len(calls) == 1 {
			// The lock is held: taking it again waits.
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if unlock, err := replhook.Lock(ctx, path); err == nil {
				unlock()
				lockErr = errors.New("took the write lock inside committed")
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	db := openDB(t, name)
	db.SetMaxOpenConns(4)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	for range 3 {
		if _, err := db.Exec("INSERT INTO t VALUES (randomblob(3000))"); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) < 4 {
		t.Fatalf("committed called %d times for 4 writes", len(calls))
	}
	for i := 1; i < len(calls); i++ {
		if calls[i] < calls[i-1] {
			t.Fatalf("frames %v go back", calls)
		}
	}
	if last := calls[len(calls)-1]; last < 4 {
		t.Fatalf("%d frames after 4 writes of a page or more", last)
	}
}

// A file attached for replication cannot be attached (ATTACH) to a
// connection of another database, nor to one of itself: writes there would
// take neither its write lock nor its committed call, and run their own
// checkpoints. The statement fails, the file is detached again, the next
// statement of the Exec does not run, and the connection goes on.
func TestSQLAttachReplicated(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	dbb := openDB(t, b)
	if _, err := dbb.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	dbb.Close()
	detach, err := replhook.Attach(realName(t, b), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()

	for _, name := range []string{a, b} {
		db := openDB(t, name)
		db.SetMaxOpenConns(1)
		if _, err := db.Exec("ATTACH ? AS other; INSERT INTO other.t VALUES (1)", b); err == nil {
			t.Fatalf("%s: attached the replicated file", filepath.Base(name))
		}
		rows, err := db.Query("ATTACH ? AS other", b)
		if err == nil {
			rows.Close()
			t.Fatalf("%s: attached the replicated file with Query", filepath.Base(name))
		}
		var n int
		if err := db.QueryRow("SELECT count(*) FROM pragma_database_list WHERE name = 'other'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: still attached: %d %v", filepath.Base(name), n, err)
		}
		// Another file attaches as before.
		if _, err := db.Exec("ATTACH ? AS other; CREATE TABLE IF NOT EXISTS other.u(y); DETACH other", filepath.Join(dir, "c.db")); err != nil {
			t.Fatalf("%s: %v", filepath.Base(name), err)
		}
		db.Close()
	}
	// ATTACH in a PRAGMA of the DSN fails the connection.
	dsn := a + "?_pragma=" + url.QueryEscape("foreign_keys(1); ATTACH '"+b+"' AS other")
	if err := openDB(t, dsn).Ping(); err == nil {
		t.Fatal("a DSN attached the replicated file")
	}
	var n int
	if err := openDB(t, b).QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows %d %v", n, err)
	}
}

// A file that a connection has attached (ATTACH) is open: it cannot be
// attached for replication until it is detached.
func TestSQLAttachedFirst(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	dbb := openDB(t, b)
	if err := dbb.Ping(); err != nil {
		t.Fatal(err)
	}
	dbb.Close()
	db := openDB(t, a)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("ATTACH ? AS other", b); err != nil {
		t.Fatal(err)
	}
	if detach, err := replhook.Attach(realName(t, b), nil); err == nil {
		detach()
		t.Fatal("attached a file a connection has attached")
	}
	if _, err := db.Exec("DETACH other"); err != nil {
		t.Fatal(err)
	}
	// So does a PRAGMA of the DSN.
	dsn := a + "?_pragma=" + url.QueryEscape("foreign_keys(1); ATTACH '"+b+"' AS other")
	other := openDB(t, dsn)
	if err := other.Ping(); err != nil {
		t.Fatal(err)
	}
	if detach, err := replhook.Attach(realName(t, b), nil); err == nil {
		detach()
		t.Fatal("attached a file the DSN of a connection attached")
	}
	other.Close()
	detach, err := replhook.Attach(realName(t, b), nil)
	if err != nil {
		t.Fatalf("once detached: %v", err)
	}
	detach()
}
