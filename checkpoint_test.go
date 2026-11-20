// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && (amd64 || arm64))

package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liustack/sqlite/internal/vfs"
)

// countSyncs counts the syncs of the file whose name ends with suffix, for
// the rest of the test.
func countSyncs(t *testing.T, suffix string) *atomic.Int64 {
	var n atomic.Int64
	vfs.SetFault(func(path string, op vfs.Op) int {
		if op == vfs.OpSync && strings.HasSuffix(path, suffix) {
			n.Add(1)
		}
		return vfs.OK
	})
	t.Cleanup(func() { vfs.SetFault(nil) })
	return &n
}

// writeBesideReaders writes n rows of walTable's table through w, one commit
// each, while a read transaction is always open on the WAL: each starts
// before the one before it ends. So the WAL never starts over, and each
// commit finds a few frames more that a checkpoint may copy.
func writeBesideReaders(t *testing.T, db *sql.DB, w execer, n int) {
	t.Helper()
	reader := walReader(t, db)
	for k := range n {
		mExec(t, w, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k%2000)
		next := walReader(t, db)
		reader.Rollback()
		reader = next
	}
	reader.Rollback()
}

// SQLite runs its automatic checkpoint after every commit that leaves the WAL
// at wal_autocheckpoint frames or more. While reads overlap the WAL stays
// longer than that, so every commit runs one, and each syncs the WAL for the
// few frames it copies. The driver runs them: one each time the WAL has grown
// by that many frames.
func TestCheckpointEvery(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(50)")
	db.SetMaxOpenConns(3)
	walTable(t, db)
	l := walLock(t, db)
	syncs := countSyncs(t, "m.db-wal")
	ran0 := l.walCheckpoints.Load()
	const writes = 600
	writeBesideReaders(t, db, db, writes)
	// About two frames a write: 24 checkpoints.
	if ran := l.walCheckpoints.Load() - ran0; ran < 12 || ran > 40 {
		t.Errorf("%d checkpoints in %d writes beside readers, want about 24", ran, writes)
	}
	if n := syncs.Load(); n < 5 || n > 60 {
		t.Errorf("the WAL was synced %d times in %d writes beside readers, want about 24", n, writes)
	}
	if n := mCount(t, db, `PRAGMA wal_autocheckpoint`); n != 50 {
		t.Errorf("PRAGMA wal_autocheckpoint reads %d, want 50", n)
	}
}

// With no reader the WAL starts over after each checkpoint, as with SQLite's
// own: it stays about wal_autocheckpoint frames long.
func TestCheckpointEveryAlone(t *testing.T) {
	path := mPath(t)
	db := mOpen(t, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(100)")
	walTable(t, db)
	l := walLock(t, db)
	ran0 := l.walCheckpoints.Load()
	var most int64
	for k := range 2000 {
		mExec(t, db, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k)
		if st, err := os.Stat(path + "-wal"); err == nil {
			most = max(most, st.Size())
		}
	}
	// A write is one or two frames of 4120 bytes.
	if most > 110*4120 {
		t.Errorf("the WAL grew to %d bytes, want at most about 100 frames", most)
	}
	if ran := l.walCheckpoints.Load() - ran0; ran < 15 || ran > 45 {
		t.Errorf("%d checkpoints in 2000 writes, want about 30", ran)
	}
}

// wal_autocheckpoint=0 in the DSN: no automatic checkpoint, by SQLite or by
// the driver.
func TestCheckpointEveryOff(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(0)&_wal_limit=0")
	walTable(t, db)
	l := walLock(t, db)
	syncs := countSyncs(t, "m.db-wal")
	ran0 := l.walCheckpoints.Load()
	for k := range 1500 {
		mExec(t, db, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k)
	}
	// The one sync is of the header of the WAL, as it starts.
	if ran, n := l.walCheckpoints.Load()-ran0, syncs.Load(); ran != 0 || n > 1 {
		t.Fatalf("%d checkpoints and %d syncs of the WAL with wal_autocheckpoint=0", ran, n)
	}
}

// PRAGMA wal_autocheckpoint=N on a connection sets what the driver does for
// that connection, as it sets what SQLite does: one checkpoint each N
// frames. 0 turns them off.
func TestCheckpointEveryPragma(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	db.SetMaxOpenConns(3)
	walTable(t, db)
	l := walLock(t, db)
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mExec(t, c, `PRAGMA wal_autocheckpoint=40`)
	syncs := countSyncs(t, "m.db-wal")
	ran0 := l.walCheckpoints.Load()
	writeBesideReaders(t, db, c, 400)
	// About two frames a write: 20 checkpoints.
	if ran, n := l.walCheckpoints.Load()-ran0, syncs.Load(); ran < 10 || ran > 35 || n > 50 {
		t.Errorf("%d checkpoints and %d syncs of the WAL in 400 writes with wal_autocheckpoint=40, want about 20", ran, n)
	}
	mExec(t, c, `PRAGMA wal_autocheckpoint=0`)
	ran0 = l.walCheckpoints.Load()
	syncs.Store(0)
	writeBesideReaders(t, db, c, 200)
	if ran, n := l.walCheckpoints.Load()-ran0, syncs.Load(); ran != 0 || n != 0 {
		t.Errorf("%d checkpoints and %d syncs of the WAL after PRAGMA wal_autocheckpoint=0", ran, n)
	}
	// SQLite sets it as the statement is prepared, run or not.
	s, err := c.PrepareContext(context.Background(), `PRAGMA wal_autocheckpoint=30`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	ran0 = l.walCheckpoints.Load()
	syncs.Store(0)
	writeBesideReaders(t, db, c, 300)
	// About two frames a write: 20 checkpoints.
	if ran, n := l.walCheckpoints.Load()-ran0, syncs.Load(); ran < 10 || ran > 35 || n > 50 {
		t.Errorf("%d checkpoints and %d syncs of the WAL in 300 writes with wal_autocheckpoint=30 prepared, want about 20", ran, n)
	}
}

// A connection with an attached database gets SQLite's own automatic
// checkpoints back, which cover every database of the connection: the WAL of
// the attached one stays short. Once it is detached, the driver runs them
// again.
func TestCheckpointEveryAttached(t *testing.T) {
	path := mPath(t)
	other := filepath.Join(filepath.Dir(path), "other.db")
	db := mOpen(t, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(100)")
	db.SetMaxOpenConns(3)
	walTable(t, db)
	l := walLock(t, db)
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	mExec(t, c, `ATTACH DATABASE ? AS o`, other)
	var mode string
	if err := c.QueryRowContext(context.Background(), `PRAGMA o.journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode of the attached database: %q, %v", mode, err)
	}
	mExec(t, c, `CREATE TABLE o.u(k INTEGER PRIMARY KEY, v BLOB)`)
	if n := queryInt(t, c, `PRAGMA wal_autocheckpoint`); n != 100 {
		t.Errorf("PRAGMA wal_autocheckpoint reads %d with a database attached, want 100", n)
	}
	var most int64
	for k := range 1000 {
		mExec(t, c, `INSERT INTO o.u VALUES (?, randomblob(3000))`, k)
		if st, err := os.Stat(other + "-wal"); err == nil {
			most = max(most, st.Size())
		}
	}
	if most > 110*4120 {
		t.Errorf("the WAL of the attached database grew to %d bytes, want at most about 100 frames", most)
	}
	// SQLite checkpoints the main database of that connection too, and the
	// driver does not.
	ran0 := l.walCheckpoints.Load()
	writeBesideReaders(t, db, c, 300)
	if ran := l.walCheckpoints.Load() - ran0; ran != 0 {
		t.Errorf("the driver ran %d checkpoints for a connection with a database attached", ran)
	}
	// What the program sets meanwhile is SQLite's, and the driver's after
	// the detach.
	mExec(t, c, `PRAGMA wal_autocheckpoint=60`)
	mExec(t, c, `DETACH DATABASE o`)
	if n := queryInt(t, c, `PRAGMA wal_autocheckpoint`); n != 60 {
		t.Errorf("PRAGMA wal_autocheckpoint reads %d after the detach, want 60", n)
	}
	ran0 = l.walCheckpoints.Load()
	writeBesideReaders(t, db, c, 300)
	if ran := l.walCheckpoints.Load() - ran0; ran < 5 || ran > 18 {
		t.Errorf("%d checkpoints in 300 writes after the detach, want about 10", ran)
	}
}

func queryInt(t *testing.T, c *sql.Conn, q string) int {
	t.Helper()
	var n int
	if err := c.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// A connection that writes only its temporary tables takes the file's write
// lock too, and does not look at the WAL again. What it knows of the WAL is
// old, and must not make the others run a checkpoint at each commit.
func TestCheckpointEveryTempWriter(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(50)")
	db.SetMaxOpenConns(4)
	walTable(t, db)
	l := walLock(t, db)
	ctx := context.Background()
	temp, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer temp.Close()
	mExec(t, temp, `CREATE TEMP TABLE tt(x)`)
	w, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// It reads the WAL once, of a few frames: what it knows of it from then
	// on is a short WAL, not none.
	mExec(t, w, `UPDATE t SET v = randomblob(1000) WHERE k = 0`)
	if n := queryInt(t, temp, `SELECT count(*) FROM t`); n != 2000 {
		t.Fatalf("%d rows", n)
	}
	ran0 := l.walCheckpoints.Load()
	reader := walReader(t, db)
	const writes = 600
	for k := range writes {
		mExec(t, w, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k%2000)
		mExec(t, temp, `INSERT INTO tt VALUES (1)`)
		next := walReader(t, db)
		reader.Rollback()
		reader = next
	}
	reader.Rollback()
	// About two frames a write: 24 checkpoints.
	if ran := l.walCheckpoints.Load() - ran0; ran < 12 || ran > 40 {
		t.Errorf("%d checkpoints in %d writes beside a connection that writes its temporary table, want about 24", ran, writes)
	}
}

// A checkpoint that cannot run, here as its connection has a query open,
// does not count as one: the next commit tries again. Else a connection
// that tries often and in vain keeps the others from ever running one.
func TestCheckpointEveryFailedTry(t *testing.T) {
	path := mPath(t)
	db := mOpen(t, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(100)&_wal_limit=0")
	db.SetMaxOpenConns(3)
	walTable(t, db)
	ctx := context.Background()
	vain, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer vain.Close()
	mExec(t, vain, `PRAGMA wal_autocheckpoint=1`)
	w, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var most int64
	for k := range 500 {
		rows, err := vain.QueryContext(ctx, `SELECT k FROM t`)
		if err != nil {
			t.Fatal(err)
		}
		if !rows.Next() {
			t.Fatal("no row")
		}
		mExec(t, vain, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k)
		rows.Close()
		mExec(t, w, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k+1000)
		if st, err := os.Stat(path + "-wal"); err == nil {
			most = max(most, st.Size())
		}
	}
	// A write is one or two frames of 4120 bytes.
	if most > 120*4120 {
		t.Errorf("the WAL grew to %d bytes, want at most about 100 frames", most)
	}
}

// The WAL starts over after a checkpoint that copied all of it. A first
// transaction that is longer than the WAL was at that checkpoint is still
// seen as one of a WAL that started over: its frames all count.
func TestCheckpointEveryStartOver(t *testing.T) {
	path := mPath(t)
	db := mOpen(t, "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(100)")
	walTable(t, db)
	l := walLock(t, db)
	ran0 := l.walCheckpoints.Load()
	for k := 0; l.walCheckpoints.Load() == ran0; k++ {
		if k > 200 {
			t.Fatal("no checkpoint in 200 writes")
		}
		mExec(t, db, `UPDATE t SET v = randomblob(1000) WHERE k = ?`, k)
	}
	// One transaction of between 100 and 200 frames.
	mExec(t, db, `UPDATE t SET v = randomblob(1000) WHERE k < 520`)
	st, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if frames := (st.Size() - 32) / 4120; frames < 110 || frames >= 200 {
		t.Fatalf("the transaction left a WAL of %d frames: the test needs between 110 and 200", frames)
	}
	if ran := l.walCheckpoints.Load() - ran0; ran != 2 {
		t.Errorf("%d checkpoints, want 2: one after the transaction that started the WAL over", ran)
	}
}

// PRAGMA wal_autocheckpoint reads what the program set, as with SQLite's
// own, and any value SQLite takes can be set.
func TestCheckpointEveryPragmaRead(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(50)")
	db.SetMaxOpenConns(3)
	walTable(t, db)
	l := walLock(t, db)
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if n := queryInt(t, c, `PRAGMA wal_autocheckpoint`); n != 50 {
		t.Errorf("PRAGMA wal_autocheckpoint reads %d, want the 50 of the DSN", n)
	}
	// A statement kept and run again reads what is set then.
	s, err := c.PrepareContext(ctx, `PRAGMA wal_autocheckpoint`)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, want := range []int{50, 30, 0, 2147483647} {
		mExec(t, c, `PRAGMA wal_autocheckpoint=`+strconv.Itoa(want))
		for range 2 {
			var n int
			if err := s.QueryRowContext(ctx).Scan(&n); err != nil || n != want {
				t.Errorf("the kept PRAGMA wal_autocheckpoint reads %d, want %d (%v)", n, want, err)
			}
		}
		if n := queryInt(t, c, `PRAGMA wal_autocheckpoint`); n != want {
			t.Errorf("PRAGMA wal_autocheckpoint reads %d, want %d", n, want)
		}
	}
	// The largest value is one as any other: no checkpoint before the WAL
	// has that many frames.
	ran0 := l.walCheckpoints.Load()
	writeBesideReaders(t, db, c, 300)
	if ran := l.walCheckpoints.Load() - ran0; ran != 0 {
		t.Errorf("%d checkpoints with wal_autocheckpoint=2147483647", ran)
	}
	// A program that reads the value, turns the checkpoints off and sets
	// the value back has them back.
	mExec(t, c, `PRAGMA wal_autocheckpoint=40`)
	was := queryInt(t, c, `PRAGMA wal_autocheckpoint`)
	mExec(t, c, `PRAGMA wal_autocheckpoint=0`)
	mExec(t, c, `PRAGMA wal_autocheckpoint=`+strconv.Itoa(was))
	ran0 = l.walCheckpoints.Load()
	writeBesideReaders(t, db, c, 400)
	if ran := l.walCheckpoints.Load() - ran0; ran < 10 || ran > 35 {
		t.Errorf("%d checkpoints in 400 writes with wal_autocheckpoint set back to %d, want about 20", ran, was)
	}
}

// A WAL that started over is known by its salt. One that is shorter than at
// the last checkpoint started over too, whatever its salt: a WAL made anew
// may get the salt of the one before.
func TestCheckpointEveryShorter(t *testing.T) {
	c := &conn{walEvery: 100}
	l := &writeLock{walSalt: 7, walAt: 500}
	if c.walDue(l, 550, 7) {
		t.Error("due 50 frames after the last checkpoint")
	}
	if !c.walDue(l, 600, 7) {
		t.Error("not due 100 frames after the last checkpoint")
	}
	if !c.walDue(l, 120, 7) {
		t.Error("not due at 120 frames of a WAL that was 500 long at the last checkpoint")
	}
	if c.walDue(l, 50, 7) || !c.walDue(l, 100, 8) {
		t.Error("a WAL that started over is due at 100 frames, not before")
	}
}

// The WAL limit looks first with the largest page there is, before it has
// read the page size, and then finds the WAL under the limit. The automatic
// checkpoint that is due at that commit still runs.
func TestCheckpointEveryFirstLook(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(100)")
	mExec(t, db, `CREATE TABLE t(x)`)
	l := walLock(t, db)
	if l.walPage != 0 {
		t.Fatalf("the page size was read before: the test needs a file whose WAL limit has not looked yet")
	}
	ran0 := l.walCheckpoints.Load()
	// 1100 frames: past the 64 MiB of the limit were a page 65536 bytes.
	mExec(t, db, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1100) INSERT INTO t SELECT randomblob(4000) FROM n`)
	if ran := l.walCheckpoints.Load() - ran0; ran != 1 {
		t.Errorf("%d checkpoints after a first transaction of 1100 frames, want 1", ran)
	}
}

// failKeptPragma runs a kept statement that sets a PRAGMA so that it fails
// after SQLite set the value: SQLite prepares such a statement again as it
// steps it, sets the value while it does, and then finds the interrupt. It
// returns the connection of the driver.
func failKeptPragma(t *testing.T, c *sql.Conn, pragma string) *conn {
	t.Helper()
	ctx := context.Background()
	// No space and a semicolon at the end: the interrupt is found at the
	// end of the text, after the PRAGMA was coded.
	kept, err := c.PrepareContext(ctx, `PRAGMA"`+pragma+`"=7;`)
	if err != nil {
		t.Fatal(err)
	}
	defer kept.Close()
	var n int
	if err := kept.QueryRowContext(ctx).Scan(&n); err != nil {
		t.Fatal(err)
	}
	mExec(t, c, `PRAGMA `+pragma+`=17`)
	// SQLite keeps an interrupt while a statement of the connection runs.
	rows, err := c.QueryContext(ctx, `SELECT 1 UNION ALL SELECT 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no row")
	}
	var dc *conn
	c.Raw(func(v any) error {
		dc = v.(*conn)
		return dc.c.Interrupt()
	})
	if err := kept.QueryRowContext(ctx).Scan(&n); err == nil {
		t.Fatalf("the kept PRAGMA gave %d: the test needs it to fail", n)
	}
	rows.Close()
	if n := queryInt(t, c, `PRAGMA `+pragma); n != 7 {
		t.Fatalf("PRAGMA %s reads %d: the test needs the failed statement to have set 7", pragma, n)
	}
	return dc
}

// A statement that sets wal_autocheckpoint and fails may have set it. The
// driver takes the checkpoints again, with the new value.
func TestCheckpointEveryFailedPragma(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_wal_limit=0")
	db.SetMaxOpenConns(3)
	walTable(t, db)
	l := walLock(t, db)
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	dc := failKeptPragma(t, c, "wal_autocheckpoint")
	if dc.walEvery != 7 {
		t.Errorf("the driver runs a checkpoint each %d frames, want 7", dc.walEvery)
	}
	// SQLite runs none of its own: beside readers they would come at every
	// commit.
	syncs := countSyncs(t, "m.db-wal")
	ran0 := l.walCheckpoints.Load()
	writeBesideReaders(t, db, c, 140)
	// About two frames a write: 40 checkpoints.
	if ran, n := l.walCheckpoints.Load()-ran0, syncs.Load(); ran < 20 || ran > 60 || n > 70 {
		t.Errorf("%d checkpoints and %d syncs of the WAL in 140 writes with wal_autocheckpoint=7, want about 40", ran, n)
	}
}

// The same for busy_timeout, which the file's write lock waits by.
func TestBusyTimeoutFailedPragma(t *testing.T) {
	db := mOpen(t, "file:"+mPath(t)+"?_pragma=journal_mode(WAL)")
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if dc := failKeptPragma(t, c, "busy_timeout"); dc.busyTimeout != 7*time.Millisecond {
		t.Errorf("the write lock waits %v, want the 7 ms SQLite has", dc.busyTimeout)
	}
}
