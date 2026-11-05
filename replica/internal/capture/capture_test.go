// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	if err := c.Sync(); err != nil {
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
