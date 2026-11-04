// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package csqlite

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// msum is a sum that a window frame moves over: Inverse takes a row out.
// The argument 13 fails Inverse, 14 panics in Value.
type msum struct{ n int64 }

func (s *msum) Step(a []driver.Value) error {
	s.n += a[0].(int64)
	return nil
}

func (s *msum) Inverse(a []driver.Value) error {
	if a[0].(int64) == 13 {
		return errors.New("cannot take 13 out")
	}
	s.n -= a[0].(int64)
	return nil
}

func (s *msum) Value() (driver.Value, error) {
	if s.n == 14 {
		panic("fourteen")
	}
	return s.n, nil
}

func (s *msum) Final() (driver.Value, error) { return s.n, nil }

func TestWindow(t *testing.T) {
	c := open(t, ":memory:")
	if err := c.CreateWindow("msum", 1, FuncDeterministic, func() WindowAgg { return &msum{} }); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(`CREATE TABLE t(x INTEGER); INSERT INTO t VALUES (1), (2), (3), (4), (5)`); err != nil {
		t.Fatal(err)
	}
	before := liveAggs()
	for q, want := range map[string]string{
		// The frame moves: Step adds the row that comes in, Inverse takes
		// out the one that leaves.
		`SELECT group_concat(s) FROM (SELECT msum(x) OVER (ORDER BY x ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING) AS s FROM t)`: "3,6,9,12,9",
		`SELECT group_concat(s) FROM (SELECT msum(x) OVER (ORDER BY x) AS s FROM t)`:                                          "1,3,6,10,15",
		// An empty frame asks Value of a group with no rows.
		`SELECT group_concat(s) FROM (SELECT msum(x) OVER (ORDER BY x ROWS BETWEEN 2 PRECEDING AND 1 PRECEDING) AS s FROM t)`: "0,1,3,5,7",
		// Partitions each have a state of their own.
		`SELECT group_concat(s) FROM (SELECT msum(x) OVER (PARTITION BY x % 2 ORDER BY x) AS s FROM t ORDER BY x)`: "1,2,4,6,9",
		// Still an aggregate without OVER.
		`SELECT msum(x) FROM t`:             "15",
		`SELECT msum(x) FROM t WHERE x > 9`: "0",
	} {
		if got := mustOne(t, c, q); got != want {
			t.Errorf("%s = %s, want %s", q, got, want)
		}
	}
	if n := liveAggs() - before; n != 0 {
		t.Errorf("%d states left", n)
	}
	if err := c.Exec(`INSERT INTO t VALUES (13), (14)`); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT group_concat(s) FROM (SELECT msum(x) OVER (ORDER BY x ROWS CURRENT ROW) AS s FROM t)`:                      "cannot take 13 out",
		`SELECT group_concat(s) FROM (SELECT msum(x) OVER (ORDER BY x ROWS UNBOUNDED PRECEDING) AS s FROM t WHERE x = 14)`: "fourteen",
	} {
		if _, err := one(t, c, q); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", q, err, want)
		}
	}
	if n := liveAggs() - before; n != 0 {
		t.Errorf("%d states left after the failures", n)
	}
	if got := mustOne(t, c, `SELECT count(*) FROM t`); got != "7" {
		t.Errorf("the connection after the failures: %s", got)
	}
}
