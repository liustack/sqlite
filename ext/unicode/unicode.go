// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package unicode gives SQLite case rules for every script, where SQLite's
// own upper, lower and NOCASE know only ASCII:
//
//	sqlite.NewConnector(dsn, unicode.Option())
//
// Then:
//
//	unicode_upper(x)        x in upper case, each character on its own as
//	                        Go's unicode.ToUpper: 'straße' gives 'STRAßE'
//	unicode_lower(x)        x in lower case, each character on its own
//	x COLLATE unicode_nocase
//	                        compares as if both were case folded: 'Ä' = 'ä',
//	                        'Σ' = 'σ' = 'ς', 'K' (Kelvin sign) = 'k'
//
// They have names of their own so that an index made with SQLite's upper,
// lower or NOCASE keeps the order it was made in. NULL gives NULL, a REAL is
// refused (write CAST(x AS TEXT)), and bytes that are not UTF-8 are kept as
// they are.
//
// unicode_nocase orders ASCII as NOCASE does, and the other characters by
// the smallest code point they fold with. The folding comes from the Unicode
// tables of the Go that built the program, and a new Unicode can add a
// character that changes the order of old ones (U+037F joined U+03F3 in
// Unicode 7.0). After moving to a Go with a newer Unicode (unicode.Version),
// run REINDEX unicode_nocase on databases with indexes that use it.
package unicode

import (
	"database/sql/driver"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/internal/arg"
)

// Option adds the functions and the collation to a connector.
func Option() sqlite.Option {
	return sqlite.Join(
		sqlite.WithFunction("unicode_upper", 1, caseFunc("unicode_upper", unicode.ToUpper), sqlite.Deterministic, sqlite.Innocuous),
		sqlite.WithFunction("unicode_lower", 1, caseFunc("unicode_lower", unicode.ToLower), sqlite.Deterministic, sqlite.Innocuous),
		sqlite.WithCollation("unicode_nocase", Compare),
	)
}

func caseFunc(name string, to func(rune) rune) sqlite.Func {
	return func(a []driver.Value) (driver.Value, error) {
		s, null, err := arg.Text(name, 0, a[0])
		if err != nil || null {
			return nil, err
		}
		return mapRunes(s, to), nil
	}
}

// mapRunes maps each character of s by to, and keeps bytes that are not
// UTF-8.
func mapRunes(s string, to func(rune) rune) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n <= 1 {
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteRune(to(r))
		i += n
	}
	return b.String()
}

// Compare is the unicode_nocase collation: -1, 0 or 1 as a sorts before,
// with or after b.
func Compare(a, b string) int {
	for len(a) > 0 && len(b) > 0 {
		ka, na := key(a)
		kb, nb := key(b)
		if ka != kb {
			if ka < kb {
				return -1
			}
			return 1
		}
		a, b = a[na:], b[nb:]
	}
	switch {
	case len(a) > 0:
		return 1
	case len(b) > 0:
		return -1
	}
	return 0
}

// key is the sort key of the first character of s, and its length in bytes:
// the smallest of the characters it folds with, an ASCII letter in lower
// case as NOCASE has it. Characters fold together exactly when their keys
// are equal. A byte that is not UTF-8 sorts after every character, by its
// value.
func key(s string) (rune, int) {
	c := s[0]
	if c < utf8.RuneSelf {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		return rune(c), 1
	}
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && n <= 1 {
		return unicode.MaxRune + 1 + rune(c), 1
	}
	k := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		k = min(k, f)
	}
	if 'A' <= k && k <= 'Z' {
		k += 'a' - 'A' // K and k with the Kelvin sign, S and s with long s
	}
	return k, n
}
