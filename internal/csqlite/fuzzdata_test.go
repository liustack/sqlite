// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFuzzData runs the cases of SQLite's fuzzcheck on the engine: the SQL
// scripts and database images of test/fuzzdata*.db in the source release of
// the SQLite the engine is built from, which go run scripts/fuzzdata.go
// downloads. It runs them as fuzzcheck does, with its limits and settings,
// where the engine allows it. A case fails when the process crashes, the
// engine still runs a minute after an interrupt, or a host callback panics.
// An SQL error is no failure: most cases are meant to give one.
//
// What fuzzcheck does that this does not:
//
//   - It stops a case with a progress handler, which the engine cannot call
//     into Go: a script with no database after 100000 steps of a statement,
//     a case with its own database after about 20000 steps or 10 seconds, an
//     oss-fuzz case after 10 seconds. This interrupts a statement after
//     FUZZDATA_STMT_MS milliseconds (200 by default), and ends a case with its
//     own database after 10 seconds.
//   - Its heap limits and its check for leaks need SQLITE_DEFAULT_MEMSTATUS,
//     which the engine turns off.
//   - It loads the extensions of ext/misc and the recover API, checks the
//     query invariants of fuzzinvariants.c, and installs an authorizer that
//     refuses ATTACH of a file and a long busy_timeout. Here the authorizer
//     is the driver's.
//   - Its files are in memory (its own VFS). Here a case with no database
//     image runs on a memory database, as fuzzcheck --native-vfs does, and a
//     case with one on a file of the engine's VFS in a temporary directory.
//     Every file a case names goes in that directory (confine), and stays
//     under fuzzcheck's 10,000,000 bytes: a damaged database can make SQLite
//     write a page at 100 GB, which Windows fills with zeros. A busy wait
//     on a file the case locked itself does not stop at an interrupt: one
//     that lasts a minute is reported as the engine not stopping.
//
// Its settings:
//
//	FUZZDATA=<dir>           the folder of fuzzdata*.db, or the test skips
//	FUZZDATA_REQUIRED=1      fail instead of skipping when FUZZDATA is unset
//	FUZZDATA_SLICE=i/n       run only the cases k with k%n == i
//	FUZZDATA_CASE=f:sql[:db] run one case, as fuzzdata3.db:1:17
//	FUZZDATA_STMT_MS=n       interrupt a statement after n milliseconds
//
// The case running when the process dies is in <FUZZDATA>/running.
func TestFuzzData(t *testing.T) {
	dir := os.Getenv("FUZZDATA")
	if dir == "" {
		if os.Getenv("FUZZDATA_REQUIRED") != "" {
			t.Fatal("FUZZDATA_REQUIRED is set and FUZZDATA is not: go run scripts/fuzzdata.go")
		}
		t.Skip("FUZZDATA is not set: go run scripts/fuzzdata.go downloads the cases")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "fuzzdata*.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no fuzzdata*.db in %s", dir)
	}
	sort.Strings(files)

	r := &fuzzRun{t: t, stmtLimit: 200 * time.Millisecond, grace: 60 * time.Second, mod: 1}
	if s := os.Getenv("FUZZDATA_STMT_MS"); s != "" {
		ms, err := strconv.Atoi(s)
		if err != nil || ms <= 0 {
			t.Fatalf("FUZZDATA_STMT_MS=%q: want a count of milliseconds", s)
		}
		r.stmtLimit = time.Duration(ms) * time.Millisecond
	}
	if s := os.Getenv("FUZZDATA_SLICE"); s != "" {
		if _, err := fmt.Sscanf(s, "%d/%d", &r.rem, &r.mod); err != nil || r.mod <= 0 || r.rem < 0 || r.rem >= r.mod {
			t.Fatalf("FUZZDATA_SLICE=%q: want i/n with 0 <= i < n", s)
		}
	}
	if s := os.Getenv("FUZZDATA_CASE"); s != "" {
		p := strings.Split(s, ":")
		if len(p) < 2 || len(p) > 3 {
			t.Fatalf("FUZZDATA_CASE=%q: want file:sqlid or file:sqlid:dbid", s)
		}
		r.onlyFile = p[0]
		if r.onlySQL, err = strconv.Atoi(p[1]); err != nil {
			t.Fatalf("FUZZDATA_CASE=%q: %v", s, err)
		}
		if len(p) == 3 {
			if r.onlyDB, err = strconv.Atoi(p[2]); err != nil {
				t.Fatalf("FUZZDATA_CASE=%q: %v", s, err)
			}
		}
	}

	// Writes of the case running, so that a crash can be traced to it.
	running, err := os.Create(filepath.Join(dir, "running"))
	if err != nil {
		t.Fatal(err)
	}
	r.running = running
	defer func() {
		running.Close()
		if !t.Failed() {
			os.Remove(running.Name())
		}
	}()

	defer func() {
		// The cases seeded SQLite's PRNG, which is global: the tests after
		// this one get it back as it was.
		testControl(r.anySP(), testctrlPrngSeed, 0, 0)
		sqlite3_randomness(r.anySP(), 0, 0)
		r.side.free()
	}()
	work := t.TempDir()
	t.Chdir(work)
	r.confine = confine(work)
	for _, f := range files {
		name := filepath.Base(f)
		if r.onlyFile != "" && r.onlyFile != name {
			continue
		}
		t.Run(name, func(t *testing.T) {
			r.t = t
			r.file(f)
		})
	}
	if r.total == 0 {
		t.Fatal("no case ran: check FUZZDATA_CASE and FUZZDATA_SLICE")
	}
}

// confine maps a file name outside dir to one in dir, so that the SQL of a
// case, which can name any file (ATTACH '/00'), writes nowhere else. A name
// in dir stays as it is.
func confine(dir string) func(string) string {
	roots := []string{dir}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		roots = append(roots, real)
	}
	return func(name string) string {
		// As the VFS will read it: against the working directory, which
		// is dir, and on Windows \00 on the current drive.
		p, err := filepath.Abs(name)
		if err != nil {
			panic(err)
		}
		for _, root := range roots {
			if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
				return name
			}
		}
		flat := strings.Map(func(c rune) rune {
			if c == '/' || c == '\\' || c == ':' {
				return '_'
			}
			return c
		}, p)
		return filepath.Join(dir, "outside"+flat)
	}
}

// fuzzRun runs the cases of the fuzzdata files one after another.
type fuzzRun struct {
	t                 *testing.T
	stmtLimit, grace  time.Duration
	rem, mod          int // FUZZDATA_SLICE
	onlyFile          string
	onlySQL, onlyDB   int
	running           *os.File
	side              *stack // anySP
	confine           func(string) string
	k                 int // the number of the next case, over all files
	id                string
	reported          bool // the case failed already
	total             int  // cases run, over all files
	cases, cut, stmts int  // of the file running
	prepared, rows    int
}

type fuzzBlob struct {
	id int
	b  []byte
}

// file runs the cases of one fuzzdata file: each SQL script on each database
// image (an empty database when there is none), or alone when the script
// carries its own database (a dbsqlfuzz case) or the file says oss-fuzz.
func (r *fuzzRun) file(path string) {
	t := r.t
	r.setRunning("reading " + filepath.Base(path))
	src, err := Open(path, OpenReadOnly, "")
	if err != nil {
		t.Fatal(err)
	}
	scripts := fuzzBlobs(t, src, "SELECT sqlid, sqltext FROM xsql")
	images := fuzzBlobs(t, src, "SELECT dbid, dbcontent FROM db")
	ossFuzz := false
	if s, _, err := src.Prepare("SELECT value FROM config WHERE name = 'oss-fuzz'"); err == nil {
		if ok, err := s.Step(); err != nil {
			t.Fatal(err)
		} else if ok {
			ossFuzz = s.ColumnInt64(0) != 0
		}
		s.Finalize()
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if len(images) == 0 {
		images = []fuzzBlob{{id: 1}}
	}
	// Only while the cases run: the fuzzdata file is not in the directory.
	testFiles.path, testFiles.maxSize = r.confine, mxFileSz
	defer func() { testFiles.path, testFiles.maxSize = nil, 0 }()
	name := filepath.Base(path)
	r.cases, r.cut, r.stmts, r.prepared, r.rows = 0, 0, 0, 0, 0
	start := time.Now()
	for _, sql := range scripts {
		if r.onlyFile != "" && r.onlySQL != sql.id {
			continue
		}
		if isDbSql(sql.b) {
			if r.take(fmt.Sprintf("%s:%d", name, sql.id)) {
				r.combined(sql.b)
			}
			continue
		}
		for _, db := range images {
			if r.onlyFile != "" && r.onlyDB != 0 && r.onlyDB != db.id {
				continue
			}
			if !r.take(fmt.Sprintf("%s:%d:%d", name, sql.id, db.id)) {
				continue
			}
			if ossFuzz {
				r.ossFuzz(sql.b)
			} else {
				r.plain(sql.b, db.b)
			}
		}
	}
	t.Logf("%d cases, %d statements (%d prepared, %d rows), %d interrupted after %v, in %v",
		r.cases, r.stmts, r.prepared, r.rows, r.cut, r.stmtLimit, time.Since(start).Round(time.Millisecond))
}

// take says whether the case of number r.k runs, and makes it the running one.
func (r *fuzzRun) take(id string) bool {
	k := r.k
	r.k++
	if k%r.mod != r.rem {
		return false
	}
	r.id = id
	r.reported = false
	r.cases++
	r.total++
	r.setRunning(id)
	return true
}

// setRunning writes what runs to <FUZZDATA>/running.
func (r *fuzzRun) setRunning(s string) {
	if _, err := r.running.WriteAt([]byte(fmt.Sprintf("%-60s\n", s)), 0); err != nil {
		r.t.Fatal(err)
	}
}

func fuzzBlobs(t *testing.T, c *Conn, sql string) []fuzzBlob {
	s, _, err := c.Prepare(sql)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		t.Fatal(err)
	}
	defer s.Finalize()
	var out []fuzzBlob
	for {
		ok, err := s.Step()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		b, err := s.ColumnBlob(1)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fuzzBlob{id: int(s.ColumnInt64(0)), b: b})
	}
}

// isDbSql is fuzzcheck's: the script starts with a database image.
func isDbSql(a []byte) bool {
	if len(a) > 4 && string(a[:4]) == "\n--\n" {
		return true
	}
	a = bytes.TrimLeft(a, " \t\n\v\f\r")
	var buf []byte
	for _, c := range a {
		if len(buf) == 8 {
			break
		}
		if isHex(c) {
			buf = append(buf, c)
		}
	}
	return string(buf) == "53514c69"
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func hexVal(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	}
	return c - 'a' + 10
}

// Constants of sqlite3.h the cases use.
const (
	limitLength      = 0
	limitExprDepth   = 3
	limitVdbeOp      = 5
	limitLikePattern = 8

	testctrlOptimizations     = 15
	testctrlInternalFunctions = 17
	testctrlPrngSeed          = 28

	dbconfigEnableFKey = 1002

	fcntlSizeLimit = 36

	deserializeFreeOnClose = 1
	deserializeResizeable  = 2

	sqliteInterrupt = 9
)

// The db_config settings of bytes 72 to 75 of a dbsqlfuzz image
// (fuzzcheck's aDbConfigSettings). 0x0400 is SQLITE_DBCONFIG_STRICT_AGGREGATE,
// which this SQLite does not have.
var dbConfigBits = []struct{ mask, op uint32 }{
	{0x0001, 1002}, // ENABLE_FKEY
	{0x0002, 1003}, // ENABLE_TRIGGER
	{0x0004, 1015}, // ENABLE_VIEW
	{0x0008, 1007}, // ENABLE_QPSG
	{0x0010, 1008}, // TRIGGER_EQP
	{0x0020, 1010}, // DEFENSIVE
	{0x0040, 1011}, // WRITABLE_SCHEMA
	{0x0080, 1012}, // LEGACY_ALTER_TABLE
	{0x0100, 1018}, // STMT_SCANSTATUS
	{0x0200, 1019}, // REVERSE_SCANORDER
	{0x0800, 1013}, // DQS_DML
	{0x1000, 1014}, // DQS_DDL
	{0x2000, 1017}, // TRUSTED_SCHEMA
}

// variadic puts vals in 8-byte slots on the shadow stack, as the engine takes
// the variable arguments of a call (the generator's -varargs-block).
func (a *args) variadic(vals ...uintptr) uintptr {
	p := a.space(8 * len(vals))
	for i, v := range vals {
		*(*uintptr)(at(p + uintptr(8*i))) = v
	}
	return p
}

func (c *Conn) limit(id, v int) { sqlite3_limit(c.sp, c.db, uintptr(id), uintptr(v)) }

// testControl calls sqlite3_test_control, which is global, from the shadow
// pointer sp.
func testControl(sp uintptr, op int, vals ...uintptr) {
	a := args{sp}
	p := a.variadic(vals...)
	sqlite3_test_control(a.top, uintptr(op), p)
}

func (c *Conn) dbConfig(op uint32, vals ...uintptr) {
	a := c.args()
	p := a.variadic(vals...)
	sqlite3_db_config(a.top, c.db, uintptr(op), p)
}

// toggleDbConfig is fuzzcheck's.
func (c *Conn) toggleDbConfig(op uint32) {
	a := c.args()
	v := a.word()
	p := a.variadic(^uintptr(0), v)
	sqlite3_db_config(a.top, c.db, uintptr(op), p)
	on := *(*int32)(at(v)) != 0
	nv := uintptr(1)
	if on {
		nv = 0
	}
	c.dbConfig(op, nv, 0)
}

// cbuf copies b to memory of sqlite3_malloc64 with a NUL after it.
func (c *Conn) cbuf(b []byte) uintptr {
	p := sqlite3_malloc64(c.sp, uintptr(len(b)+1))
	if p == 0 {
		panic("fuzzdata: out of memory")
	}
	copy(bytesAt(p, len(b)), b)
	*(*byte)(at(p + uintptr(len(b)))) = 0
	return p
}

// guard runs f, which runs one statement on c, under watch, and counts it.
func (r *fuzzRun) guard(c *Conn, f func()) {
	r.stmts++
	if r.watch(c, f) {
		r.cut++
	}
}

// watch runs f, which runs C on c. After r.stmtLimit it interrupts what runs,
// and r.grace after that it ends the process: the engine did not stop. It
// says whether it interrupted.
func (r *fuzzRun) watch(c *Conn, f func()) bool {
	var mu sync.Mutex
	active, cut := true, false
	interrupt := time.AfterFunc(r.stmtLimit, func() {
		mu.Lock()
		defer mu.Unlock()
		if active {
			cut = true
			c.Interrupt()
		}
	})
	id := r.id
	hang := time.AfterFunc(r.stmtLimit+r.grace, func() {
		// Every goroutine: the one stuck in the engine says where.
		debug.SetTraceback("all")
		panic(fmt.Sprintf("fuzzdata: case %s: the engine still runs %v after an interrupt", id, r.grace))
	})
	f()
	hang.Stop()
	interrupt.Stop()
	mu.Lock()
	active = false
	mu.Unlock()
	return cut
}

// check fails the case, once, when a host callback panicked under c.
func (r *fuzzRun) check(c *Conn) bool {
	err := c.brokenErr()
	if err == nil {
		return true
	}
	if !r.reported {
		r.reported = true
		r.t.Errorf("case %s: %v", r.id, err)
	}
	return false
}

// finish closes c, as fuzzcheck ends a case, and empties the directory.
func (r *fuzzRun) finish(c *Conn) {
	ok := r.check(c)
	if ok {
		r.watch(c, func() { c.Exec("PRAGMA temp_store_directory=''") })
	}
	// Not watched: an interrupt must not reach a connection Close frees.
	if err := c.Close(); err != nil && ok {
		r.t.Errorf("case %s: close: %v", r.id, err)
	}
	ents, err := os.ReadDir(".")
	if err != nil {
		r.t.Fatal(err)
	}
	for _, e := range ents {
		// A virus scanner on Windows can hold a file that was just
		// closed for a moment.
		for try := 0; ; try++ {
			err := os.RemoveAll(e.Name())
			if err == nil {
				break
			}
			if try == 20 {
				r.t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// plain runs a script on a database image, or on an empty database, as
// fuzzcheck's main loop does.
func (r *fuzzRun) plain(sql, image []byte) {
	t := r.t
	sqlite3_randomness(r.anySP(), 0, 0)
	name, flags := "main.db", OpenReadWrite|OpenCreate
	if len(image) == 0 {
		flags |= OpenMemory
	} else if err := os.WriteFile(name, image, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(name, flags|OpenURI, "")
	if err != nil {
		t.Fatalf("case %s: %v", r.id, err)
	}
	c.limit(limitLength, 100000000)
	c.limit(limitLikePattern, 50)
	testControl(c.sp, testctrlInternalFunctions, c.db)
	testControl(c.sp, testctrlPrngSeed, 1, c.db)
	r.runSQL(c, sql)
	r.finish(c)
}

// runSQL is fuzzcheck's runSql: each statement of the script up to its first
// NUL, its rows read and dropped, its errors ignored.
func (r *fuzzRun) runSQL(c *Conn, sql []byte) {
	if i := bytes.IndexByte(sql, 0); i >= 0 {
		sql = sql[:i]
	}
	buf := c.cbuf(sql)
	defer sqlite3_free(c.sp, buf)
	z := buf
	for *(*byte)(at(z)) != 0 {
		a := c.args()
		pStmt, pTail := a.word(), a.word()
		var more uintptr
		r.guard(c, func() {
			sqlite3_prepare_v2(a.top, c.db, z, ^uintptr(0), pStmt, pTail)
			more = *(*uintptr)(at(pTail))
			if p := *(*uintptr)(at(pStmt)); p != 0 {
				r.prepared++
				for int32(sqlite3_step(a.top, p)) == Row {
					r.rows++
				}
				sqlite3_finalize(a.top, p)
			}
		})
		if !r.check(c) || more == z || more == 0 {
			return
		}
		z = more
	}
}

// ossFuzz is the LLVMFuzzerTestOneInput of test/ossfuzz.c, for the files
// whose config says oss-fuzz: a selector byte, a memory database, and
// sqlite3_exec with a callback that reads every column as text and stops
// after a number of rows the selector gives.
func (r *fuzzRun) ossFuzz(data []byte) {
	t := r.t
	if len(data) < 3 {
		return
	}
	sel := byte(0xfd)
	if data[1] == '\n' {
		sel, data = data[0], data[2:]
	}
	sqlite3_randomness(r.anySP(), 0, 0)
	c, err := Open("fuzz.db", OpenReadWrite|OpenCreate|OpenMemory|OpenURI, "")
	if err != nil {
		t.Fatalf("case %s: %v", r.id, err)
	}
	// Before the frees deferred below run, not after: Close frees the
	// shadow stack they run on.
	defer r.finish(c)
	c.limit(limitVdbeOp, 25000)
	c.limit(limitLikePattern, 250)
	c.limit(limitLength, 50000)
	a := c.args()
	rc := a.word()
	p := a.variadic(uintptr(sel&1), rc)
	sqlite3_db_config(a.top, c.db, dbconfigEnableFKey, p)
	execCnt := uint32(sel>>1) + 1
	if i := bytes.IndexByte(data, 0); i >= 0 {
		data = data[:i]
	}
	buf := c.cbuf(data)
	defer sqlite3_free(c.sp, buf)
	sqlite3_complete(c.sp, buf)
	// sqlite3_exec: it stops at the first statement that fails, and when the
	// callback says so.
	z := buf
	stop := false
	for !stop && *(*byte)(at(z)) != 0 {
		a := c.args()
		pStmt, pTail := a.word(), a.word()
		r.guard(c, func() {
			if int32(sqlite3_prepare_v2(a.top, c.db, z, ^uintptr(0), pStmt, pTail)) != OK {
				stop = true
				return
			}
			p := *(*uintptr)(at(pStmt))
			if p == 0 {
				return
			}
			r.prepared++
			defer sqlite3_finalize(a.top, p)
			for {
				switch int32(sqlite3_step(a.top, p)) {
				case Row:
					r.rows++
					n := int(int32(sqlite3_column_count(a.top, p)))
					for i := 0; i < n; i++ {
						sqlite3_column_text(a.top, p, uintptr(i))
					}
					cnt := execCnt
					execCnt--
					if cnt == 0 {
						stop = true
						return
					}
				case Done:
					return
				default:
					stop = true
					return
				}
			}
		})
		if !r.check(c) {
			break
		}
		more := *(*uintptr)(at(pTail))
		if more == z || more == 0 {
			break
		}
		z = more
	}
}

// mxFileSz is fuzzcheck's MX_FILE_SZ, the largest database a dbsqlfuzz case
// can hold.
const mxFileSz = 10000000

// decodeDatabase is fuzzcheck's: the database image at the start of a
// dbsqlfuzz case, in hex with [offset] marks, up to "\n--\n". It gives the
// image and where the SQL starts, or ok false.
func decodeDatabase(in []byte) (image []byte, sql int, ok bool) {
	if len(in) < 4 {
		return nil, 0, false
	}
	n := uint32(len(in))
	a := make([]byte, 0, 4096)
	mx := uint32(0)
	var i, k uint32
	var b byte
	for i = 0; i < n; i++ {
		c := in[i]
		if isHex(c) {
			k++
			if k&1 != 0 {
				b = hexVal(c) * 16
				continue
			}
			b += hexVal(c)
			j := k/2 - 1
			if j >= mxFileSz {
				return nil, 0, false
			}
			if j >= mx {
				mx = (j + 4095) &^ 4095
				if mx > mxFileSz {
					mx = mxFileSz
				}
			}
			if int(j) >= len(a) {
				a = append(a, make([]byte, int(j)+1-len(a))...)
			}
			a[j] = b
		} else if c == '[' && i < n-3 {
			// isOffset: [hex] moves the cursor.
			var off uint32
			m := i + 1
			for ; m < n && in[m] != ']'; m++ {
				if !isHex(in[m]) {
					break
				}
				off = off*16 + uint32(hexVal(in[m]))
			}
			if m < n && in[m] == ']' {
				k = 2 * off
				i = m
			}
		} else if c == '\n' && i < n-4 && string(in[i:i+4]) == "\n--\n" {
			i += 4
			break
		}
	}
	if int(mx) > len(a) {
		a = append(a, make([]byte, int(mx)-len(a))...)
	}
	return a[:mx], int(i), true
}

// combined is fuzzcheck's runCombinedDbSqlInput: a dbsqlfuzz case, a database
// image and the SQL to run on it.
func (r *fuzzRun) combined(data []byte) {
	t := r.t
	if len(data) < 10 {
		return
	}
	image, iSQL, ok := decodeDatabase(data)
	if !ok {
		return
	}
	var dbFlags, dbOpt uint32
	if len(image) >= 75 {
		dbFlags = uint32(image[72])<<24 | uint32(image[73])<<16 | uint32(image[74])<<8 | uint32(image[75])
	}
	if len(image) >= 79 {
		dbOpt = uint32(image[76])<<24 | uint32(image[77])<<16 | uint32(image[78])<<8 | uint32(image[79])
	}
	sqlite3_randomness(r.anySP(), 0, 0)
	c, err := Open("", OpenReadWrite|OpenCreate|OpenURI, "")
	if err != nil {
		t.Fatalf("case %s: %v", r.id, err)
	}
	defer r.finish(c)
	testControl(c.sp, testctrlOptimizations, c.db, uintptr(dbOpt))
	for _, s := range dbConfigBits {
		if dbFlags&s.mask != 0 {
			c.toggleDbConfig(s.op)
		}
	}
	c.limit(limitVdbeOp, 25000)
	c.limit(limitLength, 1000000)
	c.limit(limitExprDepth, 500)
	c.limit(limitLikePattern, 100)

	if len(image) >= 20 && image[18] == 2 && image[19] == 2 {
		image[18], image[19] = 1, 1
	}
	a := c.args()
	zMain := a.str("main")
	// fuzzcheck's buffer is never NULL, also for an image of no bytes.
	pData := sqlite3_malloc64(a.top, uintptr(max(len(image), 1)))
	if pData == 0 {
		t.Fatalf("case %s: out of memory", r.id)
	}
	copy(bytesAt(pData, len(image)), image)
	ready := false
	r.watch(c, func() {
		if int32(sqlite3_deserialize(a.top, c.db, zMain, pData, uintptr(len(image)), uintptr(len(image)),
			deserializeResizeable|deserializeFreeOnClose)) != OK {
			return
		}
		ready = true
		x := a.word()
		*(*int64)(at(x)) = 104857600
		sqlite3_file_control(a.top, c.db, zMain, fcntlSizeLimit, x)
		// Reads the schema of the image, which can be damaged.
		zX := a.str("x")
		sqlite3_table_column_metadata(a.top, c.db, 0, zX, 0, 0, 0, 0, 0, 0)
	})
	if !ready || !r.check(c) {
		return
	}
	testControl(c.sp, testctrlPrngSeed, 1, c.db)

	// The SQL, cut into the statements sqlite3_complete finds at each ';'.
	// The case ends at an interrupt or after 10 seconds.
	sql := data[iSQL:]
	buf := c.cbuf(sql)
	defer sqlite3_free(c.sp, buf)
	end := time.Now().Add(10 * time.Second)
	rc := OK
	j := 0
	i := 0
	for ; *(*byte)(at(buf + uintptr(i))) != 0; i++ {
		if *(*byte)(at(buf + uintptr(i))) != ';' {
			continue
		}
		next := (*byte)(at(buf + uintptr(i+1)))
		saved := *next
		*next = 0
		if int32(sqlite3_complete(c.sp, buf+uintptr(j))) != 0 {
			rc = r.runDbSQL(c, buf+uintptr(j))
			j = i + 1
		}
		*next = saved
		if !r.check(c) || rc == sqliteInterrupt || time.Now().After(end) {
			return
		}
	}
	if j < i {
		r.runDbSQL(c, buf+uintptr(j))
		r.check(c)
	}
}

// runDbSQL is fuzzcheck's runDbSql without the invariants: one statement,
// its rows read and dropped. It gives what sqlite3_finalize gives.
func (r *fuzzRun) runDbSQL(c *Conn, z uintptr) int {
	for {
		ch := *(*byte)(at(z)) & 0x7f
		if ch != ' ' && (ch < '\t' || ch > '\r') {
			break
		}
		z++
	}
	if *(*byte)(at(z)) == 0 {
		return OK
	}
	a := c.args()
	pStmt := a.word()
	rc := OK
	r.guard(c, func() {
		if int32(sqlite3_prepare_v2(a.top, c.db, z, ^uintptr(0), pStmt, 0)) == OK {
			p := *(*uintptr)(at(pStmt))
			r.prepared++
			for int32(sqlite3_step(a.top, p)) == Row {
				r.rows++
			}
		}
		rc = int(int32(sqlite3_finalize(a.top, *(*uintptr)(at(pStmt)))))
	})
	return rc
}

// anySP is a shadow pointer for a call that needs no connection: one of a
// stack of the run's own.
func (r *fuzzRun) anySP() uintptr {
	if r.side == nil {
		s, err := newStack()
		if err != nil {
			r.t.Fatal(err)
		}
		r.side = s
	}
	return r.side.sp()
}

// TestConfine checks the names TestFuzzData gives the VFS: a name in the
// directory stays, any other becomes a file right in it.
func TestConfine(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f := confine(dir)
	for _, name := range []string{"main.db", "main.db-journal", filepath.Join(dir, "a.db")} {
		if got := f(name); got != name {
			t.Errorf("confine(%q) = %q, want it as it is", name, got)
		}
	}
	outside := []string{
		filepath.Join(filepath.Dir(dir), "x.db"),
		filepath.Join("..", "..", "x.db"),
		string(filepath.Separator) + "00",
	}
	for _, name := range outside {
		got := f(name)
		if filepath.Dir(got) != dir {
			t.Errorf("confine(%q) = %q, want a file in %s", name, got, dir)
		}
		if f(got) != got {
			t.Errorf("confine(%q) = %q, which confine changes again", name, got)
		}
	}
}

// TestFileCap checks testFiles.maxSize: a write that would make a file that
// large fails with SQLITE_FULL, as in fuzzcheck's VFS.
func TestFileCap(t *testing.T) {
	testFiles.maxSize = 64 << 10
	defer func() { testFiles.maxSize = 0 }()
	c, err := Open(filepath.Join(t.TempDir(), "cap.db"), OpenReadWrite|OpenCreate, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Exec("CREATE TABLE t(b)"); err != nil {
		t.Fatal(err)
	}
	err = c.Exec("INSERT INTO t VALUES (randomblob(200000))")
	var e *Error
	if !errors.As(err, &e) || e.Code() != sqliteFull {
		t.Fatalf("insert of 200000 bytes into a file capped at 64 KiB: %v, want SQLITE_FULL", err)
	}
}
