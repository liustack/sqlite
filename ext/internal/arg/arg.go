// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package arg reads the arguments of the functions of ext/.
package arg

import (
	"database/sql/driver"
	"fmt"
	"strconv"
)

// Text is the text of an argument as SQLite would give it: a string or a
// blob as it is, an integer in decimal. null is true for NULL. A REAL is
// refused: SQLite's text of a float has rules of its own that a function
// could only copy, so the caller writes CAST(x AS TEXT) instead.
func Text(fn string, i int, v driver.Value) (s string, null bool, err error) {
	switch v := v.(type) {
	case nil:
		return "", true, nil
	case string:
		return v, false, nil
	case []byte:
		return string(v), false, nil
	case int64:
		return strconv.FormatInt(v, 10), false, nil
	case float64:
		return "", false, fmt.Errorf("%s: argument %d is a REAL: use CAST(x AS TEXT)", fn, i+1)
	}
	return "", false, fmt.Errorf("%s: argument %d is a %T", fn, i+1, v)
}

// Bytes is Text for functions that read bytes, without a copy of a blob.
func Bytes(fn string, i int, v driver.Value) (b []byte, null bool, err error) {
	if v, ok := v.([]byte); ok {
		return v, false, nil
	}
	s, null, err := Text(fn, i, v)
	return []byte(s), null, err
}

// MaxLength is SQLITE_MAX_LENGTH, the most bytes SQLite takes in a string or
// a blob. A function stops building a value longer than that and fails, as
// SQLite's own would, rather than fill the memory first.
const MaxLength = 1_000_000_000
