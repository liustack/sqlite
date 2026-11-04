// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package vec_test

import (
	"database/sql"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/internal/sqltest"
	"github.com/liustack/sqlite/ext/vec"
)

func TestFunctions(t *testing.T) {
	db := sqltest.Open(t, vec.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT hex(vec_f32('[1]'))`:                                            "0000803F",
		`SELECT hex(vec_f32(x'0000803F00000040'))`:                              "0000803F00000040",
		`SELECT vec_length(vec_f32('[1, 2, 3]'))`:                               "3",
		`SELECT vec_length('[1,2]')`:                                            "2",
		`SELECT vec_to_json(vec_f32('[1, 2.5, -3, 0.1]'))`:                      "[1,2.5,-3,0.1]",
		`SELECT vec_to_json(' [ 1e2 ] ')`:                                       "[100]",
		`SELECT vec_distance_l2('[0, 0]', '[3, 4]')`:                            "5",
		`SELECT vec_distance_l2(vec_f32('[1, 2]'), '[1, 2]')`:                   "0",
		`SELECT vec_distance_l1('[1, 2]', '[3, 5]')`:                            "5",
		`SELECT vec_distance_cosine('[1, 0]', '[0, 1]')`:                        "1",
		`SELECT vec_distance_cosine('[1, 0]', '[2, 0]')`:                        "0",
		`SELECT vec_distance_cosine('[1, 0]', '[-3, 0]')`:                       "2",
		`SELECT vec_dot('[1, 2]', '[3, 4]')`:                                    "11",
		`SELECT vec_to_json(vec_normalize('[3, 4]'))`:                           "[0.6,0.8]",
		`SELECT vec_distance_l2('[1e30]', '[-1e30]')`:                           "2.0000000300949324e+30", // twice the float32 of 1e30
		`SELECT vec_dot('[3e38, 3e38]', '[3e38, 3e38]') > 1e77`:                 "1",
		`SELECT vec_to_json('[1e-50, 1e-40, -0, 3.4028235e38]')`:                "[0,1e-40,-0,3.4028235e+38]",
		`SELECT vec_to_json(' [ 1 ,' || char(10) || '2' || char(9, 13) || ']')`: "[1,2]",
		`SELECT vec_length(x'0000C07F0000807F')`:                                "2",
		`SELECT vec_f32(NULL) IS NULL`:                                          "1",
		`SELECT vec_length(NULL) IS NULL`:                                       "1",
		`SELECT vec_to_json(NULL) IS NULL`:                                      "1",
		`SELECT vec_normalize(NULL) IS NULL`:                                    "1",
		`SELECT vec_distance_l2(NULL, '[1]') IS NULL`:                           "1",
		`SELECT vec_distance_cosine('[1]', NULL) IS NULL`:                       "1",
		`SELECT vec_dot(NULL, NULL) IS NULL`:                                    "1",
		`SELECT typeof(vec_f32('[1]'))`:                                         "blob",
		`SELECT typeof(vec_distance_l1('[1]', '[1]'))`:                          "real",
		`SELECT vec_distance_l2(vec_f32(x'0100803F'), x'0000803F') > 0`:         "1",
	})
	sqltest.Fails(t, db, map[string]string{
		`SELECT vec_distance_l2('[1, 2]', '[1]')`:                              "vec_distance_l2: vectors of 2 and 1 dimensions",
		`SELECT vec_f32(x'000000')`:                                            "vec_f32: a blob of 3 bytes is not float32s",
		`SELECT vec_f32(x'')`:                                                  "vec_f32: an empty vector",
		`SELECT vec_f32('[]')`:                                                 "vec_f32: an empty vector",
		`SELECT vec_f32('[1, "a"]')`:                                           "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('1')`:                                                  "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[1] x')`:                                              "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32(x'0000C07F')`:                                          "vec_f32: element 1 of argument 1 is not finite",
		`SELECT vec_f32(x'0000803F0000807F')`:                                  "vec_f32: element 2 of argument 1 is not finite",
		`SELECT vec_distance_l2(x'0000803F', x'0000C07F')`:                     "vec_distance_l2: element 1 of argument 2 is not finite",
		`SELECT vec_dot(x'0000807F', x'00000000')`:                             "vec_dot: element 1 of argument 1 is not finite",
		`SELECT vec_distance_cosine(x'0000803F0000807F', x'0000803F0000803F')`: "vec_distance_cosine: element 2 of argument 1 is not finite",
		`SELECT vec_distance_l1(x'0000807F', x'0000807F')`:                     "vec_distance_l1: element 1 of argument 1 is not finite",
		`SELECT vec_f32('[1e39]')`:                                             "vec_f32: element 1 does not fit a float32",
		`SELECT vec_f32('[1e400]')`:                                            "vec_f32: element 1 does not fit a float32",
		`SELECT vec_f32('[1, 2,]')`:                                            "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[01]')`:                                               "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[1.]')`:                                               "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[.5]')`:                                               "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[1e]')`:                                               "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[+1]')`:                                               "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[NaN]')`:                                              "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[1 2]')`:                                              "vec_f32: not a JSON array of numbers",
		`SELECT vec_f32('[1')`:                                                 "vec_f32: not a JSON array of numbers",
		`SELECT vec_to_json(x'0000C07F')`:                                      "vec_to_json: element 1 of argument 1 is not finite",
		`SELECT vec_f32(5)`:                                                    "vec_f32: argument 1 is an integer, want a vector",
		`SELECT vec_f32(5.5)`:                                                  "vec_f32: argument 1 is a real, want a vector",
		`SELECT vec_dot('[1]', 2)`:                                             "vec_dot: argument 2 is an integer, want a vector",
		`SELECT vec_distance_cosine('[0, 0]', '[1, 0]')`:                       "vec_distance_cosine: a zero vector has no direction",
		`SELECT vec_normalize('[0, 0]')`:                                       "vec_normalize: a zero vector has no direction",
	})
}

// Each distance is the float64 sum over the float32 elements.
func TestAgainstReference(t *testing.T) {
	db := sqltest.Open(t, vec.Option())
	r := rand.New(rand.NewPCG(1, 2))
	ref := map[string]func(a, b []float64) float64{
		"vec_distance_l2": func(a, b []float64) float64 {
			s := 0.0
			for i := range a {
				d := a[i] - b[i]
				s += float64(d * d)
			}
			return math.Sqrt(s)
		},
		"vec_distance_l1": func(a, b []float64) float64 {
			s := 0.0
			for i := range a {
				s += math.Abs(a[i] - b[i])
			}
			return s
		},
		"vec_dot": dot,
		"vec_distance_cosine": func(a, b []float64) float64 {
			return min(max(1-dot(a, b)/math.Sqrt(dot(a, a)*dot(b, b)), 0), 2)
		},
	}
	for range 500 {
		n := 1 + r.IntN(300)
		a, b := randVec(r, n), randVec(r, n)
		if r.IntN(4) == 0 {
			b = scale(a, float64(r.IntN(9)+1)) // parallel: a cosine of 1, or a little more
		}
		for name, f := range ref {
			var got float64
			if err := db.QueryRow(`SELECT `+name+`(?, ?)`, blob(a), blob(b)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if want := f(a, b); got != want {
				t.Fatalf("%s of %d dimensions = %v, want %v", name, n, got, want)
			}
		}
	}
}

// The nearest rows come first.
func TestNearest(t *testing.T) {
	db := sqltest.Open(t, vec.Option())
	if _, err := db.Exec(`CREATE TABLE doc(id INTEGER PRIMARY KEY, e BLOB);
		INSERT INTO doc VALUES (1, vec_f32('[1, 0]')), (2, vec_f32('[0, 1]')), (3, vec_f32('[0.9, 0.1]')), (4, vec_f32('[-1, 0]'))`); err != nil {
		t.Fatal(err)
	}
	var ids []string
	rows, err := db.Query(`SELECT id FROM doc ORDER BY vec_distance_cosine(e, vec_f32(?)) LIMIT 3`, "[1, 0.05]")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids, " "); got != "1 3 2" {
		t.Errorf("nearest: %s, want 1 3 2", got)
	}
}

func scale(a []float64, k float64) []float64 {
	b := make([]float64, len(a))
	for i := range a {
		b[i] = float64(float32(a[i] * k))
	}
	return b
}

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// randVec gives n random float32 values, as float64s.
func randVec(r *rand.Rand, n int) []float64 {
	v := make([]float64, n)
	for i := range v {
		v[i] = float64(float32(r.NormFloat64() * math.Pow(10, float64(r.IntN(7)-3))))
	}
	return v
}

func blob(v []float64) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(float32(x)))
	}
	return b
}

// BenchmarkNearest finds the 10 nearest of 100000 vectors of 768
// dimensions.
func BenchmarkNearest(b *testing.B) {
	db := sqltest.Open(b, vec.Option())
	r := rand.New(rand.NewPCG(3, 4))
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	if _, err := tx.Exec(`CREATE TABLE doc(id INTEGER PRIMARY KEY, e BLOB)`); err != nil {
		b.Fatal(err)
	}
	ins, err := tx.Prepare(`INSERT INTO doc(e) VALUES (?)`)
	if err != nil {
		b.Fatal(err)
	}
	for range 100000 {
		if _, err := ins.Exec(blob(randVec(r, 768))); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	q := blob(randVec(r, 768))
	b.ResetTimer()
	for b.Loop() {
		n := 0
		err := query(db, q, &n)
		if err != nil || n != 10 {
			b.Fatal(err, n)
		}
	}
}

func query(db *sql.DB, q []byte, n *int) error {
	rows, err := db.Query(`SELECT id FROM doc ORDER BY vec_distance_cosine(e, ?) LIMIT 10`, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		*n++
	}
	return rows.Err()
}

// A vector larger than the stage of the shadow stack (1 MiB) is read in
// place and given back as a copy.
func TestLargeVector(t *testing.T) {
	db := sqltest.Open(t, vec.Option())
	v := make([]float64, 600000)
	for i := range v {
		v[i] = float64(i%7) - 3
	}
	if _, err := db.Exec(`CREATE TABLE big(e BLOB); INSERT INTO big VALUES (?), (zeroblob(2000000))`, blob(v)); err != nil {
		t.Fatal(err)
	}
	sqltest.Want(t, db, map[string]string{
		`SELECT count(*) FROM big WHERE vec_f32(e) = e`:                   "2",
		`SELECT vec_distance_l2(e, e) FROM big WHERE rowid = 1`:           "0",
		`SELECT vec_length(vec_normalize(e)) FROM big WHERE rowid = 1`:    "600000",
		`SELECT vec_distance_l1(e, vec_f32(e)) FROM big WHERE rowid = 2`:  "0",
		`SELECT vec_length(vec_f32(vec_f32(e))) FROM big WHERE rowid = 2`: "500000",
	})
}

// The functions work when Register gives them to every connector.
func TestRegister(t *testing.T) {
	sqlite.Register(vec.Option())
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT vec_length(vec_f32('[1, 2, 3]'))`).Scan(&n); err != nil || n != 3 {
		t.Errorf("vec_length = %d, %v", n, err)
	}
}
