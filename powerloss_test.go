// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"bytes"
	"database/sql"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/liustack/sqlite/internal/vfs"
)

// The tests of this file play a power loss. A recorder keeps everything the
// VFS does to the files of a database while a program writes (vfs.SetTrace).
// Then, for many moments of that log, plDisk builds files a power loss at
// that moment could leave: what was synced is there, and of what was not,
// any part. The database must open from each of them whole.
//
// TestCrash kills a process, which loses nothing the process wrote: the
// system still has it. These tests check that each sync comes where SQLite
// needs it.

// plEvent is one entry of the log: a change of a file, or the return of a
// commit to its caller (ack, the number of the transaction).
type plEvent struct {
	vfs.Change
	ack int // -1 for a change
}

// plRecorder keeps the log of the files of one directory.
type plRecorder struct {
	dir string
	mu  sync.Mutex
	log []plEvent
}

// plDir is a new directory, by the name the VFS knows its files by.
func plDir(t *testing.T) string {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// plRecord starts a log of the files in dir, until the test ends.
func plRecord(t *testing.T, dir string) *plRecorder {
	r := &plRecorder{dir: dir + string(filepath.Separator)}
	vfs.SetTrace(func(c vfs.Change) {
		if !strings.HasPrefix(c.Path, r.dir) {
			return
		}
		r.mu.Lock()
		r.log = append(r.log, plEvent{c, -1})
		r.mu.Unlock()
	})
	t.Cleanup(func() { vfs.SetTrace(nil) })
	return r
}

// acked writes down that the commit of transaction n returned.
func (r *plRecorder) acked(n int) {
	r.mu.Lock()
	r.log = append(r.log, plEvent{ack: n})
	r.mu.Unlock()
}

// plOp is a write or a truncate of a file that no sync has followed.
type plOp struct {
	seq   int // its place in the log
	trunc bool
	off   int64 // where it wrote, or the new size
	data  []byte
}

// plFile is a file as a power loss finds it: what its last sync put on
// disk, and what was done to it since.
type plFile struct {
	disk    []byte
	pending []plOp
}

// plDisk follows a log: the files of the directory, and the ones it held at
// its last sync.
type plDisk struct {
	names  map[string]*plFile
	synced map[string]*plFile
}

// plStart is the disk before the log: the files of dir, all of them synced.
func plStart(t *testing.T, dir string) *plDisk {
	d := &plDisk{names: map[string]*plFile{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		d.names[e.Name()] = &plFile{disk: b}
	}
	d.synced = maps.Clone(d.names)
	return d
}

// apply is entry seq of the log.
func (d *plDisk) apply(seq int, c vfs.Change) error {
	name := filepath.Base(c.Path)
	f := d.names[name]
	switch c.Kind {
	case vfs.ChangeCreate:
		if f == nil {
			d.names[name] = &plFile{}
		}
		return nil
	case vfs.ChangeDirSync:
		d.synced = maps.Clone(d.names)
		return nil
	}
	if f == nil {
		return fmt.Errorf("entry %d: change %d of %s, which is not there", seq, c.Kind, name)
	}
	switch c.Kind {
	case vfs.ChangeWrite:
		f.pending = append(f.pending, plOp{seq: seq, off: c.Off, data: c.Data})
	case vfs.ChangeTruncate:
		f.pending = append(f.pending, plOp{seq: seq, trunc: true, off: c.Off})
	case vfs.ChangeSync:
		for _, op := range f.pending {
			f.disk = op.do(f.disk, len(op.data))
		}
		f.pending = nil
	case vfs.ChangeDelete:
		delete(d.names, name)
	}
	return nil
}

// do is b after op, or after the first n bytes of its write.
func (op plOp) do(b []byte, n int) []byte {
	if op.trunc {
		if int64(len(b)) >= op.off {
			return b[:op.off]
		}
		return append(b, make([]byte, op.off-int64(len(b)))...)
	}
	if end := op.off + int64(n); int64(len(b)) < end {
		b = append(b, make([]byte, end-int64(len(b)))...)
	}
	copy(b[op.off:], op.data[:n])
	return b
}

// How a power loss treats what was not synced.
const (
	plNone  = iota // none of it arrived
	plAll          // all of it did, as when the process is killed
	plFirst        // what was done before some moment did
	plSome         // any part: of each write all its sectors, none, the first ones or any of them
	plKinds
)

var plKindNames = [...]string{"nothing unsynced arrived", "all arrived", "the first part arrived", "some writes arrived"}

// plSector is what a disk writes at once. Of a write that a power loss
// stops, some sectors are on disk and the others are as they were.
const plSector = 512

// Which sectors of a write arrived.
const (
	plWhole = iota
	plNothing
	plHead // the first ones
	plAny  // each or not
)

// some is b after the sectors of op's write that arrived, as how says. With
// junk, the file is as long as the write made it, and holds anything where
// it grew and no data arrived. Without, it holds zeros there, and ends with
// the last sector that arrived.
func (op plOp) some(b []byte, how int, junk bool, rng *rand.Rand) []byte {
	old, end := int64(len(b)), op.off+int64(len(op.data))
	head := op.off + rng.Int64N(int64(len(op.data))/plSector+1)*plSector
	for at := op.off; at < end; {
		next := min((at/plSector+1)*plSector, end)
		got := how == plWhole || how == plHead && next <= head || how == plAny && rng.IntN(2) == 0
		if (got || junk) && int64(len(b)) < next {
			b = append(b, make([]byte, next-int64(len(b)))...)
		}
		switch {
		case got:
			copy(b[at:next], op.data[at-op.off:])
		case junk:
			for i := max(at, old); i < next; i++ {
				b[i] = byte(rng.Uint32())
			}
		}
		at = next
	}
	return b
}

// crash gives the files a power loss now could leave. With garbage, where a
// write that makes a file longer did not arrive, the file may be longer all
// the same and hold anything there: SQLite assumes a file system may set
// the size of a file before it writes the data.
func (d *plDisk) crash(kind int, garbage bool, rng *rand.Rand) map[string][]byte {
	files := map[string][]byte{}
	all := maps.Clone(d.synced)
	maps.Copy(all, d.names)
	names := slices.Sorted(maps.Keys(all))
	// plFirst: what came before entry cut of the log arrived.
	cut := 0
	if kind == plFirst {
		var seqs []int
		for _, name := range names {
			if f := d.names[name]; f != nil {
				for _, op := range f.pending {
					seqs = append(seqs, op.seq)
				}
			}
		}
		if len(seqs) > 0 {
			cut = seqs[rng.IntN(len(seqs))]
		}
	}
	for _, name := range names {
		// A name that changed since the directory was synced has the file
		// it had then, or the one it has now, or none when one was deleted
		// and another made.
		was, is := d.synced[name], d.names[name]
		f := is
		if was != is && kind != plAll {
			f = was
			if kind != plNone {
				f = []*plFile{was, is, nil}[rng.IntN(3)]
				if f == nil && (was == nil || is == nil) {
					f = []*plFile{was, is}[rng.IntN(2)]
				}
			}
		}
		if f == nil {
			continue
		}
		b := slices.Clone(f.disk)
		for _, op := range f.pending {
			how := plWhole
			switch kind {
			case plNone:
				how = plNothing
			case plFirst:
				if op.seq >= cut {
					how = plNothing
				}
			case plSome:
				how = []int{plWhole, plWhole, plNothing, plHead, plAny}[rng.IntN(5)]
			}
			switch {
			case !op.trunc:
				b = op.some(b, how, garbage && rng.IntN(2) == 0, rng)
			case how != plNothing:
				b = op.do(b, 0)
			}
		}
		files[name] = b
	}
	return files
}

// plCase is one way to open the database of a power loss test.
type plCase struct {
	name, pragmas string
	// lose is how many of the last commits that returned a power loss may
	// take back: 0 where a commit is durable, -1 for any number.
	lose int
	// any: the database stands whatever part of the writes that were not
	// synced arrived (plSome), and a file that is longer than what arrived
	// (the garbage of plDisk.crash).
	any bool
}

var plCases = []plCase{
	{"wal full", "_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=wal_autocheckpoint(25)", 0, true},
	// The WAL is synced at a checkpoint: the commits since are lost.
	{"wal normal", "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=wal_autocheckpoint(25)", -1, true},
	{"wal full limit", "_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=wal_autocheckpoint(0)&_wal_limit=200000", 0, true},
	// With synchronous=FULL the journal is deleted and its directory is not
	// synced: a power loss can bring the journal back, and with it the
	// database of before the last commit. The next transaction syncs the
	// directory for its own journal, so no older commit is lost. EXTRA
	// syncs the directory at the commit.
	{"delete extra", "_pragma=journal_mode(DELETE)&_pragma=synchronous(EXTRA)", 0, true},
	{"delete full", "_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)", 1, true},
	// A journal that is cut or whose header is zeroed at a commit is synced
	// after it, so the commit stays.
	{"truncate full", "_pragma=journal_mode(TRUNCATE)&_pragma=synchronous(FULL)", 0, true},
	{"persist full", "_pragma=journal_mode(PERSIST)&_pragma=synchronous(FULL)", 0, true},
	// With synchronous=NORMAL SQLite writes the number of records into the
	// header of the journal before it syncs them, and the checksum of a
	// record looks at one byte in 200. A power loss that leaves the header
	// and a part of a record whose checked bytes are all zero puts that part
	// into the database: this test found it within 50000 power losses, as an
	// index page with its cells zeroed. It is the small chance of a damaged
	// database that the documentation of SQLite gives for NORMAL with a
	// rollback journal, and synchronous=FULL, the default, closes it. So
	// here the writes arrive in the order they were made, each whole.
	{"delete normal", "_pragma=journal_mode(DELETE)&_pragma=synchronous(NORMAL)", 1, false},
}

// plBlob is the n bytes of row i of transaction txn.
func plBlob(txn, i, n int) []byte {
	rng := rand.New(rand.NewPCG(uint64(txn), uint64(i)))
	b := make([]byte, n)
	for j := range b {
		b[j] = byte(rng.Uint32())
	}
	return b
}

// plPoints is how many moments of a log a test plays a power loss at:
// POWERLOSS_POINTS, or 300, or 60 with -short. A sync left out is found with
// 60. The sync of the header of a WAL that starts over, which only some
// orders of arrival need, was found missing by 14 runs of 16 with 300 or
// 500.
func plPoints() int {
	if n, err := strconv.Atoi(os.Getenv("POWERLOSS_POINTS")); err == nil && n > 0 {
		return n
	}
	if testing.Short() {
		return 60
	}
	return 300
}

// plPlay plays a power loss at many moments of the log r kept from the disk
// start on. check gets the database of each, opened, the entries of the log
// before the moment, and says what is wrong with it.
func plPlay(t *testing.T, tc plCase, start *plDisk, r *plRecorder, check func(db *sql.DB, before []plEvent) error) {
	t.Helper()
	vfs.SetTrace(nil)
	log := r.log
	if len(log) < 100 {
		t.Fatalf("a log of %d entries", len(log))
	}
	seed := rand.Uint64()
	rng := rand.New(rand.NewPCG(seed, 1))
	// Half of the moments are anywhere in the log. The other half come
	// shortly after a file is made, deleted, cut or written at its start
	// (the header of a WAL or a journal): where a WAL or a journal starts
	// over, an old one must not come back.
	var starts []int
	for i, e := range log {
		if e.ack < 0 && (e.Kind == vfs.ChangeCreate || e.Kind == vfs.ChangeDelete || e.Kind == vfs.ChangeTruncate || e.Kind == vfs.ChangeWrite && e.Off == 0) {
			starts = append(starts, i)
		}
	}
	at := map[int]bool{len(log): true}
	for i := range plPoints() {
		k := 1 + rng.IntN(len(log))
		if i%2 == 1 && len(starts) > 0 {
			k = min(starts[rng.IntN(len(starts))]+1+rng.IntN(24), len(log))
		}
		at[k] = true
	}
	dir := t.TempDir()
	played := 0
	for k := 1; k <= len(log); k++ {
		if e := log[k-1]; e.ack < 0 {
			if err := start.apply(k-1, e.Change); err != nil {
				t.Fatal(err)
			}
		}
		if !at[k] {
			continue
		}
		for kind := range plKinds {
			if kind == plSome && !tc.any {
				continue
			}
			played++
			sub := filepath.Join(dir, strconv.Itoa(played))
			if err := os.Mkdir(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, b := range start.crash(kind, tc.any, rng) {
				if err := os.WriteFile(filepath.Join(sub, name), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// POWERLOSS_KEEP names a directory that gets the files of the
			// power loss that fails, as they were before they were opened.
			keep := os.Getenv("POWERLOSS_KEEP")
			if keep != "" {
				os.RemoveAll(keep)
				if err := os.CopyFS(keep, os.DirFS(sub)); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", "file:"+filepath.Join(sub, "p.db")+"?"+tc.pragmas)
			if err != nil {
				t.Fatal(err)
			}
			err = plSound(db)
			if err == nil {
				err = check(db, log[:k])
			}
			db.Close()
			if err != nil {
				for i := max(k-12, 0); i < k; i++ {
					e := log[i]
					if e.ack >= 0 {
						t.Logf("  %d: commit %d returned", i, e.ack)
					} else {
						t.Logf("  %d: %s %s at %d, %d bytes", i, plChangeNames[e.Kind], filepath.Base(e.Path), e.Off, len(e.Data))
					}
				}
				t.Fatalf("power loss after entry %d of %d, %s (seed %d): %v", k-1, len(log), plKindNames[kind], seed, err)
			}
			os.RemoveAll(sub)
		}
	}
	t.Logf("%d entries, %d power losses played", len(log), played)
}

var plChangeNames = [...]string{"create", "write", "truncate", "sync", "sync the directory of", "delete"}

// plSound says what integrity_check finds wrong with db.
func plSound(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if len(all) != 1 || all[0] != "ok" {
		return fmt.Errorf("integrity_check: %s", strings.Join(all, ", "))
	}
	return nil
}

// plAcked is the largest transaction whose commit returned in the entries
// before, -1 when none did.
func plAcked(before []plEvent) int {
	n := -1
	for _, e := range before {
		n = max(n, e.ack)
	}
	return n
}

// One writer runs transactions one after the other. Each adds three rows,
// deletes the rows of an older one and writes its number down. After a power
// loss at any moment the database holds the transactions up to some number,
// each whole with the bytes it wrote, and none after it. That number is at
// least the last commit that returned, less the commits the case may lose.
func TestPowerLoss(t *testing.T) {
	const txns, kept = 150, 20
	for _, tc := range plCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := plDir(t)
			dsn := "file:" + filepath.Join(dir, "p.db") + "?" + tc.pragmas
			db := mOpen(t, dsn)
			mExec(t, db, `CREATE TABLE t(txn INTEGER, i INTEGER, v BLOB)`)
			mExec(t, db, `CREATE INDEX ti ON t(txn)`)
			mExec(t, db, `CREATE TABLE last(n INTEGER)`)
			mExec(t, db, `INSERT INTO last VALUES (-1)`)
			db.Close()
			start := plStart(t, dir)
			r := plRecord(t, dir)
			db = mOpen(t, dsn)
			for n := range txns {
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				for i := range 3 {
					mExec(t, tx, `INSERT INTO t VALUES (?, ?, ?)`, n, i, plBlob(n, i, 1500))
				}
				mExec(t, tx, `DELETE FROM t WHERE txn = ?`, n-kept)
				mExec(t, tx, `UPDATE last SET n = ?`, n)
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				r.acked(n)
			}
			db.Close()
			plPlay(t, tc, start, r, func(db *sql.DB, before []plEvent) error {
				var last int
				if err := db.QueryRow(`SELECT n FROM last`).Scan(&last); err != nil {
					return err
				}
				if acked := plAcked(before); tc.lose >= 0 && last < acked-tc.lose {
					return fmt.Errorf("the database is at transaction %d, and the commit of %d returned", last, acked)
				}
				rows, err := db.Query(`SELECT txn, i, v FROM t ORDER BY txn, i`)
				if err != nil {
					return err
				}
				defer rows.Close()
				// The next row: i of transaction txn.
				txn, i := max(last-kept+1, 0), 0
				for rows.Next() {
					var gotTxn, gotI int
					var v []byte
					if err := rows.Scan(&gotTxn, &gotI, &v); err != nil {
						return err
					}
					if gotTxn != txn || gotI != i {
						return fmt.Errorf("at transaction %d: row %d of transaction %d, want row %d of %d", last, gotI, gotTxn, i, txn)
					}
					if !bytes.Equal(v, plBlob(txn, i, 1500)) {
						return fmt.Errorf("at transaction %d: row %d of transaction %d has other bytes than it wrote", last, i, txn)
					}
					if i++; i == 3 {
						txn, i = txn+1, 0
					}
				}
				if txn != last+1 || i != 0 {
					return fmt.Errorf("at transaction %d: the rows end before row %d of transaction %d", last, i, txn)
				}
				return rows.Err()
			})
		})
	}
}

// Eight goroutines insert one row each at a time, their inserts merged into
// transactions by group commit. After a power loss at any moment the
// database is sound, each row has the bytes it wrote, no row of a goroutine
// is there without the ones that goroutine inserted before it, and where
// commits are durable every row whose insert returned is there.
func TestPowerLossGroupCommit(t *testing.T) {
	const goroutines, each = 8, 40
	for _, tc := range plCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := plDir(t)
			dsn := "file:" + filepath.Join(dir, "p.db") + "?" + tc.pragmas
			db := mOpen(t, dsn)
			mExec(t, db, `CREATE TABLE g(k INTEGER PRIMARY KEY, v BLOB)`)
			db.Close()
			start := plStart(t, dir)
			r := plRecord(t, dir)
			db = mOpen(t, dsn)
			db.SetMaxOpenConns(goroutines)
			db.SetMaxIdleConns(goroutines)
			l := walLock(t, db)
			var wg sync.WaitGroup
			for g := range goroutines {
				wg.Go(func() {
					for i := range each {
						k := g*each + i
						if _, err := db.Exec(`INSERT INTO g VALUES (?, ?)`, k, plBlob(k, 0, 700)); err != nil {
							t.Error(err)
							return
						}
						r.acked(k)
					}
				})
			}
			wg.Wait()
			merged := l.stats.merged.Load()
			db.Close()
			if t.Failed() {
				return
			}
			if merged == 0 {
				t.Fatal("no insert was merged")
			}
			plPlay(t, tc, start, r, func(db *sql.DB, before []plEvent) error {
				have := map[int]bool{}
				rows, err := db.Query(`SELECT k, v FROM g`)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var k int
					var v []byte
					if err := rows.Scan(&k, &v); err != nil {
						return err
					}
					if !bytes.Equal(v, plBlob(k, 0, 700)) {
						return fmt.Errorf("row %d has other bytes than it wrote", k)
					}
					have[k] = true
				}
				if err := rows.Err(); err != nil {
					return err
				}
				for k := range have {
					if k%each > 0 && !have[k-1] {
						return fmt.Errorf("row %d is there, and row %d that its goroutine inserted before it is not", k, k-1)
					}
				}
				if tc.lose != 0 {
					return nil
				}
				for _, e := range before {
					if e.ack >= 0 && !have[e.ack] {
						return fmt.Errorf("row %d is missing, and its insert returned", e.ack)
					}
				}
				return nil
			})
		})
	}
}
