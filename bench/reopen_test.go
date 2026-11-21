// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package bench

import (
	"database/sql"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestReopenAfterCrash has 64 goroutines insert for REOPEN, in a process of
// its own that then ends without closing the database, as a crash would.
// It then opens the database again and times its first read, which waits
// for SQLite to read the WAL back. It skips unless REOPEN is set:
//
//	REOPEN=30s go test -tags mattn -run '^TestReopenAfterCrash$' -v
//
// With REOPEN_COLD=1 on linux it empties the page cache first (sudo), as a
// restart of the machine would.
func TestReopenAfterCrash(t *testing.T) {
	if os.Getenv("REOPEN_CHILD") != "" {
		reopenGrow(t)
		return
	}
	run, err := time.ParseDuration(os.Getenv("REOPEN"))
	if err != nil {
		t.Skip("set REOPEN to how long the inserts run, as 30s")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.db")
	cmd := osexec.Command(os.Args[0], "-test.run", "^TestReopenAfterCrash$")
	cmd.Env = append(os.Environ(), "REOPEN_CHILD="+path, "REOPEN_RUN="+run.String())
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the process that inserts: %v", err)
	}
	var res reopenResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("the process that inserts said %q: %v", out, err)
	}
	if os.Getenv("REOPEN_COLD") != "" {
		if runtime.GOOS != "linux" {
			t.Skip("REOPEN_COLD works on linux only")
		}
		drop := osexec.Command("sudo", "-n", "sh", "-c", "sync && echo 3 > /proc/sys/vm/drop_caches")
		if out, err := drop.CombinedOutput(); err != nil {
			t.Fatalf("emptying the page cache: %v: %s", err, out)
		}
		res.Cold = true
	}
	t0 := time.Now()
	db, err := sql.Open(DriverName, DSN(path, "NORMAL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var g int
	if err := db.QueryRow("SELECT g FROM log WHERE rowid = 1").Scan(&g); err != nil {
		t.Fatal(err)
	}
	res.FirstRead = time.Since(t0).Nanoseconds()
	var n int64
	if err := db.QueryRow("SELECT count(*) FROM log").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != res.Rows {
		t.Errorf("%d rows after the crash, want the %d committed", n, res.Rows)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reopen-result %s", b)
}

// A reopenResult is one run of TestReopenAfterCrash: the rows committed, the
// size of the WAL as the inserts stopped and after one more insert 2 s
// later, and how long the first read after the crash took, in nanoseconds.
type reopenResult struct {
	Driver    string `json:"driver"`
	Rows      int64  `json:"rows"`
	WAL       int64  `json:"wal_bytes"`
	WALQuiet  int64  `json:"wal_bytes_quiet"`
	Database  int64  `json:"db_bytes"`
	FirstRead int64  `json:"first_read_ns"`
	Cold      bool   `json:"cold"`
}

// reopenGrow is the process that inserts. It writes its reopenResult to
// stdout and ends without closing the database.
func reopenGrow(t *testing.T) {
	path := os.Getenv("REOPEN_CHILD")
	run, err := time.ParseDuration(os.Getenv("REOPEN_RUN"))
	if err != nil {
		t.Fatal(err)
	}
	size := func(name string) int64 {
		st, err := os.Stat(name)
		if err != nil {
			return 0
		}
		return st.Size()
	}
	db, err := sql.Open(DriverName, DSN(path, "NORMAL"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(64)
	db.SetMaxIdleConns(64)
	if _, err := db.Exec("CREATE TABLE log(g INTEGER, at INTEGER, pad BLOB)"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(run)
	var wg sync.WaitGroup
	for g := range 64 {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				db.Exec("INSERT INTO log VALUES (?, ?, randomblob(100))", g, time.Now().UnixNano())
			}
		})
	}
	wg.Wait()
	res := reopenResult{Driver: DriverTag, WAL: size(path + "-wal")}
	time.Sleep(2 * time.Second)
	if _, err := db.Exec("INSERT INTO log VALUES (-1, 0, NULL)"); err != nil {
		t.Fatal(err)
	}
	res.WALQuiet = size(path + "-wal")
	if err := db.QueryRow("SELECT count(*) FROM log").Scan(&res.Rows); err != nil {
		t.Fatal(err)
	}
	res.Database = size(path)
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.Write(b)
	os.Exit(0) // no Close: as a crash
}
