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
// A result longer than 1,000,000,000 bytes is an error.
//
// A NULL argument gives NULL. A REAL argument is an error: write
// CAST(x AS TEXT). A pattern that does not compile is the error of the
// statement.
package regexp

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

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
	// A result is at most the text, the template for each match (at most one
	// more than the bytes of the text) and each $ of it as a whole match.
	n, refs := int64(len(s)), int64(strings.Count(repl, "$"))
	if n+(n+1)*int64(len(repl))+refs*n <= int64(maxLength) {
		return re.ReplaceAllString(s, repl), nil
	}
	r, err := replaceExact(re, s, repl, maxLength)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// maxLength is the most bytes regexp_replace gives, a variable for the
// tests.
var maxLength = arg.MaxLength

// replaceExact is re.ReplaceAllString(s, repl), which fails once the result
// would be longer than max: it counts the bytes of each match before it
// adds them.
func replaceExact(re *regexp.Regexp, s, repl string, max int) (string, error) {
	t := parseTemplate(re, repl)
	tooLong := fmt.Errorf("regexp_replace: the result is longer than %d bytes", max)
	var b []byte
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		if len(b)+m[0]-last > max {
			return "", tooLong
		}
		b = append(b, s[last:m[0]]...)
		n := 0
		for _, p := range t {
			n += len(p.lit) + len(group(s, m, p.group))
		}
		if len(b)+n > max {
			return "", tooLong
		}
		for _, p := range t {
			b = append(b, p.lit...)
			b = append(b, group(s, m, p.group)...)
		}
		last = m[1]
	}
	if len(b)+len(s)-last > max {
		return "", tooLong
	}
	return string(append(b, s[last:]...)), nil
}

// piece is a part of a template: text, then a group (-1 for none).
type piece struct {
	lit   string
	group int
}

// group is the text of group g of match m in s, empty when it took no part
// or g is -1.
func group(s string, m []int, g int) string {
	if g < 0 || 2*g+1 >= len(m) || m[2*g] < 0 {
		return ""
	}
	return s[m[2*g]:m[2*g+1]]
}

// parseTemplate reads repl as regexp.Regexp.Expand does: $$ is a dollar, $name
// or ${name} a group by number or name, and a $ that starts no name itself.
// A group that is not there is -1.
func parseTemplate(re *regexp.Regexp, repl string) []piece {
	var t []piece
	var lit strings.Builder
	for {
		before, after, ok := strings.Cut(repl, "$")
		lit.WriteString(before)
		if !ok {
			break
		}
		repl = after
		if repl != "" && repl[0] == '$' {
			lit.WriteByte('$')
			repl = repl[1:]
			continue
		}
		name, num, rest, ok := extract(repl)
		if !ok {
			lit.WriteByte('$')
			continue
		}
		repl = rest
		g := num
		if num < 0 {
			g = re.SubexpIndex(name)
		}
		t = append(t, piece{lit.String(), g})
		lit.Reset()
	}
	return append(t, piece{lit.String(), -1})
}

// extract is the function of the same name in Go's regexp: the name after a
// $, the group number it gives or -1, and the template after it.
func extract(str string) (name string, num int, rest string, ok bool) {
	if str == "" {
		return
	}
	brace := false
	if str[0] == '{' {
		brace = true
		str = str[1:]
	}
	i := 0
	for i < len(str) {
		r, size := utf8.DecodeRuneInString(str[i:])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			break
		}
		i += size
	}
	if i == 0 {
		return
	}
	name = str[:i]
	if brace {
		if i >= len(str) || str[i] != '}' {
			return
		}
		i++
	}
	num = 0
	for j := 0; j < len(name); j++ {
		if name[j] < '0' || '9' < name[j] || num >= 1e8 {
			num = -1
			break
		}
		num = num*10 + int(name[j]) - '0'
	}
	if name[0] == '0' && len(name) > 1 {
		num = -1
	}
	return name, num, str[i:], true
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
