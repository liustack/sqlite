// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/superfly/ltx"

	_ "github.com/liustack/sqlite"
	"github.com/liustack/sqlite/replica/internal/s3"
	"github.com/liustack/sqlite/replica/internal/s3test"
)

func TestMain(m *testing.M) {
	if name := os.Getenv(killEnvDB); name != "" {
		os.Exit(killHelper(name, os.Getenv(killEnvURL)))
	}
	code := m.Run()
	s3test.Stop()
	os.Exit(code)
}

// testConfig is a Config for a replica of its own on the test store, which
// takes no step and compacts nothing in the background: the tests call Sync
// and compact.
func testConfig(t testing.TB) Config {
	t.Helper()
	s, prefix := s3test.Store(t)
	return Config{
		URL: fmt.Sprintf("s3://%s/%s?endpoint=%s&region=%s", s.Bucket, strings.TrimSuffix(prefix, "/"),
			url.QueryEscape(s.Endpoint), s.Region),
		Credentials:  s.Credentials,
		AllowHTTP:    true,
		SyncInterval: time.Hour,
		OnError:      func(err error) { t.Logf("replica: %v", err) },
		schedule:     func(s *schedule) { s.levels = nil },
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
	r.busy <- struct{}{}
	err = r.put(context.Background(), txid)
	<-r.busy
	if err != nil {
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
	// What it staged is dropped at once: nothing is pending, so that a
	// staging stopped at its limit goes on.
	if s := b.Status(); !errors.Is(s.Err, ErrConflict) || s.Pending != 0 {
		t.Fatalf("status of the second: %+v", s)
	}
	// It stays stopped, and the program's writes go on.
	run(t, dbB, "INSERT INTO t VALUES ('b2')")
	if err := b.Sync(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("the second replicator again: %v, want ErrConflict", err)
	}
	// What it stages is dropped, but the last file.
	if entries, err := os.ReadDir(b.c.Dir()); err != nil || len(entries) != 1 {
		t.Fatalf("staged by the second replicator: %v %v", entries, err)
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

// `litestream restore` reads the replica: the latest state, and an earlier
// TXID. LITESTREAM names the binary (v0.5.18), else the test is skipped.
// litestreamBin is the litestream binary of LITESTREAM, "" when it is not
// set. With S3_TEST_REQUIRED it must be.
func litestreamBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("LITESTREAM")
	if bin == "" && os.Getenv("S3_TEST_REQUIRED") != "" {
		t.Fatal("LITESTREAM is not set, and S3_TEST_REQUIRED is")
	}
	return bin
}

// litestreamRestore has litestream restore the replica of cfg with args,
// and returns the file it wrote, "" without LITESTREAM.
func litestreamRestore(t *testing.T, cfg Config, args ...string) string {
	t.Helper()
	out, err := litestreamTry(t, cfg, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// litestreamTry is litestreamRestore, with the error of litestream.
func litestreamTry(t *testing.T, cfg Config, args ...string) (string, error) {
	t.Helper()
	bin := litestreamBin(t)
	if bin == "" {
		return "", nil
	}
	cr, err := cfg.Credentials.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "restored.db")
	cmd := exec.Command(bin, append(append([]string{"restore", "-o", out}, args...), cfg.URL)...)
	cmd.Env = append(os.Environ(), "AWS_ACCESS_KEY_ID="+cr.AccessKeyID, "AWS_SECRET_ACCESS_KEY="+cr.SecretAccessKey)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("litestream restore %v: %v\n%s", args, err, b)
	}
	return out, nil
}

func TestLitestreamRestore(t *testing.T) {
	if litestreamBin(t) == "" {
		t.Skip("LITESTREAM is not set")
	}
	cfg := testConfig(t)
	name, points := history(t, cfg)
	restore := func(args ...string) string { t.Helper(); return litestreamRestore(t, cfg, args...) }
	theirs := restore()
	if got, want := dump(t, theirs), dump(t, name); got != want {
		t.Fatalf("litestream restored\n%s\nthe database\n%s", got, want)
	}
	ours, _ := restoreTo(t, cfg, RestoreOptions{})
	sameFile(t, ours, theirs)
	// The same bytes at every TXID, and at the time after each.
	for _, p := range points {
		ours, _ := restoreTo(t, cfg, RestoreOptions{TXID: p.txid})
		theirs := restore("-txid", fmt.Sprintf("%016x", p.txid))
		sameFile(t, ours, theirs)
		if got := dump(t, theirs); got != p.dump {
			t.Fatalf("litestream restored at TXID %d\n%s\nthen\n%s", p.txid, got, p.dump)
		}
		// litestream restore refuses a time after the last file.
		if p.txid != points[len(points)-1].txid {
			ours, _ = restoreTo(t, cfg, RestoreOptions{At: p.at})
			sameFile(t, ours, restore("-timestamp", p.at.UTC().Format(time.RFC3339Nano)))
		}
	}
}

// sameFile checks that the files a and b hold the same bytes, nothing when
// b is "" (litestream restore did not run). It is called before either is
// opened.
func sameFile(t *testing.T, a, b string) {
	t.Helper()
	if b == "" {
		return
	}
	ab, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ab, bb) {
		t.Fatalf("%s (%d bytes) and %s (%d bytes) differ", a, len(ab), b, len(bb))
	}
}

// A store that lists an empty replica and refuses every write: the error of
// the uploads is in the status, with no secret, and Close does not wait on
// it.
func TestStatusErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.Query().Get("list-type") == "2" {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
	}))
	defer srv.Close()
	const secret = "secret-0123456789"
	cfg := Config{
		URL:          "s3://bucket/app.db?endpoint=" + url.QueryEscape(srv.URL),
		Credentials:  StaticCredentials(Credentials{AccessKeyID: "key", SecretAccessKey: secret}),
		AllowHTTP:    true,
		SingleWriter: true,
		SyncInterval: time.Hour,
	}
	name := filepath.Join(t.TempDir(), "x.db")
	r, err := New(context.Background(), name, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(context.Background()); err == nil {
		t.Fatal("Sync to a store that refuses writes: no error")
	}
	s := r.Status()
	if s.Err == nil || s.Uploaded != 0 || s.Staged != 1 {
		t.Fatalf("status %+v", s)
	}
	if strings.Contains(fmt.Sprintf("%v %+v", s.Err, s), secret) {
		t.Fatalf("the status shows the secret: %v", s.Err)
	}
	start := time.Now()
	if err := r.Close(); err == nil {
		t.Fatal("Close with nothing uploaded: no error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Close took %v", d)
	}
}

// lockedBuilder is a strings.Builder that OnError writes to from the
// background too.
type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuilder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// No secret and no session token in what a program sees: the errors of New,
// Sync, Close and Restore, the errors given to OnError, the status, and the
// Config printed, against a store that puts the request's headers in its
// errors.
func TestNoSecrets(t *testing.T) {
	const secret, token = "secret-0123456789", "token-0123456789"
	var listOK atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.Copy(io.Discard, req.Body)
		if listOK.Load() && req.Method == http.MethodGet && req.URL.Query().Get("list-type") == "2" {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidToken</Code><Message>%s %s</Message></Error>`,
			req.Header.Get("X-Amz-Security-Token"), req.Header.Get("Authorization"))
	}))
	defer srv.Close()
	var seen lockedBuilder
	cfg := Config{
		URL:          "s3://bucket/app.db?endpoint=" + url.QueryEscape(srv.URL),
		Credentials:  StaticCredentials(Credentials{AccessKeyID: "key", SecretAccessKey: secret, SessionToken: token}),
		AllowHTTP:    true,
		SyncInterval: 10 * time.Millisecond,
		OnError:      func(err error) { fmt.Fprintln(&seen, err) },
	}
	fmt.Fprintf(&seen, "%v %+v %#v\n", cfg, cfg, cfg)
	name := filepath.Join(t.TempDir(), "x.db")
	// The probe fails.
	if _, err := New(context.Background(), name, cfg); err == nil {
		t.Fatal("New against a store that refuses all: no error")
	} else {
		fmt.Fprintln(&seen, err)
	}
	// Listing works, writes fail.
	listOK.Store(true)
	cfg.SingleWriter = true
	r, err := New(context.Background(), name, cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (1)")
	fmt.Fprintln(&seen, r.Sync(context.Background()))
	time.Sleep(100 * time.Millisecond)
	fmt.Fprintf(&seen, "%v %+v\n", r.Status(), r.Status())
	fmt.Fprintln(&seen, r.Close())
	listOK.Store(false)
	_, err = Restore(context.Background(), filepath.Join(t.TempDir(), "out.db"), cfg, RestoreOptions{})
	fmt.Fprintln(&seen, err)
	out := seen.String()
	if !strings.Contains(out, "InvalidToken") {
		t.Fatalf("no error of the store seen:\n%s", out)
	}
	for _, s := range []string{secret, token} {
		if strings.Contains(out, s) {
			t.Fatalf("%q is in what the program sees:\n%s", s, out)
		}
	}
}

// A replicator cut off from the store while another one, from the same
// history, uploads, compacts and deletes the level 0 files it would write
// over: once back, it stops with ErrConflict and publishes nothing.
func TestStaleReplicator(t *testing.T) {
	cfg := testConfig(t)
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES ('both')")
	db.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	base := ltx.TXID(r.Status().Uploaded)
	other := copyDB(t, name)

	cfgA := cfg
	cfgA.schedule = func(s *schedule) {
		s.levels = []time.Duration{50 * time.Millisecond, time.Hour}
		s.keepL0 = 100 * time.Millisecond
	}
	cfgB, cnt := countingStore(t, cfg)
	cfgB.schedule = func(s *schedule) { s.recheck = 50 * time.Millisecond }
	a := newReplica(t, name, cfgA)
	b := newReplica(t, other, cfgB)
	cnt.down.Store(1)
	dbB := openDB(t, other)
	run(t, dbB, "INSERT INTO t VALUES ('b')")

	// A goes on until the level 0 file of base+1 is gone.
	c, path := client(t, cfg)
	key := levelKey(path, 0, base+1, base+1)
	dbA := openDB(t, name)
	gone := false
	for i := 0; !gone && i < 200; i++ {
		run(t, dbA, "INSERT INTO t VALUES ('a')")
		syncNow(t, a)
		time.Sleep(20 * time.Millisecond)
		_, err := c.Head(context.Background(), key)
		gone = errors.Is(err, s3.ErrNotFound)
	}
	if !gone {
		t.Fatal("the level 0 file was not deleted")
	}

	cnt.down.Store(0)
	if err := b.Sync(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("the replicator cut off: %v, want ErrConflict", err)
	}
	if _, err := c.Head(context.Background(), key); !errors.Is(err, s3.ErrNotFound) {
		t.Fatalf("%s is back: %v", key, err)
	}
	syncNow(t, a)
	out, _ := restoreTo(t, cfg, RestoreOptions{})
	if dump(t, out) != dump(t, name) {
		t.Fatal("the replica is not the first replicator's database")
	}
}

// An upload whose answer was lost is the replicator's own: the check that
// the replica ends where the replicator left it does not take it for
// another replicator's, however long the store was away.
func TestRecheckLostAnswer(t *testing.T) {
	cfg, cnt := countingStore(t, testConfig(t))
	cfg.manual = true // no upload but the Syncs of the test
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	cfg.schedule = func(s *schedule) {
		s.levels = nil
		s.now = func() time.Time { return time.Unix(0, clock.Load()) }
	}
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	syncNow(t, r)

	// The clock stands still: the upload comes before any check.
	cnt.down.Store(3)
	run(t, db, "INSERT INTO t VALUES (1)")
	if err := r.Sync(context.Background()); err == nil {
		t.Fatal("Sync got an answer the store did not send")
	}
	cnt.down.Store(0)
	c, path := client(t, cfg)
	lost := ltx.TXID(r.Status().Uploaded) + 1
	if _, err := c.Head(context.Background(), levelKey(path, 0, lost, lost)); err != nil {
		t.Fatalf("the store did not take the upload whose answer was lost: %v", err)
	}

	clock.Add(int64(2 * r.sched.recheck))
	run(t, db, "INSERT INTO t VALUES (2)")
	syncNow(t, r)
	if err := r.Status().Err; err != nil {
		t.Fatal(err)
	}
	out, _ := restoreTo(t, cfg, RestoreOptions{})
	if dump(t, out) != dump(t, name) {
		t.Fatal("the replica is not the database")
	}
}

// Close uploads what it staged before another process can stage the
// database: the staging folder stays locked until then.
func TestCloseHoldsStaging(t *testing.T) {
	base := testConfig(t)
	cfg, cnt := countingStore(t, base)
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (1)")
	db.Close()

	other := base
	other.SingleWriter = true
	var newErr error
	tried := false
	put := func(string) {
		if tried {
			return
		}
		tried = true
		var s *Replica
		if s, newErr = New(context.Background(), name, other); newErr == nil {
			s.Close()
		}
	}
	cnt.put.Store(&put)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	cnt.put.Store(nil)
	if !tried {
		t.Fatal("Close uploaded nothing")
	}
	if newErr == nil || !strings.Contains(newErr.Error(), "another process replicates") {
		t.Fatalf("New while Close uploads: %v, want the staging folder locked", newErr)
	}
	r = newReplica(t, name, cfg)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

// Close ends a Sync whose upload hangs before it lets another process stage
// the database: once Close returns, nothing of the replicator uploads.
func TestCloseEndsSync(t *testing.T) {
	cfg, cnt := countingStore(t, testConfig(t))
	cfg.manual = true // the PUT held is the one of the Sync
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)", "INSERT INTO t VALUES (1)")

	held := make(chan struct{})
	free := make(chan struct{})
	defer close(free)
	var once sync.Once
	put := func(string) {
		once.Do(func() {
			close(held)
			<-free
		})
	}
	cnt.put.Store(&put)
	synced := make(chan error, 1)
	go func() { synced <- r.Sync(context.Background()) }()
	<-held

	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case <-closed:
	case <-time.After(closeUpload + 5*time.Second):
		t.Fatal("Close did not return")
	}
	select {
	case err := <-synced:
		if err == nil {
			t.Fatal("the Sync Close ended did not fail")
		}
	default:
		t.Fatal("Close returned while a Sync still uploads")
	}
	if err := r.Sync(context.Background()); err == nil {
		t.Fatal("Sync after Close")
	}
}
