// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liustack/sqlite/internal/vfs"
)

// loseOnFailedSync fails the n-th sync of the file whose name ends with
// suffix, and then has the file read as the disk holds it once the kernel
// dropped the pages the failed sync could not write (Linux marks them clean
// and may evict them): every byte written since the last good sync reads
// back as zero.
func loseOnFailedSync(t *testing.T, suffix string, n int) (failed func() bool) {
	var (
		mu     sync.Mutex
		seen   int
		synced []byte
		done   bool
	)
	vfs.SetFault(func(path string, op vfs.Op) int {
		if op != vfs.OpSync || !strings.HasSuffix(path, suffix) {
			return vfs.OK
		}
		mu.Lock()
		defer mu.Unlock()
		cur, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			return vfs.OK
		}
		if seen++; seen != n {
			synced = cur
			return vfs.OK
		}
		lost := make([]byte, len(cur))
		copy(lost, synced)
		if err := os.WriteFile(path, lost, 0o644); err != nil {
			t.Errorf("write %s: %v", path, err)
		}
		done = true
		return vfs.IOErrFsync
	})
	t.Cleanup(func() { vfs.SetFault(nil) })
	return func() bool { mu.Lock(); defer mu.Unlock(); return done }
}

// A sync that fails while the kernel drops the pages it could not write
// leaves the database sound, now and when opened again. A write told
// committed is kept, except that under synchronous=NORMAL the writes since
// the last good sync of the WAL may be lost, as on a power loss. Once the
// pool has dropped the connections that saw the failure, writes go on.
func TestSyncLosesPages(t *testing.T) {
	for _, tc := range []struct {
		sync, suffix string
		n            int
	}{
		{"NORMAL", "-wal", 2}, // the checkpoint's sync of the WAL
		{"NORMAL", ".db", 1},  // the checkpoint's sync of the database
		{"FULL", "-wal", 50},  // a commit
		{"FULL", ".db", 1},
	} {
		t.Run(tc.sync+tc.suffix, func(t *testing.T) { syncLosesPages(t, tc.sync, tc.suffix, tc.n) })
	}
}

func syncLosesPages(t *testing.T, sync, suffix string, nth int) {
	path := mPath(t)
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(" + sync + ")&_pragma=wal_autocheckpoint(0)"
	db := mOpen(t, dsn)
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(k INTEGER PRIMARY KEY, v TEXT)`)
	mExec(t, db, `PRAGMA wal_checkpoint(TRUNCATE)`)
	failed := loseOnFailedSync(t, suffix, nth)

	value := func(k int) string { return strings.Repeat(fmt.Sprint(k), 50) }
	before, after := map[int]bool{}, map[int]bool{}
	for k := range 200 {
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?)`, k, value(k)); err == nil {
			before[k] = true
		}
	}
	db.Exec(`PRAGMA wal_checkpoint`)
	if !failed() {
		t.Fatal("no sync failed")
	}
	for k := 200; k < 300; k++ {
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?)`, k, value(k)); err != nil {
			t.Fatalf("insert %d after the failure: %v", k, err)
		}
		after[k] = true
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}

	check := func(db *sql.DB, when string) {
		t.Helper()
		var ic string
		if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
			t.Fatalf("%s: integrity_check %q %v", when, ic, err)
		}
		rows, err := db.Query(`SELECT k, v FROM t`)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		have := map[int]bool{}
		for rows.Next() {
			var k int
			var v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatalf("%s: %v", when, err)
			}
			if v != value(k) {
				t.Errorf("%s: row %d has a wrong value", when, k)
			}
			if !before[k] && !after[k] {
				t.Errorf("%s: row %d was told failed", when, k)
			}
			have[k] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		lost := 0
		for k := range before {
			if !have[k] {
				lost++
			}
		}
		for k := range after {
			if !have[k] {
				t.Fatalf("%s: row %d written after the failure is missing", when, k)
			}
		}
		t.Logf("%s: %d of %d rows told committed before the failure lost", when, lost, len(before))
		if lost > 0 && sync == "FULL" {
			t.Errorf("%s: %d rows told committed lost under synchronous=FULL", when, lost)
		}
	}
	check(db, "after the failure")
	db.Close()
	check(mOpen(t, dsn), "opened again")
}

// After a WAL stops, database/sql drops every connection, idle ones too,
// and the next connections open it again. database/sql takes the idle
// connection used last first, so a new connection that is kept would hide
// the old ones below it, and the WAL would never open again.
func TestStoppedWALPoolRecovers(t *testing.T) {
	dsn := "file:" + mPath(t) + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(0)"
	db := mOpen(t, dsn)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	mExec(t, db, `CREATE TABLE t(x)`)
	mExec(t, db, `PRAGMA wal_checkpoint(TRUNCATE)`)
	ctx := context.Background()
	var conns []*sql.Conn
	for range 8 {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		c.Close()
	}
	failed := loseOnFailedSync(t, "-wal", 1)
	if _, err := db.Exec(`PRAGMA wal_checkpoint`); err == nil || !failed() {
		t.Fatalf("checkpoint: %v, sync failed %v", err, failed())
	}
	tries := 0
	for {
		tries++
		_, err := db.Exec(`INSERT INTO t VALUES (2)`)
		if err == nil {
			break
		}
		if tries == 20 {
			t.Fatalf("still failing after %d writes: %v", tries, err)
		}
	}
	t.Logf("%d writes before one succeeded", tries)
	if vfs.WALsStopped() {
		t.Fatal("the WAL is still stopped")
	}
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check %q %v", ic, err)
	}
}

// A stopped WAL drops the connections of its database only.
func TestStoppedWALOtherDatabase(t *testing.T) {
	dsn := "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(0)"
	db := mOpen(t, "file:"+mPath(t)+dsn)
	cn, err := NewConnector("file:" + filepath.Join(t.TempDir(), "other.db") + dsn)
	if err != nil {
		t.Fatal(err)
	}
	counted := &countConnects{Connector: cn}
	other := sql.OpenDB(counted)
	t.Cleanup(func() { other.Close() })
	mExec(t, db, `CREATE TABLE t(x)`)
	mExec(t, other, `CREATE TABLE t(x)`)
	mExec(t, db, `INSERT INTO t VALUES (1)`)
	// A connection the program keeps holds the WAL stopped.
	kept, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer kept.Close()
	if _, err := kept.ExecContext(context.Background(), `SELECT * FROM t`); err != nil {
		t.Fatal(err)
	}
	failed := loseOnFailedSync(t, "m.db-wal", 1)
	if _, err := db.Exec(`PRAGMA wal_checkpoint`); err == nil || !failed() {
		t.Fatalf("checkpoint: %v, sync failed %v", err, failed())
	}
	if !vfs.WALsStopped() {
		t.Fatal("the WAL is not stopped")
	}
	for range 10 {
		mExec(t, other, `INSERT INTO t VALUES (1)`)
	}
	if n := counted.n.Load(); n != 1 {
		t.Fatalf("the other database opened %d connections", n)
	}
}

// countConnects counts the connections a Connector opens.
type countConnects struct {
	*Connector
	n atomic.Int64
}

func (c *countConnects) Connect(ctx context.Context) (driver.Conn, error) {
	c.n.Add(1)
	return c.Connector.Connect(ctx)
}

// While another process has the -shm open, a stopped WAL does not open
// again, and the database stays sound: the WAL index there still counts
// the frames cut. Once that process is gone, writes go on.
func TestStoppedWALOtherProcess(t *testing.T) {
	path := mPath(t)
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(0)"
	db := mOpen(t, dsn)
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(k INTEGER PRIMARY KEY, v BLOB); CREATE TABLE u(k INTEGER PRIMARY KEY, v BLOB)`)
	mExec(t, db, `PRAGMA wal_checkpoint(TRUNCATE)`)
	// In frames that the failure cuts, only the page of t: the file does
	// not grow.
	for k := range 5 {
		mExec(t, db, `INSERT INTO t VALUES (?, randomblob(10))`, k)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "SQLITE_TEST_READER_DB="+dsn)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer in.Close()
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "ready\n" {
		t.Fatalf("the reader says %q", line)
	}

	failed := loseOnFailedSync(t, "-wal", 1)
	if _, err := db.Exec(`PRAGMA wal_checkpoint`); err == nil || !failed() {
		t.Fatalf("checkpoint: %v, sync failed %v", err, failed())
	}
	// u is all in the database file: a writer that trusted the index of
	// the -shm would write past the frames cut.
	for k := 100; k < 110; k++ {
		if _, err := db.Exec(`INSERT INTO u VALUES (?, randomblob(10))`, k); err == nil {
			t.Fatalf("insert %d went on while the other process has the -shm open", k)
		}
	}
	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	tries := 0
	for k := 200; ; k++ {
		tries++
		if _, err := db.Exec(`INSERT INTO u VALUES (?, randomblob(10))`, k); err == nil {
			break
		} else if tries == 20 {
			t.Fatalf("writes do not go on: %v", err)
		}
	}
	mExec(t, db, `PRAGMA wal_checkpoint(TRUNCATE)`)
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check %q %v", ic, err)
	}
}
