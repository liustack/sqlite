// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"context"
	"database/sql"
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
	// changesConn says the statement changes its connection (a PRAGMA that
	// sets something, ATTACH, a temporary object).
	changesConn bool
	// once is a statement of conn.QueryContext: its rows finalize it.
	once bool
	// busy says rows of the statement are open.
	busy bool
	// cols, decl and isTime describe the columns of its rows. They are read
	// again when SQLite has prepared the statement again after a change of
	// the schema (prepared counts those).
	cols     []string
	decl     []string
	isTime   []bool
	prepared int
}

// columns reads the names and declared types of the columns, once for each
// time SQLite prepared the statement.
func (s *stmt) columns() {
	p := s.s.Reprepares()
	if s.cols != nil && p == s.prepared {
		return
	}
	s.prepared = p
	n := s.s.ColumnCount()
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
	if c.c.Broken() {
		return nil, driver.ErrBadConn
	}
	if err := c.txEnded(); err != nil {
		return nil, err
	}
	if s.mergeable && c.wl != nil && !c.writing && !c.private && c.c.AutoCommit() {
		return c.wl.submit(ctx, c, s, args)
	}
	if err := c.beginWrite(ctx, s.readOnly); err != nil {
		return nil, err
	}
	r, err := s.run(ctx, args)
	c.endWrite()
	if s.changesConn {
		c.readBusyTimeout()
	}
	return r, err
}

// readBusyTimeout reads busy_timeout again after a statement that may have
// set it: the file's write lock waits as long as SQLite would.
func (c *conn) readBusyTimeout() {
	if ms, err := pragmaInt(c.c, "busy_timeout"); err == nil {
		c.busyTimeout = time.Duration(ms) * time.Millisecond
	}
}

// run binds args and steps the statement once, with the write lock held if
// it writes. A context done before the step stops it: SQLite forgets an
// interrupt that comes before a statement starts.
func (s *stmt) run(ctx context.Context, args []driver.NamedValue) (*result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

// query runs the statement to its first row, so that its columns are the
// ones of the schema it runs on (SQLite prepares it again at its first step
// after a change of the schema), and a context done before then stops it.
func (s *stmt) query(ctx context.Context, args []driver.NamedValue) (*rows, error) {
	c := s.c
	if c.c.Broken() {
		return nil, driver.ErrBadConn
	}
	if err := c.txEnded(); err != nil {
		return nil, err
	}
	if s.busy {
		return nil, errors.New("sqlite: the rows of the statement are still open")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.bind(args); err != nil {
		return nil, err
	}
	if err := c.beginWrite(ctx, s.readOnly); err != nil {
		return nil, err
	}
	stop := c.watch(ctx)
	row, err := s.s.Step()
	if err != nil {
		stop()
		s.s.Reset()
		c.endWrite()
		return nil, ctxErr(ctx, err)
	}
	if s.changesConn {
		c.readBusyTimeout()
	}
	s.busy = true
	s.columns()
	r := &rows{s: s, ctx: ctx, stop: stop, first: true, done: !row}
	if !row {
		r.end()
	}
	return r, nil
}

func (s *stmt) bind(args []driver.NamedValue) error {
	if err := s.s.ClearBindings(); err != nil {
		return err
	}
	named := false
	for _, a := range args {
		named = named || a.Name != ""
	}
	if !named && len(args) < s.nparams {
		// SQLite would read the parameters left over as NULL.
		return fmt.Errorf("sqlite: %d arguments for %d parameters", len(args), s.nparams)
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
			if v == nil {
				err = s.s.BindNull(i) // as mattn/go-sqlite3 does
			} else {
				err = s.s.BindBlob(i, v)
			}
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
// stop is called. stop waits for an interrupt that has started, so that it
// never reaches a connection that is closed or runs another statement.
func (c *conn) watch(ctx context.Context) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	if ctx.Err() != nil {
		c.c.Interrupt()
		return func() {}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		c.c.Interrupt()
		close(done)
	})
	return func() {
		if !stop() {
			<-done
		}
	}
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
	s    *stmt
	ctx  context.Context
	stop func()
	// first says query stepped to the first row and NextRow has not yet
	// given it.
	first bool
	done  bool
	close bool
}

// end stops watching the context once the statement has ended, so that no
// interrupt reaches a later statement of the connection.
func (r *rows) end() {
	r.done = true
	r.stop()
	r.stop = func() {}
}

var (
	_ driver.Rows                           = (*rows)(nil)
	_ driver.RowsColumnScanner              = (*rows)(nil)
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

// NextRow steps to the next row.
func (r *rows) NextRow() error {
	if r.first {
		r.first = false
		if r.done {
			return io.EOF
		}
		return nil
	}
	if r.done {
		return io.EOF
	}
	row, err := r.s.s.Step()
	if err != nil {
		r.end()
		return ctxErr(r.ctx, err)
	}
	if !row {
		r.end()
		return io.EOF
	}
	return nil
}

// Next is NextRow and the values of the row, for database/sql before Go
// 1.27 calls ScanColumn.
func (r *rows) Next(dest []driver.Value) error {
	if err := r.NextRow(); err != nil {
		return err
	}
	for i := range dest {
		dest[i] = r.value(i)
	}
	return nil
}

// value is column i of the row as a driver.Value.
func (r *rows) value(i int) driver.Value {
	s := r.s.s
	switch s.ColumnType(i) {
	case csqlite.TypeInteger:
		v := s.ColumnInt64(i)
		if r.isTime(i) {
			return time.Unix(v, 0).UTC()
		}
		return v
	case csqlite.TypeFloat:
		return s.ColumnDouble(i)
	case csqlite.TypeText:
		v := s.ColumnText(i)
		if r.isTime(i) {
			if t, ok := parseTime(v); ok {
				return t
			}
		}
		return v
	case csqlite.TypeBlob:
		b := s.ColumnBlob(i)
		if b == nil {
			b = []byte{}
		}
		return b
	}
	return nil
}

// ScanColumn stores column i of the row in dest. The common pairs of
// column type and destination are stored directly, without putting the
// value in an interface. Everything else, and every column of a time type,
// goes through sql.ConvertAssign with the value Next gives, so both ways
// give the same results.
func (r *rows) ScanColumn(ctx driver.ScanContext, i int, dest any) error {
	s := r.s.s
	if !r.isTime(i) {
		switch s.ColumnType(i) {
		case csqlite.TypeInteger:
			switch d := dest.(type) {
			case *int64:
				*d = s.ColumnInt64(i)
				return nil
			case *int:
				*d = int(s.ColumnInt64(i)) // int is 64 bits on every target
				return nil
			case *sql.NullInt64:
				d.Int64, d.Valid = s.ColumnInt64(i), true
				return nil
			}
		case csqlite.TypeFloat:
			switch d := dest.(type) {
			case *float64:
				*d = s.ColumnDouble(i)
				return nil
			case *sql.NullFloat64:
				d.Float64, d.Valid = s.ColumnDouble(i), true
				return nil
			}
		case csqlite.TypeText:
			switch d := dest.(type) {
			case *string:
				*d = s.ColumnText(i)
				return nil
			case *sql.NullString:
				d.String, d.Valid = s.ColumnText(i), true
				return nil
			}
		}
	}
	return sql.ConvertAssign(ctx, dest, r.value(i))
}

func (r *rows) isTime(i int) bool { return r.s.isTime[i] }
