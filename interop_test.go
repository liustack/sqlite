// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package sqlite_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestInteropShell has the sqlite3 shell, SQLite built from C with the VFS
// of the system, write the same database as the driver at the same time,
// with the rollback journal (deleted, truncated or kept after each
// transaction) and with WAL: the driver writes for as long as the shell
// does. No write may be lost and the database must stay whole.
// Set SQLITE3 to the shell to run it.
func TestInteropShell(t *testing.T) {
	shell := os.Getenv("SQLITE3")
	if shell == "" {
		t.Skip("set SQLITE3 to the sqlite3 shell")
	}
	for _, mode := range []string{"delete", "truncate", "persist", "wal"} {
		t.Run(mode, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "interop.db")
			db, err := sql.Open("sqlite", "file:"+name+"?_pragma=busy_timeout(60000)&_pragma=journal_mode("+mode+")")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE t(src TEXT, n INTEGER)`); err != nil {
				t.Fatal(err)
			}

			// With the rollback journal a writer that writes without a break
			// keeps the other out, C or Go: the shell writes for a few
			// seconds only, less than busy_timeout, even where fsync is slow.
			rows, workers := 3000, 4
			if mode != "wal" {
				rows = 300
			}
			var script strings.Builder
			script.WriteString(".timeout 60000\n")
			for i := range rows {
				fmt.Fprintf(&script, "INSERT INTO t VALUES('shell', %d);\n", i)
				if i%50 == 0 {
					script.WriteString("SELECT count(*) FROM t;\n")
				}
			}
			var out bytes.Buffer
			cmd := exec.Command(shell, "-bail", name)
			cmd.Stdin = strings.NewReader(script.String())
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}

			shellDone := make(chan error)
			go func() { shellDone <- cmd.Wait() }()
			stop := make(chan struct{})
			var wg sync.WaitGroup
			// SQLite gives a waiting writer of another process no turn: its busy
			// handler sleeps and tries again, and finds the lock free only
			// between two writes. The workers queue for the file inside the
			// driver, so one takes the lock the moment another lets it go, and on
			// windows, where a commit takes milliseconds, the shell has waited
			// out its timeout on its first INSERT. So after each write all
			// workers wait three times as long as it took (1 to 20 ms), which
			// leaves the lock free most of the time while they still write
			// together.
			var pace struct {
				sync.Mutex
				next time.Time
			}
			written := make([]int, workers)
			errs := make(chan error, workers)
			for w := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						pace.Lock()
						next := pace.next
						pace.Unlock()
						select {
						case <-stop:
							return
						case <-time.After(time.Until(next)):
						}
						start := time.Now()
						if _, err := db.Exec(`INSERT INTO t VALUES('go', ?)`, written[w]); err != nil {
							errs <- err
							return
						}
						written[w]++
						now := time.Now()
						pace.Lock()
						// The time of a write includes its wait for the
						// shell, hence the upper bound.
						pause := min(max(3*now.Sub(start), time.Millisecond), 20*time.Millisecond)
						if n := now.Add(pause); n.After(pace.next) {
							pace.next = n
						}
						pace.Unlock()
					}
				}()
			}
			serr := <-shellDone
			close(stop)
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Error(err)
			}
			if serr != nil {
				t.Fatalf("shell: %v\n%s", serr, out.String())
			}
			goRows := 0
			for _, n := range written {
				goRows += n
			}
			t.Logf("the driver wrote %d rows while the shell wrote %d", goRows, rows)
			if goRows == 0 {
				t.Fatal("the driver wrote nothing while the shell wrote")
			}

			for src, want := range map[string]int{"go": goRows, "shell": rows} {
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM t WHERE src = ?`, src).Scan(&n); err != nil || n != want {
					t.Errorf("rows from %s = %d, %v, want %d", src, n, err, want)
				}
			}
			var ok string
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
				t.Errorf("integrity_check = %q, %v", ok, err)
			}
			got, err := exec.Command(shell, name, "SELECT count(*) FROM t; PRAGMA integrity_check;").CombinedOutput()
			if want := fmt.Sprintf("%d\nok\n", goRows+rows); err != nil || strings.ReplaceAll(string(got), "\r\n", "\n") != want {
				t.Errorf("shell reads %q, %v, want %q", got, err, want)
			}
		})
	}
}
