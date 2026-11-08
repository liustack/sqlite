// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package capture

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/liustack/sqlite/internal/vfs"
)

// failNextSync fails the next sync of the file name, and has every byte
// written to it since its last good sync read back as zero, as once the
// kernel drops the pages a failed sync could not write.
func failNextSync(t *testing.T, name string) *atomic.Bool {
	synced, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var done atomic.Bool
	vfs.SetFault(func(path string, op vfs.Op) int {
		if op != vfs.OpSync || path != name || done.Load() {
			return vfs.OK
		}
		done.Store(true)
		cur, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			return vfs.IOErrFsync
		}
		lost := make([]byte, len(cur))
		copy(lost, synced)
		if err := os.WriteFile(path, lost, 0o644); err != nil {
			t.Errorf("write %s: %v", path, err)
		}
		return vfs.IOErrFsync
	})
	t.Cleanup(func() { vfs.SetFault(nil) })
	return &done
}

// After a failed sync stops the WAL, the capturer closes its connections,
// so that the WAL opens again, and stages a full copy: the transactions cut
// from the WAL were staged, and the database no longer has them.
func TestWALStopped(t *testing.T) {
	ctx := context.Background()
	name := filepath.Join(realPath(t, t.TempDir()), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, "file:"+name+"?_pragma=synchronous(NORMAL)")
	run(t, db, "CREATE TABLE t(x)")
	c.mu.Lock()
	err := c.checkpoint(ctx)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// The WAL starts over, and its header is synced.
	run(t, db, "INSERT INTO t VALUES (1)")
	failed := failNextSync(t, name+"-wal")
	run(t, db, "INSERT INTO t VALUES (2)")
	step(t, c)
	// The connection of snapshots stays open after one.
	if _, _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	copies := c.FullCopies()

	c.mu.Lock()
	err = c.checkpoint(ctx)
	c.mu.Unlock()
	if err == nil || !failed.Load() {
		t.Fatalf("checkpoint: %v, sync failed %v", err, failed.Load())
	}
	ok := false
	for range 10 {
		c.Sync(ctx)
		if _, err := db.Exec("INSERT INTO t VALUES (3)"); err == nil {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatal("the database does not take writes again")
	}
	step(t, c)
	if c.FullCopies() != copies+1 {
		t.Fatalf("%d full copies after the failure", c.FullCopies()-copies)
	}
	sameAs(t, c, name)
}
