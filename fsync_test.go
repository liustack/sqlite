// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"bufio"
	"bytes"
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
	"syscall"
	"testing"

	"github.com/liustack/sqlite/internal/vfs"
)

// A syncFault is a sync that loseOnFailedSyncOf fails.
type syncFault struct {
	mu   sync.Mutex
	done bool
	last bool
}

// failed says the sync failed.
func (s *syncFault) failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// ended says the sync that failed, of a rollback journal, was the one that
// ends a commit: the journal was empty, or the database was synced since the
// journal was last synced or started over.
func (s *syncFault) ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// loseOnFailedSync fails the n-th sync of the files whose names end with
// suffix, and then has the file read as the disk holds it once the kernel
// dropped the pages the failed sync could not write (Linux marks them clean
// and may evict them): every byte written since the last good sync of the
// file reads back as it was then, or as zero past its end then. What a
// file held when the hook first saw it is taken as synced, a new file, such
// as a journal made again, starts empty, and a truncate holds though the
// sync after it fails.
func loseOnFailedSync(t *testing.T, suffix string, n int) (failed func() bool) {
	return loseOnFailedSyncOf(t, suffix, n).failed
}

func loseOnFailedSyncOf(t *testing.T, suffix string, n int) *syncFault {
	type file struct {
		ino    uint64
		synced []byte
		// The syncs of the database, the file without the suffix, when
		// this file was last synced or started over.
		dbSyncs int
	}
	var (
		s     syncFault
		seen  int
		files = map[string]*file{}
		syncs = map[string]int{} // of the other files
	)
	vfs.SetFault(func(path string, op vfs.Op) int {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !strings.HasSuffix(path, suffix) {
			if op == vfs.OpSync {
				syncs[path]++
			}
			return vfs.OK
		}
		if op == vfs.OpTruncate {
			return vfs.OK
		}
		var st syscall.Stat_t
		if err := syscall.Stat(path, &st); err != nil {
			t.Errorf("stat %s: %v", path, err)
			return vfs.OK
		}
		cur, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			return vfs.OK
		}
		dbSyncs := syncs[strings.TrimSuffix(path, suffix)]
		f := files[path]
		if f == nil || f.ino != uint64(st.Ino) {
			f = &file{ino: uint64(st.Ino), synced: cur, dbSyncs: dbSyncs}
			files[path] = f
		}
		// The hook runs before each write, and only a write makes the file
		// longer: a file shorter than what was synced was truncated.
		if len(cur) < len(f.synced) {
			f.synced = f.synced[:len(cur)]
		}
		if op != vfs.OpSync {
			if len(cur) == 0 {
				f.dbSyncs = dbSyncs
			}
			return vfs.OK
		}
		last := len(cur) == 0 || dbSyncs != f.dbSyncs
		f.dbSyncs = dbSyncs
		if seen++; seen != n {
			f.synced = cur
			return vfs.OK
		}
		lost := make([]byte, len(cur))
		copy(lost, f.synced)
		if err := os.WriteFile(path, lost, 0o644); err != nil {
			t.Errorf("write %s: %v", path, err)
		}
		s.done, s.last = true, last
		return vfs.IOErrFsync
	})
	t.Cleanup(func() { vfs.SetFault(nil) })
	return &s
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
	// A program that only pings to see the database back gets it back, also
	// with no PRAGMA that reads the database as a connection opens.
	plain := mOpen(t, "file:"+path)
	pinged := false
	for range 20 {
		if plain.Ping() == nil {
			pinged = true
			break
		}
	}
	if !pinged {
		t.Fatal("Ping fails once the other process is gone")
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

// A WAL that a failed sync of its header cut to nothing is deleted when the
// last connection closes, as SQLite has nothing to checkpoint. The next
// WAL is a new file, and the database goes on.
func TestStoppedWALDeleted(t *testing.T) {
	path := mPath(t)
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=wal_autocheckpoint(0)"
	db := mOpen(t, dsn)
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(x)`)
	mExec(t, db, `PRAGMA wal_checkpoint(TRUNCATE)`)
	// The next commit starts the WAL over and syncs its header first.
	failed := loseOnFailedSync(t, "-wal", 1)
	if _, err := db.Exec(`INSERT INTO t VALUES (1)`); err == nil || !failed() {
		t.Fatalf("insert: %v, sync failed %v", err, failed())
	}
	tries := 0
	for {
		tries++
		if _, err := db.Exec(`INSERT INTO t VALUES (2)`); err == nil {
			break
		} else if tries == 20 {
			t.Fatalf("writes do not go on: %v", err)
		}
	}
	// SQLite names the file with symbolic links resolved.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if vfs.WALStopped(real) {
		t.Fatal("the WAL is still stopped")
	}
}

// In the rollback journal modes, a failed sync of the journal whose pages the
// kernel then drops leaves the database sound, now and when opened again:
// the write that failed is not there, every write told committed is, and
// writes go on. SQLite rolls back from the journal it reads back, so a
// journal that no longer holds what was written would leave the pages of the
// failed write in the cache, for the next commit to write.
//
// The one write that may be there though it was told failed is one whose
// last sync failed, that of the journal already truncated or with its header
// zeroed: the database was written and synced before it.
func TestJournalSyncLosesPages(t *testing.T) {
	for _, mode := range []string{"DELETE", "TRUNCATE", "PERSIST"} {
		for _, sync := range []string{"NORMAL", "FULL"} {
			for _, locking := range []string{"NORMAL", "EXCLUSIVE"} {
				t.Run(mode+"/"+sync+"/"+locking, func(t *testing.T) {
					// A commit syncs the journal up to three times.
					for nth := 1; nth <= 12; nth++ {
						t.Run(fmt.Sprint(nth), func(t *testing.T) { journalSyncLosesPages(t, mode, sync, locking, nth) })
					}
				})
			}
		}
	}
}

// journalMagic starts a rollback journal that is in use.
var journalMagic = []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}

func journalSyncLosesPages(t *testing.T, mode, sync, locking string, nth int) {
	dsn := "file:" + mPath(t) + "?_pragma=locking_mode(" + locking + ")&_pragma=journal_mode(" + mode + ")&_pragma=synchronous(" + sync + ")"
	db := mOpen(t, dsn)
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(k INTEGER PRIMARY KEY, v TEXT)`)
	const steps = 12
	for k := range steps {
		mExec(t, db, `INSERT INTO t VALUES (?, 'old')`, k)
	}
	fault := loseOnFailedSyncOf(t, "-journal", nth)

	// Each step changes a row of its own, which leaves the file as long as
	// it is, and adds a row that makes it longer.
	updated := func(k int) string { return fmt.Sprint("new", k) }
	added := func(k int) string { return strings.Repeat(fmt.Sprint(k%10), 5000) }
	want := map[int]string{}
	told := map[int]string{} // told failed: the value that must not be there
	nFailed := 0
	for i := range steps {
		want[i] = "old"
		if _, err := db.Exec(`UPDATE t SET v = ? WHERE k = ?`, updated(i), i); err == nil {
			want[i] = updated(i)
		} else {
			told[i] = updated(i)
			nFailed++
		}
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?)`, 100+i, added(i)); err == nil {
			want[100+i] = added(i)
		} else {
			told[100+i] = added(i)
			nFailed++
		}
	}
	if !fault.failed() {
		t.Skip("fewer syncs")
	}
	if nFailed != 1 {
		t.Errorf("%d writes failed, want the one whose sync failed", nFailed)
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
		defer rows.Close()
		have := map[int]string{}
		for rows.Next() {
			var k int
			var v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatalf("%s: %v", when, err)
			}
			have[k] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		for k, v := range told {
			if have[k] != v {
				continue
			}
			if !fault.ended() {
				t.Errorf("%s: the write of row %d, told failed, is there", when, k)
			}
			want[k] = v
		}
		for k, v := range want {
			if got, ok := have[k]; !ok {
				t.Errorf("%s: row %d is missing", when, k)
			} else if got != v {
				t.Errorf("%s: row %d has a wrong value", when, k)
			}
		}
		if len(have) != len(want) {
			t.Errorf("%s: %d rows, want %d", when, len(have), len(want))
		}
	}
	check(db, "after the failure")
	db.Close()
	check(mOpen(t, dsn), "opened again")
}

// A journal that persists keeps the journal of an earlier transaction past
// the end of the one in it, and SQLite zeroes the first byte of the old
// header there. A failed sync that loses only that byte leaves the
// transactions committed before it as they are: a rollback that went on into
// the old journal would undo their pages. So it is when the cut of the
// journal fails as well, and is made when the journal next opens.
func TestJournalSyncLosesZeroedHeader(t *testing.T) {
	for _, cutFails := range []bool{false, true} {
		t.Run(fmt.Sprint("cutFails=", cutFails), func(t *testing.T) { journalSyncLosesZeroedHeader(t, cutFails) })
	}
}

func journalSyncLosesZeroedHeader(t *testing.T, cutFails bool) {
	hits := 0
	// The rows the failed write changes: its journal must end where an old
	// header starts.
	for rows := 1; rows <= 30; rows++ {
		t.Run(fmt.Sprint(rows), func(t *testing.T) {
			// Small pages and a small cache: the first update spills, and
			// leaves a journal of many parts, each with a header.
			dsn := "file:" + mPath(t) + "?_pragma=page_size(512)&_pragma=journal_mode(PERSIST)&_pragma=synchronous(NORMAL)&_pragma=cache_size(10)"
			db := mOpen(t, dsn)
			db.SetMaxOpenConns(1)
			mExec(t, db, `CREATE TABLE t(k INTEGER PRIMARY KEY, v TEXT)`)
			mExec(t, db, `WITH RECURSIVE n(x) AS (VALUES (1) UNION ALL SELECT x+1 FROM n WHERE x < 100) INSERT INTO t SELECT x, ? FROM n`, strings.Repeat("a", 400))
			mExec(t, db, `UPDATE t SET v = ?`, strings.Repeat("b", 400))

			failed := false
			vfs.SetFault(func(path string, op vfs.Op) int {
				if !strings.HasSuffix(path, "-journal") {
					return vfs.OK
				}
				if op == vfs.OpTruncate && cutFails {
					return vfs.IOErrTruncate
				}
				if op != vfs.OpSync || failed {
					return vfs.OK
				}
				cur, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("read %s: %v", path, err)
					return vfs.OK
				}
				for off := 512; off+8 <= len(cur); off += 512 {
					if cur[off] != 0 || !bytes.Equal(cur[off+1:off+8], journalMagic[1:]) {
						continue
					}
					cur[off] = journalMagic[0]
					if err := os.WriteFile(path, cur, 0o644); err != nil {
						t.Errorf("write %s: %v", path, err)
					}
					failed = true
					return vfs.IOErrFsync
				}
				return vfs.OK
			})
			t.Cleanup(func() { vfs.SetFault(nil) })
			_, err := db.Exec(`UPDATE t SET v = ? WHERE k <= ?`, strings.Repeat("c", 400), rows)
			vfs.SetFault(nil)
			if !failed {
				t.Skip("no old header where the journal ends")
			}
			hits++
			if err == nil {
				t.Fatal("the write whose sync failed returned no error")
			}
			check := func(db *sql.DB, when string) {
				t.Helper()
				var ic string
				if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
					t.Fatalf("%s: integrity_check %q %v", when, ic, err)
				}
				var a, b, c int
				if err := db.QueryRow(`SELECT sum(v LIKE 'a%'), sum(v LIKE 'b%'), sum(v LIKE 'c%') FROM t`).Scan(&a, &b, &c); err != nil {
					t.Fatalf("%s: %v", when, err)
				}
				if a != 0 || b != 100 || c != 0 {
					t.Errorf("%s: %d rows as before the committed update, %d as after it, %d as the failed one wrote", when, a, b, c)
				}
			}
			check(db, "after the failure")
			db.Close()
			check(mOpen(t, dsn), "opened again")
		})
	}
	if hits == 0 {
		t.Fatal("no write ended its journal at an old header")
	}
}

// A transaction larger than the cache writes pages to the database before
// it commits, each after a sync of the journal that holds it. When a later
// sync of the journal fails, the cut keeps the parts synced before, and the
// rollback undoes every page written: the transaction is there whole or not
// at all.
func TestJournalSyncLosesPagesAfterSpill(t *testing.T) {
	for _, mode := range []string{"DELETE", "TRUNCATE", "PERSIST"} {
		for _, sync := range []string{"NORMAL", "FULL"} {
			for _, locking := range []string{"NORMAL", "EXCLUSIVE"} {
				t.Run(mode+"/"+sync+"/"+locking, func(t *testing.T) {
					// The update syncs the journal about 25 times, twice
					// that under FULL, and once more where a commit ends
					// with a sync.
					for _, nth := range []int{1, 2, 13, 25, 26, 50, 51, 52} {
						t.Run(fmt.Sprint(nth), func(t *testing.T) { journalSyncLosesPagesAfterSpill(t, mode, sync, locking, nth) })
					}
				})
			}
		}
	}
}

func journalSyncLosesPagesAfterSpill(t *testing.T, mode, sync, locking string, nth int) {
	dsn := "file:" + mPath(t) + "?_pragma=page_size(1024)&_pragma=locking_mode(" + locking + ")&_pragma=journal_mode(" + mode + ")&_pragma=synchronous(" + sync + ")&_pragma=cache_size(10)"
	db := mOpen(t, dsn)
	db.SetMaxOpenConns(1)
	mExec(t, db, `CREATE TABLE t(k INTEGER PRIMARY KEY, v TEXT, w INT)`)
	mExec(t, db, `WITH RECURSIVE n(x) AS (VALUES (1) UNION ALL SELECT x+1 FROM n WHERE x < 300) INSERT INTO t SELECT x, ?, 0 FROM n`, strings.Repeat("a", 400))

	fault := loseOnFailedSyncOf(t, "-journal", nth)
	_, err := db.Exec(`UPDATE t SET v = ?`, strings.Repeat("b", 400))
	if !fault.failed() {
		t.Skip("fewer syncs")
	}
	if err == nil {
		t.Fatal("the write whose sync failed returned no error")
	}
	// The sync that ends a commit comes after the database was written
	// and synced: the write is there.
	want := "a"
	if fault.ended() {
		want = "b"
	}
	// A page the rollback missed would stay as the failed write left it.
	if _, err := db.Exec(`UPDATE t SET w = 1`); err != nil {
		t.Fatalf("the write after the failure: %v", err)
	}
	check := func(db *sql.DB, when string) {
		t.Helper()
		var ic string
		if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
			t.Fatalf("%s: integrity_check %q %v", when, ic, err)
		}
		var a, b, w int
		if err := db.QueryRow(`SELECT sum(v LIKE 'a%'), sum(v LIKE 'b%'), sum(w) FROM t`).Scan(&a, &b, &w); err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		if n := map[string]int{"a": a, "b": b}[want]; n != 300 || a+b != 300 || w != 300 {
			t.Errorf("%s: %d rows as before the failed write, %d as after it, want all %q, and %d with the write after it", when, a, b, want, w)
		}
	}
	check(db, "after the failure")
	db.Close()
	check(mOpen(t, dsn), "opened again")
}
