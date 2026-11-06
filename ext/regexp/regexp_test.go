// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package regexp_test

import (
	"database/sql"
	"fmt"
	goregexp "regexp"
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
		`SELECT regexp_replace('aaaa', 'a', 'bbbb')`:          "regexp_replace: the result is longer than 15 bytes",
		`SELECT regexp_replace('aaaa', '(a)', '$1$1$1$1')`:    "regexp_replace: the result is longer than 15 bytes",
		`SELECT regexp_replace('aaaaaaaaaaaaaaaa', 'x', 'y')`: "regexp_replace: the result is longer than 15 bytes",
	})
}

// TestReplaceExact: the result regexp_replace builds itself, when it may be
// too long, is ReplaceAllString's, empty matches and all.
func TestReplaceExact(t *testing.T) {
	for _, c := range []struct{ re, s, repl string }{
		{`a*`, "baaacada", "<$0>"},
		{`x*`, "abc", "-"},
		{`\b`, "one two", "|"},
		{`^|$`, "mid", "#"},
		{`(a)|(b)`, "abcab", "[$1${2}]"},
		{`(?P<w>\w+)`, "hi there", "${w}!$$"},
		{``, "héllo", "."},
		{`l+`, "hello world", "L"},
	} {
		re := goregexp.MustCompile(c.re)
		want := re.ReplaceAllString(c.s, c.repl)
		got, err := regexp.ReplaceExact(re, c.s, c.repl, 1<<20)
		if err != nil || got != want {
			t.Errorf("%q on %q with %q: %q, %v, want %q", c.re, c.s, c.repl, got, err, want)
		}
	}
}
