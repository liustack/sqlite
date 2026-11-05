// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	// Back: what was staged goes up, then the full copy that follows it.
	deadline := time.Now().Add(30 * time.Second)
	for {
		syncErr := r.Sync(context.Background())
		s := r.Status()
		if syncErr == nil && s.Uploaded == s.Staged && s.Pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not caught up: %+v, %v", s, syncErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	check("back", 1)
	out, txid := restoreTo(t, cfg, RestoreOptions{})
	if txid != r.Status().Uploaded || dump(t, out) != dump(t, name) {
		t.Fatal("the replica does not restore the database")
	}
}
