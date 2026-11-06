// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package stats_test

import (
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
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

// TestEdges: values that mix a base, a scale and the ends of int64 and
// float64.
func TestEdges(t *testing.T) {
	db := sqltest.Open(t, stats.Option())
	sqltest.Want(t, db, map[string]string{
		// A second overflow after the scale is set leaves it as it was.
		`SELECT stddev_pop(column1) FROM (VALUES (1.7976931348623157e308), (-1.7976931348623157e308), (-1.7976931348623157e308), (1.7976931348623157e308))`: "1.7976931348623157e+308",
		// The sum of squares, scaled down, does not round to zero.
		`SELECT stddev_pop(column1) FROM (VALUES (1e300), (-1e300), (1e-300))`: "8.164965809277261e+299",
		// Small values neither underflow.
		`SELECT stddev_pop(column1) FROM (VALUES (0), (2e-200))`: "1e-200",
		`SELECT var_pop(column1) FROM (VALUES (0), (2e-150))`:    "1e-300",
		`SELECT stddev_pop(column1) FROM (VALUES (0), (1e-320))`: "5e-321",
		// An INTEGER base keeps the fraction of a REAL near it.
		`SELECT var_pop(column1) FROM (VALUES (-1), (-0.9999999999999716))`: "2.0194839173657902e-28",
		// A REAL base that is an integer keeps INTEGER values past 2^53.
		`SELECT var_pop(column1) FROM (VALUES (9007199254740992.0), (9007199254740993))`:                              "0.25",
		`SELECT var_pop(column1) FROM (VALUES (9.2233720368547758e18), (9223372036854775807), (9223372036854775806))`: "0.6666666666666666",
	})
}

// TestRandomEdges compares with the exact variance on short runs of values
// drawn from the ends of the ranges.
func TestRandomEdges(t *testing.T) {
	db := sqltest.Open(t, stats.Option())
	r := rand.New(rand.NewPCG(3, 4))
	pick := []func() any{
		func() any { return int64(1<<53) + r.Int64N(16) - 8 },
		func() any { return float64(int64(1<<53) + r.Int64N(16) - 8) },
		func() any { return math.MaxInt64 - r.Int64N(4096) },
		func() any { return math.MinInt64 + r.Int64N(4096) },
		func() any { return float64(r.Int64N(5) - 2) },
		func() any { return float64(r.Int64N(5)-2) + r.Float64()*0x1p-40 },
		func() any { return r.Int64N(5) - 2 },
		func() any { return math.MaxFloat64 * (2*r.Float64() - 1) },
		func() any { return math.Ldexp(2*r.Float64()-1, 450+r.IntN(10)-5) },
		func() any { return math.Ldexp(2*r.Float64()-1, -1074+r.IntN(60)) },
		func() any { return math.Ldexp(2*r.Float64()-1, r.IntN(2046)-1022) },
		func() any { return 0x1p63 * (1 - r.Float64()*0x1p-50) },
		func() any { return -0x1p63 },
	}
	fns := []string{"var_pop", "stddev_pop", "var_samp", "stddev_samp"}
	for i := range 20000 {
		n := 2 + r.IntN(5)
		vals := make([]any, n)
		q := `FROM (VALUES (?)`
		for j := range vals {
			vals[j] = pick[r.IntN(len(pick))]()
			if j > 0 {
				q += `, (?)`
			}
		}
		q += `)`
		want := exactMoments(vals)
		for k, w := range want {
			var got float64
			err := db.QueryRow(`SELECT `+fns[k]+`(column1) `+q, vals...).Scan(&got)
			if math.IsInf(w, 0) {
				if err == nil {
					t.Errorf("case %d %v: result %d = %g, want an overflow", i, vals, k, got)
				}
				continue
			}
			if err != nil {
				if w < math.MaxFloat64*(1-1e-12) {
					t.Errorf("case %d %v: result %d: %v, want %g", i, vals, k, err, w)
				}
				continue
			}
			if !close(got, w) {
				t.Errorf("case %d %v: result %d = %g, want %g", i, vals, k, got, w)
			}
		}
		if t.Failed() {
			return
		}
	}
}

func close(got, want float64) bool {
	const tiny = 0x1p-1022
	if math.Abs(want) < tiny {
		// A subnormal result keeps fewer bits.
		return math.Abs(got-want) <= 4*0x1p-1074
	}
	return math.Abs(got-want) <= 1e-12*math.Abs(want)
}

// exactMoments is var_pop, stddev_pop, var_samp and stddev_samp of vals,
// rounded once to a float64 (+Inf when past it).
func exactMoments(vals []any) [4]float64 {
	const prec = 4096
	f := func(v any) *big.Float {
		x := new(big.Float).SetPrec(prec)
		switch v := v.(type) {
		case int64:
			x.SetInt64(v)
		case float64:
			x.SetFloat64(v)
		}
		return x
	}
	n := new(big.Float).SetPrec(prec).SetInt64(int64(len(vals)))
	sum := new(big.Float).SetPrec(prec)
	for _, v := range vals {
		sum.Add(sum, f(v))
	}
	mean := new(big.Float).SetPrec(prec).Quo(sum, n)
	m2 := new(big.Float).SetPrec(prec)
	for _, v := range vals {
		d := f(v)
		d.Sub(d, mean)
		m2.Add(m2, d.Mul(d, d))
	}
	var out [4]float64
	for i, div := range []int64{int64(len(vals)), int64(len(vals)) - 1} {
		v := new(big.Float).SetPrec(prec).Quo(m2, new(big.Float).SetInt64(div))
		out[2*i], _ = v.Float64()
		out[2*i+1], _ = new(big.Float).SetPrec(prec).Sqrt(v).Float64()
	}
	return out
}

// TestNeighbors: values a few steps of a float64 apart keep the exact
// differences between them, wherever they are, together with integers when
// they are near.
func TestNeighbors(t *testing.T) {
	db := sqltest.Open(t, stats.Option())
	check := func(name string, vals []any) {
		t.Helper()
		q := `FROM (VALUES (?)` + strings.Repeat(`, (?)`, len(vals)-1) + `)`
		for k, fn := range []string{"var_pop", "stddev_pop", "var_samp", "stddev_samp"} {
			w := exactMoments(vals)[k]
			var got float64
			err := db.QueryRow(`SELECT `+fn+`(column1) `+q, vals...).Scan(&got)
			switch {
			case math.IsInf(w, 0):
				if err == nil {
					t.Errorf("%s %v: %s = %g, want an overflow", name, vals, fn, got)
				}
			case err != nil:
				t.Errorf("%s %v: %s: %v, want %g", name, vals, fn, err, w)
			case !close(got, w):
				t.Errorf("%s %v: %s = %g, want %g", name, vals, fn, got, w)
			}
		}
	}
	for _, vals := range [][]any{
		{0x1p64, 0x1p64 - 2048},
		{0x1p64 - 2048, 0x1p64},
		{-0x1p116 - 2*0x1p64, -0x1p116 - 3*0x1p64},
		{-0x1p116 - 3*0x1p64, -0x1p116 - 2*0x1p64},
		// The scale changes with a sum of squares already there.
		{0.0, 1.0, 0x1p449, -0x1p449, 0x1p450},
		{0.0, 0x1p-600, 0x1p-151, -0x1p-151, 0x1p-150},
	} {
		check("case", vals)
	}
	r := rand.New(rand.NewPCG(7, 8))
	for i := range 20000 {
		var x float64
		switch r.IntN(4) {
		case 0:
			x = math.Ldexp(1, r.IntN(2098)-1074)
		case 1:
			x = math.Ldexp(1, 52+r.IntN(80))
		case 2:
			x = math.Ldexp(1+r.Float64(), r.IntN(2046)-1022)
		default:
			x = float64(r.Int64())
		}
		if r.IntN(2) == 0 {
			x = -x
		}
		vals := make([]any, 2+r.IntN(4))
		for j := range vals {
			y := x
			for range r.IntN(4) {
				y = math.Nextafter(y, math.Inf(1-2*r.IntN(2)))
			}
			if math.IsInf(y, 0) {
				y = x
			}
			vals[j] = y
			if a := math.Abs(y); a < 0x1p63 && y == math.Trunc(y) && r.IntN(3) == 0 {
				vals[j] = int64(y) + r.Int64N(5) - 2
			}
		}
		check(fmt.Sprint("neighbors ", i), vals)
		if t.Failed() {
			return
		}
	}
}
