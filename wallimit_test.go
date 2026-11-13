// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"
)

// walLock is the write lock of the file db opens.
func walLock(t *testing.T, db *sql.DB) *writeLock {
	t.Helper()
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	var l *writeLock
	sc.Raw(func(dc any) error { l = dc.(*conn).wl; return nil })
	return l
}

// walTable makes a table of 2000 rows of 1000 bytes and empties the WAL.
func walTable(t *testing.T, db *sql.DB) {
	t.Helper()
	mExec(t, db, `CREATE TABLE t(k INTEGER PRIMARY KEY, v BLOB)`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for k := range 2000 {
		mExec(t, tx, `INSERT INTO t VALUES (?, randomblob(1000))`, k)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	mExec(t, db, `PRAGMA wal_checkpoint(TRUNCATE)`)
}

// walReader is a read transaction that has read the database, and so holds
// its snapshot until it ends.
func walReader(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 2000 {
		tx.Rollback()
		t.Fatalf("%d rows, %v", n, err)
	}
	return tx
}

// While readers overlap, SQLite never finds the WAL free of readers and
// cannot start it over: it grows as long as that lasts. The driver starts it
// over once it is longer than _wal_limit, with a checkpoint that waits for
// the readers.
func TestWALLimit(t *testing.T) {
	const limit = 4 << 20
	path := mPath(t)
	db := mOpen(t, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_wal_limit=4194304")
	walTable(t, db)
	l := walLock(t, db)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				rows, err := db.Query(`SELECT k, v FROM t`)
				if err != nil {
					t.Error(err)
					return
				}
				for rows.Next() {
				}
				rows.Close()
			}
		})
	}
	wg.Go(func() {
		for k := 0; ; k++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := db.Exec(`UPDATE t SET v = randomblob(1000) WHERE k = ?`, k%2000); err != nil {
				t.Error(err)
				return
			}
		}
	})
	var most int64
	for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if st, err := os.Stat(path + "-wal"); err == nil {
			most = max(most, st.Size())
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("the WAL grew to %d KiB, started over %d times in %d tries", most>>10, l.walRestarts.Load(), l.walTries.Load())
	if l.walRestarts.Load() == 0 {
		t.Error("the WAL was never started over")
	}
	// Each try that readers hold off lets it grow a quarter of the limit.
	if most > 8*limit {
		t.Errorf("the WAL grew to %d KiB, the limit is %d KiB", most>>10, limit>>10)
	}
}

// A reader that never ends keeps the WAL from starting over. The writers do
// not wait for it at each commit: after a try that fails, the next comes
// once the WAL has grown by a quarter of the limit. When the reader ends,
// the WAL starts over.
func TestWALLimitBacksOff(t *testing.T) {
	const limit = 1 << 20
	path := mPath(t)
	db := mOpen(t, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(1234)&_wal_limit=1048576")
	// One connection for the reader and one for the writes.
	db.SetMaxOpenConns(2)
	walTable(t, db)
	l := walLock(t, db)
	// The wait for readers is not the busy timeout of the connection, which
	// is as it was after a try.
	timeout := func() {
		t.Helper()
		var ms int
		if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil || ms != 1234 {
			t.Fatalf("busy_timeout is %d after a try, want 1234 (%v)", ms, err)
		}
	}
	// A row to change first, so that the reader's snapshot is of a WAL that
	// is not empty: a reader of the database file alone holds nothing back.
	mExec(t, db, `UPDATE t SET v = randomblob(1000) WHERE k = 0`)
	reader := walReader(t, db)
	defer reader.Rollback()
	write := func(n int) {
		t.Helper()
		for k := range n {
			mExec(t, db, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k%2000)
		}
	}
	// Making the table took the WAL past the limit once.
	tries0, restarts0 := l.walTries.Load(), l.walRestarts.Load()
	// 2 MiB of frames: tries at 1 MiB and then each quarter more.
	start := time.Now()
	write(2 * limit / 4120)
	tries, restarts := l.walTries.Load()-tries0, l.walRestarts.Load()-restarts0
	if tries < 3 || tries > 7 || restarts != 0 {
		t.Fatalf("%d tries and %d restarts while a reader holds the WAL, want 3 to 7 tries and no restart", tries, restarts)
	}
	// The writers waited at those tries, and not at each commit.
	if d := time.Since(start); d > time.Duration(tries)*walWait+5*time.Second {
		t.Fatalf("the writes took %v with %d tries", d, tries)
	}
	timeout()
	reader.Rollback()
	write(limit/4/4120 + 1)
	if n := l.walRestarts.Load() - restarts0; n != 1 {
		t.Fatalf("%d restarts once the reader ended, want 1", n)
	}
	timeout()
	st, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	write(100)
	if st2, err := os.Stat(path + "-wal"); err != nil || st2.Size() != st.Size() {
		t.Fatalf("the WAL is %d bytes after %d, %v: it did not start over", st2.Size(), st.Size(), err)
	}
	// The WAL started over: the next tries come at the limit again, and not
	// where the last try that failed put them.
	reader = walReader(t, db)
	defer reader.Rollback()
	tries0 = l.walTries.Load()
	write(limit * 3 / 2 / 4120)
	if tries := l.walTries.Load() - tries0; tries < 1 || tries > 4 {
		t.Fatalf("%d tries in the half past the limit, want 1 to 4", tries)
	}
}

// _wal_limit=0 turns the limit off, and a value that is no size is refused.
func TestWALLimitParameter(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_wal_limit=0")
	walTable(t, db)
	reader := walReader(t, db)
	defer reader.Rollback()
	for k := range 600 {
		mExec(t, db, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k)
	}
	if l := walLock(t, db); l.walTries.Load() != 0 {
		t.Fatalf("%d tries with the limit off", l.walTries.Load())
	}
	for _, v := range []string{"-1", "x", "1.5", ""} {
		if _, err := NewConnector("file:x.db?_wal_limit=" + v); err == nil {
			t.Errorf("_wal_limit=%q is taken", v)
		}
	}
}

// The replicator of a file runs its checkpoints, after it has read the
// frames: the driver does not start the WAL of a replicated file over.
func TestWALLimitReplicated(t *testing.T) {
	const limit = 1 << 20
	path := mPath(t)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	detach, err := attach(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(detach) // after the database closes
	db := mOpen(t, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_wal_limit=1048576")
	mExec(t, db, `CREATE TABLE t(k INTEGER PRIMARY KEY, v BLOB)`)
	for k := range 3 * limit / 4120 {
		mExec(t, db, `INSERT INTO t VALUES (?, randomblob(4000))`, k)
	}
	if l := walLock(t, db); l.walTries.Load() != 0 {
		t.Fatalf("%d tries to start the WAL of a replicated file over", l.walTries.Load())
	}
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() < 2*limit {
		t.Fatalf("the WAL is %d bytes, %v: something ran a checkpoint", st.Size(), err)
	}
}
