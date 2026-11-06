// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package csv_test

import (
	"database/sql"
	gocsv "encoding/csv"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/liustack/sqlite/ext/csv"
	"github.com/liustack/sqlite/ext/internal/sqltest"
)

func create(t *testing.T, db *sql.DB, name, args string) {
	t.Helper()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE temp.` + name + ` USING csv(` + args + `)`); err != nil {
		t.Fatalf("csv(%s): %v", args, err)
	}
}

// The results are those of SQLite's csv.c, loaded into the sqlite3 shell.
func TestCSV(t *testing.T) {
	db := sqltest.Open(t, csv.Option())
	create(t, db, "plain", "data='a,b,c\n1,2,3\n4,5'")
	create(t, db, "head", "data='a,b,c\n1,2,3\n4,5', header=yes")
	create(t, db, "cols", "data='x,y\n1,2,3,4', columns=3")
	create(t, db, "colshead", "data='x,y\n1,2,3,4', columns=3, header")
	create(t, db, "quoted", "data='\"a,b\",\"say \"\"hi\"\"\",\"two\nlines\"\n,,'")
	create(t, db, "schema", `data="p,q" , schema='CREATE TABLE x(first, second, third)'`)
	create(t, db, "weird", `data='"we""ird",b', header=on`)
	create(t, db, "blank", "data='a\n\nb'")
	create(t, db, "crlf", "data='a,b\r\n\"c\r\nd\",e\r\n'")
	create(t, db, "bom", "data='\xef\xbb\xbfa,b\n'")
	create(t, db, "empty", "data=''")
	create(t, db, "emptyhead", "data='', header")
	create(t, db, "skip", "data='h1,h2\n1,2', schema='CREATE TABLE x(a,b)', columns=2, header=yes")
	names := func(tab string) string { return `SELECT group_concat(name) FROM pragma_table_info('` + tab + `')` }
	sqltest.Want(t, db, map[string]string{
		`SELECT group_concat(quote(c0)||quote(c1)||quote(c2), ';') FROM plain`: `'a''b''c';'1''2''3';'4''5'NULL`,
		names("head"): "a,b,c",
		`SELECT group_concat(rowid||quote(a)||quote(b)||quote(c), ';') FROM head`: `1'1''2''3';2'4''5'NULL`,
		`SELECT group_concat(quote(c0)||quote(c1)||quote(c2), ';') FROM cols`:     `'x''y'NULL;'1''2''3'`,
		names("colshead"): "x,y,c3",
		`SELECT group_concat(quote(x)||quote(y)||quote(c3), ';') FROM colshead`:           `'1''2''3'`,
		`SELECT group_concat(quote(c0)||quote(c1)||quote(c2), ';') FROM quoted`:           "'a,b''say \"hi\"''two\nlines';''''NULL",
		`SELECT group_concat(quote(first)||quote(second)||quote(third), ';') FROM schema`: `'p''q'NULL`,
		names("weird"): `we"ird,b`,
		`SELECT count(*) || ' ' || group_concat(quote(c0)) FROM blank`: `3 'a','','b'`,
		`SELECT group_concat(c0 || '|' || c1, ';') FROM crlf`:          "a|b;c\r\nd|e",
		`SELECT c0 FROM bom`:                   "a",
		`SELECT count(*) FROM bom a, bom b`:    "1", // a scan again skips the BOM too
		`SELECT count(*) FROM empty`:           "0",
		names("empty"):                         "c0",
		names("emptyhead"):                     "(NULL)",
		`SELECT group_concat(a||b) FROM skip`:  "12", // csv.c gives the header as a row here
		`SELECT typeof(c0) FROM plain LIMIT 1`: "text",
	})
	sqltest.Fails(t, db, map[string]string{
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', nonsense)`:                     "bad parameter: 'nonsense'",
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', filename='b')`:                 "must specify either filename= or data= but not both",
		`CREATE VIRTUAL TABLE temp.e USING csv(header)`:                                 "must specify either filename= or data= but not both",
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', data='b')`:                     "more than one 'data' parameter",
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', header, header=no)`:            "more than one 'header' parameter",
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', columns=0)`:                    "column= value must be positive",
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', columns=2001)`:                 "column= value too big, max 2000",
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', columns=99999999999999999999)`: "column= value too big",
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', header=maybe)`:                 "bad parameter: 'header=maybe'",
		`CREATE VIRTUAL TABLE temp.e USING csv(filename='/no/such/file.csv')`:           "cannot open '/no/such/file.csv' for reading",
		`CREATE VIRTUAL TABLE temp.e USING csv(data='a', schema='CREATE TABLE x(')`:     "incomplete input",
		`INSERT INTO plain VALUES (1, 2, 3)`:                                            "may not be modified",
	})
}

// Where csv.c reads a malformed file on, the statement fails.
func TestMalformed(t *testing.T) {
	db := sqltest.Open(t, csv.Option())
	create(t, db, "stray", "data='a,b\n\"x\"y,z\n', columns=2")
	create(t, db, "open", "data='a,b\n\"x,y\n', columns=2")
	sqltest.Fails(t, db, map[string]string{
		`SELECT count(*) FROM stray`:                         `line 2: unescaped " character`,
		`SELECT count(*) FROM open`:                          `line 2: unterminated "-quoted field`,
		`CREATE VIRTUAL TABLE temp.h USING csv(data='"a"b')`: `unescaped " character`,
	})
	// The rows before the bad one were given.
	rows, err := db.Query(`SELECT c0 FROM stray`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		got = append(got, s)
	}
	if rows.Err() == nil || !slices.Equal(got, []string{"a"}) {
		t.Errorf("rows %q, error %v", got, rows.Err())
	}
	rows.Close()
}

// A table may not be read from a view or a trigger.
func TestDirectOnly(t *testing.T) {
	db := sqltest.Open(t, csv.Option())
	create(t, db, "t", "data='1\n2'")
	if _, err := db.Exec(`CREATE TEMP VIEW v AS SELECT * FROM t`); err != nil {
		t.Fatal(err)
	}
	if got := scanOne(t, db, `SELECT count(*) FROM v`); got != "2" {
		t.Errorf("a temp view: %s", got)
	}
	if _, err := db.Exec(`CREATE TABLE m(x); CREATE VIRTUAL TABLE main.c USING csv(data='1'); CREATE VIEW mv AS SELECT * FROM c`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT * FROM mv`); err == nil || !strings.Contains(err.Error(), "unsafe use of virtual table") {
		t.Errorf("a view in main: %v", err)
	}
}

func scanOne(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(q).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

// A file of 100k rows written by encoding/csv reads as encoding/csv reads it.
func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 1))
	parts := []string{"a", "bc", ",", `"`, "\n", " ", "é", "0.5", "", "日本"}
	w := gocsv.NewWriter(f)
	w.Write([]string{"id", "x", "y", "z", "w"})
	for i := range 100_000 {
		rec := []string{fmt.Sprint(i)}
		for range 4 {
			var b strings.Builder
			for range r.IntN(4) {
				b.WriteString(parts[r.IntN(len(parts))])
			}
			rec = append(rec, b.String())
		}
		w.Write(rec)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	want, err := gocsv.NewReader(in).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	db := sqltest.Open(t, csv.Option())
	create(t, db, "big", "filename='"+strings.ReplaceAll(path, "'", "''")+"', header=yes")
	rows, err := db.Query(`SELECT rowid, id, x, y, z, w FROM big`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var rowid int
		got := make([]string, 5)
		if err := rows.Scan(&rowid, &got[0], &got[1], &got[2], &got[3], &got[4]); err != nil {
			t.Fatal(err)
		}
		n++
		if rowid != n || !slices.Equal(got, want[n]) {
			t.Fatalf("row %d: rowid %d, %q, want %q", n, rowid, got, want[n])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != len(want)-1 {
		t.Errorf("%d rows, want %d", n, len(want)-1)
	}
}

// TestLongField: a field longer than SQLite takes fails as it is read, before
// it fills the memory: a count(*) reads every field without giving one to
// SQLite. The first row of a table with no columns= fails the same way.
func TestLongField(t *testing.T) {
	csv.SetMaxLength(t, 10)
	db := sqltest.Open(t, csv.Option())
	create(t, db, "plain", "data='a,b\n0123456789,x\nlonger than ten,y\n', columns=2")
	create(t, db, "quoted", "data='a,b\n\"0123456789\",x\n\"longer, than ten\",y\n', columns=2")
	sqltest.Fails(t, db, map[string]string{
		`SELECT count(*) FROM plain`:                                        `line 3: a field longer than 10 bytes`,
		`SELECT count(*) FROM quoted`:                                       `line 3: a field longer than 10 bytes`,
		`CREATE VIRTUAL TABLE temp.h USING csv(data='longer than ten,b')`:   `line 1: a field longer than 10 bytes`,
		`CREATE VIRTUAL TABLE temp.h USING csv(data='a,b,c,d,e,f', header)`: `the header is longer than 10 bytes`,
	})
}

// TestManyColumns: a first row of more fields than a table takes fails once
// it has one too many, not after it was read whole.
func TestManyColumns(t *testing.T) {
	db := sqltest.Open(t, csv.Option())
	row := strings.Repeat("a,", 2000) + "a"
	sqltest.Fails(t, db, map[string]string{
		`CREATE VIRTUAL TABLE temp.h USING csv(data='` + row + `')`:         `the first row has more than 2000 fields`,
		`CREATE VIRTUAL TABLE temp.h USING csv(data='` + row + `', header)`: `the first row has more than 2000 fields`,
	})
}

// TestLongestField: a field of maxLength bytes is read, quoted too, and with
// CRLF after its closing quote.
func TestLongestField(t *testing.T) {
	csv.SetMaxLength(t, 10)
	db := sqltest.Open(t, csv.Option())
	create(t, db, "crlf", "data='\"0123456789\"\r\n0123456789\r\n', columns=1")
	sqltest.Want(t, db, map[string]string{`SELECT group_concat(c0, '|') FROM crlf`: "0123456789|0123456789"})
}
