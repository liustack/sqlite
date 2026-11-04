// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// The extensions built into the engine: FTS5, R*Tree, the math functions and
// percentile.

// mathFuncs are the math functions of one argument with what Go computes for
// them, nil where SQLite has its own formula or Go is too far off.
var mathFuncs = []struct {
	name string
	gof  func(float64) float64
}{
	{"acos", math.Acos}, {"acosh", math.Acosh}, {"asin", math.Asin}, {"asinh", math.Asinh},
	{"atan", math.Atan}, {"atanh", math.Atanh}, {"ceil", math.Ceil}, {"cos", math.Cos},
	{"cosh", math.Cosh}, {"exp", math.Exp}, {"floor", math.Floor}, {"ln", math.Log},
	{"log", math.Log10}, {"log10", math.Log10}, {"log2", math.Log2}, {"sin", math.Sin},
	{"sinh", math.Sinh}, {"sqrt", math.Sqrt}, {"tan", math.Tan}, {"tanh", math.Tanh},
	{"trunc", math.Trunc}, {"degrees", nil}, {"radians", nil},
	{"atan2(?, 0.3)", func(x float64) float64 { return math.Atan2(x, 0.3) }},
	{"pow(abs(?), 1.37)", func(x float64) float64 { return math.Pow(math.Abs(x), 1.37) }},
	// Go's Pow is off by more than 1e-10 here once the result is large.
	{"pow(1.0001, ?)", nil},
	{"mod(?, 0.7)", func(x float64) float64 { return math.Mod(x, 0.7) }},
	{"log(3, abs(?))", func(x float64) float64 { return math.Log(math.Abs(x)) / math.Log(3) }},
}

// mathInputs spreads over the ranges the functions treat differently: small,
// near 1, large, huge, and the edges of the domains.
func mathInputs() []float64 {
	xs := []float64{0, math.Copysign(0, -1), 1, -1, 0.5, -0.5, 1e-300, 5e-324, 1e300, -1e300,
		math.Pi, math.Pi / 2, 709.7, 710, -745, 1e22, math.MaxFloat64}
	for i := range 2000 {
		x := float64(i-1000) * 0.0137
		xs = append(xs, x, x*x*x, math.Ldexp(1+float64(i)/2000, i%200-100))
	}
	return xs
}

// mathBits is the SHA-256 of the bits of every result of mathFuncs on
// mathInputs, NULL as all ones. It is the same on every target: the
// functions are musl's, built without fused multiply-add.
const mathBits = "ae95a605d63f521ccd1229ad1ceea52528837e9111a42da2959129fe9b6ba9ba"

func TestMathFunctions(t *testing.T) {
	db := fOpen(t, ":memory:")
	for _, c := range []struct{ q, want string }{
		{`SELECT pi()`, "3.14159265358979"},
		{`SELECT pow(2, 10), power(2, 0.5) * power(2, 0.5)`, "1024.0 2.0"},
		{`SELECT ceil(-1.5), ceiling(1.2), floor(-1.5), trunc(-1.5), ceil(7)`, "-1.0 2.0 -2.0 -1.0 7"},
		{`SELECT log(100), log10(1000), log2(1024), ln(1), log(2, 64)`, "2.0 3.0 10.0 0.0 6.0"},
		{`SELECT mod(7, 3), mod(-7.5, 2)`, "1.0 -1.5"},
		{`SELECT degrees(pi()), radians(180) = pi()`, "180.0 1"},
		{`SELECT sqrt(-1) IS NULL, acos(2) IS NULL, ln(0) IS NULL, ln(-1) IS NULL`, "1 1 1 1"},
		{`SELECT sqrt(NULL) IS NULL, sin('x')`, "1 <nil>"},
		// Exact results rounded to a float64, from bc -l at 60 digits
		// (Go's math gives each of them a few units in the last place off).
		{`SELECT cos(pi() / 2)`, "6.12323399573677e-17"},
		{`SELECT log2(1.0001) = 0.0001442622910945383, log2(1.0138) = 0.019773069040027156`, "1 1"},
		{`SELECT pow(1.0001, 709.7) = 1.073545209686691, cosh(710) = 1.1169973830808555e308`, "1 1"},
	} {
		if got := fRow(t, db, c.q); got != c.want {
			t.Errorf("%s: %s, want %s", c.q, got, c.want)
		}
	}

	h := sha256.New()
	xs := mathInputs()
	for _, f := range mathFuncs {
		q := "SELECT " + f.name
		if !strings.Contains(f.name, "?") {
			q += "(?)"
		}
		st, err := db.Prepare(q)
		if err != nil {
			t.Fatal(q, err)
		}
		far := 0
		for _, x := range xs {
			var v sql.NullFloat64
			if err := st.QueryRow(x).Scan(&v); err != nil {
				t.Fatal(q, x, err)
			}
			bits := ^uint64(0)
			if v.Valid {
				bits = math.Float64bits(v.Float64)
			}
			h.Write(binary.LittleEndian.AppendUint64(nil, bits))
			if f.gof == nil || x != 0 && math.Abs(x) < 0x1p-1022 {
				// Go's math on amd64 is wrong for a subnormal (log10 of
				// 5e-324 gives -307.95).
				continue
			}
			want := f.gof(x)
			if !v.Valid {
				// SQLite gives NULL where the result is not a number.
				if !math.IsNaN(want) && !(math.IsInf(want, -1) && strings.HasPrefix(f.name, "l")) {
					t.Errorf("%s(%g): NULL, Go gives %g", f.name, x, want)
				}
				continue
			}
			if !near(v.Float64, want) {
				if far++; far <= 3 {
					t.Errorf("%s(%g) = %g, Go gives %g", f.name, x, v.Float64, want)
				}
			}
		}
		st.Close()
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != mathBits {
		t.Errorf("the results of the math functions have the hash %s, want %s", got, mathBits)
	}
}

// near says a, the result of SQLite, is within 1e-10 of b, the result of
// Go, relative to the larger. Go's math is less exact than musl's (see the
// bc values above, and Go's pow drifts by 1e-11 with a large exponent), so
// this only catches a function that is wrong. Go
// overflows to infinity a little early (cosh and sinh of 710).
func near(a, b float64) bool {
	switch {
	case a == b:
		return true
	case math.IsInf(b, 0) && !math.IsInf(a, 0):
		return math.Abs(a) > 1e307 && math.Signbit(a) == math.Signbit(b)
	case math.IsInf(a, 0) || math.IsInf(b, 0) || math.IsNaN(a) || math.IsNaN(b):
		return false
	}
	return math.Abs(a-b) <= 1e-10*max(math.Abs(a), math.Abs(b)) || math.Abs(a-b) < 1e-300
}

// fRow gives the columns of the first row of q, separated by spaces.
func fRow(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		t.Fatalf("%s: no row (%v)", q, rows.Err())
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	s := make([]string, len(vals))
	for i, v := range vals {
		switch v := v.(type) {
		case float64:
			s[i] = fmt.Sprintf("%.15g", v)
			if !strings.ContainsAny(s[i], ".en") {
				s[i] += ".0"
			}
		case []byte:
			s[i] = string(v)
		default:
			s[i] = fmt.Sprint(v)
		}
	}
	return strings.Join(s, " ")
}

func TestFTS5(t *testing.T) {
	db := fOpen(t, "file:"+mPath(t))
	mExec(t, db, `CREATE VIRTUAL TABLE docs USING fts5(title, body, tokenize = 'porter unicode61 remove_diacritics 2')`)
	mExec(t, db, `INSERT INTO docs(rowid, title, body) VALUES
		(1, 'Go and SQLite', 'running sqlite from go without cgo, sqlite all the way'),
		(2, 'Café', 'un café crème, and a little sqlite'),
		(3, 'Rust', 'a different language altogether')`)
	for _, c := range []struct{ q, want string }{
		{`SELECT group_concat(rowid) FROM (SELECT rowid FROM docs WHERE docs MATCH 'sqlite' ORDER BY rank)`, "1,2"},
		{`SELECT rowid FROM docs WHERE docs MATCH 'run'`, "1"},
		{`SELECT rowid FROM docs WHERE docs MATCH 'cafe'`, "2"},
		{`SELECT rowid FROM docs WHERE docs MATCH 'title:rust'`, "3"},
		{`SELECT rowid FROM docs WHERE docs MATCH 'differ*'`, "3"},
		{`SELECT rowid FROM docs WHERE docs MATCH 'NEAR(go cgo, 3)'`, "1"},
		{`SELECT count(*) FROM docs WHERE docs MATCH 'sqlite NOT cgo'`, "1"},
		{`SELECT highlight(docs, 1, '[', ']') FROM docs WHERE docs MATCH 'cgo'`, "running sqlite from go without [cgo], sqlite all the way"},
		{`SELECT snippet(docs, 1, '[', ']', '...', 3) FROM docs WHERE docs MATCH 'crème'`, "un café [crème]..."},
		{`SELECT bm25(docs) < 0 FROM docs WHERE docs MATCH 'language'`, "1"},
	} {
		if got := fRow(t, db, c.q); got != c.want {
			t.Errorf("%s: %s, want %s", c.q, got, c.want)
		}
	}
	mExec(t, db, `CREATE VIRTUAL TABLE tri USING fts5(s, tokenize = 'trigram')`)
	mExec(t, db, `INSERT INTO tri VALUES ('liustack/sqlite'), ('modernc.org/sqlite'), ('mattn/go-sqlite3')`)
	if got := fRow(t, db, `SELECT group_concat(s, ' ') FROM (SELECT s FROM tri WHERE s LIKE '%stack/sq%')`); got != "liustack/sqlite" {
		t.Errorf("trigram LIKE: %s", got)
	}
	mExec(t, db, `CREATE VIRTUAL TABLE vocab USING fts5vocab(docs, 'row')`)
	if got := fRow(t, db, `SELECT doc FROM vocab WHERE term = 'sqlite'`); got != "2" {
		t.Errorf("fts5vocab: %s", got)
	}
	mExec(t, db, `INSERT INTO docs(docs) VALUES ('optimize')`)
	mExec(t, db, `INSERT INTO docs(docs) VALUES ('integrity-check')`)
}

func TestRTree(t *testing.T) {
	db := fOpen(t, "file:"+mPath(t))
	mExec(t, db, `CREATE VIRTUAL TABLE box USING rtree(id, minX, maxX, minY, maxY, +name)`)
	mExec(t, db, `INSERT INTO box VALUES (1, 0, 10, 0, 10, 'a'), (2, 5, 15, 5, 15, 'b'), (3, 20, 30, 20, 30, 'c'), (4, 0.1, 0.1, 0.1, 0.1, 'd')`)
	for _, c := range []struct{ q, want string }{
		{`SELECT group_concat(name, '') FROM (SELECT name FROM box WHERE maxX >= 8 AND minX <= 12 AND maxY >= 8 AND minY <= 12 ORDER BY id)`, "ab"},
		{`SELECT count(*) FROM box WHERE minX >= 20`, "1"},
		// A REAL is kept as a float32 rounded outward, so the box still
		// holds the value.
		{`SELECT minX <= 0.1 AND maxX >= 0.1 AND minX < maxX FROM box WHERE id = 4`, "1"},
		{`SELECT rtreecheck('box')`, "ok"},
	} {
		if got := fRow(t, db, c.q); got != c.want {
			t.Errorf("%s: %s, want %s", c.q, got, c.want)
		}
	}
	if _, err := db.Exec(`INSERT INTO box VALUES (5, 10, 0, 0, 1, 'e')`); err == nil || !strings.Contains(err.Error(), "constraint") {
		t.Errorf("a box with min > max: %v", err)
	}
	mExec(t, db, `CREATE VIRTUAL TABLE ibox USING rtree_i32(id, a, b)`)
	mExec(t, db, `INSERT INTO ibox VALUES (1, -5, 2147483647)`)
	if got := fRow(t, db, `SELECT a, b FROM ibox`); got != "-5 2147483647" {
		t.Errorf("rtree_i32: %s", got)
	}
}

func TestPercentile(t *testing.T) {
	db := fOpen(t, ":memory:")
	mExec(t, db, `CREATE TABLE p(g, x); INSERT INTO p VALUES (1, 1), (1, 2), (1, 3), (1, 4), (1, 10), (2, 1), (2, 2), (2, 3), (2, 4)`)
	for _, c := range []struct{ q, want string }{
		{`SELECT median(x), percentile(x, 25), percentile_cont(x, 0.25), percentile_disc(x, 0.5) FROM p WHERE g = 1`, "3.0 2.0 2.0 3.0"},
		{`SELECT median(x) FROM p WHERE g = 2`, "2.5"},
		{`SELECT median(x) FROM p WHERE g = 3`, "<nil>"},
		{`SELECT group_concat(m, ' ') FROM (SELECT median(x) OVER (ORDER BY x ROWS 1 PRECEDING) AS m FROM p WHERE g = 2)`, "1.0 1.5 2.5 3.5"},
	} {
		if got := fRow(t, db, c.q); got != c.want {
			t.Errorf("%s: %s, want %s", c.q, got, c.want)
		}
	}
	if _, err := db.Exec(`SELECT percentile(x, 101) FROM p`); err == nil {
		t.Error("percentile(x, 101) gave no error")
	}
}

// TestMergeVirtualTables: writes to FTS5 and R*Tree tables merge as other
// writes do, and a statement that fails after it wrote part of its rows
// leaves the index of every table whole: FTS5 and R*Tree keep to the
// SAVEPOINT of each statement.
func TestMergeVirtualTables(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p)
	db.SetMaxOpenConns(32)
	mExec(t, db, schemaH+`; CREATE VIRTUAL TABLE ft USING fts5(body); CREATE VIRTUAL TABLE rt USING rtree(id, x0, x1);
		INSERT INTO ft(rowid, body) VALUES (1, 'first'); INSERT INTO rt VALUES (1, 0, 1)`)
	other := mOpen(t, "file:"+p)
	sc, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wl *writeLock
	sc.Raw(func(dc any) error { wl = dc.(*conn).wl; return nil })
	sc.Close()
	commits0, merged0 := wl.stats.commits.Load(), wl.stats.merged.Load()

	const good = 8
	type job struct {
		q    string
		args []any
		fail bool
	}
	var jobs []job
	for i := range good {
		jobs = append(jobs,
			job{`INSERT INTO ft(rowid, body) VALUES (?, 'good word' || ?)`, []any{100 + i, i}, false},
			job{`INSERT INTO rt VALUES (?, ?, ? + 1)`, []any{100 + i, i, i}, false})
	}
	// The first row of each of these is written, then the second fails: a
	// rowid that is taken, a box with min > max.
	jobs = append(jobs,
		job{`INSERT INTO ft(rowid, body) VALUES (200, 'bad'), (1, 'bad')`, nil, true},
		job{`INSERT INTO rt VALUES (200, 0, 1), (201, 5, 1)`, nil, true})

	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	errs := make(chan error, len(jobs))
	for _, j := range jobs {
		go func() {
			_, err := db.Exec(j.q, j.args...)
			if j.fail != (err != nil) {
				err = fmt.Errorf("%s: %v", j.q, err)
			} else {
				err = nil
			}
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for waiting() < len(jobs) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d writes wait", waiting(), len(jobs))
		}
		time.Sleep(time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for range jobs {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if c, m := wl.stats.commits.Load()-commits0, wl.stats.merged.Load()-merged0; c != 1 || m != 2*good {
		t.Errorf("%d batch commits of %d statements, want 1 of %d", c, m, 2*good)
	}
	for _, c := range []struct{ q, want string }{
		{`SELECT count(*) FROM ft WHERE ft MATCH 'good'`, fmt.Sprint(good)},
		{`SELECT count(*) FROM ft WHERE ft MATCH 'bad'`, "0"},
		{`SELECT count(*) FROM ft`, fmt.Sprint(good + 1)},
		{`SELECT count(*) FROM rt`, fmt.Sprint(good + 1)},
		{`SELECT count(*) FROM rt WHERE id >= 200`, "0"},
		{`SELECT rtreecheck('rt')`, "ok"},
	} {
		if got := fRow(t, db, c.q); got != c.want {
			t.Errorf("%s: %s, want %s", c.q, got, c.want)
		}
	}
	mExec(t, db, `INSERT INTO ft(ft) VALUES ('integrity-check')`)
}

// TestVirtualTableReadNotMerged: the first statement of a connection that
// uses an R*Tree table makes SQLite connect the table, which prepares
// statements of its own that write. They are not the statement's, so a read
// stays a read: it never merges, and Exec of it does not wait for the write
// lock.
func TestVirtualTableReadNotMerged(t *testing.T) {
	p := mPath(t)
	db := mOpen(t, "file:"+p)
	mExec(t, db, `CREATE VIRTUAL TABLE rt USING rtree(id, x0, x1); CREATE VIRTUAL TABLE ft USING fts5(b)`)
	for _, c := range []struct {
		q         string
		mergeable bool
	}{
		{`SELECT count(*) FROM rt`, false},
		{`SELECT count(*) FROM ft`, false},
		{`INSERT INTO rt VALUES (1, 0, 1)`, true},
		{`INSERT INTO ft VALUES ('x')`, true},
	} {
		fresh := mOpen(t, "file:"+p)
		c0, err := fresh.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		c0.Raw(func(dc any) error {
			st, err := dc.(*conn).PrepareContext(t.Context(), c.q)
			if err != nil {
				t.Fatal(c.q, err)
			}
			if got := st.(*stmt).mergeable; got != c.mergeable {
				t.Errorf("%s on a new connection: mergeable %v, want %v", c.q, got, c.mergeable)
			}
			return st.Close()
		})
		c0.Close()
	}
}

// TestMergeFTS5FlushFails: FTS5 writes the terms of an INSERT to its index
// only at the next SAVEPOINT or at COMMIT. When that write fails, the
// statement that wrote the terms fails, as it would alone, and not the one
// after it in the batch.
func TestMergeFTS5FlushFails(t *testing.T) {
	setup := func(t *testing.T) string {
		p := mPath(t)
		db := mOpen(t, "file:"+p)
		// Every segment but the first is broken, and a crisis merge, which
		// reads them, runs at the next write of the index.
		mExec(t, db, schemaH+`; CREATE TABLE plain(x); CREATE VIRTUAL TABLE ft USING fts5(b);
			INSERT INTO ft(ft, rank) VALUES ('automerge', 0); INSERT INTO ft(ft, rank) VALUES ('crisismerge', 2);
			INSERT INTO ft VALUES ('seed words here');
			UPDATE ft_data SET block = x'ffffffff' || substr(block, 5) WHERE id NOT IN (1, 10)`)
		db.Close()
		return p
	}
	const ftsInsert, plainInsert = `INSERT INTO ft VALUES ('new')`, `INSERT INTO plain VALUES (1)`

	db := mOpen(t, "file:"+setup(t))
	if _, err := db.Exec(ftsInsert); err == nil {
		t.Fatal("alone, the FTS5 insert into a broken index succeeds: the test sets up nothing")
	}
	mExec(t, db, plainInsert)

	p := setup(t)
	db = mOpen(t, "file:"+p)
	db.SetMaxOpenConns(8)
	other := mOpen(t, "file:"+p)
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mExec(t, tx, `INSERT INTO h VALUES (1)`)
	errs := map[string]chan error{ftsInsert: make(chan error, 1), plainInsert: make(chan error, 1)}
	for i, q := range []string{ftsInsert, plainInsert} {
		go func() {
			_, err := db.Exec(q)
			errs[q] <- err
		}()
		deadline := time.Now().Add(5 * time.Second)
		for waiting() <= i {
			if time.Now().After(deadline) {
				t.Fatalf("%s never waited", q)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-errs[ftsInsert]; err == nil {
		t.Error("merged, the FTS5 insert into a broken index succeeds")
	}
	if err := <-errs[plainInsert]; err != nil {
		t.Errorf("merged after the FTS5 insert, %s: %v", plainInsert, err)
	}
	if got := fRow(t, db, `SELECT count(*) FROM plain`); got != "1" {
		t.Errorf("%s rows in plain, want 1", got)
	}
}
