// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Many goroutines insert at once, some rows breaking a UNIQUE constraint.
// Each caller gets its own error or its own rowid, and the table holds
// exactly the rows that succeeded, each at the rowid its caller got.
func TestConcurrentInsertsResults(t *testing.T) {
	db := openDB(t, "file:"+filepath.Join(t.TempDir(), "t.db")+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, k TEXT UNIQUE, g INT)`); err != nil {
		t.Fatal(err)
	}
	st, err := db.Prepare(`INSERT INTO t(k, g) VALUES (?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const goroutines, each = 32, 200
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		got      = map[int64]string{}
		failures atomic.Int64
	)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// Every 10th key is shared by all goroutines: only one
				// of them can insert it.
				k := fmt.Sprintf("g%d-%d", g, i)
				if i%10 == 0 {
					k = fmt.Sprintf("shared-%d", i)
				}
				r, err := st.Exec(k, g)
				if err != nil {
					if !strings.Contains(err.Error(), "UNIQUE") {
						t.Error(err)
						return
					}
					failures.Add(1)
					continue
				}
				id, _ := r.LastInsertId()
				if n, _ := r.RowsAffected(); n != 1 {
					t.Errorf("rows affected %d", n)
				}
				mu.Lock()
				if prev, dup := got[id]; dup {
					t.Errorf("rowid %d given to %q and %q", id, prev, k)
				}
				got[id] = k
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if want := int64(each / 10 * (goroutines - 1)); failures.Load() != want {
		t.Errorf("%d failures, want %d", failures.Load(), want)
	}
	rows, err := db.Query(`SELECT id, k FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		var k string
		rows.Scan(&id, &k)
		if got[id] != k {
			t.Errorf("row %d is %q, its caller got %q", id, k, got[id])
		}
		n++
	}
	if n != len(got) {
		t.Errorf("%d rows, %d successes", n, len(got))
	}
}
