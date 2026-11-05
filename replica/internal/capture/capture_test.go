// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/internal/csqlite"
)

// testOptions take no step in the background: the tests call Sync.
func testOptions(t *testing.T) Options {
	return Options{Interval: time.Hour, OnError: func(err error) { t.Logf("capture: %v", err) }}
}

func open(t *testing.T, name string, opt Options) *Capturer {
	t.Helper()
	c, err := Open(name, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func openApp(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func run(t *testing.T, db *sql.DB, queries ...string) {
	t.Helper()
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func step(t *testing.T, c *Capturer) {
	t.Helper()
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// rebuild applies the staged files of dir, in order, to a new file, checking
// every checksum on the way, and returns the file's name and the last TXID.
func rebuild(t *testing.T, dir string) (string, ltx.TXID) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if _, _, err := ltx.ParseFilename(e.Name()); err == nil {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names) // the TXIDs are zero-padded hex
	out := filepath.Join(t.TempDir(), "rebuilt.db")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var txid ltx.TXID
	var sum ltx.Checksum
	for _, name := range names {
		r, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		dec := ltx.NewDecoder(r)
		if err := dec.DecodeHeader(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		h := dec.Header()
		if h.MinTXID != txid+1 || h.MaxTXID != h.MinTXID {
			t.Fatalf("%s after TXID %s", name, txid)
		}
		if txid == 0 && !h.IsSnapshot() || txid > 0 && h.PreApplyChecksum != sum {
			t.Fatalf("%s: pre-apply checksum %s, the database's is %s", name, h.PreApplyChecksum, sum)
		}
		data := make([]byte, h.PageSize)
		for {
			var ph ltx.PageHeader
			if err := dec.DecodePage(&ph, data); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if _, err := f.WriteAt(data, int64(ph.Pgno-1)*int64(h.PageSize)); err != nil {
				t.Fatal(err)
			}
		}
		if err := dec.Close(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r.Close()
		if err := f.Truncate(int64(h.Commit) * int64(h.PageSize)); err != nil {
			t.Fatal(err)
		}
		sum = fileChecksum(t, f, int(h.PageSize))
		if post := dec.Trailer().PostApplyChecksum; post != sum {
			t.Fatalf("%s: post-apply checksum %s, the database's is %s", name, post, sum)
		}
		txid = h.MaxTXID
	}
	return out, txid
}

func fileChecksum(t *testing.T, f *os.File, pageSize int) ltx.Checksum {
	t.Helper()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	lock := ltx.LockPgno(uint32(pageSize))
	page := make([]byte, pageSize)
	var sum ltx.Checksum
	for p := uint32(1); int64(p)*int64(pageSize) <= st.Size(); p++ {
		if p == lock {
			continue
		}
		if _, err := f.ReadAt(page, int64(p-1)*int64(pageSize)); err != nil {
			t.Fatal(err)
		}
		sum ^= ltx.ChecksumPage(p, page)
	}
	return sum | ltx.ChecksumFlag
}

// pages are the pages of the database name as a read transaction sees it,
// nil for the lock page.
func pages(t *testing.T, name string) [][]byte {
	t.Helper()
	c, err := csqlite.Open(name, csqlite.OpenReadOnly|csqlite.OpenURI, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Exec("BEGIN; SELECT 1 FROM sqlite_schema LIMIT 1"); err != nil {
		t.Fatal(err)
	}
	defer c.Exec("ROLLBACK")
	n, err := c.PageCount()
	if err != nil {
		t.Fatal(err)
	}
	ps, err := pragma(c, "page_size")
	if err != nil {
		t.Fatal(err)
	}
	size := map[string]int{"4096": 4096, "1024": 1024, "65536": 65536}[ps]
	lock := ltx.LockPgno(uint32(size))
	out := make([][]byte, n)
	for p := uint32(1); p <= n; p++ {
		if p == lock {
			continue
		}
		out[p-1] = make([]byte, size)
		if err := c.ReadPage(p, out[p-1]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// sameAs checks that the database rebuilt from the staging folder of c is
// the database name.
func sameAs(t *testing.T, c *Capturer, name string) {
	t.Helper()
	out, txid := rebuild(t, c.Dir())
	if txid != c.TXID() {
		t.Fatalf("rebuilt to TXID %s, staged %s", txid, c.TXID())
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := pages(t, name)
	if len(got) != len(want)*len(want[0]) {
		t.Fatalf("rebuilt %d bytes, the database has %d pages", len(got), len(want))
	}
	for i, p := range want {
		if p != nil && !bytes.Equal(got[i*len(p):(i+1)*len(p)], p) {
			t.Fatalf("page %d differs", i+1)
		}
	}
}

func TestPageSummer(t *testing.T) {
	s := newPageSummer()
	data := make([]byte, 4096)
	for i := range 20 {
		for j := range data {
			data[j] = byte(rand.IntN(256))
		}
		p := uint32(rand.IntN(1 << 20))
		if got, want := s.sum(p, data), ltx.ChecksumPage(p, data); got != want || i == 0 && got&ltx.ChecksumFlag == 0 {
			t.Fatalf("%x, ltx gives %x", got, want)
		}
	}
}

func TestScanFrames(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	db := openApp(t, name)
	run(t, db, "PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE t(x)")
	for range 30 {
		run(t, db, "INSERT INTO t VALUES (randomblob(2000))")
	}
	c, err := csqlite.Open(name, csqlite.OpenReadOnly|csqlite.OpenURI, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Exec("BEGIN; SELECT 1 FROM sqlite_schema LIMIT 1"); err != nil {
		t.Fatal(err)
	}
	h, err := c.WALHeader()
	if err != nil {
		t.Fatal(err)
	}
	c.Exec("ROLLBACK")
	wal, err := os.ReadFile(name + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	wh, err := readWALHeader(bytes.NewReader(wal))
	if err != nil || wh.salt != h.Salt {
		t.Fatalf("WAL header %+v %v", wh, err)
	}
	var commits uint32
	end, err := scanFrames(bytes.NewReader(wal), wh, 0, h.MaxFrame, wh.cksum, func(f *frame) error {
		if f.commit != 0 {
			commits++
		}
		return nil
	})
	if err != nil || end != h.Checksum || commits != 31 {
		t.Fatalf("scan: %v, checksum %x, header %x, %d commits", err, end, h.Checksum, commits)
	}
	// From the middle, carrying on from the frame before.
	mid, _ := scanFrames(bytes.NewReader(wal), wh, 0, 7, wh.cksum, func(*frame) error { return nil })
	if end2, err := scanFrames(bytes.NewReader(wal), wh, 7, h.MaxFrame, mid, func(*frame) error { return nil }); err != nil || end2 != end {
		t.Fatalf("from frame 7: %v", err)
	}
	// One byte changed in a page is found.
	bad := bytes.Clone(wal)
	bad[frameOffset(5, wh.pageSize)+frameHeaderSize+100] ^= 1
	if _, err := scanFrames(bytes.NewReader(bad), wh, 0, h.MaxFrame, wh.cksum, func(*frame) error { return nil }); !errors.Is(err, errFrame) {
		t.Fatalf("a changed page: %v", err)
	}
	if _, err := readWALHeader(bytes.NewReader(wal[:10])); !errors.Is(err, errNoWAL) {
		t.Fatalf("a short WAL: %v", err)
	}
}

func TestCapture(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	if c.TXID() != 1 {
		t.Fatalf("TXID %s after open", c.TXID())
	}
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	step(t, c)
	sameAs(t, c, name)
	for i := range 50 {
		run(t, db, "INSERT INTO t VALUES (randomblob(1000 + 37 * "+strconv.Itoa(i)+"))")
	}
	step(t, c)
	sameAs(t, c, name)
	// Nothing new: no file.
	before := c.TXID()
	step(t, c)
	if c.TXID() != before {
		t.Fatalf("TXID %s after a step with nothing new, was %s", c.TXID(), before)
	}
	// The database gets shorter.
	run(t, db, "DELETE FROM t WHERE rowid % 3 != 0", "VACUUM")
	step(t, c)
	sameAs(t, c, name)
	if n := c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies", n)
	}
	// The driver's connections cannot checkpoint.
	if _, err := db.Exec("PRAGMA wal_checkpoint"); !errors.Is(err, csqlite.ErrGuarded) {
		t.Fatalf("PRAGMA wal_checkpoint: %v", err)
	}
}

// With psow=0 and synchronous=FULL, SQLite repeats the last frame of each
// commit up to a sector boundary: the repeats are not commits of their own.
func TestPadding(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db, err := sql.Open("sqlite", "file:"+name+"?psow=0&_pragma=synchronous(FULL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run(t, db, "CREATE TABLE t(x)")
	for i := range 20 {
		run(t, db, "INSERT INTO t VALUES (randomblob(100 + "+strconv.Itoa(i)+"))")
	}
	step(t, c)
	if n := c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies", n)
	}
	sameAs(t, c, name)
}

// The WAL is checkpointed and starts over, with and without a reader that
// keeps TRUNCATE from emptying it: the frames are followed throughout.
func TestCaptureCheckpoint(t *testing.T) {
	for _, reader := range []bool{false, true} {
		t.Run(map[bool]string{false: "truncate", true: "reader"}[reader], func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "x.db")
			opt := testOptions(t)
			opt.CheckpointFrames = 20
			c := open(t, name, opt)
			db := openApp(t, name)
			run(t, db, "CREATE TABLE t(x)")
			gens := map[[8]byte]bool{}
			for i := range 200 {
				run(t, db, "INSERT INTO t VALUES (randomblob(3000))")
				if reader && i%10 == 0 {
					// A read transaction open across the step.
					tx, err := db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					var n int
					tx.QueryRow("SELECT count(*) FROM t").Scan(&n)
					step(t, c)
					tx.Rollback()
				} else if i%7 == 0 {
					step(t, c)
				}
				gens[c.gen] = true
			}
			step(t, c)
			sameAs(t, c, name)
			if len(gens) < 3 {
				t.Fatalf("the WAL started over %d times", len(gens)-1)
			}
			if n := c.FullCopies(); n != 0 {
				t.Fatalf("%d full copies", n)
			}
		})
	}
}

// crashImage copies the files of the database name, its WAL and its staging
// folder, as a crash of the process would leave them: nothing is closed or
// checkpointed. It returns the name of the copy.
func crashImage(t *testing.T, name string) string {
	t.Helper()
	copy := filepath.Join(t.TempDir(), "crashed.db")
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(name + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(copy+suffix, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.CopyFS(copy+"-replica", os.DirFS(name+"-replica")); err != nil {
		t.Fatal(err)
	}
	return copy
}

func TestResume(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (1)")
	db.Close()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	txid := c.TXID()
	c = open(t, name, testOptions(t))
	if c.TXID() != txid || c.FullCopies() != 0 {
		t.Fatalf("after a clean close: TXID %s, was %s, %d full copies", c.TXID(), txid, c.FullCopies())
	}
	db = openApp(t, name)
	run(t, db, "INSERT INTO t VALUES (2)")
	step(t, c)
	sameAs(t, c, name)
}

// After a crash, the frames written since the last step are staged on
// start: in the same generation of the WAL, and in the next one after a
// checkpoint.
func TestResumeAfterCrash(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "next"}[checkpoint], func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "x.db")
			opt := testOptions(t)
			if checkpoint {
				opt.CheckpointFrames = 1
			}
			c := open(t, name, opt)
			db := openApp(t, name)
			run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (randomblob(5000))")
			step(t, c)
			run(t, db, "INSERT INTO t VALUES (randomblob(5000))", "UPDATE t SET x = randomblob(100) WHERE rowid = 1")
			txid := c.TXID()
			crashed := crashImage(t, name)

			c = open(t, crashed, testOptions(t))
			if c.FullCopies() != 0 || c.TXID() != txid+1 {
				t.Fatalf("TXID %s after %s, %d full copies", c.TXID(), txid, c.FullCopies())
			}
			sameAs(t, c, crashed)
		})
	}
}

// A database written while the capturer was closed is not where the
// staged files end: a full copy is staged.
func TestResumeElsewhere(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (1)")
	db.Close()
	c.Close()
	db = openApp(t, name)
	run(t, db, "INSERT INTO t VALUES (2)")
	db.Close()
	var reported []error
	opt := testOptions(t)
	opt.OnError = func(err error) { reported = append(reported, err) }
	c = open(t, name, opt)
	if c.FullCopies() != 1 || len(reported) != 1 || !errors.Is(reported[0], ErrFullCopy) {
		t.Fatalf("%d full copies, reported %v", c.FullCopies(), reported)
	}
	sameAs(t, c, name)
}

// Another process writes: frames it appends are staged as any others. When
// it checkpoints and the WAL starts over, the commits counted are not all in
// the WAL, and a full copy is staged.
func TestOtherProcess(t *testing.T) {
	shell := os.Getenv("SQLITE3")
	if shell == "" {
		t.Skip("set SQLITE3 to the sqlite3 shell")
	}
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (1)")
	step(t, c)
	cli := func(sql string) {
		t.Helper()
		out, err := exec.Command(shell, name, sql).CombinedOutput()
		if err != nil {
			t.Fatalf("sqlite3: %v: %s", err, out)
		}
	}
	cli("INSERT INTO t VALUES (randomblob(3000)); INSERT INTO t VALUES (randomblob(3000));")
	run(t, db, "INSERT INTO t VALUES (2)")
	step(t, c)
	sameAs(t, c, name)
	if c.FullCopies() != 0 {
		t.Fatalf("%d full copies for frames another process appended", c.FullCopies())
	}
	cli("INSERT INTO t VALUES (randomblob(3000)); PRAGMA wal_checkpoint(TRUNCATE); INSERT INTO t VALUES (3);")
	run(t, db, "INSERT INTO t VALUES (4)")
	step(t, c)
	if c.FullCopies() != 1 {
		t.Fatalf("%d full copies after another process checkpointed", c.FullCopies())
	}
	sameAs(t, c, name)
	// And with the default automatic checkpoints of the shell.
	for range 5 {
		cli("WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM s WHERE i < 400) INSERT INTO t SELECT randomblob(3000) FROM s;")
	}
	step(t, c)
	sameAs(t, c, name)
	t.Logf("%d full copies", c.FullCopies())
}

// A staged file whose folder fails to sync is in place: the next file has
// the next TXID, and no checkpoint runs until the folder syncs.
func TestSyncDirFails(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 1
	c := open(t, name, opt)
	db := openApp(t, name)
	fail := errors.New("no sync")
	syncDir = func(string) error { return fail }
	t.Cleanup(func() { syncDir = syncDirFS })
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (1)")
	if err := c.Sync(context.Background()); !errors.Is(err, fail) {
		t.Fatalf("Sync: %v", err)
	}
	if c.TXID() != 2 {
		t.Fatalf("TXID %s after a staged file whose folder did not sync", c.TXID())
	}
	if st, err := os.Stat(name + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("the WAL was checkpointed (%v)", err)
	}
	syncDir = syncDirFS
	run(t, db, "INSERT INTO t VALUES (2)")
	step(t, c)
	if c.TXID() != 3 {
		t.Fatalf("TXID %s", c.TXID())
	}
	if st, err := os.Stat(name + "-wal"); err != nil || st.Size() != 0 {
		t.Fatalf("the WAL was not checkpointed (%v)", err)
	}
	sameAs(t, c, name)
}

// A checkpoint that finds the WAL lost stages a full copy, and the next
// one checkpoints. The connection that writes and checkpoints behind the
// capturer's back is not the driver's, so nothing guards it.
func TestCheckpointLost(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	step(t, c)
	raw, err := csqlite.Open(c.path, csqlite.OpenReadWrite|csqlite.OpenNoMutex, "")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.Exec("INSERT INTO t VALUES (1); PRAGMA wal_checkpoint(TRUNCATE); INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		c.mu.Lock()
		err = c.checkpoint(context.Background())
		c.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	if c.FullCopies() != 1 {
		t.Fatalf("%d full copies", c.FullCopies())
	}
	if st, err := os.Stat(name + "-wal"); err != nil || st.Size() != 0 {
		t.Fatalf("the WAL was not checkpointed (%v)", err)
	}
	sameAs(t, c, name)
}

// Close does not wait long for a write transaction: it leaves the WAL as
// it is, staged.
func TestCloseBusy(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c, err := Open(name, testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	wait := closeWait
	closeWait = 50 * time.Millisecond
	t.Cleanup(func() { closeWait = wait })
	done := make(chan error, 1)
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		tx.Rollback()
		t.Fatal("Close waits for the write transaction")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// A temporary file left by a write that failed does not stop the next one.
func TestStaleTemp(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	tmp := filepath.Join(c.Dir(), ltx.FormatFilename(c.TXID()+1, c.TXID()+1)+".tmp")
	if err := os.WriteFile(tmp, []byte("left"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, db, "CREATE TABLE t(x)")
	step(t, c)
	sameAs(t, c, name)
}

// Durable is the last TXID whose name is synced, and Staged signals it.
func TestDurable(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	if c.Durable() != 1 {
		t.Fatalf("Durable %s after open", c.Durable())
	}
	<-c.Staged()
	db := openApp(t, name)
	fail := errors.New("no sync")
	syncDir = func(string) error { return fail }
	t.Cleanup(func() { syncDir = syncDirFS })
	run(t, db, "CREATE TABLE t(x)")
	if err := c.Sync(context.Background()); !errors.Is(err, fail) {
		t.Fatalf("Sync: %v", err)
	}
	if c.TXID() != 2 || c.Durable() != 1 {
		t.Fatalf("TXID %s, Durable %s", c.TXID(), c.Durable())
	}
	select {
	case <-c.Staged():
		t.Fatal("signaled a file whose name is not synced")
	default:
	}
	syncDir = syncDirFS
	run(t, db, "INSERT INTO t VALUES (1)")
	step(t, c)
	if c.Durable() != 3 {
		t.Fatalf("Durable %s, want 3", c.Durable())
	}
	<-c.Staged()
}

// Start sees the staged files, and a restart drops them and stages a full
// copy as the TXID after the one it names.
func TestStartRestart(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	for i := range 3 {
		run(t, db, "CREATE TABLE t"+strconv.Itoa(i)+"(x)")
		step(t, c)
	}
	post := c.post
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var seen []StagedFile
	opt := testOptions(t)
	opt.Start = func(files []StagedFile) (*Restart, error) {
		seen = files
		return nil, nil
	}
	c = open(t, name, opt)
	if len(seen) != 4 {
		t.Fatalf("Start saw %d files, want 4", len(seen))
	}
	for i, f := range seen {
		if f.TXID != ltx.TXID(i+1) || (i > 0 && f.Pre != seen[i-1].Post) {
			t.Fatalf("file %d: %+v", i, f)
		}
	}
	if seen[3].Post != post {
		t.Fatalf("the last post %s, want %s", seen[3].Post, post)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	stop := errors.New("refused")
	opt.Start = func([]StagedFile) (*Restart, error) { return nil, stop }
	if _, err := Open(name, opt); !errors.Is(err, stop) {
		t.Fatalf("Open with a Start that fails: %v", err)
	}

	opt.Start = func([]StagedFile) (*Restart, error) { return &Restart{TXID: 10, Post: post}, nil }
	c = open(t, name, opt)
	entries, err := os.ReadDir(c.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ltx.FormatFilename(11, 11) {
		t.Fatalf("staged after a restart: %v", entries)
	}
	if c.TXID() != 11 || c.FullCopies() != 0 {
		t.Fatalf("TXID %s, %d full copies", c.TXID(), c.FullCopies())
	}
	f, err := os.Open(filepath.Join(c.Dir(), entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := ltx.NewDecoder(f)
	if err := dec.Verify(); err != nil {
		t.Fatal(err)
	}
	if h := dec.Header(); h.PreApplyChecksum != post || h.Commit == 0 {
		t.Fatalf("header %+v", h)
	}
}

// A restart into the staged files that stops once the full copy is placed
// (here, its folder fails to sync) leaves the copy to go on from: the files
// after it are gone already.
func TestRestartStopped(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	for i := range 3 {
		run(t, db, "CREATE TABLE t"+strconv.Itoa(i)+"(x)")
		step(t, c)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	const post = ltx.ChecksumFlag | 12345
	opt := testOptions(t)
	opt.Start = func([]StagedFile) (*Restart, error) { return &Restart{TXID: 2, Post: post}, nil }
	fail := errors.New("no sync")
	n := 0 // the first sync is of the drop of the files after TXID 2
	syncDir = func(string) error {
		if n++; n == 1 {
			return nil
		}
		return fail
	}
	_, err := Open(name, opt)
	syncDir = syncDirFS
	if !errors.Is(err, fail) {
		t.Fatalf("Open: %v", err)
	}
	files, err := stagedFiles(StageDir(realPath(t, name)))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[2].TXID != 3 || files[2].Pre != post {
		t.Fatalf("staged after the stopped restart: %+v", files)
	}
	opt.Start = nil
	c = open(t, name, opt)
	if c.TXID() != 3 || c.FullCopies() != 0 {
		t.Fatalf("TXID %s, %d full copies", c.TXID(), c.FullCopies())
	}
}

// Stage places a file in an empty staging folder, and refuses one that
// holds a staged file.
func TestStage(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	from := name + ".ltx"
	if err := os.WriteFile(from, []byte("ltx"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Stage(name, from, 7, 7); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(StageDir(name), ltx.FormatFilename(7, 7))
	if b, err := os.ReadFile(staged); err != nil || string(b) != "ltx" {
		t.Fatalf("staged: %q %v", b, err)
	}
	if err := os.WriteFile(from, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Stage(name, from, 8, 8); err == nil {
		t.Fatal("Stage into a folder with a staged file: no error")
	}
	if _, err := os.Stat(filepath.Join(StageDir(name), ltx.FormatFilename(8, 8))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the second file was staged: %v", err)
	}
}

// A snapshot taken while the program writes holds the database where the
// staged files of its TXID leave it, and the writes go on staged after it.
func TestSnapshot(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 8
	c := open(t, name, opt)
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t SELECT randomblob(3000) FROM (WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM s WHERE i < 300) SELECT i FROM s)")
	step(t, c)
	stop := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				errs <- nil
				return
			default:
			}
			if _, err := db.Exec("INSERT INTO t VALUES (randomblob(500))"); err != nil {
				errs <- err
				return
			}
			if err := c.Sync(context.Background()); err != nil {
				errs <- err
				return
			}
		}
	}()
	snap, txid, err := c.Snapshot(context.Background())
	close(stop)
	if werr := <-errs; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if snap != filepath.Join(SnapshotDir(c.path), ltx.FormatFilename(1, txid)) {
		t.Fatalf("snapshot %s of TXID %s", snap, txid)
	}
	f, err := os.Open(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := ltx.NewDecoder(f)
	if err := dec.Verify(); err != nil {
		t.Fatal(err)
	}
	staged, err := readStaged(filepath.Join(c.Dir(), ltx.FormatFilename(txid, txid)))
	if err != nil {
		t.Fatal(err)
	}
	if post := dec.Trailer().PostApplyChecksum; post != staged.Post {
		t.Fatalf("the snapshot of TXID %s has checksum %s, the staged file %s", txid, post, staged.Post)
	}
	step(t, c)
	if c.TXID() <= txid {
		t.Fatalf("nothing staged after the snapshot of TXID %s", txid)
	}
	sameAs(t, c, name)
}

// uploadAll copies the staged files to remote, as an uploader would, and
// tells c they are uploaded.
func uploadAll(t *testing.T, c *Capturer, remote string) {
	t.Helper()
	entries, err := os.ReadDir(c.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, _, err := ltx.ParseFilename(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.Dir(), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remote, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Done(c.Durable()); err != nil {
		t.Fatal(err)
	}
	if n := c.Pending(); n != 0 {
		t.Fatalf("%d bytes pending after all went up", n)
	}
}

// At the limit, staging stops and keeps what it has, checkpoints go on, and
// once all of it is uploaded a full copy goes on from it.
func TestStagingLimit(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	remote := t.TempDir()
	var full atomic.Int64
	opt := testOptions(t)
	opt.CheckpointFrames = 20
	opt.Limit = 64 << 10
	opt.OnError = func(err error) {
		if errors.Is(err, ErrStagingFull) {
			full.Add(1)
		}
		t.Logf("capture: %v", err)
	}
	c := open(t, name, opt)
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	step(t, c)
	uploadAll(t, c, remote)
	var stopped ltx.TXID
	for i := range 300 {
		run(t, db, "INSERT INTO t VALUES (randomblob(3000))")
		err := c.Sync(context.Background())
		switch {
		case err == nil:
			if stopped != 0 {
				t.Fatalf("step %d staged after staging stopped", i)
			}
		case errors.Is(err, ErrStagingFull):
			if stopped == 0 {
				stopped = c.Durable()
			}
			if c.Durable() != stopped {
				t.Fatalf("step %d: staged TXID %s after stopping at %s", i, c.Durable(), stopped)
			}
		default:
			t.Fatal(err)
		}
		if n := c.Pending(); n > opt.Limit+32<<10 {
			t.Fatalf("step %d: %d bytes pending, the limit is %d", i, n, opt.Limit)
		}
		if st, err := os.Stat(name + "-wal"); err != nil {
			t.Fatal(err)
		} else if st.Size() > 30*(4096+24)+32 {
			t.Fatalf("step %d: the WAL has %d bytes", i, st.Size())
		}
	}
	if stopped == 0 || full.Load() != 1 {
		t.Fatalf("stopped at TXID %s, %d errors of a full staging", stopped, full.Load())
	}
	if _, _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("a snapshot while staging is stopped")
	}
	// S3 is back: what is staged goes up, then a full copy follows it.
	uploadAll(t, c, remote)
	step(t, c)
	if c.Durable() != stopped+1 || c.FullCopies() != 1 {
		t.Fatalf("staged TXID %s with %d full copies after the upload, want a full copy as %s", c.Durable(), c.FullCopies(), stopped+1)
	}
	for range 20 {
		run(t, db, "INSERT INTO t VALUES (randomblob(3000))", "DELETE FROM t WHERE rowid % 5 = 0")
		step(t, c)
		uploadAll(t, c, remote)
	}
	out, txid := rebuild(t, remote)
	if txid != c.TXID() {
		t.Fatalf("rebuilt TXID %s, staged %s", txid, c.TXID())
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := pages(t, name)
	for i, p := range want {
		if p != nil && !bytes.Equal(got[i*len(p):(i+1)*len(p)], p) {
			t.Fatalf("page %d differs", i+1)
		}
	}
}

// A database larger than the limit: its full copy does not stop staging,
// also after a restart, nothing uploaded.
func TestStagingLimitLarge(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	for range 50 {
		run(t, db, "INSERT INTO t VALUES (randomblob(3000))")
	}
	db.Close()
	opt := testOptions(t)
	opt.Limit = 64 << 10
	for range 2 {
		c := open(t, name, opt)
		db = openApp(t, name)
		for i := range 5 {
			run(t, db, "INSERT INTO t VALUES (1)")
			if err := c.Sync(context.Background()); err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
		}
		if c.FullCopies() != 0 {
			t.Fatalf("%d full copies", c.FullCopies())
		}
		db.Close()
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Another process checkpoints after each change, so that each step stages a
// full copy: past the limit staging stops all the same, the copies count.
func TestStagingLimitLost(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	for range 50 {
		run(t, db, "INSERT INTO t VALUES (randomblob(3000))")
	}
	db.Close()
	opt := testOptions(t)
	opt.Limit = 64 << 10
	c := open(t, name, opt)
	raw, err := csqlite.Open(c.path, csqlite.OpenReadWrite|csqlite.OpenNoMutex, "")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	st, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if err := raw.Exec("INSERT INTO t VALUES (1); PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			t.Fatal(err)
		}
		if err := c.Sync(context.Background()); err != nil && !errors.Is(err, ErrStagingFull) {
			t.Fatal(err)
		}
		// One database more than the limit: the copy staging goes on from.
		if n := c.Pending(); n > opt.Limit+st.Size()+8<<10 {
			t.Fatalf("step %d: %d bytes pending, the limit is %d", i, n, opt.Limit)
		}
	}
}

// The staged file whose folder did not sync when staging stopped is
// uploaded all the same, so that staging goes on after.
func TestStagingLimitSyncDir(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.Limit = 1
	c := open(t, name, opt)
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	step(t, c)
	if err := c.Done(c.Durable()); err != nil {
		t.Fatal(err)
	}
	fail := errors.New("no sync")
	syncDir = func(string) error { return fail }
	t.Cleanup(func() { syncDir = syncDirFS })
	run(t, db, "INSERT INTO t VALUES (1)")
	if err := c.Sync(context.Background()); !errors.Is(err, fail) {
		t.Fatalf("Sync: %v", err)
	}
	syncDir = syncDirFS
	if err := c.Sync(context.Background()); !errors.Is(err, ErrStagingFull) {
		t.Fatalf("Sync: %v, want ErrStagingFull", err)
	}
	if c.Durable() != c.TXID() {
		t.Fatalf("durable TXID %s after stopping at %s", c.Durable(), c.TXID())
	}
	if err := c.Done(c.Durable()); err != nil {
		t.Fatal(err)
	}
	step(t, c)
	if c.FullCopies() != 1 {
		t.Fatalf("%d full copies after the upload", c.FullCopies())
	}
}

// A staged file whose folder did not sync is made durable by the next step,
// also when nothing was committed since.
func TestSyncDirIdle(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	c := open(t, name, testOptions(t))
	db := openApp(t, name)
	fail := errors.New("no sync")
	syncDir = func(string) error { return fail }
	t.Cleanup(func() { syncDir = syncDirFS })
	run(t, db, "CREATE TABLE t(x)")
	if err := c.Sync(context.Background()); !errors.Is(err, fail) {
		t.Fatalf("Sync: %v", err)
	}
	syncDir = syncDirFS
	step(t, c)
	if c.Durable() != c.TXID() {
		t.Fatalf("durable TXID %s, staged %s", c.Durable(), c.TXID())
	}
}

// A step that passes the limit stages no more: it does not catch up with
// the writers, nor checkpoint, whether its first read of the WAL passed it
// or a read as it caught up.
func TestStagingLimitStep(t *testing.T) {
	for _, tc := range []struct {
		name         string
		before, more int // rows before the step, and at each point of it
		files        ltx.TXID
	}{
		{"first read", 30, 1, 1},
		{"catching up", 5, 300, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "x.db")
			opt := testOptions(t)
			opt.CheckpointFrames = 1
			opt.Limit = 64 << 10
			c := open(t, name, opt)
			db := openApp(t, name)
			run(t, db, "CREATE TABLE t(x)")
			step(t, c)
			if err := c.Done(c.Durable()); err != nil {
				t.Fatal(err)
			}
			insert := func(n int) {
				for range n {
					run(t, db, "INSERT INTO t VALUES (randomblob(3000))")
				}
			}
			insert(tc.before)
			stepHook = func(string) { insert(tc.more) }
			defer func() { stepHook = func(string) {} }()
			before := c.TXID()
			step(t, c)
			if c.TXID() != before+tc.files {
				t.Fatalf("the step staged TXIDs %s to %s past the limit", before+1, c.TXID())
			}
		})
	}
}

// Writers go on through every step and checkpoint: no frame is checkpointed
// away before it is staged, so the WAL is followed throughout and the staged
// files rebuild the database.
func TestCheckpointUnderWrites(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 50
	c := open(t, name, opt)
	db := openApp(t, name)
	db.SetMaxOpenConns(8)
	run(t, db, "CREATE TABLE t(x)")
	stop := make(chan struct{})
	errc := make(chan error, 4)
	for range 4 {
		go func() {
			for {
				select {
				case <-stop:
					errc <- nil
					return
				default:
				}
				if _, err := db.Exec("INSERT INTO t VALUES (randomblob(500))"); err != nil {
					errc <- err
					return
				}
			}
		}()
	}
	gens := map[[8]byte]bool{}
	for start := time.Now(); time.Since(start) < 2*time.Second; {
		step(t, c)
		gens[c.gen] = true
	}
	close(stop)
	for range 4 {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	step(t, c)
	if n := c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies: the WAL was lost", n)
	}
	if len(gens) < 3 {
		t.Fatalf("the WAL started over %d times", len(gens)-1)
	}
	sameAs(t, c, name)
}

// A commit between the capture and the checkpoint outside the lock, and
// another after it: the checkpoint copies none of the first, so the second
// cannot start the WAL over before it is staged.
func TestCheckpointGuard(t *testing.T) {
	name := filepath.Join(t.TempDir(), "x.db")
	opt := testOptions(t)
	opt.CheckpointFrames = 1
	c := open(t, name, opt)
	db := openApp(t, name)
	run(t, db, "CREATE TABLE t(x)")
	step(t, c)
	stepHook = func(at string) { run(t, db, "INSERT INTO t VALUES ('"+at+"')") }
	defer func() { stepHook = func(string) {} }()
	run(t, db, "INSERT INTO t VALUES (1)")
	step(t, c)
	stepHook = func(string) {}
	step(t, c)
	if n := c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies: the WAL was lost", n)
	}
	sameAs(t, c, name)
}

// walChecksumRef is the WAL checksum as SQLite's walChecksumBytes writes it.
func walChecksumRef(bigEndian bool, s [2]uint32, b []byte) [2]uint32 {
	var order binary.ByteOrder = binary.LittleEndian
	if bigEndian {
		order = binary.BigEndian
	}
	s1, s2 := s[0], s[1]
	for i := 0; i+8 <= len(b); i += 8 {
		s1 += order.Uint32(b[i:]) + s2
		s2 += order.Uint32(b[i+4:]) + s1
	}
	return [2]uint32{s1, s2}
}

func TestWALChecksum(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{0, 8, 24, 4096, 65536} {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		for _, big := range []bool{false, true} {
			s := [2]uint32{rng.Uint32(), rng.Uint32()}
			if got, want := walChecksum(big, s, b), walChecksumRef(big, s, b); got != want {
				t.Fatalf("%d bytes, big endian %t: %x, want %x", n, big, got, want)
			}
		}
	}
}

func BenchmarkWALChecksum(b *testing.B) {
	page := make([]byte, 4096)
	b.SetBytes(int64(len(page)))
	var s [2]uint32
	for b.Loop() {
		s = walChecksum(false, s, page)
	}
}
