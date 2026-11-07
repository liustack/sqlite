// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package stats gives SQLite the variance and the standard deviation as
// aggregate functions:
//
//	sqlite.NewConnector(dsn, stats.Option())
//
// Then var_samp(x), var_pop(x), stddev_samp(x) and stddev_pop(x), with
// stddev and variance as other names of the sample ones, as in PostgreSQL.
// NULLs are skipped. The sample ones give NULL for fewer than 2 values, the
// population ones for none. A text or a blob is an error.
//
// The result is within a few units in the last place of the exact one
// (Welford's one-pass method, akin to what PostgreSQL and DuckDB use), not
// always the nearest float64. Large values with a small spread keep their digits, integers past
// 2^53 too, and values near the ends of a float64 neither overflow nor
// underflow on the way. A result whose computed value passes the largest
// float64 is an error, as in PostgreSQL. The standard deviation of values
// whose variance does not fit still comes back. Within a few units in the
// last place of the largest float64, the exact result may come back as the
// error, and one just past it as the largest float64.
package stats

import (
	"database/sql/driver"
	"fmt"
	"math"

	"github.com/liustack/sqlite"
)

// Option adds the functions to a connector.
func Option() sqlite.Option {
	agg := func(name string, sample, root bool) sqlite.Option {
		return sqlite.WithAggregate(name, 1, func() sqlite.Aggregate {
			return &moments{name: name, sample: sample, root: root}
		}, sqlite.Deterministic, sqlite.Innocuous)
	}
	return sqlite.Join(
		agg("var_samp", true, false),
		agg("variance", true, false),
		agg("var_pop", false, false),
		agg("stddev_samp", true, true),
		agg("stddev", true, true),
		agg("stddev_pop", false, true),
	)
}

// moments keeps the count, mean and sum of squared differences of the
// values (Welford's method, which does not lose digits as a sum of squares
// does). It takes each value less the first (so that large values with a
// small spread keep their digits), times 2^-k (so that their squares
// neither overflow nor underflow).
type moments struct {
	name         string
	sample, root bool
	n            int64
	mean, m2     float64 // of the values less the first, times 2^-k and 2^-2k
	// The first value, an INTEGER in baseI or a REAL in baseF.
	intBase bool
	baseI   int64
	baseF   float64
	// k is set by the first value that differs from the first, and only
	// grows: a value that would pass big times 2^k raises it.
	k     int
	kSet  bool
	scale float64 // 2^-k
}

const (
	big  = 0x1p450 // n squares of such differences stay finite
	maxK = 600     // 2^-k and 2^k are float64 values, and a difference of two float64 values times 2^-maxK is below big
)

func (m *moments) Step(a []driver.Value) error {
	switch v := a[0].(type) {
	case nil:
		return nil
	case int64, float64:
	default:
		return fmt.Errorf("%s: a %T, want a number", m.name, v)
	}
	x := a[0]
	if m.n == 0 {
		m.scale = 1
		switch v := x.(type) {
		case int64:
			m.intBase, m.baseI = true, v
		case float64:
			m.baseF = v
		}
	}
	y := m.diff(x, m.scale)
	if (!m.kSet && y != 0) || math.Abs(y) >= big || math.IsInf(y, 0) {
		if k := m.exponent(x); !m.kSet || k > m.k {
			m.setK(k)
			y = m.diff(x, m.scale)
		}
	}
	m.n++
	d := y - m.mean
	m.mean += d / float64(m.n)
	m.m2 += d * (y - m.mean)
	return nil
}

// diff is x less the first value, times scale (a power of two).
func (m *moments) diff(x driver.Value, scale float64) float64 {
	switch v := x.(type) {
	case int64:
		if m.intBase {
			return subInts(v, m.baseI) * scale
		}
		return subIntFloat(v, m.baseF) * scale
	default:
		f := x.(float64)
		if m.intBase {
			return -subIntFloat(m.baseI, f) * scale
		}
		// Rounded once, and exact when they are near.
		if d := f - m.baseF; !math.IsInf(d, 0) {
			return d * scale
		}
		return f*scale - m.baseF*scale
	}
}

// subInts is a - b, exact unless far apart.
func subInts(a, b int64) float64 {
	if d := a - b; (a >= b) == (d >= 0) {
		return float64(d)
	}
	return float64(a) - float64(b)
}

// subIntFloat is i - f, rounded once when they are near: f is taken as an
// int64 c and the rest, f - c, which is exact.
func subIntFloat(i int64, f float64) float64 {
	var c int64
	switch {
	case math.Abs(f) < 0x1p63:
		c = int64(f)
	case f >= 0x1p63 && f < 0x1p64:
		// f is 2^63 plus a multiple of 2048, so f - c is an odd multiple of 1024 below 2^63.
		c = math.MaxInt64 &^ 1023
	case f <= -0x1p63 && f > -0x1p64:
		c = math.MinInt64
	default:
		// Far from every int64, or infinite.
		return float64(i) - f
	}
	if d := i - c; (i >= c) == (d >= 0) {
		return float64(d) - (f - float64(c))
	}
	return float64(i) - f
}

func (m *moments) exponent(x driver.Value) int {
	var e int
	if y := m.diff(x, 1); !math.IsInf(y, 0) {
		e = math.Ilogb(y)
	} else {
		e = math.Ilogb(m.diff(x, 0x1p-600)) + 600
	}
	return min(max(e, -maxK), maxK)
}

// setK takes the values from now on times 2^-k, and what they were before
// with them.
func (m *moments) setK(k int) {
	if m.kSet {
		m.mean = math.Ldexp(m.mean, m.k-k)
		m.m2 = math.Ldexp(m.m2, 2*(m.k-k))
	}
	m.k, m.kSet = k, true
	m.scale = math.Ldexp(1, -k)
}

func (m *moments) Final() (driver.Value, error) {
	div := float64(m.n)
	if m.sample {
		div--
	}
	if div <= 0 {
		return nil, nil
	}
	v := m.m2 / div
	if m.root {
		v = math.Ldexp(math.Sqrt(v), m.k)
	} else {
		v = math.Ldexp(v, 2*m.k)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, fmt.Errorf("%s: the values overflow a float64", m.name)
	}
	return v, nil
}
