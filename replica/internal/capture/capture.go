// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package capture copies each change of a database in WAL mode into LTX
// files in a local staging folder, from which the replicator uploads them,
// and runs the database's checkpoints, so that no frame is checkpointed
// before it is copied.
//
// Every Interval the capturer opens a short read transaction and reads the
// WAL header SQLite sees in it: the generation of the WAL (its salts), its
// last committed frame (mxFrame) and a count of the commits made (iChange,
// one more for each commit of any process, kept when the WAL starts over).
// It reads the frames after the last one it staged up to that frame,
// checks each as SQLite's recovery does, and stages the pages as one LTX
// file of the next TXID, synced before it counts. A frame after mxFrame,
// of a transaction that has not committed, is never read.
//
// The count of commits tells whether the WAL went on in a way the capturer
// saw: the commit frames it reads must be exactly the commits made since
// the last read, also across a new generation of the WAL. When they are
// not, another process wrote and checkpointed, or the WAL went back, and
// the capturer stages a full copy of the database as the next TXID instead.
//
// Connections of the driver to the database have no automatic checkpoints
// and cannot run a checkpoint (replhook.Attach). The capturer checkpoints
// once the WAL holds CheckpointFrames frames, holding the driver's write
// lock of the file: it reads the WAL to its end, stages it and checkpoints,
// so that no frame of this process is checkpointed before it is staged. A
// writer of the driver does it as it gives the lock back (committed), as
// SQLite's automatic checkpoints run after a commit, so the WAL stays short.
// A step does it too when it finds the WAL that long.
//
// On start, the last staged file says where in the WAL staging stopped and
// the checksum of the database there. The capturer reads the database,
// works out its checksum at that point, and goes on from there only if it
// is the same. Otherwise it stages a full copy.
package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/superfly/ltx"

	// The driver fills in replhook.
	_ "github.com/liustack/sqlite"
	"github.com/liustack/sqlite/internal/csqlite"
	"github.com/liustack/sqlite/internal/replhook"
	"github.com/liustack/sqlite/internal/vfs"
)

// Options are the options of Open.
type Options struct {
	// Interval is the time between two reads of the WAL. Default 1s.
	Interval time.Duration
	// CheckpointFrames is how many frames the WAL holds before the capturer
	// checkpoints it. Default 4000 (about 16 MB of 4 KiB pages): at 1000, as
	// SQLite's automatic checkpoints, a write costs about a tenth more.
	CheckpointFrames uint32
	// Limit is the most bytes of level 0 files staged and not uploaded yet
	// (see Done): past it, staging stops until they are uploaded, and a full
	// copy of the database follows them. Checkpoints go on, so the WAL
	// stays bounded. The last full copy, which staging goes on from, does
	// not count, and the step that passes the limit stages no more after
	// that one read of the WAL. 0 is no limit.
	Limit int64
	// Uploaded is the last TXID uploaded when Open runs.
	Uploaded ltx.TXID
	// OnError, if set, gets the errors of the reads made in the background,
	// and an error under ErrFullCopy for each full copy staged because the
	// WAL could not be followed. Open, Close and the goroutine of the
	// background steps call it with no lock of the capturer held. It should
	// return soon, as a log does, and must not call Close, which waits for
	// that goroutine, nor wait for a write to the database: the caller of
	// Close may hold the write lock.
	OnError func(error)
	// Start, if set, is called once Open has the database and the lock of
	// its staging, before it resumes, with the staged files in TXID order
	// (their headers and trailers, not checked). An error stops Open. A
	// Restart drops the staged files and starts over from it.
	Start func(staged []StagedFile) (*Restart, error)
	// Hold keeps the staging folder locked after Close, until Unlock, so
	// that the caller can upload what Close staged, and call Done, before
	// another process stages the database.
	Hold bool
}

// A StagedFile is a staged LTX file, of one TXID.
type StagedFile struct {
	Min, TXID ltx.TXID     // the first and last TXID it holds: one but after a restore
	Pre, Post ltx.Checksum // the checksums of the database before and after it
}

// A Restart starts the staged files over: a full copy of the database as
// TXID+1, whose pre-apply checksum is Post (a snapshot for TXID 0).
type Restart struct {
	TXID ltx.TXID
	Post ltx.Checksum
}

// ErrFullCopy is under the error reported for a full copy of the database
// staged because the WAL went on in a way the capturer could not follow, or
// the database is not where the staged files end.
var ErrFullCopy = errors.New("capture: staged a full copy of the database")

// ErrStagingFull is under the error reported when staging stops at
// Options.Limit, and the error of Sync while it is stopped.
var ErrStagingFull = errors.New("capture: the staged files not uploaded reached the limit")

// closeWait is how long Close waits for the driver's write lock to
// checkpoint. Tests shorten it.
var closeWait = 5 * time.Second

// A Capturer stages the changes of one database file.
type Capturer struct {
	opt    Options
	path   string // the database, as SQLite names it
	dir    string // the staging folder of its level 0 files
	mode   os.FileMode
	conn   *csqlite.Conn
	detach func()
	unlock func()   // of lockStaging
	wal    *os.File // the WAL, once it exists

	ctx    context.Context // done once Close starts
	cancel context.CancelFunc
	done   chan struct{} // closed when the loop has stopped

	mu lock // one step at a time
	// What is staged: the database file and frames 1 to pos of the WAL
	// generation gen, after change commits.
	gen    [8]byte
	pos    uint32
	cksum  [2]uint32 // of frame pos, when pos > 0
	change uint32
	// lost, when not nil, says why the WAL can no longer be followed: the
	// next step stages a full copy.
	lost error

	pageSize   int
	txid       ltx.TXID       // of the last staged file
	post       ltx.Checksum   // the checksum of the database it leaves
	sums       []ltx.Checksum // the checksum of each page there, of pgno-1, 0 for the lock page
	summer     *pageSummer
	frame      []byte
	commitPage []byte // the page of the last commit frame scanned (capture)
	fullCopies int
	closed     bool
	// full says staging stopped at the limit: lost says so, and the full
	// copy waits until nothing staged is pending.
	full bool

	snapMu lock          // one snapshot at a time
	snap   *csqlite.Conn // the connection snapshots read with, under mu
	// later holds the errors of the checkpoints run for a writer of the
	// driver (committed), which the next step of run reports.
	later []error

	durable atomic.Uint64 // the last TXID whose name is synced
	staged  chan struct{} // gets a value when durable moves

	sizeMu  sync.Mutex
	acked   ltx.TXID     // the last TXID uploaded
	pending []stagedSize // the files staged after acked, in TXID order
}

// Open starts staging the changes of the database name, before the driver
// opens it: it fails when the driver of this process has it open. The
// database is put in WAL mode. Open stages a copy of the whole database
// first, unless the staged files end where the database is.
func Open(name string, opt Options) (*Capturer, error) {
	if opt.Interval <= 0 {
		opt.Interval = time.Second
	}
	if opt.CheckpointFrames == 0 {
		opt.CheckpointFrames = 4000
	}
	conn, err := csqlite.Open(name, csqlite.OpenReadWrite|csqlite.OpenCreate|csqlite.OpenURI|csqlite.OpenNoMutex, "")
	if err != nil {
		return nil, err
	}
	c := &Capturer{opt: opt, conn: conn, summer: newPageSummer(), mu: newLock(), snapMu: newLock(), done: make(chan struct{}), staged: make(chan struct{}, 1)}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	// A write of the driver after Attach waits for the capturer to be open.
	c.mu.Lock()
	err = c.open()
	later := c.later
	c.later = nil
	c.mu.Unlock()
	if err != nil {
		c.release(false)
		return nil, errors.Join(append([]error{err}, later...)...)
	}
	for _, e := range later {
		c.report(e)
	}
	go c.run()
	return c, nil
}

func (c *Capturer) open() error {
	if c.path = c.conn.Filename("main"); c.path == "" {
		return errors.New("capture: a memory or temporary database cannot be replicated")
	}
	detach, err := replhook.Attach(c.path, c.committed)
	if err != nil {
		return err
	}
	c.detach = detach
	// One process stages the database: the others are refused before they
	// touch its staging.
	if c.unlock, err = lockStaging(c.path); err != nil {
		return err
	}
	// The capturer never waits for a lock: what is busy is tried again at
	// the next step.
	if err := c.conn.BusyTimeout(0); err != nil {
		return err
	}
	if mode, err := pragma(c.conn, "journal_mode=WAL"); err != nil {
		return err
	} else if mode != "wal" {
		return fmt.Errorf("capture: the database is in journal mode %s, not WAL", mode)
	}
	ps, err := pragma(c.conn, "page_size")
	if err != nil {
		return err
	}
	if _, err := fmt.Sscan(ps, &c.pageSize); err != nil {
		return err
	}
	c.frame = make([]byte, frameSize(c.pageSize))
	c.commitPage = make([]byte, c.pageSize)
	st, err := os.Stat(c.path)
	if err != nil {
		return err
	}
	c.mode = st.Mode().Perm()
	c.dir = StageDir(c.path)
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	// A snapshot left by an earlier process is not uploaded: after a
	// restart its TXIDs may name other states.
	if err := os.RemoveAll(SnapshotDir(c.path)); err != nil {
		return err
	}
	// Nor is what a compaction killed on the way left, which may take the
	// room staging needs.
	if err := dropCompactions(c.path); err != nil {
		return err
	}
	// Before anything is staged, for the limit.
	if err := c.countPending(); err != nil {
		return err
	}
	if err := c.start(); err != nil {
		return err
	}
	// The last staged file may be from before a crash that left its name
	// unsynced, and the folders may be new.
	if err := syncUp(c.path); err != nil {
		return err
	}
	c.setDurable(c.txid)
	return c.countPending()
}

// pragma is the value PRAGMA p gives.
func pragma(c *csqlite.Conn, p string) (string, error) {
	s, _, err := c.Prepare("PRAGMA " + p)
	if err != nil {
		return "", err
	}
	defer s.Finalize()
	if row, err := s.Step(); err != nil || !row {
		return "", fmt.Errorf("PRAGMA %s: no value (%v)", p, err)
	}
	return s.ColumnText(0), nil
}

// release gives back what Open took, but the lock of the staging folder
// when hold.
func (c *Capturer) release(hold bool) {
	if c.wal != nil {
		c.wal.Close()
	}
	c.conn.Close()
	if c.snap != nil {
		c.snap.Close()
	}
	if c.detach != nil {
		c.detach()
	}
	if c.unlock != nil && !hold {
		c.unlock()
		c.unlock = nil
	}
}

// start resumes from the staged files, or stages a full copy.
func (c *Capturer) start() error {
	if c.opt.Start != nil {
		files, err := stagedFiles(c.dir)
		if err != nil {
			return err
		}
		r, err := c.opt.Start(files)
		if err != nil {
			return err
		}
		if r != nil {
			return c.restart(*r)
		}
	}
	last, post, ok, err := lastStaged(c.dir)
	if err != nil {
		return err
	}
	if !ok {
		return c.fullCopy(nil)
	}
	// Staging stopped at the limit before: it stays stopped, and the full
	// copy follows the upload of what is staged.
	if c.over() {
		c.txid, c.post = last.MaxTXID, post
		c.note(c.stop())
		return nil
	}
	if resumed, err := c.resume(last, post); err != nil || resumed {
		return err
	}
	c.txid, c.post = last.MaxTXID, post
	return c.fullCopy(errors.New("the database is not where the staged files end"))
}

// run takes a step every Interval until Close.
func (c *Capturer) run() {
	defer close(c.done)
	t := time.NewTicker(c.opt.Interval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		err := c.step(c.ctx)
		later := c.later
		c.later = nil
		c.mu.Unlock()
		for _, e := range later {
			c.report(e)
		}
		if err != nil && c.ctx.Err() == nil {
			c.report(err)
		}
	}
}

// note keeps err for OnError, which the next step, or Close, gives it with
// no lock held. c.mu is held.
func (c *Capturer) note(err error) {
	c.later = append(c.later, err)
}

func (c *Capturer) report(err error) {
	if c.opt.OnError != nil {
		c.opt.OnError(err)
	}
}

// Sync stages what was committed up to now. ctx bounds the waits for the
// capturer and for the driver's write lock, which a checkpoint takes, and
// so does Close.
func (c *Capturer) Sync(ctx context.Context) error {
	ctx, cancel := c.bind(ctx)
	defer cancel()
	if err := c.mu.LockCtx(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("capture: closed")
	}
	if err := c.step(ctx); err != nil {
		return err
	}
	if c.full {
		return c.lost
	}
	return nil
}

// bind gives ctx, ended too when Close starts.
func (c *Capturer) bind(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// Close stages what is left, checkpoints and stops. With staging stopped at
// the limit, it stages nothing and gives an error under ErrStagingFull. When
// a write transaction keeps the driver's write lock longer than closeWait,
// the WAL is left as it is, staged, for the next Open. Connections the
// driver opens to the database after Close are as before replication.
func (c *Capturer) Close() error {
	c.cancel()
	<-c.done
	later, err := c.close()
	// No step comes for what was noted.
	for _, e := range later {
		c.report(e)
	}
	return err
}

// close is Close once the background steps stopped. It returns what was
// noted for OnError.
func (c *Capturer) close() ([]error, error) {
	// A snapshot reads with c.snap, which release closes.
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil
	}
	c.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), closeWait)
	defer cancel()
	err := c.step(ctx)
	if err == nil && c.full {
		err = c.checkpointOnly(ctx)
	} else if err == nil {
		err = c.checkpoint(ctx)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = nil
	}
	// What was committed since staging stopped is in the database only:
	// the next Open stages it as a full copy.
	if c.full {
		err = errors.Join(c.lost, err)
	}
	c.release(c.opt.Hold)
	later := c.later
	c.later = nil
	return later, err
}

// TXID is the TXID of the last staged file.
func (c *Capturer) TXID() ltx.TXID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.txid
}

// FullCopies is how many full copies were staged after the first.
func (c *Capturer) FullCopies() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fullCopies
}

// Dir is the staging folder of the level 0 files.
func (c *Capturer) Dir() string { return c.dir }

// step stages what was committed, or a full copy when the WAL was lost, and
// checkpoints when the WAL is long enough. Past the limit it stops staging
// and only checkpoints, until nothing staged is pending.
func (c *Capturer) step(ctx context.Context) error {
	if vfs.WALStopped(c.path) {
		if err := c.walStopped(); err != nil {
			return err
		}
	}
	// A staged file whose folder did not sync is not uploaded until it does.
	if err := c.syncNames(); err != nil {
		return err
	}
	if err := c.goOn(); err != nil {
		return err
	}
	if !c.full && c.over() {
		c.note(c.stop())
	}
	if c.full {
		return c.checkpointLost(ctx)
	}
	if c.lost != nil {
		return c.copyLost()
	}
	return c.stageAndCheckpoint(ctx)
}

// errWALStopped is why the WAL is lost after a failed sync stopped it.
var errWALStopped = errors.New("a sync of the WAL failed: the frames it may not have written were cut from it")

// walStopped closes the connections of the capturer after a failed sync
// stopped the WAL of the database: the WAL opens again only once every
// connection that has it open is closed (internal/vfs). The frames cut from
// it may have been staged, and the database no longer has them, so a full
// copy follows. A snapshot under way closes its connection itself.
func (c *Capturer) walStopped() error {
	c.lose(errWALStopped)
	if c.wal != nil {
		c.wal.Close()
		c.wal = nil
	}
	if c.snapMu.TryLock() {
		if c.snap != nil {
			c.snap.Close()
			c.snap = nil
		}
		c.snapMu.Unlock()
	}
	// A new connection does not open the WAL before it reads.
	conn, err := csqlite.Open(c.path, csqlite.OpenReadWrite|csqlite.OpenNoMutex, "")
	if err != nil {
		return err
	}
	if err := conn.BusyTimeout(0); err != nil {
		conn.Close()
		return err
	}
	if err := c.conn.Close(); err != nil {
		conn.Close()
		return err
	}
	c.conn = conn
	return nil
}

// over says whether the staged files not uploaded passed Options.Limit.
func (c *Capturer) over() bool {
	return c.opt.Limit > 0 && c.counted() > c.opt.Limit
}

// stop stops staging at the limit, and returns the error to report. It
// replaces why the WAL was lost, if it was: the full copy follows all the
// same.
func (c *Capturer) stop() error {
	c.full = true
	c.lost = fmt.Errorf("%w (%d bytes, at TXID %s): staging stops until they are uploaded", ErrStagingFull, c.opt.Limit, c.txid)
	return c.lost
}

// copyLost stages the full copy that follows a lost WAL, or stops staging
// when the staged files not uploaded, the last full copy too, passed the
// limit: the new copy would make that one count.
func (c *Capturer) copyLost() error {
	if c.opt.Limit > 0 && c.Pending() > c.opt.Limit {
		c.note(c.stop())
		return nil
	}
	return c.fullCopy(c.lost)
}

// stageAndCheckpoint stages what was committed and, when the WAL holds
// CheckpointFrames frames, checkpoints it under the driver's write lock. The
// writers of the driver mostly do that first, as they commit (committed).
func (c *Capturer) stageAndCheckpoint(ctx context.Context) error {
	frames, err := c.capture()
	switch {
	case err != nil:
		return err
	case c.lost != nil:
		return c.copyLost()
	case frames < c.opt.CheckpointFrames || c.over():
		return nil
	}
	unlock, err := replhook.Lock(ctx, c.path)
	if err != nil {
		return err
	}
	defer unlock()
	return c.checkpointHeld()
}

// committed is called by the driver each time a writer gives the write lock
// of the file back, with the frames of the WAL it left (replhook.Attach).
// Once the WAL holds CheckpointFrames frames, the capturer stages it and
// checkpoints it there, as SQLite's automatic checkpoints run after a
// commit: no writer runs meanwhile. A step under way has the capturer, and
// the next write tries again.
func (c *Capturer) committed(frames uint32) {
	if frames < c.opt.CheckpointFrames || !c.mu.TryLock() {
		return
	}
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	// The writer holds its lock: OnError runs after the next step, with none.
	if !c.full && c.over() {
		c.note(c.stop())
	}
	// Even when staging stopped: the frames of the staged files go next.
	err := c.syncNames()
	switch {
	case err != nil:
	case c.full:
		// Staging stopped: the frames are not staged, a full copy follows.
		err = c.passive()
	case c.lost == nil:
		err = c.checkpointHeld()
	}
	if err != nil {
		c.note(err)
	}
}

// checkpointHeld stages the WAL to its end and checkpoints it. Call it
// holding the driver's write lock of the file: no frame comes meanwhile, so
// SQLite copies them all, unless a reader still uses some, and the next
// writer starts the WAL over. A WAL found lost is left to the next step,
// which stages a full copy.
func (c *Capturer) checkpointHeld() error {
	if _, err := c.capture(); err != nil || c.lost != nil {
		return err
	}
	if err := c.syncNames(); err != nil {
		return err
	}
	return c.passive()
}

// syncNames makes the names of the staged files durable, which comes before
// a checkpoint lets their frames go.
func (c *Capturer) syncNames() error {
	if c.Durable() >= c.txid {
		return nil
	}
	if err := syncDir(c.dir); err != nil {
		return err
	}
	c.setDurable(c.txid)
	return nil
}

// passive checkpoints the WAL, PASSIVE: it never waits, and copies what no
// reader still uses.
func (c *Capturer) passive() error {
	if _, _, err := c.conn.Checkpoint(csqlite.CheckpointPassive); err != nil && !busy(err) {
		return err
	}
	return nil
}

// busy says whether err is SQLITE_BUSY.
func busy(err error) bool {
	var e *csqlite.Error
	return errors.As(err, &e) && e.Code() == 5
}

// checkpoint stages the WAL to its end under the driver's write lock of the
// file and checkpoints it. When the WAL was lost, it stages a full copy
// instead, after the lock, so that writers do not wait for it, and leaves
// the checkpoint to a later step.
func (c *Capturer) checkpoint(ctx context.Context) error {
	unlock, err := replhook.Lock(ctx, c.path)
	if err != nil {
		return err
	}
	if _, err := c.capture(); err != nil || c.lost != nil {
		unlock()
		if err != nil {
			return err
		}
		return c.copyLost()
	}
	defer unlock()
	// The names of the staged files are kept before the frames go.
	if err := syncDir(c.dir); err != nil {
		return err
	}
	c.setDurable(c.txid)
	return c.truncate()
}

// truncate checkpoints the WAL. TRUNCATE starts it over, empty. With
// readers it cannot: the frames are still copied, and the next writer starts
// the WAL over. Call it holding the driver's write lock.
func (c *Capturer) truncate() error {
	if _, _, err := c.conn.Checkpoint(csqlite.CheckpointTruncate); err != nil && !busy(err) {
		return err
	}
	return nil
}

// checkpointLost checkpoints without staging, when the WAL is long enough:
// once the WAL is lost, its frames are not staged.
func (c *Capturer) checkpointLost(ctx context.Context) error {
	if err := c.beginRead(); err != nil {
		return err
	}
	h, err := c.conn.WALHeader()
	c.endRead()
	if err != nil || h.MaxFrame < c.opt.CheckpointFrames {
		return err
	}
	return c.checkpointOnly(ctx)
}

// checkpointOnly checkpoints under the driver's write lock, staging
// nothing.
func (c *Capturer) checkpointOnly(ctx context.Context) error {
	unlock, err := replhook.Lock(ctx, c.path)
	if err != nil {
		return err
	}
	defer unlock()
	return c.truncate()
}

// beginRead opens a read transaction on the capturer's connection.
func (c *Capturer) beginRead() error { return beginRead(c.conn) }

// beginRead opens a read transaction on conn.
func beginRead(conn *csqlite.Conn) error {
	if err := conn.Exec("BEGIN"); err != nil {
		return err
	}
	s, _, err := conn.Prepare("SELECT 1 FROM sqlite_schema LIMIT 1")
	if err == nil {
		_, err = s.Step()
		s.Finalize()
	}
	if err != nil {
		conn.Exec("ROLLBACK")
	}
	return err
}

func (c *Capturer) endRead() { c.conn.Exec("ROLLBACK") }

// walFile is the WAL, opened once.
func (c *Capturer) walFile() (*os.File, error) {
	if c.wal == nil {
		f, err := os.Open(c.path + "-wal")
		if err != nil {
			return nil, err
		}
		c.wal = f
	}
	return c.wal, nil
}

// lose records that the WAL can no longer be followed.
func (c *Capturer) lose(why error) {
	if c.lost == nil {
		c.lost = why
	}
}

// A version of a page in the WAL: frame n, whose checksum carries on from
// prev.
type version struct {
	n    uint32
	prev [2]uint32
}

// capture stages the frames committed since the last step. It returns the
// number of frames in the WAL. Once the WAL is lost it does nothing: the
// frames it would read no longer go on from what is staged.
func (c *Capturer) capture() (uint32, error) {
	if c.lost != nil {
		return 0, nil
	}
	if err := c.beginRead(); err != nil {
		return 0, err
	}
	defer c.endRead()
	h, err := c.conn.WALHeader()
	if err != nil {
		return 0, err
	}
	commits := h.Change - c.change
	from, cksum := c.pos, c.cksum
	switch {
	case h.Salt != c.gen:
		from = 0 // a new generation: all its frames are new
	case h.MaxFrame < c.pos:
		c.lose(fmt.Errorf("the WAL has %d frames, %d were staged", h.MaxFrame, c.pos))
		return 0, nil
	}
	if h.MaxFrame == from {
		if commits != 0 {
			c.lose(fmt.Errorf("%d commits are not in the WAL", commits))
		} else if h.Salt != c.gen {
			// The WAL started over and holds nothing yet.
			c.gen, c.pos, c.change = h.Salt, 0, h.Change
		}
		return h.MaxFrame, nil
	}
	wal, err := c.walFile()
	if err != nil {
		return 0, err
	}
	wh, err := readWALHeader(wal)
	if err != nil || wh.salt != h.Salt || wh.pageSize != c.pageSize {
		c.lose(fmt.Errorf("the WAL file is not the WAL SQLite reads (%v)", err))
		return 0, nil
	}
	if from == 0 {
		cksum = wh.cksum
	}
	last := map[uint32]version{}
	var n, last1 uint32 // commits, and the commit of frame MaxFrame
	var repeat frame    // the last frame, if a commit frame: pgno and commit
	end, err := scanFrames(wal, wh, from, h.MaxFrame, cksum, func(f *frame) error {
		last[f.pgno] = version{f.n, f.prev}
		last1 = f.commit
		if f.commit == 0 {
			repeat = frame{}
			return nil
		}
		// With synchronous=FULL and no powersafe overwrite, SQLite
		// repeats the last frame of a commit up to a sector boundary
		// (walFrames). A repeat is not a commit of its own. A commit that
		// writes exactly that frame again is not counted either: the
		// count comes short, which stages a full copy, never misses a
		// commit.
		if f.pgno == repeat.pgno && f.commit == repeat.commit && bytes.Equal(f.data, c.commitPage) {
			return nil
		}
		n++
		repeat.pgno, repeat.commit = f.pgno, f.commit
		copy(c.commitPage, f.data)
		return nil
	})
	switch {
	case errors.Is(err, errFrame):
		c.lose(err)
		return 0, nil
	case err != nil:
		return 0, err
	case end != h.Checksum || last1 != h.Pages:
		c.lose(errors.New("the frames of the WAL end elsewhere than its header says"))
		return 0, nil
	case n != commits:
		c.lose(fmt.Errorf("%d commits since the last step, %d of them in the WAL", commits, n))
		return 0, nil
	}
	if err := c.stageFrames(wal, wh, last, h, from); err != nil {
		if errors.Is(err, errFrame) {
			c.lose(err)
			return 0, nil
		}
		return 0, err
	}
	return h.MaxFrame, nil
}

// stageFrames stages as the next TXID the pages of last, the latest version
// of each page in frames from+1 to at.MaxFrame of the WAL whose header SQLite
// reads is at. Once the file is placed, it moves the checksums and the WAL
// position there, even if it then returns an error.
func (c *Capturer) stageFrames(wal *os.File, wh walHeader, last map[uint32]version, at csqlite.WALHeader, from uint32) error {
	size := at.Pages
	old := uint32(len(c.sums))
	lock := ltx.LockPgno(uint32(c.pageSize))
	for p := old + 1; p <= size; p++ {
		if _, ok := last[p]; !ok && p != lock {
			return fmt.Errorf("page %d is new and not in the WAL: %w", p, errFrame)
		}
	}
	pgnos := make([]uint32, 0, len(last))
	for p := range last {
		if p <= size {
			pgnos = append(pgnos, p)
		}
	}
	slices.Sort(pgnos)
	h := c.header(size)
	setWALPos(&h, at.Salt, from, at.MaxFrame)
	sums := make([]ltx.Checksum, len(pgnos))
	post := c.post
	for p := size + 1; p <= old; p++ {
		post ^= c.sums[p-1] // the database got shorter
	}
	placed, n, err := stageFile(c.dir, c.mode, h, func(put func(uint32, []byte) error) (ltx.Checksum, error) {
		for i, p := range pgnos {
			v := last[p]
			data, err := readFrame(wal, wh, v.n, v.prev, c.frame)
			if err != nil {
				return 0, err
			}
			sums[i] = c.summer.sum(p, data)
			if p <= old {
				post ^= c.sums[p-1]
			}
			post ^= sums[i]
			if err := put(p, data); err != nil {
				return 0, err
			}
		}
		post |= ltx.ChecksumFlag
		return post, nil
	})
	if !placed {
		return err
	}
	c.addPending(h.MaxTXID, n, false)
	if err == nil {
		defer c.setDurable(h.MaxTXID)
	}
	if size < old {
		c.sums = c.sums[:size]
	} else {
		c.sums = append(c.sums, make([]ltx.Checksum, size-old)...)
	}
	for i, p := range pgnos {
		c.sums[p-1] = sums[i]
	}
	c.txid, c.post = h.MaxTXID, post
	c.gen, c.pos, c.cksum, c.change = at.Salt, at.MaxFrame, at.Checksum, at.Change
	return err
}

// header is the header of the next staged file, for a database of size
// pages.
func (c *Capturer) header(size uint32) ltx.Header {
	h := ltx.Header{
		Version:   ltx.Version,
		PageSize:  uint32(c.pageSize),
		Commit:    size,
		MinTXID:   c.txid + 1,
		MaxTXID:   c.txid + 1,
		Timestamp: time.Now().UnixMilli(),
	}
	if c.txid > 0 {
		h.PreApplyChecksum = c.post
	}
	return h
}

// fullCopy stages every page of the database as the next TXID, a snapshot
// for TXID 1. why is the reason when it is not the first.
func (c *Capturer) fullCopy(why error) error {
	if err := c.beginRead(); err != nil {
		return err
	}
	defer c.endRead()
	h, err := c.conn.WALHeader()
	if err != nil {
		return err
	}
	size, err := c.conn.PageCount()
	if err != nil {
		return err
	}
	lock := ltx.LockPgno(uint32(c.pageSize))
	sums := make([]ltx.Checksum, size)
	hdr := c.header(size)
	setWALPos(&hdr, h.Salt, h.MaxFrame, h.MaxFrame)
	page := make([]byte, c.pageSize)
	var post ltx.Checksum
	placed, n, err := stageFile(c.dir, c.mode, hdr, func(put func(uint32, []byte) error) (ltx.Checksum, error) {
		for p := uint32(1); p <= size; p++ {
			if p == lock {
				continue
			}
			if err := c.conn.ReadPage(p, page); err != nil {
				return 0, err
			}
			sums[p-1] = c.summer.sum(p, page)
			post ^= sums[p-1]
			if err := put(p, page); err != nil {
				return 0, err
			}
		}
		post |= ltx.ChecksumFlag
		return post, nil
	})
	if !placed {
		return err
	}
	c.addPending(hdr.MaxTXID, n, true)
	if err == nil {
		defer c.setDurable(hdr.MaxTXID)
	}
	c.txid, c.post, c.sums = hdr.MaxTXID, post, sums
	c.gen, c.pos, c.cksum, c.change = h.Salt, h.MaxFrame, h.Checksum, h.Change
	c.lost = nil
	if why != nil {
		c.fullCopies++
		c.note(fmt.Errorf("%w (TXID %s): %w", ErrFullCopy, hdr.MaxTXID, why))
	}
	return err
}

// resume goes on from the last staged file, last, which leaves the database
// with checksum post, if the database is there or after it. It reads the
// whole database, works out its checksum where the file ends from the
// frames written since, and stages those frames when it is the same.
func (c *Capturer) resume(last ltx.Header, post ltx.Checksum) (bool, error) {
	if int(last.PageSize) != c.pageSize {
		return false, nil
	}
	if err := c.beginRead(); err != nil {
		return false, err
	}
	defer c.endRead()
	h, err := c.conn.WALHeader()
	if err != nil {
		return false, err
	}
	size, err := c.conn.PageCount()
	if err != nil {
		return false, err
	}
	lock := ltx.LockPgno(uint32(c.pageSize))
	sums := make([]ltx.Checksum, size)
	var now ltx.Checksum
	page := make([]byte, c.pageSize)
	for p := uint32(1); p <= size; p++ {
		if p == lock {
			continue
		}
		if err := c.conn.ReadPage(p, page); err != nil {
			return false, err
		}
		sums[p-1] = c.summer.sum(p, page)
		now ^= sums[p-1]
	}
	now |= ltx.ChecksumFlag

	// The frames staged are those to end of generation salt. Without one
	// of them in the WAL, the database file is where staging ended: the
	// WAL started over after a checkpoint of them all.
	salt, end := walPos(last)
	same := h.Salt == salt && end > 0
	if same && h.MaxFrame < end {
		return false, nil
	}
	from := uint32(0)
	if same {
		from = end
	}
	base := map[uint32]version{}  // the latest version of each page up to end
	since := map[uint32]version{} // and after it
	var wh walHeader
	var endCommit uint32
	var wal *os.File
	if h.MaxFrame > 0 {
		if wal, err = c.walFile(); err != nil {
			return false, err
		}
		if wh, err = readWALHeader(wal); err != nil || wh.salt != h.Salt || wh.pageSize != c.pageSize {
			return false, nil
		}
		ck, err := scanFrames(wal, wh, 0, h.MaxFrame, wh.cksum, func(f *frame) error {
			if f.n <= from {
				base[f.pgno] = version{f.n, f.prev}
				endCommit = f.commit
			} else {
				since[f.pgno] = version{f.n, f.prev}
			}
			return nil
		})
		if errors.Is(err, errFrame) || err == nil && ck != h.Checksum {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	// The size of the database where staging ended.
	var then uint32
	if same {
		if then = endCommit; then == 0 {
			return false, nil
		}
	} else {
		st, err := os.Stat(c.path)
		if err != nil {
			return false, err
		}
		then = uint32(st.Size() / int64(c.pageSize))
	}
	// Its checksum there: the pages written since were what the frames up
	// to end, or else the database file, hold.
	db, err := os.Open(c.path)
	if err != nil {
		return false, err
	}
	defer db.Close()
	thenSums := make([]ltx.Checksum, then)
	var was ltx.Checksum
	for p := uint32(1); p <= then; p++ {
		if p == lock {
			continue
		}
		if _, ok := since[p]; !ok && p <= size {
			thenSums[p-1] = sums[p-1]
			was ^= sums[p-1]
			continue
		}
		data := page
		if v, ok := base[p]; ok {
			if data, err = readFrame(wal, wh, v.n, v.prev, c.frame); err != nil {
				return false, nil
			}
		} else if err := readFull(db, page, int64(p-1)*int64(c.pageSize)); err != nil {
			return false, nil
		}
		thenSums[p-1] = c.summer.sum(p, data)
		was ^= thenSums[p-1]
	}
	if was|ltx.ChecksumFlag != post {
		return false, nil
	}
	c.txid, c.post, c.sums = last.MaxTXID, post, thenSums
	if len(since) > 0 {
		if err := c.stageFrames(wal, wh, since, h, from); err != nil {
			return false, err
		}
		if c.post != now {
			return false, fmt.Errorf("capture: resuming: the checksum is %s after the frames, the database's is %s", c.post, now)
		}
	}
	c.sums = sums
	c.post = now
	c.gen, c.pos, c.cksum, c.change = h.Salt, h.MaxFrame, h.Checksum, h.Change
	return true, nil
}

// restart drops the staged files and stages a full copy as r.TXID+1. The
// files after r.TXID go first, so that none is left behind the copy. Those
// before go once it is placed: a crash between leaves either the copy to
// go on from, or the old files, and the next start decides again.
func (c *Capturer) restart(r Restart) error {
	if err := dropStaged(c.dir, func(t ltx.TXID) bool { return t > r.TXID }); err != nil {
		return err
	}
	c.txid, c.post = r.TXID, r.Post
	if err := c.fullCopy(nil); err != nil {
		return err
	}
	return dropStaged(c.dir, func(t ltx.TXID) bool { return t <= r.TXID })
}

// setDurable records that the names of the staged files up to txid are
// synced, and signals Staged.
func (c *Capturer) setDurable(txid ltx.TXID) {
	c.durable.Store(uint64(txid))
	select {
	case c.staged <- struct{}{}:
	default:
	}
}

// Durable is the last TXID staged whose name is synced: a crash keeps the
// files up to it.
func (c *Capturer) Durable() ltx.TXID { return ltx.TXID(c.durable.Load()) }

// Staged gets a value when Durable moves on.
func (c *Capturer) Staged() <-chan struct{} { return c.staged }

// The size of a staged file not uploaded yet.
type stagedSize struct {
	txid ltx.TXID
	size int64
	full bool // a full copy
}

// goOn stages the full copy that follows a stop at the limit, once nothing
// staged is pending.
func (c *Capturer) goOn() error {
	if !c.full || c.pendingCount() > 0 {
		return nil
	}
	c.full = false
	return c.fullCopy(c.lost)
}

func (c *Capturer) addPending(txid ltx.TXID, size int64, full bool) {
	c.sizeMu.Lock()
	defer c.sizeMu.Unlock()
	if txid > c.acked {
		c.pending = append(c.pending, stagedSize{txid, size, full})
	}
}

// countPending finds the staged files after Options.Uploaded, once Open
// staged what it stages.
func (c *Capturer) countPending() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	var pending []stagedSize
	for _, e := range entries { // in TXID order: the names are zero-padded hex
		_, max, err := ltx.ParseFilename(e.Name())
		if err != nil || max <= c.opt.Uploaded {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		full, err := isFullCopy(filepath.Join(c.dir, e.Name()))
		if err != nil {
			return err
		}
		pending = append(pending, stagedSize{max, info.Size(), full})
	}
	c.sizeMu.Lock()
	defer c.sizeMu.Unlock()
	c.acked, c.pending = c.opt.Uploaded, pending
	return nil
}

// counted is the size of the staged files not uploaded that count against
// Options.Limit: all but the last full copy.
func (c *Capturer) counted() int64 {
	c.sizeMu.Lock()
	defer c.sizeMu.Unlock()
	last := -1
	for i, s := range c.pending {
		if s.full {
			last = i
		}
	}
	var n int64
	for i, s := range c.pending {
		if i != last {
			n += s.size
		}
	}
	return n
}

func (c *Capturer) pendingCount() int {
	c.sizeMu.Lock()
	defer c.sizeMu.Unlock()
	return len(c.pending)
}

// Pending is the size of the staged files not uploaded yet.
func (c *Capturer) Pending() int64 {
	c.sizeMu.Lock()
	defer c.sizeMu.Unlock()
	var n int64
	for _, s := range c.pending {
		n += s.size
	}
	return n
}

// Done records that the staged files up to txid are uploaded, or never
// will be, and removes them but the last staged file, which the capturer
// goes on from after a restart.
func (c *Capturer) Done(txid ltx.TXID) error {
	durable := c.Durable()
	c.sizeMu.Lock()
	if txid > c.acked {
		c.acked = txid
		i := 0
		for i < len(c.pending) && c.pending[i].txid <= txid {
			i++
		}
		c.pending = slices.Delete(c.pending, 0, i)
	}
	c.sizeMu.Unlock()
	keep := min(txid, durable-1)
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, max, err := ltx.ParseFilename(e.Name()); err == nil && max <= keep {
			if err := os.Remove(filepath.Join(c.dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Unlock lets another process stage the database, after Close with
// Options.Hold. It does nothing more when called again.
func (c *Capturer) Unlock() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed && c.unlock != nil {
		c.unlock()
		c.unlock = nil
	}
}
