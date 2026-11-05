// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/capture"
)

// dirSize is the size of the files in dir.
func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			n += info.Size()
		}
	}
	return n
}

// While the store is down under writes, the writes take as long as before,
// the WAL and the staging folder stay bounded, requests back off, and once
// the store is back the replica restores to the database.
func TestOutage(t *testing.T) {
	cfg := testConfig(t)
	cfg, cnt := countingStore(t, cfg)
	cfg.SyncInterval = 20 * time.Millisecond
	cfg.StagingLimit = 2 << 20
	var stops atomic.Int64
	cfg.OnError = func(err error) {
		if errors.Is(err, ErrStagingFull) {
			stops.Add(1)
		}
		t.Logf("replica: %v", err)
	}
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")

	// Writers, each commit timed, in phases.
	var mu sync.Mutex
	phase := 0
	took := map[int][]time.Duration{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var once sync.Once
	stopWriters := func() {
		once.Do(func() { close(stop) })
		wg.Wait()
	}
	defer stopWriters()
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				start := time.Now()
				if _, err := db.Exec("INSERT INTO t VALUES (randomblob(2000))"); err != nil {
					t.Error(err)
					return
				}
				d := time.Since(start)
				mu.Lock()
				took[phase] = append(took[phase], d)
				mu.Unlock()
				time.Sleep(500 * time.Microsecond)
			}
		})
	}
	setPhase := func(p int) {
		mu.Lock()
		phase = p
		mu.Unlock()
	}
	// The WAL holds about CheckpointFrames (1000) frames, and what is
	// written until the next checkpoint, which takes longer under -race.
	// Without checkpoints, the outage alone writes more than this.
	walBound := int64(4000 * (4096 + 24))
	// Once the store is back, the full copy that follows what was staged
	// stays staged too, until the next change: one more database.
	check := func(when string, copies int64) {
		var db int64
		if st, err := os.Stat(name); err == nil {
			db = st.Size()
		}
		if s := r.Status(); s.Pending > cfg.StagingLimit+walBound {
			t.Fatalf("%s: %d bytes pending, the limit is %d", when, s.Pending, cfg.StagingLimit)
		}
		if n := dirSize(t, capture.StageDir(name)); n > cfg.StagingLimit+walBound+copies*db {
			t.Fatalf("%s: %d bytes staged, the limit is %d", when, n, cfg.StagingLimit)
		}
		if st, err := os.Stat(name + "-wal"); err == nil && st.Size() > walBound {
			t.Fatalf("%s: the WAL has %d bytes", when, st.Size())
		}
	}

	time.Sleep(time.Second)
	if n := stops.Load(); n != 0 {
		t.Fatalf("staging stopped %d times with the store up", n)
	}
	setPhase(1)
	before := cnt.n.Load()
	cnt.down.Store(1)
	for i := range 30 {
		if i == 15 {
			cnt.down.Store(2)
		}
		time.Sleep(100 * time.Millisecond)
		check("down", 0)
	}
	requests := cnt.n.Load() - before
	cnt.down.Store(0)
	setPhase(2)
	time.Sleep(time.Second)
	stopWriters()
	if t.Failed() {
		return
	}
	// The retries back off: a second, two, four.
	if requests > 8 {
		t.Fatalf("%d requests in 3s of a store that is down", requests)
	}
	if stops.Load() == 0 {
		t.Fatal("staging never stopped at the limit")
	}
	p99 := func(p int) time.Duration {
		d := slices.Clone(took[p])
		slices.Sort(d)
		return d[len(d)*99/100]
	}
	t.Logf("p99 of a commit: %v up (%d), %v down (%d), %v back (%d); %d requests while down",
		p99(0), len(took[0]), p99(1), len(took[1]), p99(2), len(took[2]), requests)
	if p99(1) > 3*p99(0)+5*time.Millisecond {
		t.Fatalf("p99 of a commit: %v with the store down, %v up", p99(1), p99(0))
	}
	// Back: a Sync uploads what was staged, then the full copy that follows
	// it, while the uploads in the background still wait to try again.
	if err := r.Sync(context.Background()); err != nil {
		t.Fatalf("Sync with the store back: %v", err)
	}
	if s := r.Status(); s.Uploaded != s.Staged || s.Pending != 0 {
		t.Fatalf("not caught up: %+v", s)
	}
	check("back", 1)
	out, txid := restoreTo(t, cfg, RestoreOptions{})
	if txid != r.Status().Uploaded || dump(t, out) != dump(t, name) {
		t.Fatal("the replica does not restore the database")
	}
}

// A Sync while staging is stopped uploads what is staged and goes on: it
// does not wait for the uploads in the background to try again.
func TestSyncStopped(t *testing.T) {
	cfg := testConfig(t)
	cfg, cnt := countingStore(t, cfg)
	cfg.StagingLimit = 1
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	syncNow(t, r)
	cnt.down.Store(1)
	run(t, db, "INSERT INTO t VALUES (1)")
	if err := r.Sync(context.Background()); err == nil {
		t.Fatal("Sync with the store down")
	}
	run(t, db, "INSERT INTO t VALUES (2)")
	if err := r.c.Sync(context.Background()); !errors.Is(err, ErrStagingFull) {
		t.Fatalf("staging past the limit: %v, want ErrStagingFull", err)
	}
	cnt.down.Store(0)
	syncNow(t, r)
	if s := r.Status(); s.Uploaded != s.Staged || s.Pending != 0 {
		t.Fatalf("not caught up: %+v", s)
	}
	sameAs(t, cfg, name)
}

// TestSoak runs for the time REPLICA_SOAK says (such as 10m), and is
// skipped without it: writers, a store that goes down and comes back at
// random, a small staging limit, and compaction, snapshots and retention
// every few seconds. Staging and the WAL stay bounded throughout, and at the
// end every point the replica still has restores to the database as it was.
func TestSoak(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv("REPLICA_SOAK"))
	if err != nil {
		t.Skip("REPLICA_SOAK is not a duration")
	}
	cfg := testConfig(t)
	cfg, cnt := countingStore(t, cfg)
	cfg.SyncInterval = 50 * time.Millisecond
	cfg.StagingLimit = 8 << 20
	cfg.Retention = 20 * time.Second
	var stops, copies atomic.Int64
	cfg.OnError = func(err error) {
		switch {
		case errors.Is(err, capture.ErrFullCopy):
			copies.Add(1)
		case errors.Is(err, ErrStagingFull):
			stops.Add(1)
		}
	}
	cfg.schedule = func(s *schedule) {
		s.levels = []time.Duration{time.Second, 5 * time.Second}
		s.keepL0 = 2 * time.Second
		s.snapshots = 10 * time.Second
	}
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	db.SetMaxOpenConns(8)
	run(t, db, "CREATE TABLE t(x)")

	var stop atomic.Bool
	var wg sync.WaitGroup
	var pause sync.RWMutex // held to write, taken alone to record a point
	for w := range 4 {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(w), 1))
			for !stop.Load() {
				pause.RLock()
				k := rng.IntN(5000)
				var q string
				switch rng.IntN(10) {
				case 0:
					q = fmt.Sprintf("DELETE FROM t WHERE rowid BETWEEN %d AND %d", k, k+20)
				case 1, 2:
					q = fmt.Sprintf("UPDATE t SET x = randomblob(%d) WHERE rowid %% 97 = %d", rng.IntN(800), k%97)
				default:
					q = fmt.Sprintf("INSERT OR REPLACE INTO t(rowid, x) VALUES (%d, randomblob(%d))", k, rng.IntN(3000))
				}
				_, err := db.Exec(q)
				pause.RUnlock()
				if err != nil {
					t.Error(err)
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	defer func() {
		stop.Store(true)
		wg.Wait()
	}()

	walBound := int64(4000 * (4096 + 24))
	points := map[ltx.TXID]string{}
	rng := rand.New(rand.NewPCG(7, 7))
	for start := time.Now(); time.Since(start) < d && !t.Failed(); {
		// The store goes down a quarter of the time, for up to 2 seconds.
		if rng.IntN(4) == 0 {
			cnt.down.Store(int32(1 + rng.IntN(2)))
		} else {
			cnt.down.Store(0)
		}
		end := time.Now().Add(time.Duration(rng.IntN(2000)) * time.Millisecond)
		for time.Now().Before(end) {
			time.Sleep(100 * time.Millisecond)
			if n := r.Status().Pending; n > cfg.StagingLimit+walBound {
				t.Fatalf("%d bytes pending, the limit is %d", n, cfg.StagingLimit)
			}
			if st, err := os.Stat(name + "-wal"); err == nil && st.Size() > walBound {
				t.Fatalf("the WAL has %d bytes", st.Size())
			}
		}
		// A point: the database at a staged TXID.
		func() {
			pause.Lock()
			defer pause.Unlock()
			if err := r.c.Sync(context.Background()); err == nil {
				points[r.c.Durable()] = dump(t, name)
			}
		}()
	}
	cnt.down.Store(0)
	stop.Store(true)
	wg.Wait()
	if t.Failed() {
		return
	}
	deadline := time.Now().Add(time.Minute)
	for {
		err := r.Sync(context.Background())
		if s := r.Status(); err == nil && s.Uploaded == s.Staged && s.Pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not caught up: %+v %v", r.Status(), err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	points[r.c.Durable()] = dump(t, name)
	r.busy <- struct{}{}
	files := slices.Clone(r.files)
	<-r.busy
	ends := map[ltx.TXID]bool{}
	var oldest ltx.TXID = 1
	for _, f := range files {
		ends[f.max] = true
		if f.level == snapshotLevel && (oldest == 1 || f.max < oldest) {
			oldest = f.max
		}
	}
	checked := 0
	for txid, want := range points {
		if txid < oldest || !ends[txid] {
			continue
		}
		out, got := restoreTo(t, cfg, RestoreOptions{TXID: uint64(txid)})
		if got != uint64(txid) || dump(t, out) != want {
			t.Fatalf("the restore of TXID %s", txid)
		}
		checked++
	}
	levels := map[int]int{}
	for _, f := range files {
		levels[f.level]++
	}
	t.Logf("%d points, %d restored, %d requests, %d stops at the limit, %d full copies, files by level %v",
		len(points), checked, cnt.n.Load(), stops.Load(), copies.Load(), levels)
	if checked == 0 {
		t.Fatal("no point restored")
	}
}
