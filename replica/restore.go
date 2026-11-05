// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/internal/csqlite"
	"github.com/liustack/sqlite/replica/internal/capture"
	"github.com/liustack/sqlite/replica/internal/s3"
)

// RestoreOptions says which state of the database Restore brings back:
// the latest, the one at a TXID, or the one at a time.
type RestoreOptions struct {
	// TXID, if set, is the TXID to restore.
	TXID uint64
	// At, if set, restores the database as it was before that time: the
	// files of the replica made before it, as litestream restore does. A
	// file is made when its commits are staged, within SyncInterval of
	// them, and may hold several commits. Its time is the one in its header,
	// from the clock of the replicator: Restore checks the first file it
	// leaves out of each level and trusts the times of the files after it.
	At time.Time
}

// ErrNoReplica is the error of Restore when the replica holds no file.
var ErrNoReplica = errors.New("replica: there is no replica at the URL")

// Restore rebuilds the database name from the replica of cfg.URL, and
// returns the TXID it holds. Only URL, Credentials and AllowHTTP of cfg
// are used. Every file is checked: its checksum, and the checksum of the
// database before and after it. Restore writes neither over a database
// nor beside a WAL, journal or staged file of name that is there. It
// leaves the replica's last file in the staging folder of name, so that
// New goes on from the replica: after a restore to the latest state, New
// goes on with no full copy, and after a restore to an earlier one New
// fails with ErrDiverged unless Config.FreshStart.
func Restore(ctx context.Context, name string, cfg Config, opt RestoreOptions) (uint64, error) {
	if opt.TXID != 0 && !opt.At.IsZero() {
		return 0, errors.New("replica: restore at a TXID or at a time, not both")
	}
	if err := restoreTarget(name); err != nil {
		return 0, err
	}
	client, path, err := cfg.client()
	if err != nil {
		return 0, err
	}
	var id [8]byte
	rand.Read(id[:])
	tmp := name + "." + hex.EncodeToString(id[:]) + ".restoring"
	db, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp)
	defer db.Close()
	last, err := os.OpenFile(tmp+".ltx", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	defer os.Remove(last.Name())
	defer last.Close()

	// Retention may delete a file planned meanwhile: the restore plans again,
	// to the same TXID or time, from the files left.
	var a *applier
	for try := 1; ; try++ {
		a, err = restoreFiles(ctx, client, path, opt, db, last)
		if err == nil || !errors.Is(err, s3.ErrNotFound) || try == 3 {
			break
		}
	}
	if err != nil {
		return 0, err
	}
	if err := db.Sync(); err != nil {
		return 0, err
	}
	if err := integrityCheck(db); err != nil {
		return 0, err
	}
	if err := db.Close(); err != nil {
		return 0, err
	}
	if err := last.Sync(); err != nil {
		return 0, err
	}
	if err := last.Close(); err != nil {
		return 0, err
	}

	// The database goes in place first, without writing over one: once it
	// is there, the restore is done. Its staged file follows.
	if err := os.Link(tmp, name); err != nil {
		return 0, fmt.Errorf("replica: placing the restored database: %w", err)
	}
	if err := capture.Stage(name, last.Name(), a.min, a.txid); err != nil {
		os.Remove(name)
		return 0, err
	}
	return uint64(a.txid), nil
}

// restoreFiles plans the restore opt asks for and applies the files to db,
// from empty.
func restoreFiles(ctx context.Context, c *s3.Client, path string, opt RestoreOptions, db, last *os.File) (*applier, error) {
	files, err := restorePlan(ctx, c, path, ltx.TXID(opt.TXID), opt.At)
	if err != nil {
		return nil, err
	}
	if err := db.Truncate(0); err != nil {
		return nil, err
	}
	a := &applier{db: db, last: last}
	for _, f := range files {
		if err := a.apply(ctx, c, f, opt.At); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// restoreTarget checks that nothing of a database is at name: the file,
// its WAL (which SQLite would apply to the restored file), its journal,
// or staged files.
func restoreTarget(name string) error {
	for _, n := range []string{name, name + "-wal", name + "-journal"} {
		if _, err := os.Lstat(n); err == nil {
			return fmt.Errorf("replica: %s is there: Restore does not write over it", n)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	dir := capture.StageDir(name)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".tmp") {
			return fmt.Errorf("replica: %s holds staged files of another database: remove it to restore", dir)
		}
	}
	return nil
}

// restorePlan lists the files to apply, in order, to restore TXID txid
// (the last when 0), or the state before at (when set): the newest
// snapshot that fits, then each time the file of any level that goes on
// from the TXIDs so far and reaches furthest, as litestream restore picks
// them. Files may overlap the TXIDs before them. A gap is an error.
func restorePlan(ctx context.Context, c *s3.Client, path string, txid ltx.TXID, at time.Time) ([]remoteFile, error) {
	files, err := listRemote(ctx, c, path)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, ErrNoReplica
	}
	before := func(f remoteFile) (bool, error) {
		if at.IsZero() {
			return true, nil
		}
		t, err := fileTime(ctx, c, f.key)
		return t.Before(at), err
	}
	var snaps, rest []remoteFile
	for _, f := range files {
		if f.level == snapshotLevel {
			snaps = append(snaps, f)
		} else {
			rest = append(rest, f)
		}
	}
	var plan []remoteFile
	var end ltx.TXID
	// The listing has the snapshots in TXID order.
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		if s.min != 1 || txid != 0 && s.max > txid {
			continue
		}
		ok, err := before(s)
		if err != nil {
			return nil, err
		}
		if ok {
			plan, end = append(plan, s), s.max
			break
		}
	}
	skip := map[string]bool{} // made at or after at
	next := func() *remoteFile {
		var best *remoteFile
		for i := range rest {
			f := &rest[i]
			if skip[f.key] || f.min > end+1 || f.max <= end || txid != 0 && f.max > txid {
				continue
			}
			if best == nil || f.max > best.max || f.max == best.max && (f.min < best.min || f.min == best.min && f.level > best.level) {
				best = f
			}
		}
		return best
	}
	for txid == 0 || end < txid {
		f := next()
		if f == nil {
			break
		}
		ok, err := before(*f)
		if err != nil {
			return nil, err
		}
		if !ok {
			skip[f.key] = true
			continue
		}
		plan, end = append(plan, *f), f.max
	}
	// The times of the files left out come from their headers, unchecked:
	// the one of each level that ends first is checked whole, so that a
	// damaged time does not hide a state. Along a level the times grow
	// with the TXIDs, so the files of the level that end after it are after
	// at too, as long as the replicator's clock does not go back. Across
	// levels they need not: a snapshot reads its time after its TXID. The
	// times of the other files left out are trusted, as litestream restore
	// trusts them.
	if !at.IsZero() {
		first := map[int]*remoteFile{}
		for i := range files {
			f := &files[i]
			if f.min > end+1 || f.max <= end || txid != 0 && f.max > txid {
				continue
			}
			if g := first[f.level]; g == nil || f.max < g.max {
				first[f.level] = f
			}
		}
		for _, level := range slices.Sorted(maps.Keys(first)) {
			if err := madeAfter(ctx, c, *first[level], at); err != nil {
				return nil, err
			}
		}
	}
	switch {
	case end == 0 && !at.IsZero():
		return nil, fmt.Errorf("replica: the replica has no state from before %s", at.Format(time.RFC3339Nano))
	case txid != 0 && end != txid:
		return nil, fmt.Errorf("replica: the files of the replica do not lead to TXID %s", txid)
	case txid != 0:
		return plan, nil
	}
	// A file after a gap, which a restore to it would need, is an error.
	var gap *remoteFile
	for i := range rest {
		if f := &rest[i]; f.min > end+1 && (gap == nil || f.min < gap.min) {
			gap = f
		}
	}
	if gap != nil {
		ok, err := before(*gap)
		if err != nil {
			return nil, err
		}
		if ok {
			return nil, fmt.Errorf("replica: %s follows TXID %s: the replica is not whole", gap.key, end)
		}
		// As above: its time hides the gap only when it is checked.
		if err := madeAfter(ctx, c, *gap, at); err != nil {
			return nil, err
		}
	}
	if end == 0 {
		return nil, errors.New("replica: the replica is not whole: it has no snapshot and no file from TXID 1")
	}
	return plan, nil
}

// madeAfter checks the LTX file f whole, and that it was made at or after
// at, as its header read alone said.
func madeAfter(ctx context.Context, c *s3.Client, f remoteFile, at time.Time) error {
	body, err := c.Get(ctx, f.key, 0, -1)
	if err != nil {
		return err
	}
	defer body.Close()
	h, _, err := capture.Verify(body)
	switch {
	case err != nil:
		return fmt.Errorf("replica: %s: %w", f.key, err)
	case h.MinTXID != f.min || h.MaxTXID != f.max:
		return fmt.Errorf("replica: %s holds TXIDs %s to %s, not those of its name", f.key, h.MinTXID, h.MaxTXID)
	case time.UnixMilli(h.Timestamp).Before(at):
		return fmt.Errorf("replica: %s changed during the restore", f.key)
	}
	return nil
}

// fileTime is the time in the header of the LTX file key.
func fileTime(ctx context.Context, c *s3.Client, key string) (time.Time, error) {
	body, err := c.Get(ctx, key, 0, ltx.HeaderSize)
	if err != nil {
		return time.Time{}, err
	}
	defer body.Close()
	var hb [ltx.HeaderSize]byte
	if _, err := io.ReadFull(body, hb[:]); err != nil {
		return time.Time{}, fmt.Errorf("replica: %s: %w", key, err)
	}
	var h ltx.Header
	if err := h.UnmarshalBinary(hb[:]); err != nil {
		return time.Time{}, fmt.Errorf("replica: %s: %w", key, err)
	}
	return time.UnixMilli(h.Timestamp), nil
}

// An applier applies LTX files to the database it restores, checking them.
type applier struct {
	db   *os.File
	last *os.File // a copy of the last file applied

	min, txid ltx.TXID // of the last file applied
	pageSize  uint32
	sums      []ltx.Checksum // of each page, 0 for one not written yet
	sum       ltx.Checksum   // of the database
	unset     int            // pages up to the end not written yet, but the lock page
	page      []byte
}

// apply downloads and applies the file f, which must be from before at
// (when set). The file is downloaded whole and its checksum checked before
// anything of it is used.
func (a *applier) apply(ctx context.Context, c *s3.Client, f remoteFile, at time.Time) error {
	fail := func(err error) error { return fmt.Errorf("replica: %s: %w", f.key, err) }
	body, err := c.Get(ctx, f.key, 0, -1)
	if err != nil {
		return err
	}
	defer body.Close()
	if _, err := a.last.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := a.last.Truncate(0); err != nil {
		return err
	}
	w := bufio.NewWriterSize(a.last, 1<<20)
	if _, err := io.Copy(w, body); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := a.last.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h, _, err := capture.Verify(a.last)
	if err != nil {
		return fail(err)
	}
	switch {
	case h.MinTXID != f.min || h.MaxTXID != f.max:
		return fail(fmt.Errorf("holds TXIDs %s to %s, not those of its name", h.MinTXID, h.MaxTXID))
	case !at.IsZero() && !time.UnixMilli(h.Timestamp).Before(at):
		return fail(fmt.Errorf("made at %s, not before %s", time.UnixMilli(h.Timestamp).Format(time.RFC3339Nano), at.Format(time.RFC3339Nano)))
	}
	if _, err := a.last.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := a.applyFile(bufio.NewReaderSize(a.last, 1<<20)); err != nil {
		return fail(err)
	}
	return nil
}

// applyFile applies the LTX file in r, whose checksum was checked.
func (a *applier) applyFile(r io.Reader) error {
	dec := ltx.NewDecoder(r)
	if err := dec.DecodeHeader(); err != nil {
		return err
	}
	h := dec.Header()
	switch {
	case h.MinTXID > a.txid+1 || h.MaxTXID <= a.txid:
		return fmt.Errorf("holds TXIDs %s to %s after %s", h.MinTXID, h.MaxTXID, a.txid)
	// A file that overlaps the TXIDs before it starts from a state that
	// is gone: only the checksum after it is checked.
	case a.txid > 0 && h.MinTXID == a.txid+1 && h.PreApplyChecksum != a.checksum():
		return fmt.Errorf("the database's checksum before it is %s, the database's is %s", h.PreApplyChecksum, a.checksum())
	case a.txid > 0 && h.PageSize != a.pageSize:
		return fmt.Errorf("page size %d after %d", h.PageSize, a.pageSize)
	}
	a.pageSize = h.PageSize
	if len(a.page) != int(h.PageSize) {
		a.page = make([]byte, h.PageSize)
	}
	lock := ltx.LockPgno(h.PageSize)
	a.resize(max(h.Commit, uint32(len(a.sums))), lock)
	for {
		var ph ltx.PageHeader
		if err := dec.DecodePage(&ph, a.page); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		// ltx writes neither: a guard for the index below.
		if ph.Pgno == 0 || ph.Pgno > h.Commit || ph.Pgno == lock {
			return fmt.Errorf("page %d of a database of %d", ph.Pgno, h.Commit)
		}
		if _, err := a.db.WriteAt(a.page, int64(ph.Pgno-1)*int64(h.PageSize)); err != nil {
			return err
		}
		s := &a.sums[ph.Pgno-1]
		if *s == 0 {
			a.unset--
		}
		a.sum ^= *s
		*s = ltx.ChecksumPage(ph.Pgno, a.page)
		a.sum ^= *s
	}
	if err := dec.Close(); err != nil {
		return err
	}
	a.resize(h.Commit, lock)
	if err := a.db.Truncate(int64(h.Commit) * int64(h.PageSize)); err != nil {
		return err
	}
	if a.unset > 0 {
		return fmt.Errorf("%d pages of the database were never written", a.unset)
	}
	if post := dec.Trailer().PostApplyChecksum; post != a.checksum() {
		return fmt.Errorf("the database's checksum after it is %s, the database's is %s", post, a.checksum())
	}
	a.min, a.txid = h.MinTXID, h.MaxTXID
	return nil
}

// resize makes the database n pages long in the checksums.
func (a *applier) resize(n, lock uint32) {
	for p := uint32(len(a.sums)); p > n; p-- {
		s := a.sums[p-1]
		if s == 0 && p != lock {
			a.unset--
		}
		a.sum ^= s
	}
	for p := uint32(len(a.sums)) + 1; p <= n; p++ {
		if p != lock {
			a.unset++
		}
	}
	if int(n) <= cap(a.sums) {
		old := len(a.sums)
		a.sums = a.sums[:n]
		if int(n) > old {
			clear(a.sums[old:])
		}
		return
	}
	a.sums = append(a.sums, make([]ltx.Checksum, int(n)-len(a.sums))...)
}

// checksum is the checksum of the database.
func (a *applier) checksum() ltx.Checksum { return ltx.ChecksumFlag | a.sum }

// integrityCheck runs PRAGMA integrity_check on the database in f. It is
// opened in rollback journal mode, so that SQLite makes no WAL or
// shared memory file beside it, and given its journal mode back after.
func integrityCheck(f *os.File) error {
	var mode [2]byte
	if _, err := f.ReadAt(mode[:], 18); err != nil {
		return err
	}
	if _, err := f.WriteAt([]byte{1, 1}, 18); err != nil {
		return err
	}
	err := func() error {
		conn, err := csqlite.Open(f.Name(), csqlite.OpenReadOnly|csqlite.OpenNoMutex, "")
		if err != nil {
			return err
		}
		defer conn.Close()
		s, _, err := conn.Prepare("PRAGMA integrity_check")
		if err != nil {
			return err
		}
		defer s.Finalize()
		row, err := s.Step()
		if err != nil {
			return err
		}
		if !row || s.ColumnText(0) != "ok" {
			return fmt.Errorf("replica: the restored database fails integrity_check: %s", s.ColumnText(0))
		}
		return nil
	}()
	if _, werr := f.WriteAt(mode[:], 18); err == nil {
		err = werr
	}
	if err == nil {
		err = f.Sync()
	}
	return err
}
