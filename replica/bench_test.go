// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// GroupCommit: autocommit INSERTs from g goroutines, as BenchmarkInsertParallel
// of bench/, with replication off and on (to the test store, every second,
// compaction in the background). On is at most 10% slower.
func BenchmarkGroupCommit(b *testing.B) {
	for _, on := range []bool{false, true} {
		for _, g := range []int{8, 64} {
			b.Run(fmt.Sprintf("replica=%t/g=%d", on, g), func(b *testing.B) {
				name := filepath.Join(b.TempDir(), "x.db")
				if on {
					cfg := testConfig(b)
					cfg.SyncInterval = time.Second
					cfg.schedule = nil
					cfg.OnError = func(err error) { b.Logf("replica: %v", err) }
					r, err := New(b.Context(), name, cfg)
					if err != nil {
						b.Fatal(err)
					}
					defer r.Close()
				}
				q := url.Values{}
				q.Add("_pragma", "journal_mode(WAL)")
				q.Add("_pragma", "synchronous(NORMAL)")
				q.Add("_pragma", "busy_timeout(5000)")
				db, err := sql.Open("sqlite", "file:"+name+"?"+q.Encode())
				if err != nil {
					b.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score REAL NOT NULL, created INTEGER NOT NULL)`); err != nil {
					b.Fatal(err)
				}
				db.SetMaxOpenConns(g)
				db.SetMaxIdleConns(g)
				st, err := db.Prepare(`INSERT INTO t (name, score, created) VALUES (?, ?, ?)`)
				if err != nil {
					b.Fatal(err)
				}
				defer st.Close()
				var left atomic.Int64
				left.Store(int64(b.N))
				errc := make(chan error, g)
				b.ResetTimer()
				for range g {
					go func() {
						for {
							v := left.Add(-1)
							if v < 0 {
								break
							}
							if _, err := st.Exec("name-0123456789abcd", 1.5, v); err != nil {
								left.Store(-1)
								errc <- err
								return
							}
						}
						errc <- nil
					}()
				}
				for range g {
					if err := <-errc; err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
			})
		}
	}
}
