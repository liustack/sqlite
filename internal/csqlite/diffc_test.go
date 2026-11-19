// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDiffC runs the SQL of SQLite's fuzz cases on the engine and on the same
// C compiled natively by clang (engine/diff), and fails where a statement
// gives something else on the two: another result code, other columns or
// rows, another count of changes or last rowid, another error message. The C
// side is engine/diff/diff.c, which engine/diff/diff.sh builds, and it says
// what the cases are and how they run: this test makes the same calls in the
// same order and reads what the C side printed for each case as it goes. A
// case stops being compared at its first statement that either side stopped
// after its time.
//
// Its settings:
//
//	DIFFC=<path>         the program engine/diff/diff.sh builds, or the test skips
//	DIFFC_REQUIRED=1     fail instead of skipping when DIFFC is unset
//	FUZZDATA=<dir>       the folder of fuzzdata*.db (go run scripts/fuzzdata.go)
//	DIFFC_SLICE=i/n      run only the cases k with k%n == i
func TestDiffC(t *testing.T) {
	bin := os.Getenv("DIFFC")
	if bin == "" {
		if os.Getenv("DIFFC_REQUIRED") != "" {
			t.Fatal("DIFFC_REQUIRED is set and DIFFC is not: engine/diff/diff.sh builds the C side")
		}
		t.Skip("DIFFC is not set: engine/diff/diff.sh builds the C side")
	}
	dir := os.Getenv("FUZZDATA")
	if dir == "" {
		t.Fatal("DIFFC needs FUZZDATA, the fuzz cases of go run scripts/fuzzdata.go")
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
	d := &diffRun{t: t, mod: 1, bin: bin, files: files}
	var args []string
	if s := os.Getenv("DIFFC_SLICE"); s != "" {
		if _, err := fmt.Sscanf(s, "%d/%d", &d.rem, &d.mod); err != nil || d.mod <= 0 || d.rem < 0 || d.rem >= d.mod {
			t.Fatalf("DIFFC_SLICE=%q: want i/n with 0 <= i < n", s)
		}
		args = append(args, "-slice", s)
	}
	args = append(args, files...)

	// Each side in a directory of its own, should a case name a file of the
	// VFS of the machine (file:x?vfs=go).
	d.cDir = t.TempDir()
	t.Chdir(t.TempDir())
	cmd := osexec.Command(bin, args...)
	cmd.Dir = d.cDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	d.c = bufio.NewReaderSize(out, 1<<20)
	now, ok := strings.CutPrefix(d.cLine(), "now ")
	if !ok {
		t.Fatalf("the C side began with no time: %s", stderr.String())
	}
	if testNowMs, err = strconv.ParseInt(now, 10, 64); err != nil {
		t.Fatal(err)
	}
	local := time.Local
	time.Local = time.UTC
	d.fuzz = &fuzzRun{t: t, stmtLimit: 2 * time.Second, grace: 60 * time.Second}
	// go_init first: a call of SQLite would start it before go_init can
	// configure it.
	if err := start(); err != nil {
		t.Fatal(err)
	}
	// The cases set them with PRAGMA soft_heap_limit and hard_heap_limit,
	// which are global.
	soft := sqlite3_soft_heap_limit64(d.fuzz.anySP(), ^uintptr(0))
	hard := sqlite3_hard_heap_limit64(d.fuzz.anySP(), ^uintptr(0))
	defer func() {
		sqlite3_hard_heap_limit64(d.fuzz.anySP(), hard)
		sqlite3_soft_heap_limit64(d.fuzz.anySP(), soft)
		testNowMs = 0
		time.Local = local
		testControl(d.fuzz.anySP(), testctrlPrngSeed, 0, 0)
		sqlite3_randomness(d.fuzz.anySP(), 0, 0)
		d.setDefaultVFS("go")
		d.fuzz.side.free()
	}()
	d.setDefaultVFS("memdb")

	for _, f := range files {
		d.file(f)
	}
	if rest, err := io.ReadAll(d.c); err != nil || len(rest) > 0 {
		t.Fatalf("the C side printed more than the Go side ran (%v): %.200q", err, rest)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the C side: %v: %s", err, stderr.String())
	}
	if d.diffs > maxDiffs {
		t.Errorf("%d statements differ, the first %d shown", d.diffs, maxDiffs)
	}
	t.Logf("%d cases, %d statements compared, %d cases cut short, %d not the same from run to run",
		d.cases, d.compared, d.cut, d.unstable)
}

const maxDiffs = 20

type diffRun struct {
	t                    *testing.T
	fuzz                 *fuzzRun // its watch and anySP
	c                    *bufio.Reader
	rem, mod             int
	k                    int
	cases, compared, cut int
	diffs, unstable      int
	bin, cDir            string   // the C side and the directory it runs in
	files                []string // the fuzzdata files, for a run of one case
	lines                []string // the Go side's lines of the case running
	stmts                [][2]int // where the statement of each is in sql
	sql                  []byte   // the SQL of the case running
}

// cLine is the next line of the C side.
func (d *diffRun) cLine() string {
	s, err := d.c.ReadString('\n')
	if err != nil {
		d.t.Fatalf("the C side ended early: %v", err)
	}
	return strings.TrimSuffix(s, "\n")
}

func (d *diffRun) setDefaultVFS(name string) {
	sp := d.fuzz.anySP()
	a := args{sp}
	z := a.str(name)
	v := sqlite3_vfs_find(a.top, z)
	if v == 0 || int32(sqlite3_vfs_register(sp, v, 1)) != OK {
		d.t.Fatalf("cannot make %s the default VFS", name)
	}
}

// file runs the cases of one fuzzdata file, as diff.c's main does.
func (d *diffRun) file(path string) {
	t := d.t
	src, err := Open(path, OpenReadOnly, "go")
	if err != nil {
		t.Fatal(err)
	}
	scripts := fuzzBlobs(t, src, "SELECT sqlid, sqltext FROM xsql")
	images := fuzzBlobs(t, src, "SELECT dbid, dbcontent FROM db")
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if len(images) == 0 {
		images = []fuzzBlob{{id: 1}}
	}
	name := filepath.Base(path)
	for _, sql := range scripts {
		if isDbSql(sql.b) {
			if d.take() {
				id := fmt.Sprintf("%s:%d", name, sql.id)
				d.check(id, func() bool { return d.combined(id, sql.b) })
			}
			continue
		}
		for _, img := range images {
			if d.take() {
				id := fmt.Sprintf("%s:%d:%d", name, sql.id, img.id)
				d.check(id, func() bool { d.plain(id, sql.b, img.b); return true })
			}
		}
	}
}

func (d *diffRun) take() bool {
	k := d.k
	d.k++
	return k%d.mod == d.rem
}

// openCase is diff.c's.
func (d *diffRun) openCase(id string) *Conn {
	d.fuzz.id = id
	d.fuzz.reported = false
	d.lines, d.stmts, d.sql = d.lines[:0], d.stmts[:0], nil
	sqlite3_randomness(d.fuzz.anySP(), 0, 0)
	sqlite3_soft_heap_limit64(d.fuzz.anySP(), 0)
	sqlite3_hard_heap_limit64(d.fuzz.anySP(), 0)
	c, err := Open(":memory:", OpenReadWrite|OpenCreate|OpenURI, "")
	if err != nil {
		d.t.Fatalf("case %s: %v", id, err)
	}
	return c
}

// deserialize is diff.c's.
func (d *diffRun) deserialize(c *Conn, img []byte) bool {
	a := c.args()
	zMain := a.str("main")
	p := sqlite3_malloc64(a.top, uintptr(max(len(img), 1)))
	if p == 0 {
		d.t.Fatal("out of memory")
	}
	copy(bytesAt(p, len(img)), img)
	if int32(sqlite3_deserialize(a.top, c.db, zMain, p, uintptr(len(img)), uintptr(len(img)),
		deserializeResizeable|deserializeFreeOnClose)) != OK {
		return false
	}
	x := a.word()
	*(*int64)(at(x)) = mxFileSz
	sqlite3_file_control(a.top, c.db, zMain, fcntlSizeLimit, x)
	return true
}

// closeCase is diff.c's.
func (d *diffRun) closeCase(id string, c *Conn) {
	if err := c.Close(); err != nil {
		d.t.Fatalf("case %s: close: %v", id, err)
	}
}

func (d *diffRun) plain(id string, sql, img []byte) {
	c := d.openCase(id)
	if len(img) > 0 && !d.deserialize(c, img) {
		d.lines = append(d.lines, id+" 0 deserialize")
		d.closeCase(id, c)
		return
	}
	c.limit(limitLength, 10000000)
	c.limit(limitLikePattern, 50)
	testControl(c.sp, testctrlInternalFunctions, c.db)
	testControl(c.sp, testctrlPrngSeed, 1, c.db)
	d.run(id, c, sql)
	d.closeCase(id, c)
}

// combined is diff.c's. It says whether the case ran: diff.c prints
// nothing for a case with no image.
func (d *diffRun) combined(id string, sql []byte) bool {
	if len(sql) < 10 {
		return false
	}
	img, iSQL, ok := decodeDatabase(sql)
	if !ok {
		return false
	}
	var dbFlags, dbOpt uint32
	if len(img) >= 76 {
		dbFlags = uint32(img[72])<<24 | uint32(img[73])<<16 | uint32(img[74])<<8 | uint32(img[75])
	}
	if len(img) >= 80 {
		dbOpt = uint32(img[76])<<24 | uint32(img[77])<<16 | uint32(img[78])<<8 | uint32(img[79])
	}
	if len(img) >= 20 && img[18] == 2 && img[19] == 2 {
		img[18], img[19] = 1, 1
	}
	c := d.openCase(id)
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
	if !d.deserialize(c, img) {
		d.lines = append(d.lines, id+" 0 deserialize")
		d.closeCase(id, c)
		return true
	}
	testControl(c.sp, testctrlPrngSeed, 1, c.db)
	d.run(id, c, sql[iSQL:])
	d.closeCase(id, c)
	return true
}

// diffHash is diff.c's FNV-1a hash.
type diffHash uint64

func (h *diffHash) bytes(b []byte) {
	for _, c := range b {
		*h ^= diffHash(c)
		*h *= 0x100000001b3
	}
}

func (h *diffHash) u64(v uint64) {
	var b [8]byte
	for i := range b {
		b[i] = byte(v >> (8 * i))
	}
	h.bytes(b[:])
}

// blob hashes the n bytes at p, n -1 for none.
func (h *diffHash) blob(p uintptr, n int) {
	h.u64(uint64(int64(n)))
	if n > 0 {
		h.bytes(bytesAt(p, n))
	}
}

// msg is the connection's error message, escaped as diff.c's printMsg does.
func msg(c *Conn) string {
	m := cstring(sqlite3_errmsg(c.sp, c.db))
	return strings.NewReplacer("\\", `\\`, "\n", `\n`, "\r", `\r`).Replace(m)
}

// run is diff.c's: the statements of sql, cut at its first NUL.
func (d *diffRun) run(id string, c *Conn, sql []byte) {
	if i := bytes.IndexByte(sql, 0); i >= 0 {
		sql = sql[:i]
	}
	d.sql = sql
	buf := c.cbuf(sql)
	defer sqlite3_free(c.sp, buf)
	caseEnd := time.Now().Add(10 * time.Second)
	z := buf
	for n := 0; *(*byte)(at(z)) != 0; n++ {
		stmt := [2]int{int(z - buf), len(sql)}
		if time.Now().After(caseEnd) {
			d.add(fmt.Sprintf("%s %d interrupted", id, n), stmt)
			return
		}
		a := c.args()
		pStmt, pTail := a.word(), a.word()
		var line string
		cut := d.fuzz.watch(c, func() {
			rc := int32(sqlite3_prepare_v2(a.top, c.db, z, ^uintptr(0), pStmt, pTail))
			p := *(*uintptr)(at(pStmt))
			switch {
			case rc != OK:
				line = fmt.Sprintf("%s %d prepare %d %s", id, n, rc, msg(c))
			case p == 0:
				line = fmt.Sprintf("%s %d empty", id, n)
			default:
				h := diffHash(0xcbf29ce484222325)
				cols := int(int32(sqlite3_column_count(a.top, p)))
				for i := 0; i < cols; i++ {
					name := sqlite3_column_name(a.top, p, uintptr(i))
					if name == 0 {
						h.blob(0, -1)
					} else {
						h.blob(name, len(cstring(name)))
					}
				}
				explain := int32(sqlite3_stmt_isexplain(a.top, p)) == 1
				var rows int64
				for {
					rc = int32(sqlite3_step(a.top, p))
					if rc != Row {
						break
					}
					rows++
					for i := 0; i < cols; i++ {
						typ := int32(sqlite3_column_type(a.top, p, uintptr(i)))
						h.bytes([]byte{byte(typ)})
						switch typ {
						case TypeInteger:
							h.u64(uint64(sqlite3_column_int64(a.top, p, uintptr(i))))
						case TypeFloat:
							h.u64(math.Float64bits(sqlite3_column_double(a.top, p, uintptr(i))))
						case TypeText:
							t := sqlite3_column_text(a.top, p, uintptr(i))
							n := -1
							if t != 0 {
								n = int(int32(sqlite3_column_bytes(a.top, p, uintptr(i))))
							}
							// EXPLAIN shows a virtual table by its address.
							if explain && i == 5 && n > 5 && string(bytesAt(t, 5)) == "vtab:" {
								n = 5
							}
							h.blob(t, n)
						case TypeBlob:
							b := sqlite3_column_blob(a.top, p, uintptr(i))
							h.blob(b, int(int32(sqlite3_column_bytes(a.top, p, uintptr(i)))))
						}
					}
				}
				line = fmt.Sprintf("%s %d %d %d %d %016x %d %d ", id, n, rc, cols, rows, uint64(h),
					int64(sqlite3_changes64(a.top, c.db)), int64(sqlite3_last_insert_rowid(a.top, c.db)))
				if rc == Done {
					line += "-"
				} else {
					line += msg(c)
				}
				sqlite3_finalize(a.top, p)
			}
		})
		if !d.fuzz.check(c) {
			return
		}
		tail := *(*uintptr)(at(pTail))
		if cut {
			d.add(fmt.Sprintf("%s %d interrupted", id, n), stmt)
			return
		}
		if tail > z {
			stmt[1] = int(tail - buf)
		}
		d.add(line, stmt)
		if tail == 0 || tail == z {
			return
		}
		z = tail
	}
}

func (d *diffRun) add(line string, stmt [2]int) {
	d.lines = append(d.lines, line)
	d.stmts = append(d.stmts, stmt)
}

// check runs a case, which says whether it ran, reads the C side's lines of
// it and holds the Go side's to them. When a statement differs, it runs the
// case again three times on each side, the C side in a process of its own
// each time: SQLite gives some damaged databases results that depend on where
// its memory is, which changes from run to run, and a case that is not the
// same from run to run on a side is logged and not failed.
func (d *diffRun) check(id string, run func() bool) {
	if !run() {
		return
	}
	d.cases++
	cl := d.cLines(d.cLine, id)
	i, n, cut := firstDiff(id, cl, d.lines)
	d.compared += n
	if i < 0 {
		if cut {
			d.cut++
		}
		return
	}
	goLines := append([]string(nil), d.lines...)
	// Up to the statement that differs: what comes after it may be cut
	// short by its time on one run and not on another.
	upTo := func(l []string) []string { return l[:min(len(l), i+1)] }
	for try := 0; try < 3; try++ {
		if again := d.cAlone(id); !slices.Equal(upTo(again), upTo(cl)) {
			d.unstable++
			d.t.Logf("case %s is not the same from run to run in C, at statement %d: skipped", id, i)
			return
		}
	}
	for try := 0; try < 3; try++ {
		run()
		if again := d.lines; !slices.Equal(upTo(again), upTo(goLines)) {
			// The C side gave the same four times: an engine that does not
			// is wrong.
			d.diffs++
			if d.diffs <= maxDiffs {
				d.t.Errorf("case %s, statement %d: the Go side differs from run to run where the C side does not:\n  C:     %.300q\n  Go:    %.300q\n  again: %.300q",
					id, i, upTo(cl), upTo(goLines), upTo(again))
			}
			return
		}
	}
	d.diffs++
	if d.diffs > maxDiffs {
		return
	}
	var c, g string
	var stmt []byte
	if i < len(cl) {
		c = cl[i]
	}
	if i < len(goLines) {
		g = goLines[i]
	}
	if i < len(d.stmts) {
		stmt = d.sql[d.stmts[i][0]:d.stmts[i][1]]
	}
	d.t.Errorf("case %s, statement %d differs:\n  C:  %.300q\n  Go: %.300q\n  SQL: %.500q", id, i, c, g, stmt)
}

// cLines reads the lines of case id from next, up to its end line.
func (d *diffRun) cLines(next func() string, id string) []string {
	var cl []string
	for {
		l := next()
		if l == id+" end" {
			return cl
		}
		if !strings.HasPrefix(l, id+" ") {
			d.t.Fatalf("case %s: the C side runs another case: %.200q", id, l)
		}
		cl = append(cl, l)
	}
}

// cAlone runs case id on the C side in a process of its own.
func (d *diffRun) cAlone(id string) []string {
	cmd := osexec.Command(d.bin, append([]string{"-case", id}, d.files...)...)
	cmd.Dir = d.cDir
	out, err := cmd.Output()
	if err != nil {
		d.t.Fatalf("case %s on the C side alone: %v", id, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "now ") {
		d.t.Fatalf("case %s on the C side alone: %.200q", id, out)
	}
	lines = lines[1:]
	return d.cLines(func() string {
		if len(lines) == 0 {
			d.t.Fatalf("case %s on the C side alone: no end", id)
		}
		l := lines[0]
		lines = lines[1:]
		return l
	}, id)
}

// firstDiff is the first statement where c and g differ, -1 for none, up to
// the first one either side stopped after its time (cut), and n the
// statements compared before it.
func firstDiff(id string, c, g []string) (i, n int, cut bool) {
	for i := 0; i < max(len(c), len(g)); i++ {
		var cl, gl string
		if i < len(c) {
			cl = c[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if stop := fmt.Sprintf("%s %d interrupted", id, i); cl == stop || gl == stop {
			return -1, i, true
		}
		if cl != gl {
			return i, i, false
		}
	}
	return -1, max(len(c), len(g)), false
}
