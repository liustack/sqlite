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
	"strings"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
)

type conn struct {
	c      *csqlite.Conn
	begin  string
	closed bool

	path        string     // the database file, "" for memory
	wl          *writeLock // the file's write lock, nil for memory
	writing     bool       // the connection holds wl
	busyTimeout time.Duration
	cache       map[string]*stmt // statements of the queue (writer.go)

	// key is the connector's key: group commit merges only between
	// connections of one key (writer.go).
	key string
	// private says the connection changed itself after it opened (a PRAGMA
	// that sets something, ATTACH, a temporary object) or opened read-only:
	// it no longer runs as the others of its key, and never merges.
	private bool
	// readOnlyTx says a read-only transaction is open: it takes no write
	// lock.
	readOnlyTx bool
	// tx is the transaction of database/sql that is open, or nil.
	tx *tx
	// bad says the connection is in a state it cannot leave (a transaction
	// that would not roll back): database/sql must drop it.
	bad bool
}

var (
	_ driver.Conn               = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.ConnBeginTx        = (*conn)(nil)
	_ driver.ExecerContext      = (*conn)(nil)
	_ driver.QueryerContext     = (*conn)(nil)
	_ driver.Pinger             = (*conn)(nil)
	_ driver.SessionResetter    = (*conn)(nil)
	_ driver.Validator          = (*conn)(nil)
	_ driver.NamedValueChecker  = (*conn)(nil)
)

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, sql, err := c.prepareOne(query)
	if err != nil {
		return nil, err
	}
	return newStmt(c, s, sql, false), nil
}

// prepareOne prepares a query of one statement, which may be followed by
// spaces and comments. It returns the statement's own text.
func (c *conn) prepareOne(query string) (*csqlite.Stmt, string, error) {
	if c.c.Broken() {
		return nil, "", driver.ErrBadConn
	}
	s, tail, err := c.c.Prepare(query)
	if err != nil {
		return nil, "", err
	}
	if s == nil {
		return nil, "", errors.New("sqlite: no statement in the query")
	}
	if strings.TrimSpace(tail) != "" {
		next, _, err := c.c.Prepare(tail)
		if next != nil {
			next.Finalize()
			err = errors.New("sqlite: a prepared statement or a query takes one statement: use Exec for several")
		}
		if err != nil {
			s.Finalize()
			return nil, "", err
		}
	}
	return s, query[:len(query)-len(tail)], nil
}

func newStmt(c *conn, s *csqlite.Stmt, sql string, once bool) *stmt {
	st := &stmt{c: c, s: s, sql: sql, nparams: s.ParamCount(), readOnly: s.ReadOnly(), changesConn: s.ChangesConn(), once: once}
	if st.changesConn {
		// From here on the connection may run differently from the others
		// of its key. Marked as the statement is prepared, before it runs.
		c.private = true
	}
	return st
}

func (c *conn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.writing {
		c.writing = false
		c.wl.release(c)
	}
	for _, s := range c.cache {
		s.Close()
	}
	if c.wl != nil {
		closeWriteLock(c.path, c.wl)
	}
	return c.c.Close()
}

func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.c.Broken() {
		return nil, driver.ErrBadConn
	}
	t := &tx{c: c, readOnly: opts.ReadOnly}
	begin := c.begin
	if opts.ReadOnly {
		begin = "BEGIN"
		on, err := pragmaInt(c.c, "query_only")
		if err != nil {
			return nil, err
		}
		t.setQueryOnly = on == 0
		if t.setQueryOnly {
			if err := c.c.Exec("PRAGMA query_only=1"); err != nil {
				return nil, err
			}
		}
		c.readOnlyTx = true
	} else if begin != "BEGIN" {
		// BEGIN IMMEDIATE and EXCLUSIVE take SQLite's write lock at once.
		if err := c.beginWrite(ctx, false); err != nil {
			return nil, err
		}
	}
	if err := c.c.Exec(begin); err != nil {
		t.restore()
		c.endWrite()
		return nil, err
	}
	c.tx = t
	return t, nil
}

type tx struct {
	c        *conn
	readOnly bool
	// setQueryOnly says BeginTx turned query_only on, for the transaction.
	setQueryOnly bool
}

// end ends the transaction with sql. A COMMIT that fails can leave the
// transaction open (a deferred foreign key, SQLITE_BUSY): it is rolled back
// then, since database/sql is done with it either way.
func (t *tx) end(sql string) error {
	c := t.c
	c.tx = nil
	if c.c.AutoCommit() {
		// SQLite ended the transaction before: an error rolled it back, or
		// a statement of the transaction ended it.
		err := t.restore()
		c.endWrite()
		if sql == "COMMIT" {
			return errTxEnded
		}
		return err
	}
	err := c.c.Exec(sql)
	if !c.c.AutoCommit() {
		if rerr := c.c.Exec("ROLLBACK"); rerr != nil || !c.c.AutoCommit() {
			c.bad = true
		}
	}
	if rerr := t.restore(); err == nil {
		err = rerr
	}
	c.endWrite()
	return err
}

// errTxEnded is the error of the statements, and the Commit, of a
// transaction that SQLite ended before them.
var errTxEnded = errors.New("sqlite: the transaction already ended: an error rolled it back, or a statement of it ended it")

// txEnded fails a statement of a database/sql transaction that SQLite ended
// already. Run as an autocommit statement, it would commit on its own.
func (c *conn) txEnded() error {
	if c.tx != nil && c.c.AutoCommit() {
		return errTxEnded
	}
	return nil
}

// restore undoes what BeginTx set for a read-only transaction.
func (t *tx) restore() error {
	if !t.readOnly {
		return nil
	}
	t.c.readOnlyTx = false
	if !t.setQueryOnly {
		return nil
	}
	if err := t.c.c.Exec("PRAGMA query_only=0"); err != nil {
		t.c.bad = true
		return err
	}
	return nil
}

func (t *tx) Commit() error { return t.end("COMMIT") }

func (t *tx) Rollback() error { return t.end("ROLLBACK") }

// ExecContext runs every statement of query in turn. The arguments go to the
// statements in order: each takes as many as it has parameters. A statement
// that fails ends it: the statements before it stay done.
func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.c.Broken() {
		return nil, driver.ErrBadConn
	}
	var res result
	for {
		s, tail, err := c.c.Prepare(query)
		if err != nil {
			return nil, err
		}
		if s == nil {
			if len(args) > 0 {
				return nil, errors.New("sqlite: more arguments than parameters")
			}
			return &res, nil
		}
		st := newStmt(c, s, query[:len(query)-len(tail)], false)
		n := min(st.nparams, len(args))
		part := make([]driver.NamedValue, n)
		for i := range part {
			part[i] = args[i]
			part[i].Ordinal = i + 1 // its place in this statement
		}
		r, err := st.exec(ctx, part)
		s.Finalize()
		if err != nil {
			return nil, err
		}
		res = *r
		args = args[n:]
		query = tail
	}
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s, sql, err := c.prepareOne(query)
	if err != nil {
		return nil, err
	}
	st := newStmt(c, s, sql, true)
	r, err := st.query(ctx, args)
	if err != nil {
		s.Finalize()
		return nil, err
	}
	return r, nil
}

func (c *conn) Ping(ctx context.Context) error {
	if !c.IsValid() {
		return driver.ErrBadConn
	}
	return nil
}

// ResetSession drops a connection that is closed, broken, or still in a
// transaction (a BEGIN run with Exec and never ended), which would hold the
// file's write lock and give the next user a transaction it did not open.
func (c *conn) ResetSession(ctx context.Context) error {
	if !c.IsValid() || !c.c.AutoCommit() {
		return driver.ErrBadConn
	}
	return nil
}

func (c *conn) IsValid() bool { return !c.closed && !c.bad && !c.c.Broken() }

func (c *conn) CheckNamedValue(v *driver.NamedValue) error {
	var err error
	v.Value, err = convert(v.Value)
	return err
}
