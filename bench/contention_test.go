// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package bench

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestContention has many goroutines write at once and counts the writes
// that fail because the database is locked, and how long each write that
// succeeds takes. Set CONTENTION to how long each case runs:
//
//	CONTENTION=3s go test -tags liustack -run '^TestContention$' -v
//
// Inserts are autocommit INSERTs. A transfer is a transaction that reads two
// balances and moves an amount between them, started as the DSN says:
// deferred, the default of every driver, or _txlock=immediate. A deferred
// transfer that writes first takes the write lock with its first statement,
// as one started immediate does. hold is work the program does inside the
// transaction, holding the lock. The sum of the balances never changes.
func TestContention(t *testing.T) {
	run, err := time.ParseDuration(os.Getenv("CONTENTION"))
	if err != nil {
		t.Skip("set CONTENTION to how long each case runs, as 3s")
	}
	for _, c := range []struct {
		work   string
		txlock string
		g      int
		hold   time.Duration // work inside the transaction, the lock held
		sync   string
	}{
		{"insert", "", 64, 0, "NORMAL"},
		{"transfer", "", 64, 0, "NORMAL"},
		{"transfer", "immediate", 64, 0, "NORMAL"},
		{"transfer", "immediate", 64, 2 * time.Millisecond, "NORMAL"},
		{"writefirst", "", 64, 2 * time.Millisecond, "NORMAL"},
		{"transfer", "immediate", 64, 0, "FULL"},
		{"transfer", "immediate", 256, time.Millisecond, "NORMAL"},
	} {
		dsn := DSN(filepath.Join(t.TempDir(), "x.db"), c.sync)
		if c.txlock != "" {
			dsn += "&_txlock=" + c.txlock
		}
		db, err := sql.Open(DriverName, dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(c.g)
		db.SetMaxIdleConns(c.g)
		const accounts, start = 1000, 100
		for _, q := range []string{
			"CREATE TABLE log(g INTEGER, at INTEGER)",
			"CREATE TABLE account(id INTEGER PRIMARY KEY, balance INTEGER NOT NULL)",
			fmt.Sprintf("WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM s WHERE i < %d) INSERT INTO account SELECT i, %d FROM s", accounts, start),
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		var done, locked atomic.Int64
		var other sync.Map
		var latMu sync.Mutex
		var lat []time.Duration
		deadline := time.Now().Add(run)
		var wg sync.WaitGroup
		for g := range c.g {
			wg.Go(func() {
				r := rand.New(rand.NewPCG(uint64(g), 1))
				for time.Now().Before(deadline) {
					t0 := time.Now()
					var err error
					if c.work == "insert" {
						_, err = db.Exec("INSERT INTO log VALUES (?, ?)", g, time.Now().UnixNano())
					} else {
						err = transfer(db, 1+r.IntN(accounts), 1+r.IntN(accounts), c.hold, c.work == "writefirst")
					}
					switch {
					case err == nil:
						done.Add(1)
						latMu.Lock()
						lat = append(lat, time.Since(t0))
						latMu.Unlock()
					case isLocked(err):
						locked.Add(1)
					default:
						other.Store(err.Error(), true)
					}
				}
			})
		}
		wg.Wait()
		var sum int
		if err := db.QueryRow("SELECT sum(balance) FROM account").Scan(&sum); err != nil {
			t.Fatal(err)
		}
		db.Close()
		mode := "deferred"
		if c.txlock != "" {
			mode = c.txlock
		}
		if c.work == "insert" {
			mode = "autocommit"
		}
		total := done.Load() + locked.Load()
		slices.Sort(lat)
		pct := func(p float64) time.Duration {
			if len(lat) == 0 {
				return 0
			}
			return lat[min(len(lat)-1, int(p*float64(len(lat))))]
		}
		t.Logf("%-8s %-10s %-10s %-6s g=%-3d hold=%-4v %8.0f/s done, %6d locked (%5.2f%% of %d), p50 %v p99 %v max %v", DriverTag, c.work, mode, c.sync, c.g, c.hold,
			float64(done.Load())/run.Seconds(), locked.Load(), 100*float64(locked.Load())/float64(max(total, 1)), total,
			pct(0.5).Round(time.Microsecond), pct(0.99).Round(time.Microsecond), pct(1).Round(time.Microsecond))
		other.Range(func(k, _ any) bool {
			t.Errorf("%s: %v", c.work, k)
			return true
		})
		if sum != accounts*start {
			t.Errorf("%s %s: the balances sum to %d, want %d", c.work, mode, sum, accounts*start)
		}
	}
}

// transfer moves an amount from account a to account b in a transaction
// that reads both balances first, or writes a row of log before when
// writeFirst.
func transfer(db *sql.DB, a, b int, hold time.Duration, writeFirst bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if writeFirst {
		if _, err := tx.Exec("INSERT INTO log VALUES (?, ?)", a, b); err != nil {
			return err
		}
	}
	var x, y int
	if err := tx.QueryRow("SELECT balance FROM account WHERE id = ?", a).Scan(&x); err != nil {
		return err
	}
	if err := tx.QueryRow("SELECT balance FROM account WHERE id = ?", b).Scan(&y); err != nil {
		return err
	}
	if hold > 0 {
		time.Sleep(hold)
	}
	n := min(x, 5)
	if _, err := tx.Exec("UPDATE account SET balance = ? WHERE id = ?", x-n, a); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE account SET balance = balance + ? WHERE id = ?", n, b); err != nil {
		return err
	}
	return tx.Commit()
}

// isLocked says whether err is SQLITE_BUSY or SQLITE_LOCKED, in the words
// of any of the drivers.
func isLocked(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "locked") || strings.Contains(s, "busy")
}
