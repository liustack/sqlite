// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package replica

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liustack/sqlite/internal/powerloss"
	"github.com/liustack/sqlite/internal/vfs"
	"github.com/liustack/sqlite/replica/internal/capture"
	"github.com/liustack/sqlite/replica/internal/s3"
)

// TestPowerLoss plays a power loss on a machine that replicates a database.
// One log keeps, in the order they happened, everything the VFS does to the
// files of the database (vfs.SetTrace), everything the replicator does to
// its staged files (capture.SetTrace) and every write and delete the store
// answered. Then, for many moments of the log, the test builds what a power
// loss at that moment could leave: the files of the database and the staged
// files as internal/powerloss gives them, and the store with the objects it
// had answered for, and of the uploads under way each or not.
//
// TestKill kills a process, which loses nothing the process wrote. This
// test checks that each sync of the replicator comes before what relies on
// it: the sync of a staged file before its name, the sync of the names
// before an upload and before a checkpoint.

type plKind int

const (
	plDB         plKind = iota // db: a change of a file of the database
	plStage                    // stage: a change of the staging folder
	plPut                      // obj: the store got an upload
	plPutDone                  // obj: the store answered that it has the object
	plPutThere                 // obj: the store refused it, as the key has an object
	plPutFail                  // obj: the store refused it otherwise, or the upload broke
	plDelete                   // obj.key: the store got a delete
	plDeleted                  // obj.key: the store answered that the object is gone
	plDeleteFail               // obj.key: the store refused it, or the delete broke
	plAck                      // n: the commit of transaction n returned
	plSynced                   // n: a Sync that started after the commit of n returned
)

// plObject is an object of the replica. key is what follows the path of
// the replica in its key.
type plObject struct {
	key  string
	body []byte
	meta map[string]string
}

type plEntry struct {
	kind  plKind
	db    vfs.Change
	stage capture.Change
	data  []byte // of the staged file, for capture.ChangeWritten
	obj   *plObject
	n     int
}

func (e plEntry) String() string {
	switch e.kind {
	case plDB:
		return fmt.Sprintf("database: %s %s at %d, %d bytes", plDBNames[e.db.Kind], filepath.Base(e.db.Path), e.db.Off, len(e.db.Data))
	case plStage:
		s := fmt.Sprintf("staging: %s %s", plStageNames[e.stage.Kind], filepath.Base(e.stage.Path))
		if e.stage.Kind == capture.ChangeRename {
			s += " to " + filepath.Base(e.stage.To)
		}
		return s
	case plPut:
		return "store: upload of " + e.obj.key + " starts"
	case plPutDone:
		return "store: has " + e.obj.key
	case plPutThere:
		return "store: no upload of " + e.obj.key + ", which is there"
	case plPutFail:
		return "store: no upload of " + e.obj.key
	case plDelete:
		return "store: delete of " + e.obj.key + " starts"
	case plDeleted:
		return "store: deleted " + e.obj.key
	case plDeleteFail:
		return "store: no delete of " + e.obj.key
	case plAck:
		return fmt.Sprintf("commit %d returned", e.n)
	}
	return fmt.Sprintf("Sync returned, after commit %d", e.n)
}

var (
	plDBNames    = [...]string{"create", "write", "truncate", "sync", "sync the directory of", "delete"}
	plStageNames = [...]string{"create", "wrote", "sync", "rename", "remove", "sync the folder"}
)

// plLog is the log of a run.
type plLog struct {
	mu  sync.Mutex
	log []plEntry
}

func (l *plLog) add(e plEntry) {
	l.mu.Lock()
	l.log = append(l.log, e)
	l.mu.Unlock()
}

// plRecord starts the log of the database in dir and of its staging folder
// stage, until stop.
func plRecord(t *testing.T, dir, stage string) (l *plLog, stop func()) {
	l = &plLog{}
	prefix := dir + string(filepath.Separator)
	vfs.SetTrace(func(c vfs.Change) {
		if filepath.Dir(c.Path)+string(filepath.Separator) == prefix {
			l.add(plEntry{kind: plDB, db: c})
		}
	})
	capture.SetTrace(func(c capture.Change) {
		at := c.Path
		if c.Kind != capture.ChangeDirSync {
			at = filepath.Dir(at)
		}
		if at != stage {
			return // a folder above, synced when the folders are made
		}
		e := plEntry{kind: plStage, stage: c}
		if c.Kind == capture.ChangeWritten {
			b, err := os.ReadFile(c.Path)
			if err != nil {
				t.Error(err)
			}
			e.data = b
		}
		l.add(e)
	})
	stop = func() {
		vfs.SetTrace(nil)
		capture.SetTrace(nil)
	}
	t.Cleanup(stop)
	return l, stop
}

// plStore puts a proxy before the store of cfg that writes each upload and
// delete of the replica into l, before the replicator gets the answer.
func plStore(t *testing.T, cfg Config, l *plLog) Config {
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
	base := "/" + u.Host + u.Path + "/" // of the keys of the replica, as paths
	proxy := httputil.NewSingleHostReverseProxy(target)
	// The request is signed for the proxy's host, which the store gets.
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		host := req.Host
		director(req)
		req.Host = host
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet || req.Method == http.MethodHead {
			proxy.ServeHTTP(w, req)
			return
		}
		key, ok := strings.CutPrefix(req.URL.Path, base)
		if !ok || req.URL.RawQuery != "" || req.Method != http.MethodPut && req.Method != http.MethodDelete {
			t.Errorf("the test does not follow %s %s", req.Method, req.URL)
			http.Error(w, "not followed", http.StatusNotImplemented)
			return
		}
		obj := &plObject{key: key}
		if req.Method == http.MethodPut {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				// The replicator gave the upload up before its end, as
				// Close does to a compaction: the store never gets it.
				return
			}
			obj.body = body
			req.Body = io.NopCloser(bytes.NewReader(obj.body))
			obj.meta = map[string]string{}
			for name, v := range req.Header {
				if m, ok := strings.CutPrefix(name, "X-Amz-Meta-"); ok {
					obj.meta[strings.ToLower(m)] = v[0]
				}
			}
			l.add(plEntry{kind: plPut, obj: obj})
		} else {
			l.add(plEntry{kind: plDelete, obj: obj})
		}
		// The store gets the whole request even when the replicator gives
		// it up, so that the log knows what became of it. The answer waits
		// for the log: what the replicator does once it knows comes after
		// it there.
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req.WithContext(context.WithoutCancel(req.Context())))
		ok = rec.Code/100 == 2
		switch {
		case req.Method == http.MethodDelete && ok:
			l.add(plEntry{kind: plDeleted, obj: obj})
		case req.Method == http.MethodDelete:
			l.add(plEntry{kind: plDeleteFail, obj: obj})
		case req.Method == http.MethodPut && ok:
			l.add(plEntry{kind: plPutDone, obj: obj})
		case req.Method == http.MethodPut && rec.Code == http.StatusPreconditionFailed:
			l.add(plEntry{kind: plPutThere, obj: obj})
		case req.Method == http.MethodPut:
			l.add(plEntry{kind: plPutFail, obj: obj})
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(srv.Close)
	q.Set("endpoint", srv.URL)
	u.RawQuery = q.Encode()
	cfg.URL = u.String()
	// The requests are signed for the proxy's host: a store that checks the
	// host a request was signed for, as R2 and AWS do, refuses them.
	c, path := client(t, cfg)
	for _, err := range c.List(context.Background(), path+"/", "") {
		var e *s3.Error
		if errors.As(err, &e) && e.StatusCode == http.StatusForbidden {
			t.Skipf("goes through a proxy, which this store refuses: %v", err)
		} else if err != nil {
			t.Fatal(err)
		}
		break
	}
	return cfg
}

// plState is the machine and the store at a moment of the log.
type plState struct {
	db, stage *powerloss.Disk
	store     map[string]*plObject
	going     map[*plObject]bool // the uploads under way
	leaving   map[string]int     // the deletes under way of each key
	acked     int                // the last commit that returned, -1 for none
	synced    int                // the last commit a Sync returned after
}

// apply is entry seq of the log.
func (s *plState) apply(seq int, e plEntry) (err error) {
	switch e.kind {
	case plDB:
		return s.db.Apply(seq, e.db)
	case plStage:
		name := filepath.Base(e.stage.Path)
		switch e.stage.Kind {
		case capture.ChangeCreate:
			s.stage.Create(name)
			err = s.stage.Truncate(seq, name, 0)
		case capture.ChangeWritten:
			err = s.stage.Write(seq, name, 0, e.data)
		case capture.ChangeSync:
			err = s.stage.Sync(name)
		case capture.ChangeRename:
			err = s.stage.Rename(name, filepath.Base(e.stage.To))
		case capture.ChangeRemove:
			err = s.stage.Delete(name)
		case capture.ChangeDirSync:
			s.stage.SyncDir()
		}
		if err != nil {
			return fmt.Errorf("entry %d: %v: %w", seq, e, err)
		}
	case plPut:
		s.going[e.obj] = true
	case plPutDone:
		delete(s.going, e.obj)
		s.store[e.obj.key] = e.obj
	case plPutThere:
		// The key has an object. When the log knows of none, it is the one
		// of an upload under way, which the store has and has not answered
		// for yet: the replicator takes its own file for uploaded from here.
		delete(s.going, e.obj)
		for o := range s.going {
			if o.key == e.obj.key && s.store[o.key] == nil {
				s.store[o.key] = o
			}
		}
		if s.store[e.obj.key] == nil {
			return fmt.Errorf("entry %d: %v, and the log knows of no upload of it", seq, e)
		}
	case plPutFail:
		delete(s.going, e.obj)
	case plDelete:
		s.leaving[e.obj.key]++
	case plDeleted:
		s.leaving[e.obj.key]--
		delete(s.store, e.obj.key)
	case plDeleteFail:
		s.leaving[e.obj.key]--
	case plAck:
		s.acked = e.n
	case plSynced:
		s.synced = e.n
	}
	return nil
}

// plPoints is how many moments of the log the test plays a power loss at:
// POWERLOSS_POINTS, or 15, or 5 with -short. Each is played four ways.
func plPoints() int {
	if n, err := strconv.Atoi(os.Getenv("POWERLOSS_POINTS")); err == nil && n > 0 {
		return n
	}
	if testing.Short() {
		return 5
	}
	return 15
}

const plKept = 20 // a transaction deletes the rows of the one plKept before it

// plBlob is the n bytes of row i of transaction txn.
func plBlob(txn, i, n int) []byte {
	rng := rand.New(rand.NewPCG(uint64(txn), uint64(i)))
	b := make([]byte, n)
	for j := range b {
		b[j] = byte(rng.Uint32())
	}
	return b
}

// plCommit runs transaction n: three rows, less the rows of an older one.
func plCommit(db *sql.DB, n int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range 3 {
		if _, err := tx.Exec(`INSERT INTO t VALUES (?, ?, ?)`, n, i, plBlob(n, i, 1500)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM t WHERE txn = ?`, n-plKept); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE last SET n = ?`, n); err != nil {
		return err
	}
	return tx.Commit()
}

// plCheck says what is wrong with the database name, or the transaction it
// is at: it is sound and holds the transactions up to that one, each whole
// with the bytes it wrote, and none after it.
func plCheck(name string) (last int, err error) {
	db, err := sql.Open("sqlite", "file:"+name)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil {
		return 0, fmt.Errorf("integrity_check: %w", err)
	} else if ok != "ok" {
		return 0, fmt.Errorf("integrity_check: %s", ok)
	}
	if err := db.QueryRow(`SELECT n FROM last`).Scan(&last); err != nil {
		return 0, err
	}
	rows, err := db.Query(`SELECT txn, i, v FROM t ORDER BY txn, i`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	// The next row: i of transaction txn.
	txn, i := max(last-plKept+1, 0), 0
	for rows.Next() {
		var gotTxn, gotI int
		var v []byte
		if err := rows.Scan(&gotTxn, &gotI, &v); err != nil {
			return 0, err
		}
		if gotTxn != txn || gotI != i {
			return 0, fmt.Errorf("at transaction %d: row %d of transaction %d, want row %d of %d", last, gotI, gotTxn, i, txn)
		}
		if !bytes.Equal(v, plBlob(txn, i, 1500)) {
			return 0, fmt.Errorf("at transaction %d: row %d of transaction %d has other bytes than it wrote", last, i, txn)
		}
		if i++; i == 3 {
			txn, i = txn+1, 0
		}
	}
	if txn != last+1 || i != 0 {
		return 0, fmt.Errorf("at transaction %d: the rows end before row %d of transaction %d", last, i, txn)
	}
	return last, rows.Err()
}

type plCase struct {
	name, pragmas string
	// durable: a commit that returned is not lost, and the database is
	// never behind the staged files, so no full copy follows a power loss.
	durable bool
	// maintain: the run takes two seconds, in which the replicator also
	// compacts into levels 1 and 2, takes snapshots, and deletes the level
	// 0 files compacted and what retention lets go.
	maintain bool
}

var plCases = []plCase{
	{"full", "_pragma=synchronous(FULL)", true, false},
	// The WAL is synced at a checkpoint: the commits since can be lost, and
	// with them what was staged and uploaded of them. The replicator then
	// uploads a full copy of the database as the next TXID.
	{"normal", "_pragma=synchronous(NORMAL)", false, false},
	{"full with compactions", "_pragma=synchronous(FULL)", true, true},
}

func TestPowerLoss(t *testing.T) {
	const txns = 90
	for _, tc := range plCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			// The probe of New uploads in parts, which the log does not
			// follow, and has nothing to do with a power loss.
			cfg.SingleWriter = true
			cfg.checkpointFrames = 30
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, "p.db")
			dsn := "file:" + name + "?" + tc.pragmas
			db, err := sql.Open("sqlite", dsn+"&_pragma=journal_mode(WAL)")
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`CREATE TABLE t(txn INTEGER, i INTEGER, v BLOB)`,
				`CREATE INDEX ti ON t(txn)`,
				`CREATE TABLE last(n INTEGER)`,
				`INSERT INTO last VALUES (-1)`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()

			// The run that is logged: the replicator starts on the database,
			// which has no replica yet, and a program writes. The staged
			// files go up in the background, and every few commits at a Sync.
			start := &plState{stage: powerloss.Empty(), store: map[string]*plObject{}, going: map[*plObject]bool{}, leaving: map[string]int{}, acked: -1, synced: -1}
			if start.db, err = powerloss.Start(dir); err != nil {
				t.Fatal(err)
			}
			l, stop := plRecord(t, dir, capture.StageDir(name))
			rcfg := plStore(t, cfg, l)
			rcfg.SyncInterval = 2 * time.Millisecond
			if tc.maintain {
				rcfg.Retention = 600 * time.Millisecond
				rcfg.schedule = func(s *schedule) {
					s.levels = []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}
					s.keepL0 = 200 * time.Millisecond
				}
			}
			ctx := context.Background()
			r, err := New(ctx, name, rcfg)
			if err != nil {
				t.Fatal(err)
			}
			if db, err = sql.Open("sqlite", dsn); err != nil {
				t.Fatal(err)
			}
			for n := range txns {
				if err := plCommit(db, n); err != nil {
					t.Fatal(err)
				}
				l.add(plEntry{kind: plAck, n: n})
				if tc.maintain {
					time.Sleep(25 * time.Millisecond)
				}
				if n%7 == 6 {
					if err := r.Sync(ctx); err != nil {
						t.Fatal(err)
					}
					l.add(plEntry{kind: plSynced, n: n})
				}
			}
			db.Close()
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			stop()
			if t.Failed() {
				t.FailNow()
			}
			// What the store got of each level, and what it deleted.
			var puts [snapshotLevel + 1]int
			deletes := 0
			for _, e := range l.log {
				switch e.kind {
				case plPutDone:
					if level, err := strconv.ParseUint(e.obj.key[:4], 16, 8); err == nil && level <= snapshotLevel {
						puts[level]++
					}
				case plDeleted:
					deletes++
				}
			}
			t.Logf("the store got %d files of level 0, %d of level 1, %d of level 2, %d snapshots and %d deletes", puts[0], puts[1], puts[2], puts[snapshotLevel], deletes)
			if tc.maintain && (puts[1] == 0 || puts[2] == 0 || puts[snapshotLevel] < 2 || deletes == 0) {
				t.Fatal("the run did not compact, take snapshots and delete")
			}
			plPlay(t, tc, cfg, start, l.log)
		})
	}
}

// plPlay plays a power loss at many moments of log, from the state start
// on. cfg is the store without the proxy: each power loss gets a replica of
// its own under the one of cfg.
func plPlay(t *testing.T, tc plCase, cfg Config, start *plState, log []plEntry) {
	t.Helper()
	seed := rand.Uint64()
	rng := rand.New(rand.NewPCG(seed, 1))
	// A third of the moments are anywhere in the log. The others come
	// shortly after the replicator or the store did something.
	var steps []int
	for i, e := range log {
		if e.kind == plStage || e.kind == plPut || e.kind == plPutDone || e.kind == plDelete || e.kind == plDeleted {
			steps = append(steps, i)
		}
	}
	if len(steps) < 50 {
		t.Fatalf("the replicator and the store did %d things", len(steps))
	}
	at := map[int]bool{len(log): true}
	for i := range plPoints() {
		k := 1 + rng.IntN(len(log))
		if i%3 != 0 {
			k = min(steps[rng.IntN(len(steps))]+1+rng.IntN(4), len(log))
		}
		at[k] = true
	}
	dir := t.TempDir()
	played, copies := 0, 0
	for k := 1; k <= len(log); k++ {
		if err := start.apply(k-1, log[k-1]); err != nil {
			t.Fatal(err)
		}
		if !at[k] {
			continue
		}
		for kind := range powerloss.Kinds {
			played++
			sub := filepath.Join(dir, strconv.Itoa(played))
			n, err := plLose(t, tc, cfg, start, sub, kind, rng)
			copies += n
			if err != nil {
				for i := max(k-25, 0); i < k; i++ {
					t.Logf("  %d: %v", i, log[i])
				}
				t.Fatalf("power loss after entry %d of %d, %s (seed %d): %v", k-1, len(log), powerloss.KindNames[kind], seed, err)
			}
			os.RemoveAll(sub)
		}
	}
	t.Logf("%d entries, %d power losses played, %d full copies after them", len(log), played, copies)
}

// plLose builds in dir what a power loss of the given kind leaves of s, and
// checks what the replica and the replicator make of it. It gives the
// number of full copies the replicator staged.
func plLose(t *testing.T, tc plCase, cfg Config, s *plState, dir string, kind int, rng *rand.Rand) (copies int, err error) {
	t.Helper()
	name := filepath.Join(dir, "p.db")
	stage := capture.StageDir(name)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	for file, b := range s.db.Crash(kind, true, rng) {
		if err := os.WriteFile(filepath.Join(dir, file), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for file, b := range s.stage.Crash(kind, true, rng) {
		if err := os.WriteFile(filepath.Join(stage, file), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The store has what it answered for. An upload or a delete under way
	// arrived or did not.
	arrived := func() bool { return kind == powerloss.All || kind != powerloss.None && rng.IntN(2) == 0 }
	var objs []*plObject
	for _, key := range slices.Sorted(maps.Keys(s.store)) {
		if s.leaving[key] == 0 || !arrived() {
			objs = append(objs, s.store[key])
		}
	}
	going := slices.SortedFunc(maps.Keys(s.going), func(a, b *plObject) int { return strings.Compare(a.key, b.key) })
	for _, o := range going {
		if s.store[o.key] == nil && arrived() {
			objs = append(objs, o)
		}
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path += "/" + filepath.Base(dir)
	cfg.URL = u.String()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, path := client(t, cfg)
	for _, o := range objs {
		if err := c.Put(ctx, path+"/"+o.key, bytes.NewReader(o.body), int64(len(o.body)), s3.PutOptions{Metadata: o.meta}); err != nil {
			t.Fatal(err)
		}
	}
	// POWERLOSS_KEEP names a directory that gets the files of the power
	// loss that fails, as they were before they were opened.
	if keep := os.Getenv("POWERLOSS_KEEP"); keep != "" {
		os.RemoveAll(keep)
		if err := os.CopyFS(keep, os.DirFS(dir)); err != nil {
			t.Fatal(err)
		}
	}

	// The machine is gone: the replica alone restores a database at a
	// commit, with every commit a Sync returned after.
	gone := filepath.Join(dir, "gone.db")
	if _, err := Restore(ctx, gone, cfg, RestoreOptions{}); err == nil {
		last, err := plCheck(gone)
		if err != nil {
			return 0, fmt.Errorf("the replica alone: %w", err)
		}
		if last < s.synced {
			return 0, fmt.Errorf("the replica alone restores transaction %d, and a Sync returned after the commit of %d", last, s.synced)
		}
	} else if !errors.Is(err, ErrNoReplica) || s.synced >= 0 {
		return 0, fmt.Errorf("the replica alone: %w", err)
	}

	// The machine comes back: the replicator goes on from what is there,
	// the program writes, and the replica restores the database.
	var mu sync.Mutex
	var errs []error
	cfg.OnError = func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	r, err := New(ctx, name, cfg)
	if err != nil {
		return 0, fmt.Errorf("New: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			r.Close()
		}
	}()
	last, err := plCheck(name)
	if err != nil {
		return 0, err
	}
	if tc.durable && last < s.acked {
		return 0, fmt.Errorf("the database is at transaction %d, and the commit of %d returned", last, s.acked)
	}
	db, err := sql.Open("sqlite", "file:"+name+"?"+tc.pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := plCommit(db, last+1); err != nil {
		return 0, err
	}
	if err := r.Sync(ctx); err != nil {
		return 0, fmt.Errorf("Sync: %w", err)
	}
	copies = r.c.FullCopies()
	db.Close()
	closed = true
	if err := r.Close(); err != nil {
		return 0, fmt.Errorf("Close: %w", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, err := range errs {
		if !errors.Is(err, capture.ErrFullCopy) {
			return 0, fmt.Errorf("the replicator reported: %w", err)
		}
	}
	if tc.durable && copies != 0 {
		return 0, fmt.Errorf("%d full copies: %v", copies, errs)
	}
	back := filepath.Join(dir, "back.db")
	if _, err := Restore(ctx, back, cfg, RestoreOptions{}); err != nil {
		return 0, fmt.Errorf("Restore: %w", err)
	}
	got, err := plCheck(back)
	if err != nil {
		return 0, fmt.Errorf("the database restored: %w", err)
	}
	if got != last+1 {
		return 0, fmt.Errorf("the replica restores transaction %d, the database is at %d", got, last+1)
	}
	return copies, nil
}
