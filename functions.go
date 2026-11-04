// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
)

// Func is a SQL function written in Go.
//
// Its arguments are copies, each an int64, float64, string, []byte or nil,
// so it may keep them. It returns a value database/sql can bind (an integer,
// float, bool, string, []byte, time.Time, nil or a driver.Valuer), or an
// error, which becomes the error of the statement. A panic in it is
// recovered and becomes the error of the statement too: the connection
// stays good.
//
// It may run on any connection of its connector, and from several
// goroutines at once. It must not use the database: it runs in the middle
// of a statement. It may also run more than once for the same row: SQLite
// does not promise how often it calls a function, and group commit runs a
// batch again when a failure of one statement ended its transaction. Keep
// side effects out of it, or make them safe to repeat.
type Func func(args []driver.Value) (driver.Value, error)

// FuncFlag tells SQLite how a function behaves.
type FuncFlag int

const (
	// Deterministic: the same arguments always give the same result, so
	// SQLite may compute it once, and use it in indexes and generated
	// columns.
	Deterministic FuncFlag = csqlite.FuncDeterministic
	// DirectOnly: the function may be called only from SQL the program
	// runs, never from a trigger, view, index or generated column of the
	// schema. Give it to a function with side effects, so that a database
	// file someone else made cannot call it.
	DirectOnly FuncFlag = csqlite.FuncDirectOnly
	// Innocuous: the function has no side effects and reveals nothing, so
	// it is safe wherever SQL comes from.
	Innocuous FuncFlag = csqlite.FuncInnocuous
)

// maxFuncArgs is SQLITE_MAX_FUNCTION_ARG and maxFuncName the longest name
// SQLite takes.
const (
	maxFuncArgs = 1000
	maxFuncName = 255
)

// An Option adds something to the connections of a connector: give it to
// NewConnector, or to Register for the connectors sql.Open makes.
type Option struct {
	apply func(*options) error
}

type options struct {
	funcs      []function
	collations []collation
}

type function struct {
	name   string
	nArg   int
	flags  int
	fn     csqlite.Func       // a scalar function, or
	newAgg func() csqlite.Agg // the states of an aggregate
}

type collation struct {
	name string
	cmp  func(a, b string) int
}

// WithFunction makes fn the SQL function name of nArg arguments (-1 for any
// number) on every connection of the connector. A later option of the same
// name and nArg replaces an earlier one.
func WithFunction(name string, nArg int, fn Func, flags ...FuncFlag) Option {
	return Option{func(o *options) error {
		f, err := newFunction(name, nArg, fn == nil, flags)
		if err != nil {
			return err
		}
		f.fn = func(args []driver.Value) (driver.Value, error) {
			v, err := fn(args)
			if err != nil {
				return nil, err
			}
			return resultValue(v)
		}
		o.funcs = append(o.funcs, f)
		return nil
	}}
}

// Aggregate is the state of one group of an aggregate function written in
// Go. Step runs for each row of the group, with arguments as a Func gets
// them, then Final once, with a result as a Func gives it. An error or a
// panic in either is the error of the statement.
type Aggregate interface {
	Step(args []driver.Value) error
	Final() (driver.Value, error)
}

// WithAggregate makes an aggregate function name of nArg arguments: for
// each group, newAggregate makes a state. A group of no rows gets one too,
// for Final alone.
func WithAggregate(name string, nArg int, newAggregate func() Aggregate, flags ...FuncFlag) Option {
	return Option{func(o *options) error {
		f, err := newFunction(name, nArg, newAggregate == nil, flags)
		if err != nil {
			return err
		}
		f.newAgg = func() csqlite.Agg {
			a := newAggregate()
			if a == nil {
				return nil
			}
			return aggResult{a}
		}
		o.funcs = append(o.funcs, f)
		return nil
	}}
}

// aggResult gives Final's result the conversions of a bound argument.
type aggResult struct{ Aggregate }

func (a aggResult) Final() (driver.Value, error) {
	v, err := a.Aggregate.Final()
	if err != nil {
		return nil, err
	}
	return resultValue(v)
}

// newFunction checks what every function and aggregate takes.
func newFunction(name string, nArg int, isNil bool, flags []FuncFlag) (function, error) {
	switch {
	case name == "":
		return function{}, errors.New("sqlite: a function without a name")
	case strings.ContainsRune(name, 0):
		return function{}, fmt.Errorf("sqlite: function %q: a NUL in the name", name)
	case len(name) > maxFuncName:
		return function{}, fmt.Errorf("sqlite: function %.20q...: the name is longer than %d bytes", name, maxFuncName)
	case isNil:
		return function{}, fmt.Errorf("sqlite: function %s is nil", name)
	case nArg < -1 || nArg > maxFuncArgs:
		return function{}, fmt.Errorf("sqlite: function %s: %d arguments, want -1 to %d", name, nArg, maxFuncArgs)
	}
	f := function{name: name, nArg: nArg}
	for _, fl := range flags {
		switch fl {
		case Deterministic, DirectOnly, Innocuous:
			f.flags |= int(fl)
		default:
			return function{}, fmt.Errorf("sqlite: function %s: unknown flag %#x", name, int(fl))
		}
	}
	return f, nil
}

// WithCollation makes cmp the collation name, for COLLATE name in queries
// and in the schema. cmp returns a negative number, 0 or a positive number
// as a sorts before, with or after b, and must be a total order. SQLite has
// no way to fail a comparison, so after a panic in cmp the statement goes on
// with comparisons that say equal. None of what it does then is kept: it
// fails with the panic, the transaction it ran in is rolled back, and no
// commit happens before that. The connection stays good.
func WithCollation(name string, cmp func(a, b string) int) Option {
	return Option{func(o *options) error {
		switch {
		case name == "":
			return errors.New("sqlite: a collation without a name")
		case strings.ContainsRune(name, 0):
			return fmt.Errorf("sqlite: collation %q: a NUL in the name", name)
		case cmp == nil:
			return fmt.Errorf("sqlite: collation %s is nil", name)
		}
		o.collations = append(o.collations, collation{name, cmp})
		return nil
	}}
}

// resultValue turns what a function returned into a value the engine sets:
// the same conversions as a bound argument.
func resultValue(v driver.Value) (driver.Value, error) {
	v, err := convert(v)
	if err != nil {
		return nil, err
	}
	if t, ok := v.(time.Time); ok {
		return t.Format(timeFormat), nil
	}
	return v, nil
}

// registry holds what Register gave. A connector takes what is there when it
// is made.
var registry struct {
	sync.Mutex
	opts []Option
	gen  uint64 // how many times Register ran
}

// Register adds opts to every connector made after it, by sql.Open or by
// NewConnector, before the connector's own options. Call it before opening
// databases, as in an init function. An option it was given that is not
// valid fails those connectors.
func Register(opts ...Option) {
	registry.Lock()
	defer registry.Unlock()
	registry.opts = append(slices.Clip(registry.opts), opts...)
	registry.gen++
}

// ownOptions numbers the connectors made with options of their own.
var ownOptions atomic.Uint64

// applyOptions gives the options of a new connector: Register's, then its
// own. The second result goes into the connector's key, so that group
// commit merges only between connections of the same functions. Connectors
// with options of their own never share it, as functions cannot be compared.
func applyOptions(own []Option) (options, string, error) {
	registry.Lock()
	global, gen := registry.opts, registry.gen
	registry.Unlock()
	var o options
	for _, opt := range slices.Concat(global, own) {
		if opt.apply == nil {
			return o, "", errors.New("sqlite: an Option that is not made by this package")
		}
		if err := opt.apply(&o); err != nil {
			return o, "", err
		}
	}
	switch {
	case len(own) > 0:
		return o, fmt.Sprintf("own %d", ownOptions.Add(1)), nil
	case len(global) > 0:
		return o, fmt.Sprintf("registered %d", gen), nil
	}
	return o, "", nil
}

// setUp gives a new connection the functions and collations of the options.
func (o *options) setUp(c *csqlite.Conn) error {
	for _, f := range o.funcs {
		var err error
		if f.newAgg != nil {
			err = c.CreateAggregate(f.name, f.nArg, f.flags, f.newAgg)
		} else {
			err = c.CreateFunction(f.name, f.nArg, f.flags, f.fn)
		}
		if err != nil {
			return fmt.Errorf("sqlite: function %s: %w", f.name, err)
		}
	}
	for _, cl := range o.collations {
		if err := c.CreateCollation(cl.name, cl.cmp); err != nil {
			return fmt.Errorf("sqlite: collation %s: %w", cl.name, err)
		}
	}
	return nil
}
