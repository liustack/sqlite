// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/liustack/sqlite/internal/csqlite"
)

// VTable is a table of a virtual table module, written in Go, on one
// connection. It only reads: INSERT, UPDATE and DELETE on it fail.
//
// Each connection connects a table of its own, and uses it from one
// goroutine at a time. Something the tables of several connections share is
// used from several goroutines at once. Like a Func, a table must not use
// the database.
type VTable interface {
	// Schema is the CREATE TABLE statement of its columns, such as
	// "CREATE TABLE x(value, start HIDDEN)". The name of the table in it
	// is not used. A HIDDEN column is not in SELECT *, and when the module
	// is used by its name as a function, its arguments give the hidden
	// columns, in order: they reach BestIndex as constraints of EQ.
	Schema() string
	// BestIndex chooses how to scan the table under the constraints of
	// info, and says what the scan costs. SQLite may ask several times
	// with different constraints and runs the cheapest. It returns
	// ErrUnusablePlan when the scan cannot work with the constraints that
	// are usable, such as when an argument it needs is missing.
	BestIndex(info *IndexInfo) error
	// Open makes a cursor for a scan.
	Open() (VCursor, error)
	// Disconnect ends the table on its connection.
	Disconnect() error
}

// VCursor is a scan of a VTable.
type VCursor interface {
	// Filter starts the scan with the plan BestIndex chose: idxNum and
	// idxStr as it set them, and args the values of the constraints it gave
	// an ArgvIndex, args[ArgvIndex-1] for each. The values are copies, as
	// a Func gets them. Filter may run again on the same cursor, to start
	// over.
	Filter(idxNum int, idxStr string, args []driver.Value) error
	// Next goes to the next row.
	Next() error
	// EOF says the scan is past its last row. It runs after Filter and
	// after each Next.
	EOF() bool
	// Column is column i of the row, a value as a Func returns.
	Column(i int) (driver.Value, error)
	// RowID is the rowid of the row.
	RowID() (int64, error)
	Close() error
}

// ErrUnusablePlan is what BestIndex returns when the scan cannot work with
// the constraints it was given.
var ErrUnusablePlan = errors.New("sqlite: no usable plan for the virtual table")

// IndexOp is the operator of a constraint.
type IndexOp int

// The operators of IndexConstraint.
const (
	IndexEQ        IndexOp = csqlite.IndexEQ
	IndexGT        IndexOp = csqlite.IndexGT
	IndexLE        IndexOp = csqlite.IndexLE
	IndexLT        IndexOp = csqlite.IndexLT
	IndexGE        IndexOp = csqlite.IndexGE
	IndexMatch     IndexOp = csqlite.IndexMatch
	IndexLike      IndexOp = csqlite.IndexLike
	IndexGlob      IndexOp = csqlite.IndexGlob
	IndexRegexp    IndexOp = csqlite.IndexRegexp
	IndexNE        IndexOp = csqlite.IndexNE
	IndexIsNot     IndexOp = csqlite.IndexIsNot
	IndexIsNotNull IndexOp = csqlite.IndexIsNotNull
	IndexIsNull    IndexOp = csqlite.IndexIsNull
	IndexIs        IndexOp = csqlite.IndexIs
	IndexLimit     IndexOp = csqlite.IndexLimit
	IndexOffset    IndexOp = csqlite.IndexOffset
)

// IndexConstraint is a term of the WHERE clause: column Op value. A
// constraint that is not Usable may not get an ArgvIndex.
type IndexConstraint struct {
	Column int // -1 for the rowid
	Op     IndexOp
	Usable bool
}

// IndexOrderBy is a term of the ORDER BY clause.
type IndexOrderBy struct {
	Column int
	Desc   bool
}

// IndexUsage is what BestIndex says of a constraint. ArgvIndex > 0 hands its
// value to Filter, at args[ArgvIndex-1]: give each used constraint its own
// number from 1 up. Omit says the scan only gives rows that meet it, so
// SQLite need not check them again.
type IndexUsage struct {
	ArgvIndex int
	Omit      bool
}

// IndexInfo is the question BestIndex answers. Constraints, OrderBy and
// ColUsed are the question. The rest is the answer, with SQLite's defaults
// in EstimatedCost and EstimatedRows.
type IndexInfo struct {
	Constraints []IndexConstraint
	OrderBy     []IndexOrderBy
	// ColUsed has bit i set when the statement uses column i, and bit 63
	// for any column from 63 up.
	ColUsed uint64

	// Usage is as long as Constraints, one for each.
	Usage []IndexUsage
	// IdxNum and IdxStr name the plan for Filter.
	IdxNum int
	IdxStr string
	// OrderByConsumed says the scan gives its rows in the order of OrderBy.
	OrderByConsumed bool
	EstimatedCost   float64
	EstimatedRows   int64
	// Unique says the scan gives at most one row.
	Unique bool
}

// ModuleFlag tells SQLite how a module behaves.
type ModuleFlag int

const (
	// EponymousOnly: the module is only used by its name, as a function
	// whose arguments are its hidden columns (a table-valued function such
	// as generate_series). CREATE VIRTUAL TABLE with it fails. Without the
	// flag, the module serves only CREATE VIRTUAL TABLE.
	EponymousOnly ModuleFlag = csqlite.ModEponymousOnly
	// ModuleDirectOnly: its tables may be used only by SQL the program
	// runs and by temp triggers and views, never by a trigger or a view
	// stored in a database. Give it to a module that reaches
	// outside the database, such as one that reads files, so that a
	// database file someone else made cannot use it. It cannot go with
	// ModuleInnocuous: a table has one of the two.
	ModuleDirectOnly ModuleFlag = csqlite.ModDirectOnly
	// ModuleInnocuous: the module has no side effects and reveals nothing,
	// so its tables are safe wherever SQL comes from.
	ModuleInnocuous ModuleFlag = csqlite.ModInnocuous
)

type module struct {
	name    string
	connect csqlite.Module
	flags   int
}

// WithModule makes a virtual table module name on every connection of the
// connector. connect makes a table for CREATE VIRTUAL TABLE t USING
// name(args...), and when a connection first uses such a table, or, for an
// EponymousOnly module, for its name used as a table, with no args. args are the arguments as SQL wrote
// them, without quotes taken off. A later option of the same name replaces
// an earlier one.
func WithModule(name string, connect func(args []string) (VTable, error), flags ...ModuleFlag) Option {
	return Option{func(o *options) error {
		switch {
		case name == "":
			return errors.New("sqlite: a module without a name")
		case strings.ContainsRune(name, 0):
			return fmt.Errorf("sqlite: module %q: a NUL in the name", name)
		case len(name) > maxFuncName:
			return fmt.Errorf("sqlite: module %.20q...: the name is longer than %d bytes", name, maxFuncName)
		case connect == nil:
			return fmt.Errorf("sqlite: module %s is nil", name)
		}
		m := module{name: name}
		for _, f := range flags {
			switch f {
			case EponymousOnly, ModuleDirectOnly, ModuleInnocuous:
				m.flags |= int(f)
			default:
				return fmt.Errorf("sqlite: module %s: unknown flag %#x", name, int(f))
			}
		}
		if m.flags&int(ModuleDirectOnly) != 0 && m.flags&int(ModuleInnocuous) != 0 {
			return fmt.Errorf("sqlite: module %s: ModuleDirectOnly and ModuleInnocuous together", name)
		}
		m.connect = func(args []string) (csqlite.VTable, error) {
			t, err := connect(args)
			if err != nil {
				return nil, err
			}
			if t == nil {
				return nil, nil
			}
			return vtable{t}, nil
		}
		o.modules = append(o.modules, m)
		return nil
	}}
}

// vtable and vcursor give a VTable to the engine.
type vtable struct{ VTable }

func (t vtable) BestIndex(ci *csqlite.IndexInfo) error {
	info := IndexInfo{
		Constraints:   make([]IndexConstraint, len(ci.Constraints)),
		OrderBy:       make([]IndexOrderBy, len(ci.OrderBy)),
		ColUsed:       ci.ColUsed,
		Usage:         make([]IndexUsage, len(ci.Usage)),
		EstimatedCost: ci.EstimatedCost,
		EstimatedRows: ci.EstimatedRows,
	}
	for i, c := range ci.Constraints {
		info.Constraints[i] = IndexConstraint{c.Column, IndexOp(c.Op), c.Usable}
	}
	for i, o := range ci.OrderBy {
		info.OrderBy[i] = IndexOrderBy(o)
	}
	if err := t.VTable.BestIndex(&info); err != nil {
		if errors.Is(err, ErrUnusablePlan) {
			return csqlite.ErrConstraint
		}
		return err
	}
	if len(info.Usage) != len(ci.Usage) {
		return fmt.Errorf("sqlite: BestIndex left %d usages for %d constraints", len(info.Usage), len(ci.Usage))
	}
	for i, u := range info.Usage {
		ci.Usage[i] = csqlite.IndexUsage(u)
	}
	ci.IdxNum = info.IdxNum
	ci.IdxStr = info.IdxStr
	ci.OrderByConsumed = info.OrderByConsumed
	ci.EstimatedCost = info.EstimatedCost
	ci.EstimatedRows = info.EstimatedRows
	ci.Unique = info.Unique
	return nil
}

func (t vtable) Open() (csqlite.VCursor, error) {
	c, err := t.VTable.Open()
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}
	return vcursor{c}, nil
}

type vcursor struct{ VCursor }

// Column gives a value the conversions of a bound argument, as a Func's.
func (c vcursor) Column(i int) (driver.Value, error) {
	v, err := c.VCursor.Column(i)
	if err != nil {
		return nil, err
	}
	return resultValue(v)
}
