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
// population ones for none. A text or a blob is an error, and so is a result
// that does not fit a float64 (the standard deviation of values whose
// variance does not fit may). Large values with a small spread keep their
// digits, integers past 2^53 too, and values near the ends of a float64
// neither overflow nor underflow on the way.
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
// does). It takes each value less a base, the first value (so that large
// values with a small spread keep their digits), times 2^-k (so that their
// squares neither overflow nor underflow).
type moments struct {
	name         string
	sample, root bool
	n            int64
	mean, m2     float64 // of the values less the base, times 2^-k and 2^-2k
	// The base is baseI + baseF. baseI is an int64 that is also a float64,
	// so that an integer value less baseI is exact, even past 2^53, and a
	// float64 value less it is too when near.
	baseI int64
	baseF float64
	// k is set by the first value that differs from the base, and only
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
		m.setBase(x)
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

// setBase makes x the base, as an int64 that is a float64 and the rest.
func (m *moments) setBase(x driver.Value) {
	m.scale = 1
	switch v := x.(type) {
	case int64:
		m.baseI = v
		if v > 1<<53 || v < -(1<<53) {
			m.baseI = v &^ 1023
		}
	case float64:
		switch {
		case math.IsInf(v, 0):
			m.baseF = v
		case v >= 0x1p63:
			m.baseI = math.MaxInt64 &^ 1023
		case v < -0x1p63:
			m.baseI = math.MinInt64
		default:
			m.baseI = int64(math.Trunc(v))
		}
		if !math.IsInf(v, 0) {
			m.baseF = v - float64(m.baseI)
		}
	}
}

// diff is x less the base, times scale, each part scaled before it is
// added so that none overflows.
func (m *moments) diff(x driver.Value, scale float64) float64 {
	switch v := x.(type) {
	case int64:
		// v - baseI is exact unless it overflows an int64.
		if d := v - m.baseI; (v >= m.baseI) == (d >= 0) {
			return float64(d)*scale - m.baseF*scale
		}
		return float64(v)*scale - float64(m.baseI)*scale - m.baseF*scale
	default:
		f := x.(float64)
		return f*scale - float64(m.baseI)*scale - m.baseF*scale
	}
}

// exponent is the k that brings x less the base near 1, within ±maxK.
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
