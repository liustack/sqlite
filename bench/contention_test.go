// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package bench

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
// With CONTENTION_DIR set, every write of each case goes to
// CONTENTION_DIR/<driver>-<case>.csv.gz.
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
		key := fmt.Sprintf("%s-%s-%s-g%d-hold%v", c.work, mode(c.work, c.txlock), c.sync, c.g, c.hold)
		var other sync.Map
		var mu sync.Mutex
		var samples []sample
		begin := time.Now()
		deadline := begin.Add(run)
		var wg sync.WaitGroup
		for g := range c.g {
			wg.Go(func() {
				r := rand.New(rand.NewPCG(uint64(g), 1))
				var mine []sample
				for time.Now().Before(deadline) {
					t0 := time.Now()
					var err error
					if c.work == "insert" {
						_, err = db.Exec("INSERT INTO log VALUES (?, ?)", g, time.Now().UnixNano())
					} else {
						err = transfer(db, 1+r.IntN(accounts), 1+r.IntN(accounts), c.hold, c.work == "writefirst")
					}
					s := sample{at: t0.Sub(begin), wait: time.Since(t0)}
					switch {
					case err == nil:
					case isLocked(err):
						s.locked = true
					default:
						other.Store(err.Error(), true)
						continue
					}
					mine = append(mine, s)
				}
				mu.Lock()
				samples = append(samples, mine...)
				mu.Unlock()
			})
		}
		wg.Wait()
		var sum int
		if err := db.QueryRow("SELECT sum(balance) FROM account").Scan(&sum); err != nil {
			t.Fatal(err)
		}
		db.Close()
		res := summarize(key, run, samples)
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %s", resultPrefix, b)
		if dir := os.Getenv("CONTENTION_DIR"); dir != "" {
			if err := writeSamples(filepath.Join(dir, DriverTag+"-"+key+".csv.gz"), samples); err != nil {
				t.Fatal(err)
			}
		}
		other.Range(func(k, _ any) bool {
			t.Errorf("%s: %v", key, k)
			return true
		})
		if sum != accounts*start {
			t.Errorf("%s: the balances sum to %d, want %d", key, sum, accounts*start)
		}
	}
}

// resultPrefix starts the line of each result, which benchrun contention
// reads.
const resultPrefix = "contention-result"

// A sample is one write: when it started, after the start of the case, how
// long it took, and whether it failed because the database was locked.
type sample struct {
	at, wait time.Duration
	locked   bool
}

// A contentionResult is one case on one driver. The waits are of the writes
// that succeeded, in nanoseconds.
type contentionResult struct {
	Case    string  `json:"case"`
	Driver  string  `json:"driver"`
	Seconds float64 `json:"seconds"`
	Done    int     `json:"done"`
	Locked  int     `json:"locked"`
	P50     int64   `json:"p50_ns"`
	P99     int64   `json:"p99_ns"`
	Max     int64   `json:"max_ns"`
}

func summarize(key string, run time.Duration, samples []sample) contentionResult {
	res := contentionResult{Case: key, Driver: DriverTag, Seconds: run.Seconds()}
	var waits []time.Duration
	for _, s := range samples {
		if s.locked {
			res.Locked++
			continue
		}
		waits = append(waits, s.wait)
	}
	res.Done = len(waits)
	slices.Sort(waits)
	pct := func(p float64) int64 {
		if len(waits) == 0 {
			return 0
		}
		return int64(waits[min(len(waits)-1, int(p*float64(len(waits))))])
	}
	res.P50, res.P99, res.Max = pct(0.5), pct(0.99), pct(1)
	return res
}

// writeSamples writes every write of a case, in the order they started:
// start_ms,wait_ms,locked.
func writeSamples(path string, samples []sample) error {
	slices.SortFunc(samples, func(a, b sample) int { return cmp.Compare(a.at, b.at) })
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	w := bufio.NewWriter(z)
	fmt.Fprintln(w, "start_ms,wait_ms,locked")
	for _, s := range samples {
		fmt.Fprintf(w, "%.3f,%.3f,%d\n", float64(s.at)/1e6, float64(s.wait)/1e6, map[bool]int{false: 0, true: 1}[s.locked])
	}
	return errors.Join(w.Flush(), z.Close(), f.Close())
}

// mode is how a case writes: autocommit, or how its transactions begin.
func mode(work, txlock string) string {
	switch {
	case work == "insert":
		return "autocommit"
	case txlock != "":
		return txlock
	}
	return "deferred"
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
