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
// A result that may be longer than 1,000,000,000 bytes is an error, each
// group the template names counted as long as its whole match.
//
// The memory a match takes grows with the size of the pattern times its
// groups, and its time with the size of the pattern times the length of
// the text. So a pattern is an error when it is longer than 50,000 bytes
// (what SQLite takes for LIKE), when it has more than 100 groups, or when,
// with each repeat written out (x{3} as xxx), it has more than 50,000
// characters and operators, or more than 1,048,576 once multiplied by one
// more than its groups.
//
// A NULL argument gives NULL. A REAL argument is an error: write
// CAST(x AS TEXT). A pattern that does not compile is the error of the
// statement.
package regexp

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
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
	if !fits(re, s, repl, maxLength) {
		return nil, fmt.Errorf("regexp_replace: the result may be longer than %d bytes", maxLength)
	}
	return re.ReplaceAllString(s, repl), nil
}

// maxLength is the most bytes regexp_replace gives, a variable for the
// tests.
var maxLength = arg.MaxLength

// fits says re.ReplaceAllString(s, repl) is at most max bytes long, counting
// each group the template names as long as its whole match: so it never says
// yes to a result that is longer, and it says no to one that is not only
// when the groups are shorter than their matches.
func fits(re *regexp.Regexp, s, repl string, max int) bool {
	// There are at most len(s)+1 matches, together at most len(s) bytes. The
	// template gives each at most len(repl) bytes and the match once for each
	// $. With SQLite's limit of 1e9 bytes on s and repl, no sum overflows.
	n, m := len(s), len(repl)
	if n+(n+1)*m+strings.Count(repl, "$")*n <= max {
		return true
	}
	// What the template gives exactly, by Go's own rules: lit bytes of its
	// own, and refs groups that each give a part of the match.
	none := make([]int, 2*(re.NumSubexp()+1))
	one := make([]int, len(none))
	for i := range none {
		none[i], one[i] = -1, i%2
	}
	lit := len(re.ExpandString(nil, repl, "", none))
	refs := len(re.ExpandString(nil, repl, "x", one)) - lit
	if n+(n+1)*lit+refs*n <= max {
		return true
	}
	// Then the matches themselves, kept only as a count and a sum.
	k, sum := 0, 0
	re.ReplaceAllStringFunc(s, func(match string) string {
		k++
		sum += len(match)
		return ""
	})
	return n-sum+k*lit+refs*sum <= max
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

// The limits of a pattern. maxPattern is what SQLite takes for a LIKE
// pattern, and is checked before the pattern is parsed. maxSize and
// maxSizeGroups bound the program regexp compiles: a match keeps up to one
// thread per instruction, each with the bounds of every group. At the
// limits a match takes up to about 25 MiB.
const (
	maxPattern    = 50000
	maxGroups     = 100
	maxSize       = 50000
	maxSizeGroups = 1 << 20
)

// size is how many instructions re compiles to at most, a repeat written
// out, besides the two every program has. It is how regexp/syntax counts
// for its own, larger, limit.
func size(re *syntax.Regexp) int64 {
	var n int64
	switch re.Op {
	case syntax.OpLiteral:
		n = int64(len(re.Rune))
	case syntax.OpCapture, syntax.OpStar:
		n = 2 + size(re.Sub[0])
	case syntax.OpPlus, syntax.OpQuest:
		n = 1 + size(re.Sub[0])
	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			n += size(sub)
		}
		if re.Op == syntax.OpAlternate {
			n += int64(len(re.Sub)) - 1
		}
	case syntax.OpRepeat:
		sub := size(re.Sub[0])
		switch {
		case re.Max == -1 && re.Min == 0:
			n = 2 + sub
		case re.Max == -1:
			n = 1 + int64(re.Min)*sub
		default:
			n = int64(re.Max)*sub + int64(re.Max-re.Min)
		}
	}
	return max(1, n)
}

func compile(p string) (*regexp.Regexp, error) {
	cache.Lock()
	re, ok := cache.m[p]
	cache.Unlock()
	if ok {
		return re, nil
	}
	if len(p) > maxPattern {
		return nil, fmt.Errorf("regexp: the pattern is longer than %d bytes", maxPattern)
	}
	// The size is checked on the parsed pattern, before a repeat is
	// written out by compiling it.
	tree, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		return nil, err
	}
	groups := tree.MaxCap()
	if groups > maxGroups {
		return nil, fmt.Errorf("regexp: the pattern has more than %d groups", maxGroups)
	}
	if n := size(tree); n > maxSize || n*int64(groups+1) > maxSizeGroups {
		return nil, fmt.Errorf("regexp: the pattern is too large with its repeats written out")
	}
	re, err = regexp.Compile(p)
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
