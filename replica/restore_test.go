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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/capture"
	"github.com/liustack/sqlite/replica/internal/s3"
)

// restoreTo restores the replica of cfg into a new file and returns its
// name and the TXID restored.
func restoreTo(t *testing.T, cfg Config, opt RestoreOptions) (string, uint64) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "restored.db")
	txid, err := Restore(context.Background(), out, cfg, opt)
	if err != nil {
		t.Fatalf("Restore %+v: %v", opt, err)
	}
	return out, txid
}

// A point of the history of a database: what it held at a TXID, and a
// time after that TXID's file and before the next.
type point struct {
	txid uint64
	at   time.Time
	dump string
}

// history writes rounds to a replicated database, one uploaded file each,
// and returns the points after each. A VACUUM makes the database shrink.
func history(t *testing.T, cfg Config) (string, []point) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	var points []point
	for i, q := range []string{
		"CREATE TABLE t(x)",
		"WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM s WHERE i < 40) INSERT INTO t SELECT randomblob(3000) FROM s",
		"UPDATE t SET x = randomblob(100) WHERE rowid % 3 = 0",
		"DELETE FROM t WHERE rowid > 10",
		"VACUUM",
		"INSERT INTO t VALUES (zeroblob(70000))",
	} {
		run(t, db, q)
		syncNow(t, r)
		time.Sleep(2 * time.Millisecond)
		p := point{txid: r.Status().Uploaded, at: time.Now(), dump: dump(t, name)}
		points = append(points, p)
		if i > 0 && p.txid <= points[i-1].txid {
			t.Fatalf("round %d: TXID %d after %d", i, p.txid, points[i-1].txid)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return name, points
}

func TestRestore(t *testing.T) {
	cfg := testConfig(t)
	name, points := history(t, cfg)
	last := points[len(points)-1]

	out, txid := restoreTo(t, cfg, RestoreOptions{})
	if txid != last.txid {
		t.Fatalf("restored TXID %d, the replica ends at %d", txid, last.txid)
	}
	// Nothing is left beside it but its staging folder, which holds the
	// last file.
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if fmt.Sprint(names) != "[restored.db restored.db-replica]" {
		t.Fatalf("beside the restored database: %v", names)
	}
	staged, err := os.ReadDir(capture.StageDir(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 || staged[0].Name() != ltx.FormatFilename(ltx.TXID(txid), ltx.TXID(txid)) {
		t.Fatalf("staged: %v", staged)
	}
	if got, want := dump(t, out), dump(t, name); got != want {
		t.Fatalf("restored\n%s\nthe database\n%s", got, want)
	}

	for _, p := range points {
		out, txid := restoreTo(t, cfg, RestoreOptions{TXID: p.txid})
		if txid != p.txid {
			t.Fatalf("restored TXID %d, asked for %d", txid, p.txid)
		}
		if got := dump(t, out); got != p.dump {
			t.Fatalf("restored at TXID %d\n%s\nthen\n%s", p.txid, got, p.dump)
		}
		out, txid = restoreTo(t, cfg, RestoreOptions{At: p.at})
		if txid != p.txid {
			t.Fatalf("restored TXID %d at %v, want %d", txid, p.at, p.txid)
		}
		if got := dump(t, out); got != p.dump {
			t.Fatalf("restored at %v\n%s\nthen\n%s", p.at, got, p.dump)
		}
	}

	ctx := context.Background()
	for _, opt := range []RestoreOptions{
		{TXID: last.txid + 1},
		{At: time.Now().Add(-time.Hour)},
		{TXID: 1, At: time.Now()},
	} {
		out := filepath.Join(t.TempDir(), "restored.db")
		if _, err := Restore(ctx, out, cfg, opt); err == nil {
			t.Fatalf("Restore %+v: no error", opt)
		}
		if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 0 {
			t.Fatalf("Restore %+v left %v", opt, entries)
		}
	}
}

// Restore never writes over a database, its WAL or its staged files, and
// an empty replica has nothing to restore.
func TestRestoreRefuses(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "restored.db")
	if _, err := Restore(ctx, out, cfg, RestoreOptions{}); !errors.Is(err, ErrNoReplica) {
		t.Fatalf("Restore of an empty replica: %v", err)
	}
	history(t, cfg)
	for _, there := range []string{"restored.db", "restored.db-wal", "restored.db-journal", "restored.db-replica/ltx/0/0000000000000001-0000000000000001.ltx"} {
		dir := t.TempDir()
		keep := filepath.Join(dir, there)
		if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "restored.db")
		if _, err := Restore(ctx, out, cfg, RestoreOptions{}); err == nil {
			t.Fatalf("Restore with %s there: no error", there)
		}
		if b, err := os.ReadFile(keep); err != nil || string(b) != "keep" {
			t.Fatalf("%s after Restore: %q %v", there, b, err)
		}
		if there != "restored.db" {
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Restore with %s there wrote the database: %v", there, err)
			}
		}
	}
}

// reencode decodes the LTX file body and encodes it again with the header
// hdr gives and the pages page gives (in place, false drops one). With
// fixPost the post-apply checksum follows the pages, else it is the file's.
func reencode(t *testing.T, body []byte, hdr func(*ltx.Header), page func(pgno uint32, data []byte) bool, fixPost bool) []byte {
	t.Helper()
	dec := ltx.NewDecoder(bytes.NewReader(body))
	if err := dec.DecodeHeader(); err != nil {
		t.Fatal(err)
	}
	h := dec.Header()
	if hdr != nil {
		hdr(&h)
	}
	var out bytes.Buffer
	enc, err := ltx.NewEncoder(&out)
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.EncodeHeader(h); err != nil {
		t.Fatal(err)
	}
	var change ltx.Checksum
	data := make([]byte, h.PageSize)
	for {
		var ph ltx.PageHeader
		if err := dec.DecodePage(&ph, data); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		was := ltx.ChecksumPage(ph.Pgno, data)
		if page != nil && !page(ph.Pgno, data) {
			change ^= was
			continue
		}
		change ^= was ^ ltx.ChecksumPage(ph.Pgno, data)
		if err := enc.EncodePage(ph, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := dec.Close(); err != nil {
		t.Fatal(err)
	}
	post := dec.Trailer().PostApplyChecksum
	if fixPost {
		post = (post ^ change) | ltx.ChecksumFlag
	}
	enc.SetPostApplyChecksum(post)
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A replica that is not whole or not right is refused, at the bad file and
// after it, and nothing is left of the restore.
func TestRestoreBad(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		point int                                    // the point whose file is spoiled, 2 when 0
		want  string                                 // in the error
		spoil func(t *testing.T, body []byte) []byte // nil: delete the file
	}{
		{"gap", 0, "do not lead", nil},
		{"crc", 0, "checksum mismatch", func(t *testing.T, body []byte) []byte {
			body[len(body)/2] ^= 0x40
			return body
		}},
		{"page", 0, "checksum after it", func(t *testing.T, body []byte) []byte {
			// A valid file with the right post-apply checksum, but a page
			// that is not the one written.
			first := true
			return reencode(t, body, nil, func(_ uint32, data []byte) bool {
				if first {
					data[len(data)-1] ^= 1
					first = false
				}
				return true
			}, false)
		}},
		{"missing", 0, "checksum after it", func(t *testing.T, body []byte) []byte {
			first := true
			return reencode(t, body, nil, func(uint32, []byte) bool {
				keep := !first
				first = false
				return keep
			}, true)
		}},
		{"hole", 1, "never written", func(t *testing.T, body []byte) []byte {
			// The file that makes the database grow leaves out its last
			// page, and its checksum agrees.
			last := uint32(0)
			reencode(t, body, nil, func(pgno uint32, _ []byte) bool { last = max(last, pgno); return true }, false)
			return reencode(t, body, nil, func(pgno uint32, _ []byte) bool { return pgno != last }, true)
		}},
		{"txids", 0, "holds TXIDs", func(t *testing.T, body []byte) []byte {
			return reencode(t, body, func(h *ltx.Header) { h.MaxTXID++ }, nil, false)
		}},
		{"pre", 0, "checksum before it", func(t *testing.T, body []byte) []byte {
			return reencode(t, body, func(h *ltx.Header) { h.PreApplyChecksum ^= 1 }, nil, false)
		}},
		{"corrupt", 0, "integrity_check", func(t *testing.T, body []byte) []byte {
			// Every checksum agrees with the pages, which SQLite cannot
			// read: a b-tree page of no type.
			done := false
			return reencode(t, body, nil, func(pgno uint32, data []byte) bool {
				if pgno > 1 && !done {
					data[0], done = 0xff, true
				}
				return true
			}, true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			_, points := history(t, cfg)
			c, path := client(t, cfg)
			if tc.point == 0 {
				tc.point = 2
			}
			bad := ltx.TXID(points[tc.point].txid)
			key := levelKey(path, 0, bad, bad)
			if tc.spoil == nil {
				if err := c.Delete(ctx, key); err != nil {
					t.Fatal(err)
				}
			} else {
				body, err := c.Get(ctx, key, 0, -1)
				if err != nil {
					t.Fatal(err)
				}
				b, err := io.ReadAll(body)
				body.Close()
				if err != nil {
					t.Fatal(err)
				}
				b = tc.spoil(t, b)
				if err := c.Put(ctx, key, bytes.NewReader(b), int64(len(b)), s3.PutOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			for _, opt := range []RestoreOptions{{TXID: uint64(bad)}, {}} {
				out := filepath.Join(t.TempDir(), "restored.db")
				_, err := Restore(ctx, out, cfg, opt)
				if err == nil {
					t.Fatalf("Restore %+v: no error", opt)
				}
				t.Logf("Restore %+v: %v", opt, err)
				// Past the bad file, it may be the check of the next.
				if opt.TXID != 0 && !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Restore %+v: %v, want an error about %q", opt, err, tc.want)
				}
				if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 0 {
					t.Fatalf("Restore %+v left %v", opt, entries)
				}
			}
			// Before the bad file, the replica is fine.
			before := points[tc.point-1]
			out, txid := restoreTo(t, cfg, RestoreOptions{TXID: before.txid})
			if txid != before.txid || dump(t, out) != before.dump {
				t.Fatalf("restored TXID %d before the bad file", txid)
			}
		})
	}
}

// A restored database is replicated from where the replica ends, without
// a full copy.
func TestRestoreResume(t *testing.T) {
	cfg := testConfig(t)
	history(t, cfg)
	out, txid := restoreTo(t, cfg, RestoreOptions{})
	r := newReplica(t, out, cfg)
	if n := r.c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies", n)
	}
	if s := r.Status(); s.Uploaded != txid || s.Staged != txid {
		t.Fatalf("status %+v after restoring TXID %d", s, txid)
	}
	db := openDB(t, out)
	run(t, db, "INSERT INTO t VALUES (42)")
	syncNow(t, r)
	if got := sameAs(t, cfg, out); uint64(got) != txid+1 {
		t.Fatalf("the replica ends at TXID %s, want %d", got, txid+1)
	}
	if n := r.c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies", n)
	}
}

// A database restored to an earlier point is not where the replica ends.
func TestRestorePast(t *testing.T) {
	cfg := testConfig(t)
	_, points := history(t, cfg)
	out, _ := restoreTo(t, cfg, RestoreOptions{TXID: points[1].txid})
	if _, err := New(context.Background(), out, cfg); !errors.Is(err, ErrDiverged) {
		t.Fatalf("New after a restore to the past: %v", err)
	}
}

// compactNow merges level from into level to, as the background work does.
func compactNow(t *testing.T, r *Replica, from, to int) {
	t.Helper()
	r.busy <- struct{}{}
	defer func() { <-r.busy }()
	if err := r.compact(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
}

// The replica restores from a snapshot and compacted files, to every TXID
// a file ends at and to every time, as litestream restore does, and a
// database restored from a compacted file goes on replicating.
func TestRestoreLevels(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	name := filepath.Join(t.TempDir(), "x.db")
	r := newReplica(t, name, cfg)
	db := openDB(t, name)
	run(t, db, "CREATE TABLE t(x)")
	var points []point
	for i := range 12 {
		run(t, db, fmt.Sprintf("INSERT INTO t VALUES (randomblob(%d))", 500+i*300))
		if i == 7 {
			run(t, db, "DELETE FROM t WHERE rowid < 4", "VACUUM")
		}
		syncNow(t, r)
		time.Sleep(2 * time.Millisecond)
		points = append(points, point{txid: r.Status().Uploaded, at: time.Now(), dump: dump(t, name)})
		time.Sleep(2 * time.Millisecond)
		switch i {
		case 3:
			compactNow(t, r, 0, snapshotLevel) // TXIDs 1 to 4ish, a snapshot
		case 5, 9:
			compactNow(t, r, 0, 1)
		case 10:
			compactNow(t, r, 1, 2)
		}
	}
	// A TXID before the snapshot comes from the level 0 files.
	if out, txid := restoreTo(t, cfg, RestoreOptions{TXID: points[1].txid}); txid != points[1].txid || dump(t, out) != points[1].dump {
		t.Fatalf("restored TXID %d, want %d", txid, points[1].txid)
	}
	compactNow(t, r, 0, 1)
	// The level 0 files the compacted ones hold go, but the last.
	c, path := client(t, cfg)
	files, err := listRemote(ctx, c, path)
	if err != nil {
		t.Fatal(err)
	}
	var l1, last ltx.TXID
	for _, f := range files {
		if f.level == 1 {
			l1 = max(l1, f.max)
		}
		last = max(last, f.max)
	}
	if l1 != last {
		t.Fatalf("level 1 ends at %s, the replica at %s", l1, last)
	}
	ends := map[uint64]bool{}
	for _, f := range files {
		if f.level == 0 && f.max < last {
			if err := c.Delete(ctx, f.key); err != nil {
				t.Fatal(err)
			}
			continue
		}
		ends[uint64(f.max)] = true
	}
	t.Logf("files: %v", ends)

	// The latest state comes from the snapshot and the compacted files.
	plan, err := restorePlan(ctx, c, path, 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range plan {
		if i == 0 && f.level != snapshotLevel || i > 0 && f.level == 0 {
			t.Fatalf("plan: %+v", plan)
		}
	}
	out, txid := restoreTo(t, cfg, RestoreOptions{})
	if txid != uint64(last) {
		t.Fatalf("restored TXID %d, the replica ends at %s", txid, last)
	}
	sameFile(t, out, litestreamRestore(t, cfg))

	reached := uint64(0) // the last TXID a file ends at, up to the point
	compared := 0
	for _, p := range points {
		for txid := reached + 1; txid <= p.txid; txid++ {
			if ends[txid] {
				reached = txid
			}
		}
		out := filepath.Join(t.TempDir(), "restored.db")
		_, err := Restore(ctx, out, cfg, RestoreOptions{TXID: p.txid})
		switch {
		case !ends[p.txid] && err == nil:
			t.Fatalf("Restore TXID %d, which no file ends at: no error", p.txid)
		case ends[p.txid] && err != nil:
			t.Fatalf("Restore TXID %d: %v", p.txid, err)
		case ends[p.txid]:
			if got := dump(t, out); got != p.dump {
				t.Fatalf("restored at TXID %d\n%s\nthen\n%s", p.txid, got, p.dump)
			}
		}
		if reached == 0 {
			continue
		}
		out, txid := restoreTo(t, cfg, RestoreOptions{At: p.at})
		if txid != reached {
			t.Fatalf("restored TXID %d at the time of TXID %d, want %d", txid, p.txid, reached)
		}
		// litestream restore takes times from the first upload of the files
		// there to the last.
		theirs, err := litestreamTry(t, cfg, "-timestamp", p.at.UTC().Format(time.RFC3339Nano))
		if err != nil && !strings.Contains(err.Error(), "timestamp does not exist") {
			t.Fatal(err)
		}
		if err == nil {
			sameFile(t, out, theirs)
			compared++
		}
	}
	if litestreamBin(t) != "" && compared == 0 {
		t.Fatal("litestream restore took none of the times")
	}
	t.Logf("%d times compared with litestream restore", compared)

	// From the database restored from the compacted file, replication goes
	// on without a full copy.
	r.Close()
	out, txid = restoreTo(t, cfg, RestoreOptions{})
	staged, err := os.ReadDir(capture.StageDir(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 || staged[0].Name() == ltx.FormatFilename(ltx.TXID(txid), ltx.TXID(txid)) {
		t.Fatalf("staged: %v, want a compacted file", staged)
	}
	r2 := newReplica(t, out, cfg)
	if n := r2.c.FullCopies(); n != 0 {
		t.Fatalf("%d full copies", n)
	}
	db2 := openDB(t, out)
	run(t, db2, "INSERT INTO t VALUES (42)")
	syncNow(t, r2)
	again, txid2 := restoreTo(t, cfg, RestoreOptions{})
	if txid2 != txid+1 || dump(t, again) != dump(t, out) {
		t.Fatalf("restored TXID %d after going on from %d", txid2, txid)
	}
}
