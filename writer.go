// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"context"
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
type writeLock struct {
	ch   chan struct{}
	refs int
}

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
		l = &writeLock{ch: make(chan struct{}, 1)}
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

func (l *writeLock) acquire(ctx context.Context, timeout time.Duration) error {
	select {
	case l.ch <- struct{}{}:
		return nil
	default:
	}
	if timeout <= 0 {
		return errBusy
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case l.ch <- struct{}{}:
		return nil
	case <-t.C:
		return errBusy
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *writeLock) release() { <-l.ch }

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
		c.wl.release()
	}
}
