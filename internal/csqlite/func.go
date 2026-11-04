// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package csqlite

import (
	"database/sql/driver"
	"fmt"
	"math"
	"unsafe"
)

// Func is a SQL function written in Go. Its arguments are copies, of the
// types int64, float64, string, []byte and nil. It returns one of those, an
// int or a bool, or an error that becomes the error of the statement. A
// panic in it is recovered and becomes the error of the statement too.
type Func func(args []driver.Value) (driver.Value, error)

// Flags of CreateFunction (sqlite3.h).
const (
	FuncDeterministic = 0x000000800
	FuncDirectOnly    = 0x000080000
	FuncInnocuous     = 0x000200000
)

const utf8 = 1 // SQLITE_UTF8

// funcs holds the functions SQLite knows by handle. SQLite gives a handle
// back to host_func_destroy when it drops the function.
var funcs table[Func]

// CreateFunction makes f the SQL function name of nArg arguments (-1 for any
// number) on the connection, in place of one of the same name and nArg.
func (c *Conn) CreateFunction(name string, nArg int, flags int, f Func) error {
	if err := c.brokenErr(); err != nil {
		return err
	}
	if len(name) > maxName {
		return &Error{Code: 21, Msg: "function name too long"}
	}
	h := funcs.add(&f)
	a := c.args()
	z := a.str(name)
	// SQLite gives h to host_func_destroy when this fails as well.
	return c.err(int(int32(go_create_function(a.top, c.db, z, uintptr(nArg), uintptr(flags|utf8), h))))
}

// host_func runs the Go function of a call. Everything it hands the C is
// written from sp, above the frames of the C that called it.
func host_func(sp, ctx, argc, argv uintptr) {
	defer failQuiet(sp)
	f := *funcs.get(sqlite3_user_data(sp, ctx))
	args := make([]driver.Value, int(int32(argc)))
	for i := range args {
		args[i] = value(sp, *(*uintptr)(at(argv + uintptr(i)*8)))
	}
	v, err := call(f, args)
	if err == nil {
		err = result(sp, ctx, v)
	}
	if err != nil {
		resultError(sp, ctx, err.Error())
	}
}

func host_func_destroy(sp, h uintptr) {
	defer failQuiet(sp)
	funcs.remove(h)
}

// call runs f and turns a panic in it into an error: the panic is the user's,
// and the C under host_func is still whole.
func call(f Func, args []driver.Value) (v driver.Value, err error) {
	defer func() {
		if e := recover(); e != nil {
			v, err = nil, fmt.Errorf("panic in a Go function: %v", e)
		}
	}()
	return f(args)
}

// SQLite's fundamental types (sqlite3.h).
const (
	typeInteger = 1
	typeFloat   = 2
	typeText    = 3
	typeBlob    = 4
	typeNull    = 5
)

// value copies a sqlite3_value into Go.
func value(sp, v uintptr) driver.Value {
	switch int32(sqlite3_value_type(sp, v)) {
	case typeInteger:
		return int64(sqlite3_value_int64(sp, v))
	case typeFloat:
		return sqlite3_value_double(sp, v)
	case typeText:
		p := sqlite3_value_text(sp, v)
		n := int(int32(sqlite3_value_bytes(sp, v)))
		if p == 0 || n == 0 {
			return ""
		}
		return string(bytesAt(p, n))
	case typeBlob:
		p := sqlite3_value_blob(sp, v)
		n := int(int32(sqlite3_value_bytes(sp, v)))
		b := make([]byte, n)
		if n > 0 {
			copy(b, bytesAt(p, n))
		}
		return b
	}
	return nil
}

// result sets the result of a call to v.
func result(sp, ctx uintptr, v driver.Value) error {
	switch v := v.(type) {
	case nil:
		sqlite3_result_null(sp, ctx)
	case int64:
		sqlite3_result_int64(sp, ctx, uintptr(v))
	case int:
		sqlite3_result_int64(sp, ctx, uintptr(v))
	case bool:
		var i uintptr
		if v {
			i = 1
		}
		sqlite3_result_int64(sp, ctx, i)
	case float64:
		sqlite3_result_double(sp, ctx, v)
	case string:
		if len(v) > math.MaxInt32 {
			sqlite3_result_error_toobig(sp, ctx)
			return nil
		}
		z, done, err := stageAt(sp, unsafe.StringData(v), len(v))
		if err != nil {
			sqlite3_result_error_nomem(sp, ctx)
			return nil
		}
		defer done()
		sqlite3_result_text(sp+stageRoom(len(v)), ctx, z, uintptr(len(v)), transient)
	case []byte:
		if v == nil {
			sqlite3_result_null(sp, ctx)
			return nil
		}
		if len(v) > math.MaxInt32 {
			sqlite3_result_error_toobig(sp, ctx)
			return nil
		}
		z, done, err := stageAt(sp, unsafe.SliceData(v), len(v))
		if err != nil {
			sqlite3_result_error_nomem(sp, ctx)
			return nil
		}
		defer done()
		sqlite3_result_blob(sp+stageRoom(len(v)), ctx, z, uintptr(len(v)), transient)
	default:
		return fmt.Errorf("a Go function returned a %T", v)
	}
	return nil
}

// resultError makes msg the error of a call. SQLite copies it.
func resultError(sp, ctx uintptr, msg string) {
	const most = 1 << 16
	if len(msg) > most {
		msg = msg[:most]
	}
	a := args{sp}
	z := a.str(msg)
	sqlite3_result_error(a.top, ctx, z, uintptr(len(msg)))
}
