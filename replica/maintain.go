// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/superfly/ltx"
)

// The work on the replica besides uploads, and when it runs. Tests change
// it.
type schedule struct {
	now func() time.Time
	// Level i+1 is compacted from level i every levels[i], at multiples
	// of it: 1 minute, then 1 hour.
	levels []time.Duration
	// keepL0 is how long the level 0 files a level 1 file holds stay after
	// it, for restores to a time in between.
	keepL0 time.Duration
	// A snapshot is taken every snapshots, at multiples of it, when the
	// database changed since the last.
	snapshots time.Duration
	// Snapshots older than retention go, but the newest, and with them
	// the files only they need.
	retention time.Duration
	// keepProbe is how long the objects of a probe that did not finish
	// stay.
	keepProbe time.Duration
	// recheck is how old the last check that the replica ends where this
	// replicator uploaded last may be before an upload. keepL0 is longer
	// than it and the 15 minutes a signed request is good for, so that a
	// replicator that was cut off for a while most often finds the level 0
	// files of another. It is a check, not a fence (see recheck).
	recheck time.Duration
}

func defaultSchedule(retention time.Duration) schedule {
	return schedule{
		now:       time.Now,
		levels:    []time.Duration{time.Minute, time.Hour},
		keepL0:    30 * time.Minute,
		snapshots: min(retention, 24*time.Hour),
		retention: retention,
		keepProbe: time.Hour,
		recheck:   5 * time.Minute,
	}
}

// maintain compacts, takes a snapshot and deletes what retention lets go,
// as far as each is due, and returns when it should run again: zero when
// nothing is due until the database changes. Call it holding busy.
func (r *Replica) maintain(ctx context.Context) (time.Time, error) {
	now := r.sched.now()
	var next time.Time
	soon := func(t time.Time) {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	for i, every := range r.sched.levels {
		from, to := i, i+1
		if r.last(from) <= r.last(to) {
			continue
		}
		due := r.newest(to).Truncate(every).Add(every)
		if now.Before(due) {
			soon(due)
			continue
		}
		did, err := r.compact(ctx, from, to)
		if err != nil {
			return time.Time{}, err
		}
		if did && r.last(from) > r.last(to) {
			// More than one compaction holds.
			soon(now)
		}
	}
	if t, err := r.dropLevel0(ctx, now); err != nil {
		return time.Time{}, err
	} else if !t.IsZero() {
		soon(t)
	}
	if t, err := r.snapshot(ctx, now); err != nil {
		return time.Time{}, err
	} else if !t.IsZero() {
		soon(t)
	}
	if t, err := r.retain(ctx, now); err != nil {
		return time.Time{}, err
	} else if !t.IsZero() {
		soon(t)
	}
	return next, nil
}

// newest is when the store got the newest file of level, zero for none.
func (r *Replica) newest(level int) time.Time {
	var t time.Time
	for _, f := range r.files {
		if f.level == level && f.modified.After(t) {
			t = f.modified
		}
	}
	return t
}

// last is the last TXID of level.
func (r *Replica) last(level int) ltx.TXID {
	var txid ltx.TXID
	for _, f := range r.files {
		if f.level == level {
			txid = max(txid, f.max)
		}
	}
	return txid
}

// remove deletes the files of the replica drop says, and takes them out of
// r.files.
func (r *Replica) remove(ctx context.Context, drop func(remoteFile) bool) error {
	kept := r.files[:0:0]
	var err error
	for _, f := range r.files {
		if err == nil && drop(f) {
			if err = r.client.Delete(ctx, f.key); err == nil {
				continue
			}
		}
		kept = append(kept, f)
	}
	r.files = kept
	return err
}

// dropLevel0 deletes the level 0 files a level 1 file holds, all of them
// at once when it is older than keepL0, but the last level 0 file. A
// restore to a time inside a level 1 file needs every level 0 file it
// holds, so they never go one by one. It returns when the next can go.
func (r *Replica) dropLevel0(ctx context.Context, now time.Time) (time.Time, error) {
	l0 := r.last(0)
	holds := func(l1, f remoteFile) bool {
		return f.level == 0 && f.max != l0 && f.min >= l1.min && f.max <= l1.max
	}
	var next time.Time
	var old []remoteFile
	for _, l1 := range r.files {
		if l1.level != 1 {
			continue
		}
		if t := l1.modified.Add(r.sched.keepL0); !now.Before(t) {
			old = append(old, l1)
		} else if (next.IsZero() || t.Before(next)) && slices.ContainsFunc(r.files, func(f remoteFile) bool { return holds(l1, f) }) {
			next = t
		}
	}
	err := r.remove(ctx, func(f remoteFile) bool {
		return slices.ContainsFunc(old, func(l1 remoteFile) bool { return holds(l1, f) })
	})
	return next, err
}

// snapshot takes a snapshot and uploads it, when one is due: the database
// changed since the last snapshot, and a multiple of the snapshot interval
// passed since it, or since the replica's first file when there is none.
// It returns when the next is due, zero until the database changes.
func (r *Replica) snapshot(ctx context.Context, now time.Time) (time.Time, error) {
	if len(r.files) == 0 {
		return time.Time{}, nil
	}
	snapped := r.last(snapshotLevel)
	since := r.newest(snapshotLevel) // else the replica's first file
	if snapped == 0 {
		since = r.files[0].modified
		for _, f := range r.files {
			if f.modified.Before(since) {
				since = f.modified
			}
		}
	}
	if r.c.Durable() <= snapped && r.snap == "" {
		return time.Time{}, nil
	}
	every := r.sched.snapshots
	if due := since.Truncate(every).Add(every); now.Before(due) {
		return due, nil
	}
	if r.snap == "" {
		name, txid, err := r.c.Snapshot(ctx)
		if err != nil {
			return time.Time{}, err
		}
		r.snap, r.snapTXID = name, txid
	}
	// The files up to the snapshot go first: the replica never ends at a
	// snapshot whose level 0 files are not there.
	if err := r.uploadFiles(ctx); err != nil {
		return time.Time{}, err
	}
	if r.uploaded() < r.snapTXID {
		return time.Time{}, fmt.Errorf("replica: the snapshot of TXID %s waits for its files", r.snapTXID)
	}
	if err := r.putFile(ctx, snapshotLevel, r.snap); err != nil {
		return time.Time{}, err
	}
	if err := os.Remove(r.snap); err != nil {
		return time.Time{}, err
	}
	r.snap, r.snapTXID = "", 0
	// Daily or so: the objects of probes that did not finish go.
	return time.Time{}, r.dropProbes(ctx, now)
}

// retain keeps what restores every point of the last retention: the
// newest snapshot made before it, and what comes after. It deletes the
// snapshots before that one, and the files of the other levels that end at
// or before it and that the next level holds, once it checks that the kept
// files restore every point from it to the end. It returns when it has
// more to delete.
func (r *Replica) retain(ctx context.Context, now time.Time) (time.Time, error) {
	var snaps []remoteFile
	for _, f := range r.files {
		if f.level == snapshotLevel {
			snaps = append(snaps, f)
		}
	}
	if len(snaps) < 2 {
		return time.Time{}, nil
	}
	// A restore to retention ago starts from the newest snapshot made by
	// then: it is the oldest kept, and the ones before it go. The listing
	// has the snapshots in TXID order.
	line := now.Add(-r.sched.retention)
	k := -1
	for i, s := range snaps {
		if !s.modified.After(line) {
			k = i
		}
	}
	var next time.Time // when the snapshot after it is old enough to take its place
	if k+1 < len(snaps) {
		next = snaps[k+1].modified.Add(r.sched.retention)
	}
	if k <= 0 {
		return next, nil
	}
	oldest := snaps[k]
	lastOf := map[int]ltx.TXID{}
	for _, f := range r.files {
		lastOf[f.level] = max(lastOf[f.level], f.max)
	}
	drop := func(f remoteFile) bool {
		if f.level == snapshotLevel {
			return f.max < oldest.max
		}
		// A file the next level does not hold yet stays for its compaction.
		if f.level < len(r.sched.levels) && f.max > lastOf[f.level+1] {
			return false
		}
		return f.max <= oldest.max
	}
	var kept []remoteFile
	for _, f := range r.files {
		if f.level != snapshotLevel && !drop(f) {
			kept = append(kept, f)
		}
	}
	want := oldest.max
	for _, f := range kept {
		want = max(want, f.max)
	}
	if end, whole := chainEnd(kept, oldest.max); !whole || end != want {
		return time.Time{}, fmt.Errorf("replica: retention kept nothing: without the files of TXIDs up to %s, the replica would not be whole from the snapshot of TXID %s", oldest.max, oldest.max)
	}
	return next, r.remove(ctx, drop)
}

// chainEnd follows the files from TXID from, each the one that goes on
// and reaches furthest, and returns where they end, and whether no file
// is left after a gap.
func chainEnd(files []remoteFile, from ltx.TXID) (ltx.TXID, bool) {
	end := from
	for {
		var best ltx.TXID
		for _, f := range files {
			if f.min <= end+1 && f.max > end {
				best = max(best, f.max)
			}
		}
		if best == 0 {
			break
		}
		end = best
	}
	for _, f := range files {
		if f.min > end+1 {
			return end, false
		}
	}
	return end, true
}

// dropProbes deletes the objects of probes older than keepProbe.
func (r *Replica) dropProbes(ctx context.Context, now time.Time) error {
	var errs []error
	for o, err := range r.client.List(ctx, r.path+"/probe/", "") {
		if err != nil {
			return err
		}
		if now.Sub(o.LastModified) > r.sched.keepProbe {
			errs = append(errs, r.client.Delete(ctx, o.Key))
		}
	}
	return errors.Join(errs...)
}
