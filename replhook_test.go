// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
	"github.com/liustack/sqlite/internal/replhook"
)

// realName is name with the symbolic links of its folder resolved, as SQLite
// names the file.
func realName(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, filepath.Base(name))
}

func TestAttach(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	path := realName(t, name)
	detach, err := replhook.Attach(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replhook.Attach(path); err == nil {
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
	if _, err := replhook.Attach(realName(t, name)); err == nil {
		t.Fatal("attached a file the driver has open")
	}
	db.Close()
	detach, err := replhook.Attach(realName(t, name))
	if err != nil {
		t.Fatalf("attach once closed: %v", err)
	}
	detach()
}
