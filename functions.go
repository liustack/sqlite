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
// of a statement.
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
	funcs []function
}

type function struct {
	name  string
	nArg  int
	flags int
	fn    csqlite.Func
}

// WithFunction makes fn the SQL function name of nArg arguments (-1 for any
// number) on every connection of the connector. A later option of the same
// name and nArg replaces an earlier one.
func WithFunction(name string, nArg int, fn Func, flags ...FuncFlag) Option {
	return Option{func(o *options) error {
		switch {
		case name == "":
			return errors.New("sqlite: a function without a name")
		case len(name) > maxFuncName:
			return fmt.Errorf("sqlite: function %.20q...: the name is longer than %d bytes", name, maxFuncName)
		case fn == nil:
			return fmt.Errorf("sqlite: function %s is nil", name)
		case nArg < -1 || nArg > maxFuncArgs:
			return fmt.Errorf("sqlite: function %s: %d arguments, want -1 to %d", name, nArg, maxFuncArgs)
		}
		f := function{name: name, nArg: nArg}
		for _, fl := range flags {
			switch fl {
			case Deterministic, DirectOnly, Innocuous:
				f.flags |= int(fl)
			default:
				return fmt.Errorf("sqlite: function %s: unknown flag %#x", name, int(fl))
			}
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

// resultValue turns what a function returned into a value the engine sets:
// the same conversions as a bound argument.
func resultValue(v driver.Value) (driver.Value, error) {
	v, err := driver.DefaultParameterConverter.ConvertValue(v)
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

// setUp gives a new connection the functions of the options.
func (o *options) setUp(c *csqlite.Conn) error {
	for _, f := range o.funcs {
		if err := c.CreateFunction(f.name, f.nArg, f.flags, f.fn); err != nil {
			return fmt.Errorf("sqlite: function %s: %w", f.name, err)
		}
	}
	return nil
}
