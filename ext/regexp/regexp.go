// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package regexp gives SQLite the REGEXP operator and regexp functions, with
// Go's regexp syntax (RE2: no backreferences, and time linear in the text).
//
//	sqlite.NewConnector(dsn, regexp.Option())
//
// Then:
//
//	x REGEXP pattern                       1 if pattern matches somewhere in x, else 0
//	regexp_like(x, pattern)                the same
//	regexp_substr(x, pattern)              the first match, or NULL
//	regexp_replace(x, pattern, repl)       x with every match replaced
//
// repl takes Go's syntax, not \1: $1 or ${1} is a group by number, ${name} a
// group by name, and $$ a dollar. A name runs as far as letters, digits and _
// go, so write ${1}x, not $1x (a group named 1x). A group that is not there
// gives the empty text.
//
// A NULL argument gives NULL. A REAL argument is an error: write
// CAST(x AS TEXT). A pattern that does not compile is the error of the
// statement.
package regexp

import (
	"database/sql/driver"
	"regexp"
	"sync"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/internal/arg"
)

// Option adds the operator and the functions to a connector.
func Option() sqlite.Option {
	return sqlite.Join(
		sqlite.WithFunction("regexp", 2, func(a []driver.Value) (driver.Value, error) {
			// x REGEXP y calls regexp(y, x).
			return like("regexp", a[1], 1, a[0], 0)
		}, sqlite.Deterministic, sqlite.Innocuous),
		sqlite.WithFunction("regexp_like", 2, func(a []driver.Value) (driver.Value, error) {
			return like("regexp_like", a[0], 0, a[1], 1)
		}, sqlite.Deterministic, sqlite.Innocuous),
		sqlite.WithFunction("regexp_substr", 2, substr, sqlite.Deterministic, sqlite.Innocuous),
		sqlite.WithFunction("regexp_replace", 3, replace, sqlite.Deterministic, sqlite.Innocuous),
	)
}

func like(fn string, x driver.Value, xi int, pattern driver.Value, pi int) (driver.Value, error) {
	s, re, null, err := textAndPattern(fn, x, xi, pattern, pi)
	if null || err != nil {
		return nil, err
	}
	return re.MatchString(s), nil
}

func substr(a []driver.Value) (driver.Value, error) {
	s, re, null, err := textAndPattern("regexp_substr", a[0], 0, a[1], 1)
	if null || err != nil {
		return nil, err
	}
	loc := re.FindStringIndex(s)
	if loc == nil {
		return nil, nil
	}
	return s[loc[0]:loc[1]], nil
}

func replace(a []driver.Value) (driver.Value, error) {
	s, re, null, err := textAndPattern("regexp_replace", a[0], 0, a[1], 1)
	if null || err != nil {
		return nil, err
	}
	repl, null, err := arg.Text("regexp_replace", 2, a[2])
	if null || err != nil {
		return nil, err
	}
	return re.ReplaceAllString(s, repl), nil
}

// textAndPattern reads the text x and the pattern, arguments xi and pi of fn
// (from 0).
func textAndPattern(fn string, x driver.Value, xi int, pattern driver.Value, pi int) (string, *regexp.Regexp, bool, error) {
	s, null, err := arg.Text(fn, xi, x)
	if null || err != nil {
		return "", nil, null, err
	}
	p, null, err := arg.Text(fn, pi, pattern)
	if null || err != nil {
		return "", nil, null, err
	}
	re, err := compile(p)
	return s, re, false, err
}

// cache keeps compiled patterns: a statement runs the same pattern on every
// row. It is emptied when full.
var cache struct {
	sync.Mutex
	m map[string]*regexp.Regexp
}

const cacheSize = 256

func compile(p string) (*regexp.Regexp, error) {
	cache.Lock()
	re, ok := cache.m[p]
	cache.Unlock()
	if ok {
		return re, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	cache.Lock()
	if cache.m == nil || len(cache.m) >= cacheSize {
		cache.m = make(map[string]*regexp.Regexp, cacheSize)
	}
	cache.m[p] = re
	cache.Unlock()
	return re, nil
}
