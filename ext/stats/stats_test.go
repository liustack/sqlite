// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package stats_test

import (
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
		`SELECT var_pop(x) FROM (SELECT 'a' AS x)`: "var_pop: a string, want a number",
	})
}
