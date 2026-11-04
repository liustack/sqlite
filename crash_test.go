// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package sqlite_test

import (
	"bufio"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The crash helper writes transactions of crashBatch rows to the database
// named in its environment until it is killed.
const (
	crashEnv   = "SQLITE_TEST_CRASH_DB"
	crashBatch = 100
)

func TestMain(m *testing.M) {
	if name := os.Getenv(crashEnv); name != "" {
		os.Exit(crashHelper(name))
	}
	os.Exit(m.Run())
}

func crashHelper(dsn string) int {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS t(n INTEGER, pad BLOB)`); err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println("ready")
	pad := make([]byte, 500)
	for i := 0; ; i++ {
		tx, err := db.Begin()
		if err != nil {
			fmt.Println(err)
			return 1
		}
		for j := range crashBatch {
			if _, err := tx.Exec(`INSERT INTO t VALUES (?, ?)`, i*crashBatch+j, pad); err != nil {
				fmt.Println(err)
				return 1
			}
		}
		if err := tx.Commit(); err != nil {
			fmt.Println(err)
			return 1
		}
	}
}

// TestCrash kills a process in the middle of its writes, many times, with
// the rollback journal and with WAL. Each time the database must open
// whole, with every transaction there entirely or not at all.
func TestCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("kills processes")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"delete", "wal"} {
		t.Run(mode, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "crash.db")
			dsn := "file:" + name + "?_pragma=journal_mode(" + mode + ")"
			last := 0
			for round := range 8 {
				cmd := exec.Command(exe, "-test.run=^$")
				cmd.Env = append(os.Environ(), crashEnv+"="+dsn)
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				line, err := bufio.NewReader(stdout).ReadString('\n')
				if err != nil || line != "ready\n" {
					cmd.Process.Kill()
					cmd.Wait()
					t.Fatalf("helper said %q, %v", line, err)
				}
				time.Sleep(time.Duration(20+rand.IntN(80)) * time.Millisecond)
				cmd.Process.Kill()
				cmd.Wait()

				db, err := sql.Open("sqlite", dsn)
				if err != nil {
					t.Fatal(err)
				}
				var ok string
				if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
					t.Fatalf("round %d: integrity_check = %q, %v", round, ok, err)
				}
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				db.Close()
				if n%crashBatch != 0 || n < last {
					t.Fatalf("round %d: %d rows after %d, want whole transactions of %d", round, n, last, crashBatch)
				}
				t.Logf("round %d: %d rows", round, n)
				last = n
			}
			if last == 0 {
				t.Error("no transaction was ever committed")
			}
		})
	}
}
