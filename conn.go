// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

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
	s, tail, err := c.c.Prepare(query)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("sqlite: no statement in the query")
	}
	if strings.TrimSpace(tail) != "" {
		s.Finalize()
		return nil, errors.New("sqlite: a prepared statement takes one statement: use Exec for several")
	}
	return newStmt(c, s, false), nil
}

func newStmt(c *conn, s *csqlite.Stmt, once bool) *stmt {
	return &stmt{c: c, s: s, nparams: s.ParamCount(), readOnly: s.ReadOnly(), once: once}
}

func (c *conn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.writing {
		c.writing = false
		c.wl.release()
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
	begin := c.begin
	if opts.ReadOnly {
		begin = "BEGIN"
		if err := c.c.Exec("PRAGMA query_only=1"); err != nil {
			return nil, err
		}
	} else if begin != "BEGIN" {
		// BEGIN IMMEDIATE and EXCLUSIVE take SQLite's write lock at once.
		if err := c.beginWrite(ctx, false); err != nil {
			return nil, err
		}
	}
	if err := c.c.Exec(begin); err != nil {
		if opts.ReadOnly {
			c.c.Exec("PRAGMA query_only=0")
		}
		c.endWrite()
		return nil, err
	}
	return &tx{c: c, readOnly: opts.ReadOnly}, nil
}

type tx struct {
	c        *conn
	readOnly bool
}

func (t *tx) end(sql string) error {
	err := t.c.c.Exec(sql)
	t.c.endWrite()
	if t.readOnly {
		if e := t.c.c.Exec("PRAGMA query_only=0"); err == nil {
			err = e
		}
	}
	return err
}

func (t *tx) Commit() error { return t.end("COMMIT") }

func (t *tx) Rollback() error { return t.end("ROLLBACK") }

// ExecContext runs every statement of query in turn. The arguments go to the
// statements in order: each takes as many as it has parameters.
func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
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
		st := newStmt(c, s, false)
		n := min(st.nparams, len(args))
		r, err := st.exec(ctx, args[:n])
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
	s, tail, err := c.c.Prepare(query)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("sqlite: no statement in the query")
	}
	if strings.TrimSpace(tail) != "" {
		s.Finalize()
		return nil, errors.New("sqlite: a query takes one statement")
	}
	st := newStmt(c, s, true)
	r, err := st.query(ctx, args)
	if err != nil {
		s.Finalize()
		return nil, err
	}
	return r, nil
}

func (c *conn) Ping(ctx context.Context) error {
	if c.closed {
		return driver.ErrBadConn
	}
	return nil
}

func (c *conn) ResetSession(ctx context.Context) error {
	if c.closed {
		return driver.ErrBadConn
	}
	return nil
}

func (c *conn) IsValid() bool { return !c.closed }

func (c *conn) CheckNamedValue(v *driver.NamedValue) error {
	var err error
	v.Value, err = convert(v.Value)
	return err
}
