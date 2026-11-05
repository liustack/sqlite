// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
)

// One database file has one writer inside the driver. A connection takes the
// file's write lock before a statement that may write, and gives it back once
// the connection is out of its transaction. Writers of one process wait for
// each other here, in order, and not in SQLite's busy handler, whose sleeps
// grow while the writer that holds the lock waits for a thread to run on.
// Readers never take it. Other processes still meet SQLite's own locks.
//
// Group commit: an autocommit statement that only changes the main database
// and depends on nothing of its connection (csqlite.Stmt.Mergeable) does not wait
// for the lock when another writer holds it. It joins the file's queue. The
// writer that gives the lock back runs the requests of the queue that its
// connection can run exactly as their own would (same key, neither
// connection changed since it opened: conn.canMerge) as one transaction,
// each statement in a SAVEPOINT of its own, and commits once. Each caller
// gets its own result after the commit. A request no writer can run that
// way gets the lock itself, in turn, and runs on its own connection. With no
// other writer a statement runs directly.
type writeLock struct {
	mu      sync.Mutex
	held    bool
	waiters []chan struct{} // writers that wait for the lock, in order
	queue   []*request      // statements to merge, in order
	refs    int             // connections of the file, and 1 while attached
	file    os.FileInfo     // the file, whatever name it is opened by
	// attached says the file is replicated (replhook.Attach). Guarded by
	// writeLocks.
	attached bool
	stats    batchStats
}

// batchStats counts what group commit does on one file, for the benchmarks
// that decide whether waiting a little before a batch would pay.
type batchStats struct {
	batches atomic.Int64 // batches taken from the queue
	full    atomic.Int64 // batches that took maxBatch statements
	commits atomic.Int64 // batches committed
	merged  atomic.Int64 // statements committed by those
	// commitTime is the time spent in the COMMIT of those, in nanoseconds.
	commitTime atomic.Int64
}

// A request is one statement of the queue.
type request struct {
	ctx  context.Context
	key  string // the key of the caller's connection
	sql  string
	args []driver.NamedValue
	done chan struct{}
	// handed says the lock was handed to the caller, which runs the
	// statement itself.
	handed bool
	res    result
	// inserted says the statement set last_insert_rowid: res.id is then the
	// caller's new one.
	inserted bool
	err      error
}

// maxBatch is the most statements one transaction of the queue takes.
const maxBatch = 256

// writeLocks holds the write lock of each file open. A file is found by
// what it is, not by its name: one file has more than one name (case on a
// file system that ignores it, a short 8.3 name on windows).
var writeLocks struct {
	sync.Mutex
	all []*writeLock
}

// fileOf is the file named path, for writeLocks.
func fileOf(path string) (os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// On windows the identity of the file is read when first compared:
	// read it now, while path names it. A FileInfo whose identity could not
	// be read is the same as no file.
	if !os.SameFile(fi, fi) {
		return nil, fmt.Errorf("sqlite: %s: cannot read what file it is", path)
	}
	return fi, nil
}

// findWriteLock is the write lock of file, nil if none. writeLocks is held.
func findWriteLock(file os.FileInfo) *writeLock {
	for _, l := range writeLocks.all {
		if os.SameFile(l.file, file) {
			return l
		}
	}
	return nil
}

// openWriteLock is the write lock of path for a new connection, and whether
// the file is replicated.
func openWriteLock(path string) (l *writeLock, attached bool, err error) {
	file, err := fileOf(path)
	if err != nil {
		return nil, false, err
	}
	writeLocks.Lock()
	defer writeLocks.Unlock()
	if l = findWriteLock(file); l == nil {
		l = &writeLock{file: file}
		writeLocks.all = append(writeLocks.all, l)
	}
	l.refs++
	return l, l.attached, nil
}

func closeWriteLock(l *writeLock) {
	writeLocks.Lock()
	defer writeLocks.Unlock()
	l.refs--
	if l.refs == 0 {
		writeLocks.all = slices.DeleteFunc(writeLocks.all, func(x *writeLock) bool { return x == l })
	}
}

// errBusy is what a writer gets when the lock stays taken past the busy
// timeout, as SQLite would answer.
var errBusy = &csqlite.Error{Code: 5, Msg: "database is locked"}

// errForeignKey is what a merged statement that leaves a deferred foreign
// key unresolved gets: the error its own COMMIT would have given.
var errForeignKey = &csqlite.Error{Code: 787, Msg: "FOREIGN KEY constraint failed"}

// acquire takes the lock, waiting up to timeout.
func (l *writeLock) acquire(ctx context.Context, timeout time.Duration) error {
	l.mu.Lock()
	if !l.held {
		l.held = true
		l.mu.Unlock()
		return nil
	}
	if timeout <= 0 {
		l.mu.Unlock()
		return errBusy
	}
	ch := make(chan struct{})
	l.waiters = append(l.waiters, ch)
	l.mu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	var err error
	select {
	case <-ch:
		return nil
	case <-t.C:
		err = errBusy
	case <-ctx.Done():
		err = ctx.Err()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, w := range l.waiters {
		if w == ch {
			l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
			return err
		}
	}
	return nil // handed the lock as the wait ended
}

// release gives the lock back from c, nil for the replicator. c first runs
// one batch of the requests it can run. Then the lock goes to the first writer that waits for
// it, or else to the first request of the queue, so that neither waits on
// the other for long.
func (l *writeLock) release(c *conn) {
	l.mu.Lock()
	if c != nil && c.canMerge() {
		if batch := l.take(c, nil); len(batch) > 0 {
			l.mu.Unlock()
			back := c.runBatch(batch)
			l.mu.Lock()
			l.queue = append(back, l.queue...)
		}
	}
	switch {
	case len(l.waiters) > 0:
		ch := l.waiters[0]
		l.waiters = l.waiters[1:]
		close(ch) // the lock goes to it, still held
	case len(l.queue) > 0:
		r := l.queue[0]
		l.queue = l.queue[1:]
		r.handed = true
		close(r.done)
	default:
		l.held = false
	}
	l.mu.Unlock()
}

// take moves the requests of the queue that c can run, after batch, into
// batch, up to maxBatch. l.mu is held.
func (l *writeLock) take(c *conn, batch []*request) []*request {
	n := 0
	for _, r := range l.queue {
		if r.key == c.key {
			n++
		}
	}
	if n = min(n, maxBatch-len(batch)); n > 0 {
		out := make([]*request, len(batch), len(batch)+n)
		copy(out, batch)
		rest := l.queue[:0] // in place: it writes behind where it reads
		for _, r := range l.queue {
			if r.key == c.key && len(out) < cap(out) {
				out = append(out, r)
			} else {
				rest = append(rest, r)
			}
		}
		clear(l.queue[len(rest):])
		l.queue = rest
		batch = out
	}
	if len(batch) > 0 {
		l.stats.batches.Add(1)
		if len(batch) == maxBatch {
			l.stats.full.Add(1)
		}
	}
	return batch
}

// lead runs req, whose connection c has just been handed the lock, as the
// first of a batch with the requests of the queue c can run: a request
// handed the lock does not take a commit of its own. It reports false when
// c cannot run req that way, which then runs alone.
func (l *writeLock) lead(c *conn, req *request) bool {
	req.handed = false
	req.done = make(chan struct{}) // runBatch closes it again
	l.mu.Lock()
	batch := l.take(c, []*request{req})
	l.mu.Unlock()
	back := c.runBatch(batch)
	ran := true
	for i, r := range back {
		if r == req {
			back = append(back[:i:i], back[i+1:]...)
			ran = false
			break
		}
	}
	if len(back) > 0 {
		l.mu.Lock()
		l.queue = append(back, l.queue...)
		l.mu.Unlock()
	}
	return ran
}

// submit runs a mergeable statement: directly when the lock is free, in the
// queue when not.
func (l *writeLock) submit(ctx context.Context, c *conn, s *stmt, args []driver.NamedValue) (*result, error) {
	l.mu.Lock()
	if !l.held {
		l.held = true
		l.mu.Unlock()
		return c.runHeld(ctx, s, args)
	}
	if c.busyTimeout <= 0 {
		l.mu.Unlock()
		return nil, errBusy
	}
	req := &request{ctx: ctx, key: c.key, sql: s.sql, args: args, done: make(chan struct{})}
	l.queue = append(l.queue, req)
	l.mu.Unlock()
	t := time.NewTimer(c.busyTimeout)
	defer t.Stop()
	var err error
	select {
	case <-req.done:
		return c.answered(ctx, s, req)
	case <-t.C:
		err = errBusy
	case <-ctx.Done():
		err = ctx.Err()
	}
	l.mu.Lock()
	for i, q := range l.queue {
		if q == req {
			l.queue = append(l.queue[:i], l.queue[i+1:]...)
			l.mu.Unlock()
			return nil, err
		}
	}
	l.mu.Unlock()
	<-req.done // taken already: a writer runs it, or it has the lock
	return c.answered(ctx, s, req)
}

// runHeld runs s on c, which has just been given the write lock, and gives
// the lock back.
func (c *conn) runHeld(ctx context.Context, s *stmt, args []driver.NamedValue) (*result, error) {
	c.writing = true
	r, err := s.run(ctx, args)
	c.endWrite()
	return r, err
}

// answered is the result of a request of c once it is done.
func (c *conn) answered(ctx context.Context, s *stmt, req *request) (*result, error) {
	if req.handed {
		if err := ctx.Err(); err != nil {
			c.writing = true
			c.endWrite()
			return nil, err
		}
		if !c.canMerge() || !c.wl.lead(c, req) {
			return c.runHeld(ctx, s, req.args)
		}
		c.writing = true
		c.endWrite()
	}
	if req.err != nil {
		return nil, req.err
	}
	r := &req.res // req is done with: its result is the caller's
	if req.inserted {
		c.c.SetLastInsertRowID(r.id)
	} else {
		r.id = c.c.LastInsertRowID()
	}
	return r, nil
}

// canMerge says c may run statements of other connections of its key: it
// runs them exactly as theirs would, since no connection of the key that
// changed itself (conn.private) ever joins the queue.
func (c *conn) canMerge() bool {
	return !c.private && !c.closed && !c.bad && !c.readOnlyTx && !c.c.Broken() && c.c.AutoCommit()
}

// beginWrite takes the write lock before a statement that may write.
func (c *conn) beginWrite(ctx context.Context, readOnly bool) error {
	if c.wl == nil || c.writing || readOnly || c.readOnlyTx {
		return nil
	}
	if err := c.wl.acquire(ctx, c.busyTimeout); err != nil {
		return err
	}
	c.writing = true
	return nil
}

// endWrite gives the lock back once the connection is out of its
// transaction.
func (c *conn) endWrite() {
	if c.writing && c.c.AutoCommit() {
		c.writing = false
		c.wl.release(c)
	}
}

// runBatch runs requests of the queue as one transaction on c, which holds
// the lock and is out of any transaction, and answers each after the
// commit. It returns the requests c turned out not to be able to run, for
// the queue.
//
// A request whose context is done before it runs is answered with its
// error. A statement that fails keeps what SQLite kept of it, as its own
// autocommit transaction would (nothing, or the rows before the failure for
// ON CONFLICT FAIL), and its caller gets the error. One that leaves a
// deferred foreign key unresolved is undone by its SAVEPOINT, as its own
// COMMIT would fail. When a failure ends the whole transaction (an IO error,
// an interrupt, ON CONFLICT ROLLBACK), the statements that had run are run
// again in a new one.
func (c *conn) runBatch(batch []*request) (back []*request) {
	// The statements set c's last_insert_rowid, which is c's own.
	own := c.c.LastInsertRowID()
	defer c.c.SetLastInsertRowID(own)
	for {
		live := batch[:0] // in place: batch is runBatch's own
		for _, r := range batch {
			if err := r.ctx.Err(); err != nil {
				r.err = err
				close(r.done)
				continue
			}
			live = append(live, r)
		}
		batch = live
		if len(batch) == 0 {
			return back
		}
		if err := c.execCached("BEGIN IMMEDIATE"); err != nil {
			answer(batch, err)
			return back
		}
		ok := make([]*request, 0, len(batch))
		lost := false
		for i, r := range batch {
			err := c.runOne(r)
			if errors.Is(err, errNotHere) {
				back = append(back, r)
				continue
			}
			if err == nil {
				ok = append(ok, r)
				continue
			}
			r.err = err
			close(r.done)
			if c.c.AutoCommit() {
				// The transaction is gone: run the others again.
				batch = append(ok, batch[i+1:]...)
				lost = true
				break
			}
		}
		if lost {
			continue
		}
		start := time.Now()
		if err := c.execCached("COMMIT"); err != nil {
			answer(ok, c.abandon(err))
			return back
		}
		c.wl.stats.commits.Add(1)
		c.wl.stats.commitTime.Add(int64(time.Since(start)))
		c.wl.stats.merged.Add(int64(len(ok)))
		answer(ok, nil)
		return back
	}
}

// errNotHere is what runOne gives for a statement c does not run as its own
// connection would: after a change of the schema, it no longer qualifies.
var errNotHere = errors.New("sqlite: not mergeable here")

// runOne runs one statement of a batch in a SAVEPOINT. It leaves the
// transaction open unless SQLite ended it.
func (c *conn) runOne(r *request) error {
	s, err := c.cached(r.sql)
	if err != nil {
		return err
	}
	if !s.s.Mergeable() {
		return errNotHere
	}
	if err := c.execCached("SAVEPOINT merge"); err != nil {
		return c.abandon(err)
	}
	const unset = math.MinInt64 // a rowid no INSERT of the queue gives
	c.c.SetLastInsertRowID(unset)
	c.c.SetMerging(true)
	res, err := s.run(r.ctx, r.args)
	if c.c.SetMerging(false) {
		// SQLite prepared the statement again and it is no longer
		// mergeable: it failed before it did anything. A virtual table
		// whose own prepare was refused may have hidden the refusal and
		// let the statement run, so what it did is undone: it runs again
		// on its caller's connection.
		s.s.Reclassify()
		if e := c.execCached("ROLLBACK TO merge"); e != nil {
			return c.abandon(e)
		}
		if e := c.execCached("RELEASE merge"); e != nil {
			return c.abandon(e)
		}
		return errNotHere
	}
	if c.c.AutoCommit() {
		if err == nil {
			err = errBusy // cannot happen: a statement that succeeds keeps the transaction
		}
		return err
	}
	undo := false
	if err == nil && c.c.DeferredFKs() {
		err, undo = errForeignKey, true
	}
	if c.c.VirtualTablesInTx() {
		// FTS5 writes the terms of an INSERT to its index only at the next
		// SAVEPOINT or at COMMIT, so a failure to write them (a broken
		// index, a full disk) would fall on the next statement of the batch
		// or on all of them. A SAVEPOINT of its own writes them now, inside
		// the statement's SAVEPOINT, also after the statement failed, which
		// may leave terms. When it fails, the statement is undone: alone, its
		// COMMIT would have failed.
		if ferr := c.flushVirtualTables(); ferr != nil {
			if err == nil {
				err = ferr
			}
			if c.c.AutoCommit() {
				return err // the failure ended the transaction
			}
			undo = true
		}
	}
	if undo {
		if e := c.execCached("ROLLBACK TO merge"); e != nil {
			return c.abandon(e)
		}
	}
	if e := c.execCached("RELEASE merge"); e != nil {
		return c.abandon(e)
	}
	if err != nil {
		return err
	}
	r.res = *res
	r.inserted = res.id != unset
	return nil
}

// flushVirtualTables makes the virtual tables of the transaction write what
// they hold back: SQLite calls their xSavepoint as a SAVEPOINT begins.
func (c *conn) flushVirtualTables() error {
	if err := c.execCached("SAVEPOINT flush"); err != nil {
		return err
	}
	return c.execCached("RELEASE flush")
}

// abandon rolls the transaction back when a SAVEPOINT of the batch fails, so
// that the batch runs again without it.
func (c *conn) abandon(err error) error {
	if !c.c.AutoCommit() {
		c.execCached("ROLLBACK")
	}
	if !c.c.AutoCommit() {
		c.bad = true
	}
	return err
}

// answer gives each request its result, with err for all when it is not nil.
func answer(reqs []*request, err error) {
	for _, r := range reqs {
		if err != nil {
			r.err = err
		}
		close(r.done)
	}
}

// cached is a statement of the connection's cache for the queue's SQL.
func (c *conn) cached(sql string) (*stmt, error) {
	if s := c.cache[sql]; s != nil {
		return s, nil
	}
	p, tail, err := c.c.Prepare(sql)
	if err != nil {
		return nil, err
	}
	if p == nil || tail != "" {
		if p != nil {
			p.Finalize()
		}
		return nil, errNotHere // a request holds exactly one statement
	}
	if c.cache == nil {
		c.cache = map[string]*stmt{}
	}
	if len(c.cache) >= maxCached {
		for k, s := range c.cache {
			s.Close()
			delete(c.cache, k)
			break
		}
	}
	s := newStmt(c, p, sql, false)
	c.cache[sql] = s
	return s, nil
}

// maxCached is the most statements a connection keeps for the queue.
const maxCached = 64

// execCached runs a statement of a batch with no parameters, such as
// SAVEPOINT merge. These few are kept apart from the statements of the
// requests and never closed while c is open: making room in c.cache for a
// request then never closes a statement in use, such as the request's own
// before its SAVEPOINT.
func (c *conn) execCached(sql string) error {
	s := c.control[sql]
	if s == nil {
		p, tail, err := c.c.Prepare(sql)
		if err != nil {
			return err
		}
		if p == nil || tail != "" {
			if p != nil {
				p.Finalize()
			}
			return fmt.Errorf("sqlite: %q is not one statement", sql)
		}
		if c.control == nil {
			c.control = map[string]*stmt{}
		}
		s = newStmt(c, p, sql, false)
		c.control[sql] = s
	}
	_, err := s.s.Step()
	if rerr := s.s.Reset(); err == nil {
		err = rerr
	}
	return err
}
