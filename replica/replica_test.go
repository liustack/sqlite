// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/superfly/ltx"

	_ "github.com/liustack/sqlite"
	"github.com/liustack/sqlite/replica/internal/s3"
	"github.com/liustack/sqlite/replica/internal/s3test"
)

func TestMain(m *testing.M) {
	code := m.Run()
	s3test.Stop()
	os.Exit(code)
}

// testConfig is a Config for a replica of its own on the test store, which
// takes no step in the background: the tests call Sync.
func testConfig(t *testing.T) Config {
	t.Helper()
	s, prefix := s3test.Store(t)
	return Config{
		URL: fmt.Sprintf("s3://%s/%s?endpoint=%s&region=%s", s.Bucket, strings.TrimSuffix(prefix, "/"),
			url.QueryEscape(s.Endpoint), s.Region),
		Credentials:  s.Credentials,
		AllowHTTP:    true,
		SyncInterval: time.Hour,
		OnError:      func(err error) { t.Logf("replica: %v", err) },
	}
}

func newReplica(t *testing.T, name string, cfg Config) *Replica {
	t.Helper()
	r, err := New(context.Background(), name, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func openDB(t *testing.T, name string) *sql.DB {
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

func syncNow(t *testing.T, r *Replica) {
	t.Helper()
	if err := r.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func client(t *testing.T, cfg Config) (*s3.Client, string) {
	t.Helper()
	scfg, path, err := parseURL(cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	scfg.Credentials, scfg.AllowHTTP = cfg.Credentials, cfg.AllowHTTP
	c, err := s3.New(scfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, path
}

// rebuild applies the replica's level 0 files, in order, to a new file,
// checking every checksum on the way, and returns the file's name and the
// last TXID.
func rebuild(t *testing.T, cfg Config) (string, ltx.TXID) {
	t.Helper()
	c, path := client(t, cfg)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "rebuilt.db")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var txid ltx.TXID
	var sum ltx.Checksum
	for o, err := range c.List(ctx, path+"/0000/", "") {
		if err != nil {
			t.Fatal(err)
		}
		body, err := c.Get(ctx, o.Key, 0, -1)
		if err != nil {
			t.Fatal(err)
		}
		dec := ltx.NewDecoder(body)
		if err := dec.DecodeHeader(); err != nil {
			t.Fatalf("%s: %v", o.Key, err)
		}
		h := dec.Header()
		if h.MinTXID != txid+1 || h.MaxTXID != h.MinTXID {
			t.Fatalf("%s after TXID %s", o.Key, txid)
		}
		if txid == 0 && !h.IsSnapshot() || txid > 0 && h.PreApplyChecksum != sum {
			t.Fatalf("%s: pre-apply checksum %s, the database's is %s", o.Key, h.PreApplyChecksum, sum)
		}
		data := make([]byte, h.PageSize)
		for {
			var ph ltx.PageHeader
			if err := dec.DecodePage(&ph, data); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", o.Key, err)
			}
			if _, err := f.WriteAt(data, int64(ph.Pgno-1)*int64(h.PageSize)); err != nil {
				t.Fatal(err)
			}
		}
		if err := dec.Close(); err != nil {
			t.Fatalf("%s: %v", o.Key, err)
		}
		body.Close()
		if err := f.Truncate(int64(h.Commit) * int64(h.PageSize)); err != nil {
			t.Fatal(err)
		}
		sum = checksum(t, f, h.PageSize)
		if post := dec.Trailer().PostApplyChecksum; post != sum {
			t.Fatalf("%s: post-apply checksum %s, the database's is %s", o.Key, post, sum)
		}
		txid = h.MaxTXID
	}
	return out, txid
}

func checksum(t *testing.T, f *os.File, pageSize uint32) ltx.Checksum {
	t.Helper()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	lock := ltx.LockPgno(pageSize)
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
	return ltx.ChecksumFlag | sum
}

// dump is the schema and rows of the database name.
func dump(t *testing.T, name string) string {
	t.Helper()
	db, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("%s: integrity_check %q %v", name, ok, err)
	}
	var b strings.Builder
	rows, err := db.Query("SELECT name, sql FROM sqlite_schema WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
		fmt.Fprintln(&b, s)
	}
	rows.Close()
	for _, tb := range tables {
		var s sql.NullString
		if err := db.QueryRow("SELECT group_concat(hex(x), ',') FROM (SELECT x FROM " + tb + " ORDER BY rowid)").Scan(&s); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&b, tb, s.String)
	}
	return b.String()
}

// sameAs checks that the replica rebuilds the database name.
func sameAs(t *testing.T, cfg Config, name string) ltx.TXID {
	t.Helper()
	out, txid := rebuild(t, cfg)
	if got, want := dump(t, out), dump(t, name); got != want {
		t.Fatalf("the replica holds\n%s\nthe database\n%s", got, want)
	}
	return txid
}

func TestReplicate(t *testing.T) {
	cfg := testConfig(t)
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	for i := range 20 {
		run(t, db, fmt.Sprintf("INSERT INTO t VALUES (randomblob(%d))", 100+i*50))
	}
	syncNow(t, r)
	txid := sameAs(t, cfg, name)
	if s := r.Status(); s.Uploaded != uint64(txid) || s.Staged != s.Uploaded || s.Err != nil {
		t.Fatalf("status %+v, the replica ends at %s", s, txid)
	}
	// What was uploaded is no longer staged, but the last file.
	entries, err := os.ReadDir(r.c.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ltx.FormatFilename(txid, txid) {
		t.Fatalf("staged: %v", entries)
	}
	// The probe left nothing.
	c, path := client(t, cfg)
	for o, err := range c.List(context.Background(), path+"/probe/", "") {
		t.Fatalf("left by the probe: %s %v", o.Key, err)
	}
	// A file uploaded again, as after a PUT that got no answer, is ours.
	if err := r.put(context.Background(), txid); err != nil {
		t.Fatalf("a second upload of TXID %s: %v", txid, err)
	}
}

// Close uploads what is left, and New goes on from it.
func TestResume(t *testing.T) {
	cfg := testConfig(t)
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (1)")
	db.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	sameAs(t, cfg, name)

	r = newReplica(t, name, cfg)
	db = openDB(t, name)
	run(t, db, "INSERT INTO t VALUES (2)")
	syncNow(t, r)
	sameAs(t, cfg, name)
	if n := r.c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies", n)
	}
}

// New is refused once the program has the database open.
func TestOpenFirst(t *testing.T) {
	cfg := testConfig(t)
	name := filepath.Join(t.TempDir(), "x.db")
	db := openDB(t, name)
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), name, cfg); err == nil {
		t.Fatal("New with the database open: no error")
	}
}

// copyDB copies the database name, closed, and its staging folder, as a
// second machine with the same history would have them.
func copyDB(t *testing.T, name string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), "copy.db")
	if err := os.CopyFS(to+"-replica", os.DirFS(name+"-replica")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return to
}

// Two replicators that go on from the same history to one replica: the
// second to upload stops at its first file, and the replica holds the
// first one's database.
func TestTwoReplicators(t *testing.T) {
	cfg := testConfig(t)
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES ('both')")
	db.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	other := copyDB(t, name)

	a := newReplica(t, name, cfg)
	b := newReplica(t, other, cfg)
	dbA, dbB := openDB(t, name), openDB(t, other)
	run(t, dbA, "INSERT INTO t VALUES ('a')")
	run(t, dbB, "INSERT INTO t VALUES ('b')")
	syncNow(t, a)
	if err := b.Sync(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("the second replicator: %v, want ErrConflict", err)
	}
	if s := b.Status(); !errors.Is(s.Err, ErrConflict) {
		t.Fatalf("status of the second: %+v", s)
	}
	// It stays stopped, and the program's writes go on.
	run(t, dbB, "INSERT INTO t VALUES ('b2')")
	if err := b.Sync(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("the second replicator again: %v, want ErrConflict", err)
	}
	run(t, dbA, "INSERT INTO t VALUES ('a2')")
	syncNow(t, a)
	sameAs(t, cfg, name)
}

// A replica written from another database is refused, and taken over with
// FreshStart.
func TestDiverged(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	first, second := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	r := newReplica(t, first, cfg)
	db := openDB(t, first)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES ('a')")
	syncNow(t, r)

	other := openDB(t, second)
	run(t, other, "CREATE TABLE t(x)", "INSERT INTO t VALUES ('b')")
	other.Close()
	if _, err := New(context.Background(), second, cfg); !errors.Is(err, ErrDiverged) {
		t.Fatalf("New of another database: %v, want ErrDiverged", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := cfg
	fresh.FreshStart = true
	r2 := newReplica(t, second, fresh)
	syncNow(t, r2)
	sameAs(t, cfg, second)
}

// Staged files of another replica start over at TXID 1 in an empty one.
func TestNewReplica(t *testing.T) {
	cfg := testConfig(t)
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	for i := range 3 {
		run(t, db, fmt.Sprintf("CREATE TABLE t%d(x)", i))
		syncNow(t, r)
	}
	db.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	moved := testConfig(t)
	r = newReplica(t, name, moved)
	syncNow(t, r)
	if txid := sameAs(t, moved, name); txid != 1 {
		t.Fatalf("the new replica ends at TXID %s, want 1", txid)
	}
}

// Without Sync, commits reach the replica in the background.
func TestBackground(t *testing.T) {
	cfg := testConfig(t)
	cfg.SyncInterval = 10 * time.Millisecond
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	for i := range 10 {
		run(t, db, fmt.Sprintf("INSERT INTO t VALUES (%d)", i))
		time.Sleep(5 * time.Millisecond)
	}
	want := dump(t, name)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if s := r.Status(); s.Uploaded == s.Staged {
			if out, _ := rebuild(t, cfg); dump(t, out) == want {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the replica did not catch up: status %+v", r.Status())
		}
	}
}
