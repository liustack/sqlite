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
// does).
type moments struct {
	name         string
	sample, root bool
	n            int64
	mean, m2     float64
}

func (m *moments) Step(a []driver.Value) error {
	var x float64
	switch v := a[0].(type) {
	case nil:
		return nil
	case int64:
		x = float64(v)
	case float64:
		x = v
	default:
		return fmt.Errorf("%s: a %T, want a number", m.name, v)
	}
	m.n++
	d := x - m.mean
	m.mean += d / float64(m.n)
	m.m2 += d * (x - m.mean)
	return nil
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
		v = math.Sqrt(v)
	}
	return v, nil
}
