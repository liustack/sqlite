// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && (amd64 || arm64))

package csqlite

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestSQLLogic runs SQL Logic Test, SQLite's suite of about 600 scripts and
// 6 million SQL statements with the results they must give, on the engine,
// as the suite's run-all.tcl runs its C runner (sqllogictest.c with
// slt_sqlite.c, in verify mode, -parameter integrity_check;optimizer=0 and
// optimizer=-1): each script twice, on a new database file each time, the
// second time with the optimizations of the query planner turned off,
// statements through sqlite3_exec and each that worked followed by PRAGMA
// integrity_check, queries through one prepared statement, each value
// turned to text as that runner does, results sorted and hashed with MD5 as
// the script says. A statement that gives the wrong outcome or leaves a
// database that integrity_check finds wrong, a query whose result or hash is
// not the one in the script, and two queries of a script with the same
// label and different results all fail the script. The runner's check for
// leaked memory is left out, as it reads a count of all the connections.
//
// Its settings:
//
//	SLT=<dir>          the test folder of the suite, or the test skips
//	SLT_REQUIRED=1     fail instead of skipping when SLT is unset
//	SLT_SLICE=i/n      run only the scripts k with k%n == i, in name order
//	SLT_FILE=<path>    run only the scripts whose path under SLT has this
//	                   prefix, as index/between or select1.test
func TestSQLLogic(t *testing.T) {
	dir := os.Getenv("SLT")
	if dir == "" {
		if os.Getenv("SLT_REQUIRED") != "" {
			t.Fatal("SLT_REQUIRED is set and SLT is not: scripts/slt.sh gets the suite")
		}
		t.Skip("SLT is not set: scripts/slt.sh gets the suite")
	}
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".test") {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	rem, mod := 0, 1
	if s := os.Getenv("SLT_SLICE"); s != "" {
		if _, err := fmt.Sscanf(s, "%d/%d", &rem, &mod); err != nil || mod <= 0 || rem < 0 || rem >= mod {
			t.Fatalf("SLT_SLICE=%q: want i/n with 0 <= i < n", s)
		}
	}
	only := os.Getenv("SLT_FILE")
	n := 0
	for k, f := range files {
		if k%mod != rem || !strings.HasPrefix(f, only) {
			continue
		}
		n++
		t.Run(f, func(t *testing.T) {
			t.Parallel()
			script, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				t.Fatal(err)
			}
			// As the suite's run-all.tcl: once as built, once with every
			// optimization of the query planner turned off.
			for _, opt := range []uint32{0, math.MaxUint32} {
				runSLT(t, fmt.Sprintf("%s optimizer=%d", f, int32(opt)), script, opt)
			}
		})
	}
	if n == 0 {
		t.Fatalf("no script in %s: check SLT_FILE and SLT_SLICE", dir)
	}
}

// sltScript reads a script as sqllogictest.c's Script does: line by line,
// a line of white space read as empty, a line that starts with # skipped.
type sltScript struct {
	raw   []string // the lines of the file, without \n and a \r before it
	next  int      // index in raw of the line after the current one
	line  string   // the current line
	nLine int      // its number, from 1
	end   bool     // the current line is past the last one
	// lastNL says the file ends with a newline: when it does not, its last
	// line is never blank to nextIsBlank, which looks for a newline.
	lastNL bool
}

func newSLTScript(b []byte) *sltScript {
	s := string(b)
	p := &sltScript{lastNL: strings.HasSuffix(s, "\n")}
	s = strings.TrimSuffix(s, "\n")
	if len(b) > 0 {
		p.raw = strings.Split(s, "\n")
	}
	return p
}

func isCSpace(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }

// nextLine is sqllogictest.c's: it goes to the next line that is not a
// comment, and says false at the end of the file.
func (p *sltScript) nextLine() bool {
	for {
		if p.next >= len(p.raw) {
			p.end, p.line = true, ""
			return false
		}
		l := strings.TrimSuffix(p.raw[p.next], "\r")
		p.next++
		p.nLine++
		blank := true
		for i := 0; i < len(l); i++ {
			if !isCSpace(l[i]) {
				blank = false
				break
			}
		}
		if blank {
			l = ""
		}
		p.line = l
		if !strings.HasPrefix(l, "#") {
			return true
		}
	}
}

// nextIsBlank says the line after the current one, comment or not, is all
// white space, or there is none.
func (p *sltScript) nextIsBlank() bool {
	if p.next >= len(p.raw) {
		return true
	}
	l := p.raw[p.next]
	for i := 0; i < len(l); i++ {
		if !isCSpace(l[i]) {
			return false
		}
	}
	return p.next < len(p.raw)-1 || p.lastNL
}

// findStartOfNextRecord skips the rest of the current record and the empty
// lines after it.
func (p *sltScript) findStartOfNextRecord() bool {
	// sqllogictest.c tests the offset of the current line: past the first
	// line of the file, or at its end.
	if p.next-1 > 0 || p.end {
		for p.line != "" && !p.end {
			p.nextLine()
		}
	} else {
		p.nextLine()
	}
	for p.line == "" && !p.end {
		p.nextLine()
	}
	return !p.end
}

// tokens are the first four words of the current line, each cut to 199
// bytes, "" for those missing.
func (p *sltScript) tokens() [4]string {
	var tok [4]string
	for i, f := range strings.FieldsFunc(p.line, func(r rune) bool { return r < 0x80 && isCSpace(byte(r)) }) {
		if i == len(tok) {
			break
		}
		if len(f) > 199 {
			f = f[:199]
		}
		tok[i] = f
	}
	return tok
}

// sltHashThreshold is sqllogictest.c's DEFAULT_HASH_THRESHOLD.
const sltHashThreshold = 8

// runSLT is the main loop of sqllogictest.c in verify mode, on the engine
// SQLite.
func runSLT(t *testing.T, name string, script []byte, opt uint32) {
	c, err := Open(filepath.Join(t.TempDir(), "test.db"), OpenReadWrite|OpenCreate, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Errorf("%s: close: %v", name, err)
		}
	}()
	c.Exec("PRAGMA synchronous=OFF")
	testControl(c.sp, testctrlOptimizations, c.db, uintptr(opt))

	const maxErrors = 20
	nErr := 0
	fail := func(line int, format string, args ...any) {
		nErr++
		if nErr <= maxErrors {
			t.Errorf("%s:%d: %s", name, line, fmt.Sprintf(format, args...))
		}
	}
	labels := map[string]string{}
	checkLabel := func(label, hash string) bool {
		if len(label) > 23 {
			label = label[:23]
		}
		if h, ok := labels[label]; ok {
			return h == hash
		}
		labels[label] = hash
		return true
	}
	nStmt, nQuery, nSkip := 0, 0, 0
	hashThreshold := sltHashThreshold
	// zHash is sqllogictest.c's: the last hash taken, by a skipped query or
	// a run one, which a query compares its hash line with when the
	// threshold is below 0 and it has no label.
	var zHash string
	p := newSLTScript(script)
	for p.findStartOfNextRecord() {
		tok := p.tokens()
		start := p.nLine
		skip := false
		for tok[0] == "skipif" || tok[0] == "onlyif" {
			match := strings.EqualFold(tok[1], "SQLite")
			if tok[0] == "skipif" && match || tok[0] == "onlyif" && !match {
				skip = true
			}
			p.nextLine()
			tok = p.tokens()
			start = p.nLine
		}
		if skip {
			nSkip++
			// A query skipped with a label still has its hash checked
			// against the other queries of that label.
			if tok[0] != "query" || tok[3] == "" {
				continue
			}
			for !p.nextIsBlank() && p.nextLine() && p.line != "----" {
			}
			if p.line == "----" {
				p.nextLine()
			}
			if p.line == "" {
				continue
			}
			var hash string
			var nv int
			if n, _ := fmt.Sscanf(p.line, "%d values hashing to %32s", &nv, &hash); n != 2 {
				h := md5.New()
				h.Write([]byte(p.line + "\n"))
				for !p.nextIsBlank() && p.nextLine() {
					h.Write([]byte(p.line + "\n"))
				}
				hash = hex.EncodeToString(h.Sum(nil))
			}
			zHash = hash
			if !checkLabel(tok[3], hash) {
				fail(start, "labeled result [%s] does not agree with previous values", tok[3])
			}
			continue
		}

		switch tok[0] {
		case "statement":
			nStmt++
			var sql []string
			for p.nextLine() && p.line != "" {
				sql = append(sql, p.line)
			}
			err := c.Exec(strings.Join(sql, "\n"))
			if err == nil {
				err = sltIntegrityCheck(c)
			}
			if err := c.brokenErr(); err != nil {
				t.Fatalf("%s:%d: %v", name, start, err)
			}
			switch tok[1] {
			case "ok":
				if err != nil {
					fail(start, "statement error: %v", err)
				}
			case "error":
				if err == nil {
					fail(start, "statement should have failed")
				}
			default:
				fail(start, "statement neither ok nor error")
			}

		case "query":
			nQuery++
			types := tok[1]
			bad := false
			for i := 0; i < len(types); i++ {
				if c := types[i]; c != 'T' && c != 'I' && c != 'R' {
					fail(start, "unknown type character %q", c)
					bad = true
					break
				}
			}
			if bad {
				continue
			}
			if types == "" {
				fail(start, "missing type string")
				return
			}
			var sql []string
			for !p.nextIsBlank() && p.nextLine() && p.line != "" && p.line != "----" {
				sql = append(sql, p.line)
			}
			res, err := sltQuery(c, strings.Join(sql, "\n"), types)
			if err != nil {
				fail(start, "query failed: %v", err)
				continue
			}
			switch tok[2] {
			case "", "nosort":
			case "rowsort":
				n := len(types)
				rows := make([][]string, len(res)/n)
				for i := range rows {
					rows[i] = res[i*n : i*n+n]
				}
				sort.Slice(rows, func(a, b int) bool {
					for i := range n {
						if rows[a][i] != rows[b][i] {
							return rows[a][i] < rows[b][i]
						}
					}
					return false
				})
				sorted := make([]string, 0, len(res))
				for _, r := range rows {
					sorted = append(sorted, r...)
				}
				res = sorted
			case "valuesort":
				sort.Strings(res)
			default:
				fail(start, "unknown sort method %q", tok[2])
			}
			label := tok[3]
			if len(label) > 20 {
				label = label[:20]
			}
			if label != "" || hashThreshold > 0 && len(res) > hashThreshold {
				h := md5.New()
				for _, v := range res {
					h.Write([]byte(v + "\n"))
				}
				sum := hex.EncodeToString(h.Sum(nil))
				zHash = fmt.Sprintf("%d values hashing to %s", len(res), sum)
				if label != "" && !checkLabel(label, sum) {
					fail(start, "labeled result [%s] does not agree with previous values", label)
				}
			}
			if p.line == "----" {
				p.nextLine()
			}
			if hashThreshold == 0 || len(res) <= hashThreshold {
				i := 0
				for ; i < len(res) && p.line != ""; i++ {
					if p.line != res[i] {
						fail(p.nLine, "wrong result: expected [%s] got [%s]", p.line, res[i])
					}
					p.nextLine()
				}
				if i < len(res) {
					fail(p.nLine, "too many result rows: expected %d got %d", i, len(res))
				} else if p.line != "" {
					for {
						p.nextLine()
						i++
						if p.line == "" {
							break
						}
					}
					fail(p.nLine, "too few result rows: expected %d got %d", i, len(res))
				}
			} else if p.line != zHash {
				fail(p.nLine, "wrong result hash: expected [%s] got [%s]", p.line, zHash)
			}

		case "hash-threshold":
			hashThreshold, _ = strconv.Atoi(tok[1])

		case "halt":
			return

		default:
			fail(start, "unknown record type %q", tok[0])
			return
		}
	}
	if nErr > maxErrors {
		t.Errorf("%s: %d errors, the first %d shown", name, nErr, maxErrors)
	}
	t.Logf("%d statements, %d queries, %d skipped", nStmt, nQuery, nSkip)
}

// sltQuery is slt_sqlite.c's sqliteQuery: the first statement of sql, each
// value of each row as text, by the type of its column: NULL as "NULL", T
// as its text up to a NUL ("(empty)" for none, a byte outside ' ' to '~' as
// '@'), I as sqlite3_column_int with %d, R as sqlite3_column_double with
// SQLite's %.3f.
func sltQuery(c *Conn, sql, types string) ([]string, error) {
	s, _, err := c.Prepare(sql)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("no statement")
	}
	defer s.Finalize()
	if n := s.ColumnCount(); n != len(types) {
		return nil, fmt.Errorf("wrong number of result columns: expected %d but got %d", len(types), n)
	}
	var res []string
	for {
		ok, err := s.Step()
		if err := c.brokenErr(); err != nil {
			return nil, err
		}
		if !ok || err != nil {
			// sqlite3_step's error ends the rows, as in slt_sqlite.c.
			return res, nil
		}
		for i := 0; i < len(types); i++ {
			if s.ColumnType(i) == TypeNull {
				res = append(res, "NULL")
				continue
			}
			switch types[i] {
			case 'T':
				v, err := s.ColumnText(i)
				if err != nil {
					return nil, err
				}
				if j := strings.IndexByte(v, 0); j >= 0 {
					v = v[:j]
				}
				if v == "" {
					v = "(empty)"
				}
				b := []byte(v)
				for j, ch := range b {
					if ch < ' ' || ch > '~' {
						b[j] = '@'
					}
				}
				res = append(res, string(b))
			case 'I':
				res = append(res, strconv.Itoa(int(int32(s.ColumnInt64(i)))))
			case 'R':
				res = append(res, c.sqlitePrintf3f(s.ColumnDouble(i)))
			}
		}
	}
}

// sltIntegrityCheck is the check slt_sqlite.c runs after each statement that
// worked: the first row of PRAGMA integrity_check must be "ok".
func sltIntegrityCheck(c *Conn) error {
	s, _, err := c.Prepare("PRAGMA integrity_check;")
	if err != nil {
		return fmt.Errorf("cannot prepare integrity_check: %w", err)
	}
	defer s.Finalize()
	ok, err := s.Step()
	if err != nil || !ok {
		return fmt.Errorf("cannot run integrity_check: %v", err)
	}
	v, err := s.ColumnText(0)
	if err != nil {
		return err
	}
	if s.ColumnType(0) == TypeNull || v != "ok" {
		return fmt.Errorf("integrity_check returns: %s", v)
	}
	return nil
}

// sqlitePrintf3f is sqlite3_snprintf(200, buf, "%.3f", r).
func (c *Conn) sqlitePrintf3f(r float64) string {
	a := c.args()
	buf := a.space(200)
	f := a.str("%.3f")
	va := a.variadic(uintptr(math.Float64bits(r)))
	sqlite3_snprintf(a.top, 200, buf, f, va)
	return cstring(buf)
}
