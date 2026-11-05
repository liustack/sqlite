// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The kill helper stages the changes of the database named in its
// environment while it writes to it, until it is killed.
const killEnv = "CAPTURE_TEST_KILL_DB"

// Writers 0 to 3 commit transactions of rowsPerTx rows, all with the same
// k, and delete those of k-50: every k of a writer has rowsPerTx rows or
// none. Writer 9 inserts one row at a time, merged by group commit. The
// spiller writes a transaction too large for the page cache, which spills
// to the WAL, and rolls it back.
const rowsPerTx = 5

func TestMain(m *testing.M) {
	if name := os.Getenv(killEnv); name != "" {
		os.Exit(killHelper(name))
	}
	os.Exit(m.Run())
}

func killHelper(name string) int {
	fail := func(err error) int {
		fmt.Println("error:", err)
		return 1
	}
	c, err := Open(name, Options{
		Interval:         5 * time.Millisecond,
		CheckpointFrames: 64,
		OnError: func(err error) {
			// Only this process writes: the WAL is always followed.
			if errors.Is(err, ErrFullCopy) {
				fmt.Println("error:", err)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "capture:", err)
		},
	})
	if err != nil {
		return fail(err)
	}
	db, err := sql.Open("sqlite", "file:"+name+"?_pragma=cache_size(-64)")
	if err != nil {
		return fail(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS t(w INTEGER, k INTEGER, pad BLOB);
		CREATE INDEX IF NOT EXISTS tk ON t(w, k);
		CREATE TABLE IF NOT EXISTS u(pad BLOB)`); err != nil {
		return fail(err)
	}
	fmt.Println("ready", c.FullCopies())
	errs := make(chan error)
	for w := range 4 {
		go func() {
			var k int
			db.QueryRow(`SELECT coalesce(max(k), 0) FROM t WHERE w = ?`, w).Scan(&k)
			for k++; ; k++ {
				tx, err := db.Begin()
				if err != nil {
					errs <- err
					return
				}
				for range rowsPerTx {
					if _, err := tx.Exec(`INSERT INTO t VALUES (?, ?, randomblob(300))`, w, k); err != nil {
						errs <- err
						return
					}
				}
				if _, err := tx.Exec(`DELETE FROM t WHERE w = ? AND k = ?`, w, k-50); err != nil {
					errs <- err
					return
				}
				if err := tx.Commit(); err != nil {
					errs <- err
					return
				}
				if w == 0 {
					fmt.Println("committed", k)
				}
			}
		}()
	}
	go func() {
		var k int
		db.QueryRow(`SELECT coalesce(max(k), 0) FROM t WHERE w = 9`).Scan(&k)
		for k++; ; k++ {
			if _, err := db.Exec(`INSERT INTO t VALUES (9, ?, randomblob(50))`, k); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		for {
			tx, err := db.Begin()
			if err != nil {
				errs <- err
				return
			}
			if _, err := tx.Exec(`WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM s WHERE i < 100) INSERT INTO u SELECT randomblob(4000) FROM s`); err != nil {
				errs <- err
				return
			}
			time.Sleep(time.Millisecond)
			tx.Rollback()
		}
	}()
	return fail(<-errs)
}

// TestKill kills a process that writes and stages, many times. After each
// kill the staged files rebuild a database at a commit: whole, with every
// transaction there entirely or not at all, never a transaction that did
// not commit, and with the commits made well before the kill. The next
// process goes on from the staged files without a full copy. At the end the
// staged files rebuild the database exactly.
func TestKill(t *testing.T) {
	if testing.Short() {
		t.Skip("kills processes")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "kill.db")
	rounds := 12
	for round := range rounds {
		cmd := exec.Command(exe, "-test.run=^$")
		cmd.Env = append(os.Environ(), killEnv+"="+name)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		lines := make(chan string)
		go func() {
			defer close(lines)
			r := bufio.NewScanner(stdout)
			for r.Scan() {
				lines <- r.Text()
			}
		}()
		if line := <-lines; line != "ready 0" {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("round %d: the helper said %q, want ready with no full copy", round, line)
		}
		// Kill at a random time after the first commit. Commits reported
		// more than safe before the kill must be staged.
		const safe = 300 * time.Millisecond
		killAt := time.Now().Add(time.Duration(50+rand.IntN(400)) * time.Millisecond)
		staged := 0
		var commits []int
		var times []time.Time
	read:
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					cmd.Wait()
					t.Fatalf("round %d: the helper stopped", round)
				}
				k, err := strconv.Atoi(strings.TrimPrefix(line, "committed "))
				if err != nil {
					cmd.Process.Kill()
					cmd.Wait()
					t.Fatalf("round %d: the helper said %q", round, line)
				}
				commits, times = append(commits, k), append(times, time.Now())
			case <-time.After(time.Until(killAt)):
				break read
			}
		}
		cmd.Process.Kill()
		killed := time.Now()
		cmd.Wait()
		for range lines {
		}
		for i, at := range times {
			if killed.Sub(at) > safe {
				staged = commits[i]
			}
		}

		out, txid := rebuild(t, stageDir(realPath(t, name)))
		k := check(t, out)
		if k < staged {
			t.Fatalf("round %d: the staged files hold writer 0 to k=%d, k=%d was committed %v before the kill", round, k, staged, safe)
		}
		t.Logf("round %d: TXID %s, writer 0 at k=%d, %d commits reported", round, txid, k, len(commits))
	}
	c := open(t, name, testOptions(t))
	if c.FullCopies() != 0 {
		t.Fatalf("%d full copies at the end", c.FullCopies())
	}
	step(t, c)
	sameAs(t, c, name)
	check(t, name)
}

// realPath is name as SQLite names it, its folder's symbolic links
// resolved.
func realPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, filepath.Base(name))
}

// check opens the database name and checks that it is whole, holds every
// transaction entirely or not at all, and nothing of the transactions
// rolled back. It returns the last k of writer 0.
func check(t *testing.T, name string) int {
	t.Helper()
	db, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check: %q %v", ok, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM u`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows of transactions rolled back (%v)", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM (SELECT w, k FROM t WHERE w < 9 GROUP BY w, k HAVING count(*) != ?)`, rowsPerTx).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d transactions not whole (%v)", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM (SELECT k FROM t WHERE w = 9 GROUP BY k HAVING count(*) != 1)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d single-row commits not whole (%v)", n, err)
	}
	var k int
	if err := db.QueryRow(`SELECT coalesce(max(k), 0) FROM t WHERE w = 0`).Scan(&k); err != nil {
		t.Fatal(err)
	}
	return k
}
