// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package csv gives SQLite the csv virtual table of SQLite's csv extension
// (ext/misc/csv.c), ported to Go:
//
//	sqlite.NewConnector(dsn, csv.Option())
//
// Then:
//
//	CREATE VIRTUAL TABLE temp.t USING csv(filename='data.csv', header=yes);
//	SELECT * FROM t;
//
// The arguments:
//
//	filename=FILE   read the file FILE (relative to the working directory)
//	data=TEXT       or read TEXT itself
//	header=YES|NO   the first row names the columns and is not a row
//	                (default no: the columns are c0, c1, ...)
//	columns=N       the rows have N columns (default: as many as the first)
//	schema=SQL      the CREATE TABLE statement of the columns instead
//	                (with header=yes the first row is still skipped, which
//	                csv.c does not do when columns= is given too)
//
// Every value is TEXT, a missing field is NULL, and fields past the last
// column are left out. The rowid counts the rows from 1. The text is RFC 4180:
// fields separated by commas, rows by LF or CRLF, a field in double quotes
// may hold commas, line breaks and doubled double quotes. A UTF-8 BOM at the
// start is skipped.
//
// Where SQLite's csv.c reads a malformed file on, this one fails the
// statement: a double quote inside a quoted field that is not doubled, or a
// quoted field that the file ends in. A table never reads a file named by
// the schema of a database when it is used from a trigger or a view: it is
// for SQL the program runs (SQLite's DIRECTONLY). Keep such tables in temp,
// as SQLite advises.
package csv

import (
	"bufio"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/internal/arg"
)

// Option adds the csv module to a connector.
func Option() sqlite.Option {
	return sqlite.WithModule("csv", connect, sqlite.ModuleDirectOnly)
}

// maxColumns is SQLITE_MAX_COLUMN, the most columns a table may have.
const maxColumns = 2000

// maxLength is the most bytes of a field, and of the header SQLite is
// given: SQLite takes no longer string, so a field fails as it gets longer,
// before it fills the memory. A variable for the tests.
var maxLength = arg.MaxLength

type table struct {
	filename string // or
	data     string
	isFile   bool
	start    int64 // where the rows start, after the header
	startLn  int   // the line they start on
	nCol     int
	schema   string
}

const eof = -1

// reader reads the fields of CSV text as csv.c's csv_read_one_field does.
type reader struct {
	in       *bufio.Reader
	off      int64 // bytes read
	line     int   // the line being read, from 1
	notFirst bool  // a field has been read: no BOM to skip
	term     int   // what ended the last field: ',', '\n' or eof
	buf      []byte
	err      error // of the input
}

func newReader(in io.Reader, line int, first bool) *reader {
	return &reader{in: bufio.NewReaderSize(in, 64<<10), line: line, notFirst: !first}
}

func (r *reader) getc() int {
	b, err := r.in.ReadByte()
	if err != nil {
		if err != io.EOF {
			r.err = err
		}
		return eof
	}
	r.off++
	return int(b)
}

// field reads one field. ok is false at the end of the input, where no
// field starts.
func (r *reader) field() (f []byte, ok bool, err error) {
	r.buf = r.buf[:0]
	c := r.getc()
	if c == eof {
		r.term = eof
		return nil, false, r.err
	}
	if c == '"' {
		startLine := r.line
		pc, ppc := 0, 0
		for {
			c = r.getc()
			if c <= '"' || pc == '"' {
				if c == '\n' {
					r.line++
				}
				if c == '"' && pc == '"' {
					// A doubled quote: the first is kept.
					pc = 0
					continue
				}
				if c == ',' && pc == '"' || c == '\n' && pc == '"' ||
					c == '\n' && pc == '\r' && ppc == '"' || c == eof && pc == '"' {
					// The closing quote, and a CR after it.
					n := len(r.buf) - 1
					for r.buf[n] != '"' {
						n--
					}
					r.buf = r.buf[:n]
					r.term = c
					break
				}
				if pc == '"' && c != '\r' {
					return nil, false, fmt.Errorf("csv: line %d: unescaped \" character", r.line)
				}
				if c == eof {
					if r.err != nil {
						return nil, false, r.err
					}
					return nil, false, fmt.Errorf("csv: line %d: unterminated \"-quoted field", startLine)
				}
			}
			r.buf = append(r.buf, byte(c))
			if len(r.buf) > maxLength+2 { // the closing quote and a CR may follow
				return nil, false, r.tooLong()
			}
			ppc, pc = pc, c
		}
	} else {
		if c == 0xef && !r.notFirst {
			// A UTF-8 BOM at the start is skipped.
			r.buf = append(r.buf, byte(c))
			if c = r.getc(); c == 0xbb {
				r.buf = append(r.buf, byte(c))
				if c = r.getc(); c == 0xbf {
					r.notFirst = true
					return r.field()
				}
			}
		}
		for c != eof && c != ',' && c != '\n' {
			r.buf = append(r.buf, byte(c))
			if len(r.buf) > maxLength+1 { // a CR may follow, which a LF after it drops
				return nil, false, r.tooLong()
			}
			c = r.getc()
		}
		if c == '\n' {
			r.line++
			if n := len(r.buf); n > 0 && r.buf[n-1] == '\r' {
				r.buf = r.buf[:n-1]
			}
		}
		r.term = c
	}
	if len(r.buf) > maxLength {
		return nil, false, r.tooLong()
	}
	r.notFirst = true
	return r.buf, true, r.err
}

// errManyColumns is the error of a first row of more fields than a table
// takes columns.
var errManyColumns = fmt.Errorf("csv: the first row has more than %d fields", maxColumns)

// countColumns counts the fields of the first row in n, and stops it once it
// has more than a table takes.
func countColumns(n *int) func([]byte) error {
	return func([]byte) error {
		if *n == maxColumns {
			return errManyColumns
		}
		*n++
		return nil
	}
}

// tooLong is the error of a field longer than maxLength.
func (r *reader) tooLong() error {
	return fmt.Errorf("csv: line %d: a field longer than %d bytes", r.line, maxLength)
}

// row reads the fields of a row, as many as fit in vals, and says which it
// set. ok is false at the end of the input.
func (r *reader) row(vals []string, set []bool) (bool, error) {
	i := 0
	for {
		f, ok, err := r.field()
		if err != nil {
			return false, err
		}
		if !ok {
			if i == 0 {
				return false, nil
			}
			break
		}
		if i < len(vals) {
			vals[i], set[i] = string(f), true
			i++
		}
		if r.term != ',' {
			break
		}
	}
	for ; i < len(vals); i++ {
		vals[i], set[i] = "", false
	}
	return true, nil
}

func connect(args []string) (sqlite.VTable, error) {
	var filename, data, schema *string
	header := -1
	nCol := -99
	for _, a := range args {
		if name, v, ok := stringParam(a); ok {
			p := map[string]**string{"filename": &filename, "data": &data, "schema": &schema}[name]
			if *p != nil {
				return nil, fmt.Errorf("csv: more than one '%s' parameter", name)
			}
			*p = &v
			continue
		}
		if b, ok := boolParam("header", a); ok {
			if header >= 0 {
				return nil, errors.New("csv: more than one 'header' parameter")
			}
			header = b
			continue
		}
		if v, ok := param("columns", a); ok {
			if nCol > 0 {
				return nil, errors.New("csv: more than one 'columns' parameter")
			}
			nCol = atoi(v)
			switch {
			case nCol <= 0:
				return nil, errors.New("csv: column= value must be positive")
			case nCol > maxColumns:
				return nil, fmt.Errorf("csv: column= value too big, max %d", maxColumns)
			}
			continue
		}
		return nil, fmt.Errorf("csv: bad parameter: '%s'", a)
	}
	if (filename == nil) == (data == nil) {
		return nil, errors.New("csv: must specify either filename= or data= but not both")
	}
	t := &table{isFile: filename != nil, nCol: nCol}
	if t.isFile {
		t.filename = *filename
	} else {
		t.data = *data
	}

	var r *reader
	if nCol <= 0 || header == 1 {
		in, err := t.open()
		if err != nil {
			return nil, err
		}
		defer in.Close()
		r = newReader(in, 1, true)
	}
	// readFirst reads the first row, and calls each with its fields, nil
	// for a field the input ends before. An error of each stops it.
	readFirst := func(each func(f []byte) error) error {
		for {
			f, ok, err := r.field()
			if err != nil {
				return err
			}
			if each != nil {
				if !ok {
					f = nil
				}
				if err := each(f); err != nil {
					return err
				}
			}
			if r.term != ',' {
				return nil
			}
		}
	}
	if schema == nil {
		var b strings.Builder
		b.WriteString("CREATE TABLE x(")
		sep := ""
		if nCol < 0 && header < 1 {
			nCol = 0
			if err := readFirst(countColumns(&nCol)); err != nil {
				return nil, err
			}
		}
		if nCol > 0 && header < 1 {
			for i := range nCol {
				fmt.Fprintf(&b, "%sc%d TEXT", sep, i)
				sep = ","
			}
		} else {
			i := 0
			err := readFirst(func(f []byte) error {
				if nCol < 0 && i == maxColumns {
					return errManyColumns
				}
				if nCol > 0 && i < nCol || nCol < 0 {
					name := "(NULL)" // csv.c's %w of no field
					if f != nil {
						name = string(f)
					}
					fmt.Fprintf(&b, `%s"%s" TEXT`, sep, strings.ReplaceAll(name, `"`, `""`))
					sep = ","
					i++
					if b.Len() > maxLength {
						return fmt.Errorf("csv: the header is longer than %d bytes", maxLength)
					}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if nCol < 0 {
				nCol = i
			} else {
				for i < nCol {
					i++
					fmt.Fprintf(&b, "%sc%d TEXT", sep, i)
					sep = ","
				}
			}
		}
		b.WriteString(")")
		t.schema = b.String()
	} else {
		t.schema = *schema
		if nCol < 0 {
			nCol = 0
			if err := readFirst(countColumns(&nCol)); err != nil {
				return nil, err
			}
		} else if header == 1 {
			// csv.c reads nothing here and so gives the header as a row.
			if err := readFirst(nil); err != nil {
				return nil, err
			}
		}
	}
	t.nCol = nCol
	if header == 1 {
		t.start, t.startLn = r.off, r.line
	} else {
		t.startLn = 1
	}
	return t, nil
}

func (t *table) open() (io.ReadSeekCloser, error) {
	if !t.isFile {
		return nopCloser{strings.NewReader(t.data)}, nil
	}
	f, err := os.Open(t.filename)
	if err != nil {
		return nil, fmt.Errorf("csv: cannot open '%s' for reading: %w", t.filename, err)
	}
	return f, nil
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func (t *table) Schema() string { return t.schema }

// BestIndex: a full scan, the only plan.
func (t *table) BestIndex(info *sqlite.IndexInfo) error {
	info.EstimatedCost = 1000000
	return nil
}

func (t *table) Disconnect() error { return nil }

func (t *table) Open() (sqlite.VCursor, error) {
	in, err := t.open()
	if err != nil {
		return nil, err
	}
	return &cursor{t: t, in: in, vals: make([]string, t.nCol), set: make([]bool, t.nCol)}, nil
}

type cursor struct {
	t     *table
	in    io.ReadSeekCloser
	r     *reader
	vals  []string
	set   []bool
	rowid int64 // -1 past the last row
}

func (c *cursor) Filter(int, string, []driver.Value) error {
	if _, err := c.in.Seek(c.t.start, io.SeekStart); err != nil {
		return err
	}
	c.r = newReader(c.in, c.t.startLn, c.t.start == 0)
	c.rowid = 0
	return c.Next()
}

func (c *cursor) Next() error {
	ok, err := c.r.row(c.vals, c.set)
	if err != nil {
		c.rowid = -1
		return err
	}
	if !ok {
		c.rowid = -1
		return nil
	}
	c.rowid++
	return nil
}

func (c *cursor) EOF() bool { return c.rowid < 0 }

func (c *cursor) Column(i int) (driver.Value, error) {
	if i >= 0 && i < len(c.vals) && c.set[i] {
		return c.vals[i], nil
	}
	return nil, nil
}

func (c *cursor) RowID() (int64, error) { return c.rowid, nil }
func (c *cursor) Close() error          { return c.in.Close() }

// isSpace is C's isspace.
func isSpace(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }

func skipSpace(s string) string {
	for len(s) > 0 && isSpace(s[0]) {
		s = s[1:]
	}
	return s
}

// param reads "TAG = VALUE", with spaces anywhere around the tokens, and
// gives VALUE.
func param(tag, s string) (string, bool) {
	s = skipSpace(s)
	if !strings.HasPrefix(s, tag) {
		return "", false
	}
	s = skipSpace(s[len(tag):])
	if !strings.HasPrefix(s, "=") {
		return "", false
	}
	return skipSpace(s[1:]), true
}

// stringParam reads filename=, data= or schema=, its value without the
// spaces after it and dequoted.
func stringParam(s string) (name, v string, ok bool) {
	for _, name := range []string{"filename", "data", "schema"} {
		if v, ok := param(name, s); ok {
			return name, dequote(strings.TrimRightFunc(v, func(r rune) bool { return r < 0x80 && isSpace(byte(r)) })), true
		}
	}
	return "", "", false
}

// dequote takes the quotes off 'x' or "x", and undoubles the quotes inside.
func dequote(s string) string {
	if len(s) < 2 || s[0] != '\'' && s[0] != '"' || s[len(s)-1] != s[0] {
		return s
	}
	q := s[:1]
	return strings.ReplaceAll(s[1:len(s)-1], q+q, q)
}

// boolParam reads "TAG" (true) or "TAG = yes|no|on|off|true|false|1|0".
func boolParam(tag, s string) (int, bool) {
	s = skipSpace(s)
	if !strings.HasPrefix(s, tag) {
		return 0, false
	}
	s = skipSpace(s[len(tag):])
	if s == "" {
		return 1, true
	}
	if s[0] != '=' {
		return 0, false
	}
	switch v := skipSpace(s[1:]); {
	case strings.EqualFold(v, "yes"), strings.EqualFold(v, "on"), strings.EqualFold(v, "true"), v == "1":
		return 1, true
	case strings.EqualFold(v, "no"), strings.EqualFold(v, "off"), strings.EqualFold(v, "false"), v == "0":
		return 0, true
	}
	return 0, false
}

// atoi is C's atoi: the number at the start of s, 0 if none. A number too
// big for an int is maxColumns+1, too big either way.
func atoi(s string) int {
	s = skipSpace(s)
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.ParseInt(s[:end], 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			if s[0] == '-' {
				return -1
			}
			return maxColumns + 1
		}
		return 0
	}
	if n > maxColumns {
		return maxColumns + 1
	}
	return int(n)
}
