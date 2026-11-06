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
// digits, integers past 2^53 too.
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
// does), of the values less the first one (so that large values with a small
// spread keep their digits) and times scale (so that the squares of a large
// spread do not overflow). Integers are taken less the first one as
// integers, so that two past 2^53 that differ by 1 still do.
type moments struct {
	name         string
	sample, root bool
	n            int64
	mean, m2     float64
	// shift is the first value, as a float64, and as an int64 too when it is
	// one (shiftI, intShift), rounded down to a multiple of 1024 so that the
	// two are equal.
	shiftI   int64
	intShift bool
	shift    float64
	// scale is 1 until a difference passes big, then 2^-600 (exact, as a
	// power of 2): the sum of squares is kept as m2 times 2^1200.
	scale float64
}

const (
	big       = 0x1p450 // the square of n such differences stays finite
	scaleDown = 0x1p-600
	scaleExp  = 600
)

func (m *moments) Step(a []driver.Value) error {
	var y float64
	switch v := a[0].(type) {
	case nil:
		return nil
	case int64:
		if m.n == 0 {
			m.shiftI, m.intShift = v&^1023, true
			m.shift = float64(m.shiftI)
			m.scale = 1
		}
		// v - shiftI is exact unless it overflows an int64.
		if d := v - m.shiftI; m.intShift && (v >= m.shiftI) == (d >= 0) {
			y = float64(d)
		} else {
			y = float64(v) - m.shift
		}
		y *= m.scale
	case float64:
		if m.n == 0 {
			m.shift = v
			m.scale = 1
		}
		y = (v - m.shift) * m.scale
		if math.IsInf(y, 0) && !math.IsInf(v, 0) {
			m.scaleDown()
			y = v*m.scale - m.shift*m.scale
		}
	default:
		return fmt.Errorf("%s: a %T, want a number", m.name, v)
	}
	m.n++
	d := y - m.mean
	if math.Abs(d) >= big && m.scale == 1 {
		m.scaleDown()
		y *= m.scale
		d = y - m.mean
	}
	m.mean += d / float64(m.n)
	m.m2 += d * (y - m.mean)
	return nil
}

// scaleDown keeps from now on the values times 2^-600.
func (m *moments) scaleDown() {
	m.scale = scaleDown
	m.mean *= scaleDown
	m.m2 *= scaleDown * scaleDown
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
	if m.scale != 1 {
		if m.root {
			v = math.Ldexp(math.Sqrt(v), scaleExp)
		} else {
			v = math.Ldexp(v, 2*scaleExp)
		}
	} else if m.root {
		v = math.Sqrt(v)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, fmt.Errorf("%s: the values overflow a float64", m.name)
	}
	return v, nil
}
