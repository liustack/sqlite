// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/capture"
	"github.com/liustack/sqlite/replica/internal/s3"
)

// maintainNow does the work on the replica that is due, as the background
// work does.
func maintainNow(t *testing.T, r *Replica) {
	t.Helper()
	r.busy <- struct{}{}
	defer func() { <-r.busy }()
	if _, err := r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Over thirteen hours of a clock the test moves, with a retention of three
// hours: after every pass of the work on the replica, the replica restores
// the oldest snapshot it keeps, each point after it a file ends at, and
// the latest state. Old snapshots and the files only they need go, and
// level 0 holds a few files only.
func TestRetention(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	clock := time.Now()
	cfg := testConfig(t)
	cfg.Retention = 3 * time.Hour
	cfg.schedule = func(s *schedule) {
		s.now = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return clock
		}
	}
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	c, path := client(t, cfg)
	dumps := map[uint64]string{}
	type stamp struct {
		at   time.Time
		txid uint64
	}
	var stamps []stamp // the clock at each upload
	dropped := 0       // snapshots
	seen := map[string]bool{}
	for i := range 40 {
		run(t, db, fmt.Sprintf("INSERT INTO t VALUES (randomblob(%d))", 200+i*50), "DELETE FROM t WHERE rowid % 7 = 0")
		syncNow(t, r)
		dumps[r.Status().Uploaded] = dump(t, name)
		mu.Lock()
		stamps = append(stamps, stamp{clock, r.Status().Uploaded})
		clock = clock.Add(20 * time.Minute)
		line := clock.Add(-cfg.Retention)
		mu.Unlock()
		maintainNow(t, r)

		// The database as it was retention ago restores.
		for j := len(stamps) - 1; j >= 0; j-- {
			if s := stamps[j]; !s.at.After(line) {
				out, got := restoreTo(t, cfg, RestoreOptions{TXID: s.txid})
				if got != s.txid || dump(t, out) != dumps[s.txid] {
					t.Fatalf("pass %d: the restore of TXID %d, retention ago", i, s.txid)
				}
				break
			}
		}

		files, err := listRemote(ctx, c, path)
		if err != nil {
			t.Fatal(err)
		}
		var snaps []remoteFile
		ends := map[ltx.TXID]bool{}
		level0 := 0
		for _, f := range files {
			ends[f.max] = true
			switch f.level {
			case snapshotLevel:
				snaps = append(snaps, f)
				seen[f.key] = true
			case 0:
				level0++
			}
		}
		// The clock moves 20 minutes a pass and level 0 files stay 30 minutes
		// after a level 1 file holds them: those of the pass before stay, with
		// the last one.
		if level0 > 3 {
			t.Fatalf("pass %d: %d files at level 0", i, level0)
		}
		if len(snaps) > 3 {
			t.Fatalf("pass %d: %d snapshots, with a retention of 3 hours and a snapshot each 3 hours", i, len(snaps))
		}
		dropped = len(seen) - len(snaps)
		// The oldest kept point: the oldest snapshot, else the first end of a
		// file. Level 0 files go once a level 1 file held them 30 minutes, so
		// the ends inside it go with them.
		var from ltx.TXID
		if len(snaps) > 0 {
			from = snaps[0].max
		} else {
			for end := range ends {
				if from == 0 || end < from {
					from = end
				}
			}
		}
		checked := 0
		for txid, want := range dumps {
			if ltx.TXID(txid) < from || !ends[ltx.TXID(txid)] {
				continue
			}
			out, got := restoreTo(t, cfg, RestoreOptions{TXID: txid})
			if got != txid || dump(t, out) != want {
				t.Fatalf("pass %d: restore of TXID %d", i, txid)
			}
			checked++
		}
		if !ends[from] || checked == 0 {
			t.Fatalf("pass %d: the oldest kept point, TXID %s, does not restore", i, from)
		}
		// Once a snapshot went, each file left is needed by a restore to a
		// kept point, or by the compaction of the next level.
		lastOf := map[int]ltx.TXID{}
		for _, f := range files {
			lastOf[f.level] = max(lastOf[f.level], f.max)
		}
		for _, f := range files {
			if dropped > 0 && f.level != snapshotLevel && f.max < from && (f.level == 2 || f.max <= lastOf[f.level+1]) {
				t.Fatalf("pass %d: %s is before the oldest kept snapshot, TXID %s", i, f.key, from)
			}
		}
	}
	if dropped == 0 {
		t.Fatal("no snapshot was dropped")
	}
	out, txid := restoreTo(t, cfg, RestoreOptions{})
	if dump(t, out) != dumps[txid] {
		t.Fatal("the latest restore is not the database")
	}
}

// A proxy in front of the test store that counts the requests.
type counter struct {
	n    atomic.Int64
	puts sync.Map // key: count
	// down makes the store fail: 1 answers 503, 2 closes the connection
	// without an answer, 3 sends each PUT to the store and then closes
	// the connection without its answer, and lets the other requests
	// through.
	down atomic.Int32
	// get, if set, runs before each GET of an object, given its path.
	get atomic.Pointer[func(path string)]
	// put, if set, runs before each PUT, given its path.
	put atomic.Pointer[func(path string)]
}

func countingStore(t *testing.T, cfg Config) (Config, *counter) {
	t.Helper()
	u, err := url.Parse(cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	target, err := url.Parse(q.Get("endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	// The requests are signed for the proxy's host, which only a store of
	// this machine takes: R2 and AWS check the host they are signed for.
	if ip := net.ParseIP(target.Hostname()); target.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Skip("goes through a proxy: needs a store on this machine, such as MinIO")
	}
	cnt := &counter{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// The request is signed for the proxy's host, which the store gets.
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		host := req.Host
		director(req)
		req.Host = host
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cnt.n.Add(1)
		switch cnt.down.Load() {
		case 1:
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		case 2:
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				conn.Close()
			}
			return
		case 3:
			if req.Method == http.MethodPut {
				proxy.ServeHTTP(httptest.NewRecorder(), req)
				if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
					conn.Close()
				}
				return
			}
		}
		if req.Method == http.MethodPut {
			cnt.puts.Store(req.URL.Path, true)
			if f := cnt.put.Load(); f != nil {
				(*f)(req.URL.Path)
			}
		}
		if f := cnt.get.Load(); f != nil && req.Method == http.MethodGet && req.URL.RawQuery == "" {
			(*f)(req.URL.Path)
		}
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	q.Set("endpoint", srv.URL)
	u.RawQuery = q.Encode()
	cfg.URL = u.String()
	return cfg, cnt
}

// A busy database makes at most one upload of level 0 each SyncInterval,
// and an idle one makes no request once the work due is done.
func TestIdle(t *testing.T) {
	cfg := testConfig(t)
	cfg, cnt := countingStore(t, cfg)
	cfg.SyncInterval = 20 * time.Millisecond
	cfg.Retention = 600 * time.Millisecond
	var bgErrs atomic.Int64
	cfg.OnError = func(err error) {
		bgErrs.Add(1)
		t.Logf("replica: %v", err)
	}
	cfg.schedule = func(s *schedule) {
		s.levels = []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}
		s.keepL0 = 200 * time.Millisecond
		s.keepProbe = time.Millisecond
	}
	name := filepath.Join(t.TempDir(), "x.db")
	// What a probe that did not finish left goes after a snapshot.
	c, path, err := cfg.client()
	if err != nil {
		t.Fatal(err)
	}
	left := path + "/probe/left-false"
	if err := c.Put(context.Background(), left, bytes.NewReader([]byte("probe")), 5, s3.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		run(t, db, "INSERT INTO t VALUES (randomblob(100))")
	}
	busy := time.Since(start)
	level0 := 0
	cnt.puts.Range(func(k, _ any) bool {
		if strings.Contains(k.(string), "/0000/") {
			level0++
		}
		return true
	})
	// A snapshot and a checkpoint stage what came since the last step too.
	if most := int(busy/cfg.SyncInterval)*5/4 + 2; level0 > most {
		t.Fatalf("%d uploads of level 0 in %v, at most %d", level0, busy, most)
	}
	// Settle: compactions, the drop of level 0, a snapshot, retention.
	time.Sleep(3 * time.Second)
	before := cnt.n.Load()
	time.Sleep(2 * time.Second)
	if n := cnt.n.Load() - before; n != 0 {
		t.Fatalf("%d requests in 2s of an idle database", n)
	}
	if s := r.Status(); s.Err != nil {
		t.Fatal(s.Err)
	}
	if n := bgErrs.Load(); n > 0 {
		t.Fatalf("%d errors in the background", n)
	}
	for o, err := range c.List(context.Background(), path+"/probe/", "") {
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("%s is left", o.Key)
	}
	out, txid := restoreTo(t, cfg, RestoreOptions{})
	if txid != r.Status().Uploaded || dump(t, out) != dump(t, name) {
		t.Fatal("the replica does not restore the database")
	}
	t.Logf("%d uploads of level 0 in %v, %d requests in all", level0, busy, cnt.n.Load())
}

// Retention deletes nothing when what it would keep is not whole from the
// oldest kept snapshot.
func TestRetainWhole(t *testing.T) {
	now := time.Now()
	old, young := now.Add(-48*time.Hour), now.Add(-time.Minute)
	file := func(level int, min, max ltx.TXID, at time.Time) remoteFile {
		return remoteFile{level: level, min: min, max: max, key: levelKey("p", level, min, max), modified: at}
	}
	r := &Replica{sched: defaultSchedule(24 * time.Hour)}
	r.files = []remoteFile{
		file(0, 14, 14, young), file(0, 15, 15, young),
		file(1, 1, 3, old), file(1, 4, 12, young), // 13 is missing
		file(2, 1, 3, old),
		file(snapshotLevel, 1, 5, old), file(snapshotLevel, 1, 10, now.Add(-30*time.Hour)), // the oldest kept
	}
	before := len(r.files)
	if _, err := r.retain(context.Background(), now); err == nil || !strings.Contains(err.Error(), "not be whole") {
		t.Fatalf("retain with a gap after the oldest kept snapshot: %v", err)
	}
	if len(r.files) != before {
		t.Fatalf("retain deleted %d files", before-len(r.files))
	}
	for _, tc := range []struct {
		from  ltx.TXID
		end   ltx.TXID
		whole bool
	}{{10, 12, false}, {12, 12, false}, {0, 12, false}} {
		if end, whole := chainEnd(r.files[:5], tc.from); end != tc.end || whole != tc.whole {
			t.Fatalf("chainEnd from %s: %s %t, want %s %t", tc.from, end, whole, tc.end, tc.whole)
		}
	}
	if end, whole := chainEnd(append(r.files[:5:5], file(0, 13, 13, young)), 10); end != 15 || !whole {
		t.Fatalf("chainEnd without the gap: %s %t", end, whole)
	}
}

// deleteStore is a Replica on a store that takes DELETE requests only, and
// the keys deleted.
func deleteStore(t *testing.T) (*Replica, *[]string) {
	t.Helper()
	var mu sync.Mutex
	deleted := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete {
			t.Errorf("%s %s", req.Method, req.URL)
		}
		mu.Lock()
		*deleted = append(*deleted, strings.TrimPrefix(req.URL.Path, "/bucket/"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	cfg := Config{
		URL:         "s3://bucket/p?endpoint=" + url.QueryEscape(srv.URL),
		Credentials: StaticCredentials(Credentials{AccessKeyID: "key", SecretAccessKey: "secret"}),
		AllowHTTP:   true,
	}
	c, _, err := cfg.client()
	if err != nil {
		t.Fatal(err)
	}
	return &Replica{client: c, sched: defaultSchedule(24 * time.Hour)}, deleted
}

func testFile(level int, min, max ltx.TXID, at time.Time) remoteFile {
	return remoteFile{level: level, min: min, max: max, key: levelKey("p", level, min, max), modified: at}
}

// The level 0 files a level 1 file holds go together, once it is older than
// keepL0, but the last. Until then a restore to a time inside it needs them
// all.
func TestDropLevel0(t *testing.T) {
	r, deleted := deleteStore(t)
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	r.files = []remoteFile{
		testFile(0, 1, 1, ago(time.Hour)), testFile(0, 2, 2, ago(time.Hour)), testFile(0, 3, 3, ago(40*time.Minute)),
		testFile(0, 4, 4, ago(time.Hour)), testFile(0, 5, 5, ago(time.Hour)), testFile(0, 6, 6, ago(time.Minute)),
		testFile(0, 7, 7, ago(time.Hour)), testFile(0, 8, 8, ago(time.Hour)),
		testFile(1, 1, 3, ago(40*time.Minute)),
		testFile(1, 4, 6, ago(time.Minute)), // 4 and 5 are old, but 6 is not
		testFile(1, 7, 8, ago(time.Hour)),   // 8 is the last
	}
	next, err := r.dropLevel0(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{levelKey("p", 0, 1, 1), levelKey("p", 0, 2, 2), levelKey("p", 0, 3, 3), levelKey("p", 0, 7, 7)}
	if fmt.Sprint(*deleted) != fmt.Sprint(want) {
		t.Fatalf("deleted %v, want %v", *deleted, want)
	}
	if !next.Equal(ago(time.Minute).Add(r.sched.keepL0)) {
		t.Fatalf("next at %v, want when level 1 file 4-6 is old enough", next)
	}
	if len(r.files) != 7 {
		t.Fatalf("%d files left", len(r.files))
	}
}

// Between the drops of level 0, the replica restores each time after a
// commit to that commit, and after the drop to a state the database had.
func TestDropLevel0Restore(t *testing.T) {
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	tick := func(d time.Duration) {
		mu.Lock()
		clock = clock.Add(d)
		mu.Unlock()
	}
	cfg := testConfig(t)
	cfg.schedule = func(s *schedule) { s.levels, s.now = nil, now }
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	syncNow(t, r)
	dumps := map[uint64]string{r.Status().Uploaded: dump(t, name)}
	compactNow(t, r, 0, 1)
	var points []point
	for i := range 6 {
		tick(10 * time.Second)
		run(t, db, fmt.Sprintf("INSERT INTO t VALUES (randomblob(%d))", 300+i*100))
		syncNow(t, r)
		time.Sleep(3 * time.Millisecond)
		p := point{txid: r.Status().Uploaded, at: time.Now(), dump: dump(t, name)}
		points, dumps[p.txid] = append(points, p), p.dump
		time.Sleep(3 * time.Millisecond)
	}
	compactNow(t, r, 0, 1) // the inserts, a minute after the first file of level 1
	drop := func() {
		r.busy <- struct{}{}
		defer func() { <-r.busy }()
		if _, err := r.dropLevel0(context.Background(), now()); err != nil {
			t.Fatal(err)
		}
	}
	cfg.schedule = nil
	// The first inserts are older than keepL0, the level 1 file of all of
	// them is not.
	tick(r.sched.keepL0 - 30*time.Second)
	drop()
	for _, p := range points {
		if out, txid := restoreTo(t, cfg, RestoreOptions{At: p.at}); txid != p.txid || dump(t, out) != p.dump {
			t.Fatalf("restored TXID %d at the time after TXID %d", txid, p.txid)
		}
	}
	tick(time.Minute)
	drop()
	for _, p := range points {
		if out, txid := restoreTo(t, cfg, RestoreOptions{At: p.at}); txid > p.txid || dump(t, out) != dumps[txid] {
			t.Fatalf("restored TXID %d at the time after TXID %d", txid, p.txid)
		}
	}
}

// Retention keeps the snapshot a restore to retention ago starts from, and
// the files the next level does not hold yet.
func TestRetainKeeps(t *testing.T) {
	r, deleted := deleteStore(t)
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	r.files = []remoteFile{
		testFile(0, 9, 9, ago(30*time.Hour)), testFile(0, 10, 10, ago(30*time.Hour)),
		testFile(0, 11, 11, ago(2*time.Hour)), testFile(0, 12, 12, ago(time.Hour)),
		testFile(1, 1, 8, ago(30*time.Hour)),
		testFile(2, 1, 8, ago(30*time.Hour)),
		testFile(snapshotLevel, 1, 5, ago(50*time.Hour)),
		testFile(snapshotLevel, 1, 10, ago(26*time.Hour)), // the newest before retention
		testFile(snapshotLevel, 1, 12, ago(time.Hour)),
	}
	next, err := r.retain(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	// Level 0 up to 10 stays: level 1 does not hold it yet.
	want := []string{levelKey("p", 1, 1, 8), levelKey("p", 2, 1, 8), levelKey("p", snapshotLevel, 1, 5)}
	if fmt.Sprint(*deleted) != fmt.Sprint(want) {
		t.Fatalf("deleted %v, want %v", *deleted, want)
	}
	if !next.Equal(ago(time.Hour).Add(r.sched.retention)) {
		t.Fatalf("next at %v, want when the newest snapshot is retention old", next)
	}
}

// Compaction follows level 0 past files that other files there hold or
// overlap, as after uploads whose answer was lost, and merges once.
func TestCompactOverlap(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	for i := range 6 {
		run(t, db, fmt.Sprintf("INSERT INTO t VALUES (randomblob(%d))", 300+i*100), "UPDATE t SET x = randomblob(50) WHERE rowid = 1")
		syncNow(t, r)
	}
	r.busy <- struct{}{}
	var l0 []remoteFile
	for _, f := range r.files {
		if f.level == 0 {
			l0 = append(l0, f)
		}
	}
	if len(l0) < 6 {
		<-r.busy
		t.Fatalf("%d files in level 0", len(l0))
	}
	// Files 2 to 4 and 3 to 5 of level 0, merged as compaction would.
	dir := t.TempDir()
	put := func(lo, hi int) {
		var in []io.Reader
		for _, f := range l0[lo : hi+1] {
			file, _, _, err := r.download(ctx, f, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			in = append(in, file)
		}
		out := filepath.Join(dir, ltx.FormatFilename(l0[lo].min, l0[hi].max))
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		comp, err := ltx.NewCompactor(f, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := comp.Compact(ctx); err != nil {
			t.Fatal(err)
		}
		if err := r.putFile(ctx, 0, out); err != nil {
			t.Fatal(err)
		}
	}
	put(1, 3)
	put(2, 4)
	<-r.busy
	compactNow(t, r, 0, 1)
	var l1 []remoteFile
	for _, f := range r.files {
		if f.level == 1 {
			l1 = append(l1, f)
		}
	}
	if len(l1) != 1 || l1[0].min != l0[0].min || l1[0].max != r.last(0) {
		t.Fatalf("level 1 holds %v, want one file of TXIDs %s to %s", l1, l0[0].min, r.last(0))
	}
	r.busy <- struct{}{}
	did, err := r.compact(ctx, 0, 1)
	<-r.busy
	if err != nil || did {
		t.Fatalf("compacted again: %t %v", did, err)
	}
	cfg.schedule = nil
	out, txid := restoreTo(t, cfg, RestoreOptions{})
	if txid != uint64(r.last(1)) || dump(t, out) != dump(t, name) {
		t.Fatal("the replica does not restore the database")
	}
}

// What a compaction killed on the way left is removed when the next New
// starts.
func TestCompactLeftovers(t *testing.T) {
	// A name that is no glob pattern.
	dir := filepath.Join(t.TempDir(), "a[b*")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "x.db")
	ltxDir := filepath.Join(name+"-replica", "ltx")
	left := []string{filepath.Join(ltxDir, "compact"), filepath.Join(ltxDir, "compact-123")}
	for _, d := range left {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "0000000000000001-0000000000000009.ltx"), []byte("left"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	newReplica(t, name, testConfig(t))
	for _, d := range left {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is left (%v)", d, err)
		}
	}
}

// The files of a compaction are copies of the database: only the user of
// the process reads them.
func TestCompactDirMode(t *testing.T) {
	dir := capture.CompactDir(filepath.Join(t.TempDir(), "x.db"))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makeCompactDir(dir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, want 0700", st.Mode().Perm())
	}
}

// The leftovers of a compaction are found where the staging is, also when
// the database is named through a link.
func TestCompactLeftoversLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need rights on windows")
	}
	dir := t.TempDir()
	real, link := filepath.Join(dir, "real.db"), filepath.Join(dir, "link.db")
	db := openDB(t, real)
	run(t, db, "CREATE TABLE t(x)")
	db.Close()
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(real+"-replica", "ltx", "compact")
	if err := os.MkdirAll(left, 0o755); err != nil {
		t.Fatal(err)
	}
	r := newReplica(t, link, testConfig(t))
	want, err := filepath.EvalSymlinks(filepath.Dir(left))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.c.Dir(), want) {
		t.Skipf("the staging is %s: the link is not followed", r.c.Dir())
	}
	if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s is left (%v)", left, err)
	}
}
