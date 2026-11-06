// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package regexp_test

import (
	"database/sql"
	"fmt"
	goregexp "regexp"
	"strings"
	"testing"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/internal/sqltest"
	"github.com/liustack/sqlite/ext/regexp"
)

func TestRegexp(t *testing.T) {
	db := sqltest.Open(t, regexp.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT 'abc' REGEXP 'b'`:                                                       "1",
		`SELECT 'abc' REGEXP '^b'`:                                                      "0",
		`SELECT 'abc' NOT REGEXP '^b'`:                                                  "1",
		`SELECT NULL REGEXP 'a'`:                                                        "NULL",
		`SELECT 'a' REGEXP NULL`:                                                        "NULL",
		`SELECT 12345 REGEXP '^123'`:                                                    "1",
		`SELECT x'616263' REGEXP 'b'`:                                                   "1",
		`SELECT regexp_like('Hello', '(?i)^hello$')`:                                    "1",
		`SELECT regexp_substr('order 66 and 99', '[0-9]+')`:                             "66",
		`SELECT regexp_substr('no digits', '[0-9]+')`:                                   "NULL",
		`SELECT regexp_replace('a1b22', '[0-9]+', '<$0>')`:                              "a<1>b<22>",
		`SELECT regexp_replace('2026-11-04', '(\d+)-(\d+)-(\d+)', '$3.$2.$1')`:          "04.11.2026",
		`SELECT regexp_replace(NULL, 'a', 'b')`:                                         "NULL",
		`SELECT regexp_replace('abc', '(b)', '${1}x')`:                                  "abxc",
		`SELECT regexp_replace('a.b', '[.]', '$$')`:                                     "a$b",
		`SELECT count(*) FROM (VALUES ('ab'), ('b'), ('ba')) WHERE column1 REGEXP '^b'`: "2",
	})
	sqltest.Fails(t, db, map[string]string{
		`SELECT 'a' REGEXP '('`:   "missing closing )",
		`SELECT 1.5 REGEXP '1'`:   "CAST(x AS TEXT)",
		`SELECT 1.5 REGEXP 'a'`:   "regexp: argument 2 is a REAL",
		`SELECT regexp_like('a')`: "wrong number of arguments",
	})
}

// The cache of compiled patterns is emptied when full, not grown.
func TestManyPatterns(t *testing.T) {
	db := sqltest.Open(t, regexp.Option())
	for i := range 1000 {
		var ok bool
		if err := db.QueryRow(`SELECT ? REGEXP ?`, fmt.Sprint(i), fmt.Sprintf("^%d$", i)).Scan(&ok); err != nil || !ok {
			t.Fatalf("pattern %d: %v %v", i, ok, err)
		}
	}
}

func Example() {
	c, err := sqlite.NewConnector(":memory:", regexp.Option())
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	var match bool
	var first, swapped string
	err = db.QueryRow(`SELECT 'v1.22.3' REGEXP '^v\d+\.\d+\.\d+$',
		regexp_substr('order 66 and 99', '\d+'),
		regexp_replace('Doe, Jane', '(\w+), (\w+)', '$2 $1')`).Scan(&match, &first, &swapped)
	if err != nil {
		panic(err)
	}
	fmt.Println(match, first, swapped)
	// Output: true 66 Jane Doe
}

// TestReplaceTooLong: a result longer than SQLite takes fails before it is
// built, however small the arguments: here 9 bytes would give 16.
func TestReplaceTooLong(t *testing.T) {
	regexp.SetMaxLength(t, 15)
	db := sqltest.Open(t, regexp.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT regexp_replace('aaaa', 'a', 'bbb')`:              "bbbbbbbbbbbb",
		`SELECT regexp_replace('aaaa', 'a', '$0$0')`:             "aaaaaaaa",
		`SELECT regexp_replace('aaaaaaaaaaaaaaaaaaaa', 'a', '')`: "",
	})
	sqltest.Fails(t, db, map[string]string{
		`SELECT regexp_replace('aaaa', 'a', 'bbbb')`:          "regexp_replace: the result may be longer than 15 bytes",
		`SELECT regexp_replace('aaaa', '(a)', '$1$1$1$1')`:    "regexp_replace: the result may be longer than 15 bytes",
		`SELECT regexp_replace('aaaaaaaaaaaaaaaa', 'x', 'y')`: "regexp_replace: the result may be longer than 15 bytes",
	})
}

// TestFitsIsABound: fits never lets a result longer than the limit through,
// and lets through one exactly as long when the template names no group
// shorter than its match.
func TestFitsIsABound(t *testing.T) {
	for _, c := range []struct {
		re, s, repl string
		exact       bool
	}{
		{`a*`, "baaacada", "<$0>", true},
		{`x*`, "abc", "-", true},
		{`\b`, "one two", "|", true},
		{`^|$`, "mid", "#", true},
		{`(a)|(b)`, "abcab", "[$1${2}]", false},
		{`(?P<w>\w+)`, "hi there", "${w}!$$", true},
		{``, "héllo", ".", true},
		{`l+`, "hello world", "L", true},
		{`(?P<x>a)|(?P<x>b)`, "abba", "<${x}>", true},
		{`(a)b`, "abab", "$1$1$1", false},
		{`a`, "aaaa", "${missing}$9$$", true},
		{`(a)`, "aaaa", "$1x${1}x", true},
		{`(a)`, "aaaa", "${1", true},
	} {
		re := goregexp.MustCompile(c.re)
		n := len(re.ReplaceAllString(c.s, c.repl))
		if n > 0 && regexp.Fits(re, c.s, c.repl, n-1) {
			t.Errorf("%q on %q with %q: %d bytes fit in %d", c.re, c.s, c.repl, n, n-1)
		}
		if c.exact && !regexp.Fits(re, c.s, c.repl, n) {
			t.Errorf("%q on %q with %q: %d bytes do not fit in %d", c.re, c.s, c.repl, n, n)
		}
	}
}

// TestReplaceDuplicateName: a name two groups have is the one that took part
// in the match, as in Go, also when the result may be near the limit.
func TestReplaceDuplicateName(t *testing.T) {
	regexp.SetMaxLength(t, 15)
	db := sqltest.Open(t, regexp.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT regexp_replace('bzzzzzzzzz', '(?P<x>a)|(?P<x>b)', '${x}')`: "bzzzzzzzzz",
	})
}

// TestReplaceManyGroups: a pattern of many groups on a long text, with a
// template that names none of them, gives its result at the real limit.
func TestReplaceManyGroups(t *testing.T) {
	db := sqltest.Open(t, regexp.Option())
	var got string
	pattern := "a" + strings.Repeat("()", 256)
	repl := strings.Repeat("${missing}", 100)
	if err := db.QueryRow(`SELECT regexp_replace(?, ?, ?)`, strings.Repeat("a", 1<<12), pattern, repl).Scan(&got); err != nil || got != "" {
		t.Fatalf("%q, %v", got, err)
	}
}
