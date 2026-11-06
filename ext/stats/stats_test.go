// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package stats_test

import (
	"math"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/liustack/sqlite/ext/internal/sqltest"
	"github.com/liustack/sqlite/ext/stats"
)

func TestStats(t *testing.T) {
	db := sqltest.Open(t, stats.Option())
	if _, err := db.Exec(`CREATE TABLE t(g, x);
		INSERT INTO t VALUES (1, 2), (1, 4), (1, 4), (1, 4), (1, 5), (1, 5), (1, 7), (1, 9), (1, NULL),
			(2, 1e9 + 4), (2, 1e9 + 7), (2, 1e9 + 13), (2, 1e9 + 16), (3, 42), (4, NULL)`); err != nil {
		t.Fatal(err)
	}
	// A REAL scanned into a string is Go's text of it ("4"), and SQLite's
	// own is in group_concat ("4.0").
	sqltest.Want(t, db, map[string]string{
		// 2, 4, 4, 4, 5, 5, 7, 9: mean 5, squared differences 32.
		`SELECT var_pop(x) FROM t WHERE g = 1`:     "4",
		`SELECT stddev_pop(x) FROM t WHERE g = 1`:  "2",
		`SELECT var_samp(x) FROM t WHERE g = 1`:    "4.571428571428571",
		`SELECT variance(x) FROM t WHERE g = 1`:    "4.571428571428571",
		`SELECT stddev_samp(x) FROM t WHERE g = 1`: "2.138089935299395",
		`SELECT stddev(x) FROM t WHERE g = 1`:      "2.138089935299395",
		// Large values with a small spread keep their digits.
		`SELECT var_samp(x) FROM t WHERE g = 2`:                                                     "30",
		`SELECT var_samp(x) FROM t WHERE g = 3`:                                                     "NULL",
		`SELECT var_pop(x) FROM t WHERE g = 3`:                                                      "0",
		`SELECT var_pop(x) FROM t WHERE g = 4`:                                                      "NULL",
		`SELECT var_pop(x) FROM t WHERE 0`:                                                          "NULL",
		`SELECT group_concat(v, ',') FROM (SELECT g, var_pop(x) AS v FROM t GROUP BY g ORDER BY g)`: "4.0,22.5,0.0",
	})
	sqltest.Fails(t, db, map[string]string{
		`SELECT var_pop(x) FROM (SELECT 'a' AS x)`:                 "var_pop: a string, want a number",
		`SELECT var_samp(column1) FROM (VALUES (1e308), (-1e308))`: "var_samp: the values overflow a float64",
		`SELECT var_pop(column1) FROM (VALUES (9e999), (1))`:       "var_pop: the values overflow a float64",
	})
}

// TestRange: values whose squared differences pass a float64 still give
// their variance when it fits, and their standard deviation when it fits.
func TestRange(t *testing.T) {
	db := sqltest.Open(t, stats.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT var_pop(column1) FROM (VALUES (1e154), (-1e154))`:                         "1e+308",
		`SELECT stddev_pop(column1) FROM (VALUES (1e154), (-1e154))`:                      "1e+154",
		`SELECT var_samp(column1) FROM (VALUES (1e154), (-1e154), (0.0))`:                 "1e+308",
		`SELECT var_pop(column1) FROM (VALUES (1.5e154), (-1.5e154), (0), (0), (0), (0))`: "7.500000000000001e+307",
		`SELECT stddev_pop(column1) FROM (VALUES (1e200), (-1e200))`:                      "1e+200",
		`SELECT stddev_samp(column1) FROM (VALUES (1e308), (-1e308))`:                     "1.4142135623730951e+308",
	})
	sqltest.Fails(t, db, map[string]string{
		`SELECT var_pop(column1) FROM (VALUES (1e200), (-1e200))`: "var_pop: the values overflow a float64",
		`SELECT stddev_pop(column1) FROM (VALUES (9e999), (1))`:   "stddev_pop: the values overflow a float64",
	})
}

// TestBigIntegers: integers past 2^53 that differ little keep their
// differences.
func TestBigIntegers(t *testing.T) {
	db := sqltest.Open(t, stats.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT var_pop(column1) FROM (VALUES (9007199254740993), (9007199254740994))`:          "0.25",
		`SELECT var_samp(column1) FROM (VALUES (9223372036854775807), (9223372036854775806))`:   "0.5",
		`SELECT var_samp(column1) FROM (VALUES (-9223372036854775808), (-9223372036854775807))`: "0.5",
		`SELECT var_samp(column1) FROM (VALUES (-9223372036854775808), (9223372036854775807))`:  "1.7014118346046923e+38",
	})
}

// TestAccuracy compares with the exact variance, computed with big.Float, on
// data that loses digits in a plain sum of squares.
func TestAccuracy(t *testing.T) {
	db := sqltest.Open(t, stats.Option())
	r := rand.New(rand.NewPCG(1, 2))
	for _, c := range []struct {
		name string
		gen  func() any
	}{
		{"int64 near 2^62", func() any { return int64(1<<62) + r.Int64N(1<<20) }},
		{"1e9 plus a fraction", func() any { return 1e9 + r.Float64() }},
		{"mixed signs", func() any { return (r.Float64() - 0.5) * 1e6 }},
	} {
		if _, err := db.Exec(`DROP TABLE IF EXISTS d; CREATE TABLE d(x)`); err != nil {
			t.Fatal(err)
		}
		var vals []any
		for range 10000 {
			v := c.gen()
			vals = append(vals, v)
			if _, err := db.Exec(`INSERT INTO d VALUES (?)`, v); err != nil {
				t.Fatal(err)
			}
		}
		var got float64
		if err := db.QueryRow(`SELECT var_pop(x) FROM d`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		want := exactVarPop(vals)
		if e := math.Abs(got-want) / want; e > 1e-12 {
			t.Errorf("%s: %g, exact %g, relative error %g", c.name, got, want, e)
		}
	}
}

func exactVarPop(vals []any) float64 {
	f := func(v any) *big.Float {
		x := new(big.Float).SetPrec(1024)
		switch v := v.(type) {
		case int64:
			x.SetInt64(v)
		case float64:
			x.SetFloat64(v)
		}
		return x
	}
	n := new(big.Float).SetPrec(1024).SetInt64(int64(len(vals)))
	sum := new(big.Float).SetPrec(1024)
	for _, v := range vals {
		sum.Add(sum, f(v))
	}
	mean := new(big.Float).SetPrec(1024).Quo(sum, n)
	m2 := new(big.Float).SetPrec(1024)
	for _, v := range vals {
		d := f(v)
		d.Sub(d, mean)
		m2.Add(m2, d.Mul(d, d))
	}
	out, _ := m2.Quo(m2, n).Float64()
	return out
}
