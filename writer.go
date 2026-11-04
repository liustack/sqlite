// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"context"
	"database/sql/driver"
	"sync"
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
// and depends on nothing of its connection (stmt.mergeable) does not wait
// for the lock when another writer holds it. It joins the file's queue, and
// the writer that gives the lock back runs the whole queue on its own
// connection as one transaction, each statement in a SAVEPOINT of its own,
// and commits once. Each caller gets its own result after the commit. With
// no other writer a statement runs directly.
type writeLock struct {
	mu      sync.Mutex
	held    bool
	waiters []chan struct{} // writers that wait for the lock, in order
	queue   []*request      // statements to merge, in order
	refs    int             // connections of the file
}

// A request is one statement of the queue.
type request struct {
	sql  string
	args []driver.NamedValue
	done chan struct{}
	res  result
	err  error
}

// maxBatch is the most statements one transaction of the queue takes.
const maxBatch = 256

var writeLocks struct {
	sync.Mutex
	m map[string]*writeLock
}

func openWriteLock(path string) *writeLock {
	writeLocks.Lock()
	defer writeLocks.Unlock()
	if writeLocks.m == nil {
		writeLocks.m = map[string]*writeLock{}
	}
	l := writeLocks.m[path]
	if l == nil {
		l = &writeLock{}
		writeLocks.m[path] = l
	}
	l.refs++
	return l
}

func closeWriteLock(path string, l *writeLock) {
	writeLocks.Lock()
	defer writeLocks.Unlock()
	l.refs--
	if l.refs == 0 {
		delete(writeLocks.m, path)
	}
}

// errBusy is what a writer gets when the lock stays taken past the busy
// timeout, as SQLite would answer.
var errBusy = &csqlite.Error{Code: 5, Msg: "database is locked"}

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

// release gives the lock back from c. The queue runs first, on c.
func (l *writeLock) release(c *conn) {
	for {
		l.mu.Lock()
		if n := len(l.queue); n > 0 {
			batch := l.queue[:min(n, maxBatch)]
			l.queue = l.queue[len(batch):]
			l.mu.Unlock()
			c.runBatch(batch)
			continue
		}
		if len(l.waiters) > 0 {
			ch := l.waiters[0]
			l.waiters = l.waiters[1:]
			l.mu.Unlock()
			close(ch) // the lock goes to it, still held
			return
		}
		l.held = false
		l.mu.Unlock()
		return
	}
}

// submit runs a mergeable statement: directly when the lock is free, in the
// queue when not.
func (l *writeLock) submit(ctx context.Context, c *conn, s *stmt, args []driver.NamedValue) (*result, error) {
	l.mu.Lock()
	if !l.held {
		l.held = true
		l.mu.Unlock()
		c.writing = true
		r, err := s.run(ctx, args)
		c.endWrite()
		return r, err
	}
	if c.busyTimeout <= 0 {
		l.mu.Unlock()
		return nil, errBusy
	}
	req := &request{sql: s.sql, args: args, done: make(chan struct{})}
	l.queue = append(l.queue, req)
	l.mu.Unlock()
	t := time.NewTimer(c.busyTimeout)
	defer t.Stop()
	var err error
	select {
	case <-req.done:
		return &req.res, req.err
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
	<-req.done // a writer runs it already
	return &req.res, req.err
}

// beginWrite takes the write lock before a statement that may write.
func (c *conn) beginWrite(ctx context.Context, readOnly bool) error {
	if c.wl == nil || c.writing || readOnly {
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

// runBatch runs statements of the queue as one transaction on c, which holds
// the lock and is out of any transaction, and answers each after the
// commit. A statement that fails is undone by its SAVEPOINT and answered
// with its error. When a failure ends the whole transaction (an IO error, ON
// CONFLICT ROLLBACK), the statements that had run are run again in a new one.
func (c *conn) runBatch(batch []*request) {
	for len(batch) > 0 {
		if err := c.execCached("BEGIN IMMEDIATE"); err != nil {
			answer(batch, err)
			return
		}
		var ok []*request
		lost := false
		for i, req := range batch {
			req.err = c.runOne(req)
			if req.err == nil {
				ok = append(ok, req)
				continue
			}
			close(req.done)
			if c.c.AutoCommit() {
				// The transaction is gone: run the rest again.
				batch = append(ok, batch[i+1:]...)
				lost = true
				break
			}
		}
		if lost {
			continue
		}
		if err := c.execCached("COMMIT"); err != nil {
			if !c.c.AutoCommit() {
				c.execCached("ROLLBACK")
			}
			answer(ok, err)
			return
		}
		answer(ok, nil)
		return
	}
}

// runOne runs one statement of a batch in a SAVEPOINT.
func (c *conn) runOne(req *request) error {
	if err := c.execCached("SAVEPOINT merge"); err != nil {
		return err
	}
	s, err := c.cached(req.sql)
	if err == nil {
		var r *result
		r, err = s.run(context.Background(), req.args)
		if err == nil {
			req.res = *r
		}
	}
	if err != nil {
		if !c.c.AutoCommit() {
			c.execCached("ROLLBACK TO merge")
			c.execCached("RELEASE merge")
		}
		return err
	}
	return c.execCached("RELEASE merge")
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
		return nil, errBusy // a mergeable statement is always one statement
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

// execCached runs a statement with no parameters from the cache.
func (c *conn) execCached(sql string) error {
	s, err := c.cached(sql)
	if err != nil {
		return err
	}
	_, err = s.s.Step()
	if rerr := s.s.Reset(); err == nil {
		err = rerr
	}
	return err
}
