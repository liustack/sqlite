// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// BenchmarkGroupCommit is InsertParallel of bench/ with what group commit
// did: statements per batch, the share of batches that hit maxBatch, and the
// time of a batch's COMMIT. It shows whether waiting a little before a batch
// could pay: only where a COMMIT costs much next to the wait.
func BenchmarkGroupCommit(b *testing.B) {
	for _, sync := range []string{"NORMAL", "FULL"} {
		for _, g := range []int{1, 8, 64} {
			b.Run(fmt.Sprintf("sync=%s/g=%d", sync, g), func(b *testing.B) {
				groupCommit(b, sync, g, false)
			})
		}
		// One write in 100 of each writer is slow: p50 and p99 are those of
		// the fast ones.
		b.Run(fmt.Sprintf("sync=%s/g=8/slow", sync), func(b *testing.B) {
			groupCommit(b, sync, 8, true)
		})
	}
}

// slowInsert takes a few milliseconds to run.
const slowInsert = `INSERT INTO t(name, x, n) SELECT 'slow', 0, count(*) FROM (WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r LIMIT 20000) SELECT i FROM r)`

func groupCommit(b *testing.B, synchronous string, g int, slow bool) {
	path := filepath.Join(b.TempDir(), "bench.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous("+synchronous+")")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(g)
	db.SetMaxIdleConns(g)
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT, x REAL, n INTEGER)`); err != nil {
		b.Fatal(err)
	}
	st, err := db.Prepare(`INSERT INTO t(name, x, n) VALUES (?, ?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	sc, err := db.Conn(b.Context())
	if err != nil {
		b.Fatal(err)
	}
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	sc.Close()
	s := &wl.stats
	batches0, full0, commits0, merged0, time0 := s.batches.Load(), s.full.Load(), s.commits.Load(), s.merged.Load(), s.commitTime.Load()

	n := b.N
	var next sync.Mutex
	errs := make(chan error, g)
	// took holds how long each write took, for p50 and p99.
	took := make([][]time.Duration, g)
	b.ResetTimer()
	for i := range g {
		go func() {
			for {
				next.Lock()
				if n <= 0 {
					next.Unlock()
					errs <- nil
					return
				}
				n--
				k := n
				next.Unlock()
				if slow && k%100 == 0 {
					if _, err := db.Exec(slowInsert); err != nil {
						errs <- err
						return
					}
					continue
				}
				start := time.Now()
				if _, err := st.Exec("name-0123456789abcd", 1.5, 1); err != nil {
					errs <- err
					return
				}
				took[i] = append(took[i], time.Since(start))
			}
		}()
	}
	for range g {
		if err := <-errs; err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	batches, full := s.batches.Load()-batches0, s.full.Load()-full0
	commits, merged := s.commits.Load()-commits0, s.merged.Load()-merged0
	ct := time.Duration(s.commitTime.Load() - time0)
	b.ReportMetric(float64(merged)/float64(b.N), "merged/op")
	if commits > 0 {
		b.ReportMetric(float64(merged)/float64(commits), "stmts/batch")
		b.ReportMetric(float64(ct.Microseconds())/float64(commits), "commit-µs")
	}
	if batches > 0 {
		b.ReportMetric(float64(full)/float64(batches), "full/batch")
	}
	all := slices.Concat(took...)
	slices.Sort(all)
	if len(all) > 0 {
		b.ReportMetric(float64(all[len(all)/2].Microseconds()), "p50-µs")
		b.ReportMetric(float64(all[len(all)*99/100].Microseconds()), "p99-µs")
	}
}
