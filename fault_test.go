// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liustack/sqlite/internal/vfs"
)

// failOp has the n-th op on a file whose name ends with suffix fail with
// rc, once, for the rest of the test.
func failOp(t *testing.T, suffix string, op vfs.Op, n int64, rc int) *atomic.Int64 {
	var seen, failed atomic.Int64
	vfs.SetFault(func(path string, o vfs.Op) int {
		if o == op && strings.HasSuffix(path, suffix) && seen.Add(1) == n {
			failed.Add(1)
			return rc
		}
		return vfs.OK
	})
	t.Cleanup(func() { vfs.SetFault(nil) })
	return &failed
}

// Merged inserts while one write or sync of the WAL or the database fails:
// every caller told its insert committed finds its row, now and after the
// database is opened again, and the file stays sound.
func TestMergeDiskFails(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		op           vfs.Op
		n            int64
		rc           int
	}{
		{"wal sync", "-wal", vfs.OpSync, 20, vfs.IOErrFsync},
		{"wal write", "-wal", vfs.OpWrite, 20, vfs.IOErrWrite},
		{"db sync", ".db", vfs.OpSync, 2, vfs.IOErrFsync},
		{"db write", ".db", vfs.OpWrite, 20, vfs.IOErrWrite},
	} {
		t.Run(tc.name, func(t *testing.T) { mergeDiskFails(t, tc.suffix, tc.op, tc.n, tc.rc) })
	}
}

func mergeDiskFails(t *testing.T, suffix string, op vfs.Op, n int64, rc int) {
	path := mPath(t)
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=wal_autocheckpoint(100)"
	db := mOpen(t, dsn)
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	mExec(t, db, `CREATE TABLE t(k TEXT PRIMARY KEY)`)
	failed := failOp(t, suffix, op, n, rc)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   = map[string]bool{}
		errs = map[string]error{}
	)
	for g := range 32 {
		wg.Go(func() {
			for i := range 50 {
				k := fmt.Sprintf("g%d-%d", g, i)
				_, err := db.Exec(`INSERT INTO t VALUES (?)`, k)
				mu.Lock()
				if err == nil {
					ok[k] = true
				} else {
					errs[k] = err
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	vfs.SetFault(nil)
	if failed.Load() != 1 {
		t.Fatalf("%d syncs failed, want 1", failed.Load())
	}
	check := func(db interface {
		Query(string, ...any) (*sql.Rows, error)
	}, when string) {
		t.Helper()
		have := map[string]bool{}
		rows, err := db.Query(`SELECT k FROM t`)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		for rows.Next() {
			var k string
			rows.Scan(&k)
			have[k] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		lost, ghost := 0, 0
		for k := range ok {
			if !have[k] {
				lost++
			}
		}
		for k := range errs {
			if have[k] {
				ghost++
			}
		}
		kinds := map[string]int{}
		for _, err := range errs {
			kinds[err.Error()]++
		}
		t.Logf("%s: %d committed, %d failed %v, %d rows, %d told committed but missing, %d told failed but present", when, len(ok), len(errs), kinds, len(have), lost, ghost)
		if lost > 0 {
			t.Errorf("%s: %d rows told committed are missing", when, lost)
		}
	}
	check(db, "after the failure")
	db.Close()
	db2 := mOpen(t, dsn)
	check(db2, "opened again")
	var ic string
	if err := db2.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check: %q %v", ic, err)
	}
}
