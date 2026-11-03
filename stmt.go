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
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
)

type stmt struct {
	c       *conn
	s       *csqlite.Stmt
	sql     string
	nparams int
	// mergeable says the statement may join the queue of group commit
	// (writer.go).
	mergeable bool
	// readOnly says the statement writes nothing (sqlite3_stmt_readonly).
	readOnly bool
	// once is a statement of conn.QueryContext: its rows finalize it.
	once bool
	// busy says rows of the statement are open.
	busy bool
	// cols, decl and isTime describe the columns of its rows, read once
	// (columns change only when SQLite prepares it again after a change of
	// the schema, which columns checks for).
	cols   []string
	decl   []string
	isTime []bool
}

// columns reads the names and declared types of the columns once.
func (s *stmt) columns() {
	n := s.s.ColumnCount()
	if s.cols != nil && len(s.cols) == n {
		return
	}
	s.cols = make([]string, n)
	s.decl = make([]string, n)
	s.isTime = make([]bool, n)
	for i := range n {
		s.cols[i] = s.s.ColumnName(i)
		s.decl[i] = strings.ToUpper(s.s.ColumnDeclType(i))
		switch s.decl[i] {
		case "DATE", "DATETIME", "TIMESTAMP":
			s.isTime[i] = true
		}
	}
}

var (
	_ driver.Stmt             = (*stmt)(nil)
	_ driver.StmtExecContext  = (*stmt)(nil)
	_ driver.StmtQueryContext = (*stmt)(nil)
)

func (s *stmt) Close() error {
	if s.s == nil {
		return nil
	}
	err := s.s.Finalize()
	s.s = nil
	return err
}

func (s *stmt) NumInput() int { return -1 }

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(args))
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(args))
}

func named(args []driver.Value) []driver.NamedValue {
	n := make([]driver.NamedValue, len(args))
	for i, v := range args {
		n[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return n
}

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.exec(ctx, args)
}

func (s *stmt) exec(ctx context.Context, args []driver.NamedValue) (*result, error) {
	c := s.c
	if s.mergeable && c.wl != nil && !c.writing && c.c.AutoCommit() {
		return c.wl.submit(ctx, c, s, args)
	}
	if err := c.beginWrite(ctx, s.readOnly); err != nil {
		return nil, err
	}
	r, err := s.run(ctx, args)
	c.endWrite()
	return r, err
}

// run binds args and steps the statement once, with the write lock held if
// it writes.
func (s *stmt) run(ctx context.Context, args []driver.NamedValue) (*result, error) {
	if err := s.bind(args); err != nil {
		return nil, err
	}
	stop := s.c.watch(ctx)
	_, err := s.s.Step()
	stop()
	if rerr := s.s.Reset(); err == nil {
		err = rerr
	}
	if err != nil {
		return nil, ctxErr(ctx, err)
	}
	return &result{id: s.c.c.LastInsertRowID(), n: s.c.c.Changes()}, nil
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.query(ctx, args)
}

func (s *stmt) query(ctx context.Context, args []driver.NamedValue) (*rows, error) {
	if s.busy {
		return nil, errors.New("sqlite: the rows of the statement are still open")
	}
	if err := s.bind(args); err != nil {
		return nil, err
	}
	if err := s.c.beginWrite(ctx, s.readOnly); err != nil {
		return nil, err
	}
	s.busy = true
	s.columns()
	return &rows{s: s, ctx: ctx, stop: s.c.watch(ctx)}, nil
}

func (s *stmt) bind(args []driver.NamedValue) error {
	if err := s.s.ClearBindings(); err != nil {
		return err
	}
	for _, a := range args {
		i := a.Ordinal
		if a.Name != "" {
			i = s.paramIndex(a.Name)
			if i == 0 {
				return fmt.Errorf("sqlite: no parameter named %s", a.Name)
			}
		} else if i > s.nparams {
			return fmt.Errorf("sqlite: %d arguments for %d parameters", len(args), s.nparams)
		}
		var err error
		switch v := a.Value.(type) {
		case nil:
			err = s.s.BindNull(i)
		case int64:
			err = s.s.BindInt64(i, v)
		case float64:
			err = s.s.BindDouble(i, v)
		case bool:
			var n int64
			if v {
				n = 1
			}
			err = s.s.BindInt64(i, n)
		case []byte:
			err = s.s.BindBlob(i, v)
		case string:
			err = s.s.BindText(i, v)
		case time.Time:
			err = s.s.BindText(i, v.Format(timeFormat))
		default:
			return fmt.Errorf("sqlite: cannot bind %T", v)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// paramIndex finds the parameter of a name given without its prefix.
func (s *stmt) paramIndex(name string) int {
	for i := 1; i <= s.nparams; i++ {
		n := s.s.ParamName(i)
		if len(n) > 1 && n[1:] == name {
			return i
		}
	}
	return 0
}

// watch interrupts the statement of the connection when ctx is done, until
// stop is called.
func (c *conn) watch(ctx context.Context) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	if ctx.Err() != nil {
		c.c.Interrupt()
		return func() {}
	}
	stop := context.AfterFunc(ctx, func() { c.c.Interrupt() })
	return func() { stop() }
}

// ctxErr is the error of the context when it ended the statement.
func ctxErr(ctx context.Context, err error) error {
	var e *csqlite.Error
	if errors.As(err, &e) && e.Primary() == 9 && ctx.Err() != nil { // SQLITE_INTERRUPT
		return ctx.Err()
	}
	return err
}

type result struct{ id, n int64 }

func (r *result) LastInsertId() (int64, error) { return r.id, nil }
func (r *result) RowsAffected() (int64, error) { return r.n, nil }

type rows struct {
	s     *stmt
	ctx   context.Context
	stop  func()
	done  bool
	close bool
}

var (
	_ driver.Rows                           = (*rows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*rows)(nil)
)

func (r *rows) Columns() []string { return r.s.cols }

func (r *rows) ColumnTypeDatabaseTypeName(i int) string { return r.s.decl[i] }

func (r *rows) Close() error {
	if r.close {
		return nil
	}
	r.close = true
	r.stop()
	r.s.busy = false
	err := r.s.s.Reset()
	r.s.c.endWrite()
	if r.s.once {
		if ferr := r.s.Close(); err == nil {
			err = ferr
		}
	}
	if r.done {
		return nil // Next already gave the error of the statement
	}
	return err
}

func (r *rows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	row, err := r.s.s.Step()
	if err != nil {
		r.done = true
		return ctxErr(r.ctx, err)
	}
	if !row {
		r.done = true
		return io.EOF
	}
	s := r.s.s
	for i := range dest {
		switch s.ColumnType(i) {
		case csqlite.TypeInteger:
			v := s.ColumnInt64(i)
			if r.isTime(i) {
				dest[i] = time.Unix(v, 0).UTC()
			} else {
				dest[i] = v
			}
		case csqlite.TypeFloat:
			dest[i] = s.ColumnDouble(i)
		case csqlite.TypeText:
			v := s.ColumnText(i)
			if r.isTime(i) {
				if t, ok := parseTime(v); ok {
					dest[i] = t
					break
				}
			}
			dest[i] = v
		case csqlite.TypeBlob:
			b := s.ColumnBlob(i)
			if b == nil {
				b = []byte{}
			}
			dest[i] = b
		default:
			dest[i] = nil
		}
	}
	return nil
}

func (r *rows) isTime(i int) bool { return r.s.isTime[i] }
