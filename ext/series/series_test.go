// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package series_test

import (
	"testing"

	"github.com/liustack/sqlite/ext/internal/sqltest"
	"github.com/liustack/sqlite/ext/series"
)

// The results are those of SQLite's own generate_series (the sqlite3 shell
// of 3.51.2), except where it is wrong (see TestDescendingLimit).
func TestSeries(t *testing.T) {
	db := sqltest.Open(t, series.Option())
	gc := func(from string) string { return `SELECT group_concat(value) FROM ` + from }
	sqltest.Want(t, db, map[string]string{
		gc(`generate_series(0,100,5)`):                                        "0,5,10,15,20,25,30,35,40,45,50,55,60,65,70,75,80,85,90,95,100",
		gc(`generate_series(0,10)`):                                           "0,1,2,3,4,5,6,7,8,9,10",
		gc(`(SELECT value FROM generate_series(20) LIMIT 10)`):                "20,21,22,23,24,25,26,27,28,29",
		gc(`generate_series(0,-20,-5)`):                                       "0,-5,-10,-15,-20",
		`SELECT count(*) FROM generate_series(0,-1)`:                          "0",
		gc(`generate_series(1,11,2) WHERE value BETWEEN 4 AND 8`):             "5,7",
		gc(`(SELECT value FROM generate_series(1,15,3) ORDER BY value DESC)`): "13,10,7,4,1",
		gc(`(SELECT value FROM generate_series(15,1,-3) ORDER BY value)`):     "3,6,9,12,15",
		// The step of 1<<63, and the ends of the 64-bit integers.
		gc(`(SELECT value FROM generate_series(9223372036854775807,-9223372036854775808,-9223372036854775808) ORDER BY value)`): "-1,9223372036854775807",
		gc(`generate_series(9223372036854775800,9223372036854775807,3)`):                                                        "9223372036854775800,9223372036854775803,9223372036854775806",
		gc(`generate_series(-9223372036854775808,-9223372036854775800,4)`):                                                      "-9223372036854775808,-9223372036854775804,-9223372036854775800",
		gc(`generate_series WHERE value <= -9223372036854775807 AND value > -1e30`):                                             "-9223372036854775808,-9223372036854775807",
		// Constraints on value in place of start and stop.
		gc(`generate_series WHERE value BETWEEN 3 AND 7`):             "3,4,5,6,7",
		gc(`generate_series WHERE value > 3 AND value < 7`):           "4,5,6",
		gc(`generate_series WHERE value = 42`):                        "42",
		gc(`generate_series WHERE value IS 5`):                        "5",
		gc(`generate_series WHERE value = 4.5`):                       "NULL",
		gc(`generate_series WHERE value = 4.0`):                       "4",
		gc(`generate_series WHERE value >= 2.5 AND value <= 6.5`):     "3,4,5,6",
		gc(`generate_series WHERE value > 2.0 AND value < 6.0`):       "3,4,5",
		gc(`generate_series WHERE value >= 9223372036854775806`):      "NULL",
		gc(`generate_series(1,10) WHERE value > 9223372036854775807`): "NULL",
		gc(`generate_series(1,10) WHERE rowid > 7`):                   "8,9,10",
		gc(`generate_series WHERE start=2 AND stop=8 AND step=2`):     "2,4,6,8",
		// A step of 0 is 1, a REAL is cut, a NULL gives no rows.
		gc(`generate_series(1,5,0)`):       "1,2,3,4,5",
		gc(`generate_series(1.9,5.9,1.9)`): "1,2,3,4,5",
		gc(`generate_series(5,1)`):         "NULL",
		gc(`generate_series(1,NULL)`):      "NULL",
		gc(`generate_series(NULL)`):        "NULL",
		// The hidden columns and the rowid.
		`SELECT group_concat(value || ',' || start || ',' || stop || ',' || step, ';') FROM generate_series(1,7,3)`: "1,1,7,3;4,1,7,3;7,1,7,3",
		`SELECT group_concat(rowid) FROM generate_series(3,6)`:                                                      "3,4,5,6",
		// LIMIT and OFFSET.
		gc(`(SELECT value FROM generate_series(1,100) LIMIT 5 OFFSET 10)`): "11,12,13,14,15",
		gc(`(SELECT value FROM generate_series(1,10) LIMIT 0)`):            "NULL",
		gc(`(SELECT value FROM generate_series(1,10) LIMIT 20 OFFSET 8)`):  "9,10",
		gc(`(SELECT value FROM generate_series(1,10) LIMIT 3 OFFSET 20)`):  "NULL",
		gc(`(SELECT value FROM generate_series(1,10) LIMIT -1 OFFSET 3)`):  "4,5,6,7,8,9,10",
		gc(`(SELECT value FROM generate_series(1) LIMIT 3)`):               "1,2,3",
		// Joins.
		`SELECT group_concat(a.value || '-' || b.value) FROM generate_series(1,3) a, generate_series(a.value, 3) b`:      "1-1,1-2,1-3,2-2,2-3,3-3",
		`SELECT group_concat(b.value) FROM generate_series(1,3) a JOIN generate_series(1,10) b ON b.value = a.value * 2`: "2,4,6",
		`SELECT sum(value) FROM generate_series(1,1000000)`:                                                              "500000500000",
		gc(`generate_series(10,1,-4) WHERE value < 9`):                                                                   "6,2",
		gc(`generate_series(10,1,-4) WHERE value >= 3`):                                                                  "10,6",
	})
	sqltest.Fails(t, db, map[string]string{
		`SELECT count(*) FROM generate_series`:                         `first argument to "generate_series()" missing or unusable`,
		`SELECT count(*) FROM generate_series WHERE value < 5`:         `first argument to "generate_series()" missing or unusable`,
		`SELECT count(*) FROM generate_series('1', 5)`:                 "an argument is TEXT: use CAST(x AS INTEGER)",
		`SELECT count(*) FROM generate_series(1, x'05')`:               "an argument is BLOB",
		`SELECT count(*) FROM generate_series(1, 9) WHERE value = '5'`: "an argument is TEXT",
		`CREATE VIRTUAL TABLE s USING generate_series`:                 "no such module",
		`INSERT INTO generate_series VALUES (1)`:                       "may not be modified",
	})
}

// series.c (3.51.2 and 3.54.1) moves the end of a LIMIT up from the start
// even when the values go down, so the scan never meets it: its asserts stop
// the shell, and without them the scan runs on. This port gives the rows.
func TestDescendingLimit(t *testing.T) {
	db := sqltest.Open(t, series.Option())
	gc := func(from string) string { return `SELECT group_concat(value) FROM ` + from }
	sqltest.Want(t, db, map[string]string{
		gc(`(SELECT value FROM generate_series(1,100,7) ORDER BY value DESC LIMIT 3 OFFSET 2)`): "85,78,71",
		gc(`(SELECT value FROM generate_series(11,2147483647) ORDER BY value DESC LIMIT 3)`):    "2147483647,2147483646,2147483645",
		gc(`(SELECT value FROM generate_series(10,1,-1) LIMIT 3)`):                              "10,9,8",
		gc(`(SELECT value FROM generate_series(1,10,-1) ORDER BY value LIMIT 2 OFFSET 1)`):      "NULL",
		gc(`(SELECT value FROM generate_series(10,1,-3) ORDER BY value LIMIT 2 OFFSET 1)`):      "4,7",
	})
}

// TestRealEnds: a REAL compared with value at the ends of the 64-bit range.
// float64(math.MaxInt64) is 2^63, one past the largest integer, and -2^63 is
// the smallest integer itself. SQLite's own gives 1 and 2 to the first two.
func TestRealEnds(t *testing.T) {
	db := sqltest.Open(t, series.Option())
	const top, bottom = `generate_series(9223372036854775806,9223372036854775807)`, `generate_series(-9223372036854775808,-9223372036854775807)`
	n := func(from, where string) string { return `SELECT count(*) FROM ` + from + ` WHERE ` + where }
	sqltest.Want(t, db, map[string]string{
		n(top, `value = 9223372036854775808.0`):      "0",
		n(top, `value >= 9223372036854775808.0`):     "0",
		n(top, `value > 9223372036854775808.0`):      "0",
		n(top, `value < 9223372036854775808.0`):      "2",
		n(top, `value <= 9223372036854775808.0`):     "2",
		n(bottom, `value > -9223372036854775808.0`):  "1",
		n(bottom, `value >= -9223372036854775808.0`): "2",
		n(bottom, `value <= -9223372036854775808.0`): "1",
		n(bottom, `value < -9223372036854775808.0`):  "0",
		n(bottom, `value = -9223372036854775808.0`):  "1",
	})
}

// TestJoinOnValue: a join that gives the start through value from another
// table. The planner also tries the order with generate_series first, where
// the constraint is not usable: that plan is refused, not the statement.
// SQLite's own fails it.
func TestJoinOnValue(t *testing.T) {
	db := sqltest.Open(t, series.Option())
	sqltest.Want(t, db, map[string]string{
		`WITH t(x) AS (VALUES (1), (2)) SELECT group_concat(s.value) FROM t JOIN generate_series s ON s.value = t.x`:             "1,2",
		`WITH t(x) AS (VALUES (1), (2)) SELECT group_concat(s.value) FROM t CROSS JOIN generate_series s ON s.value = t.x`:       "1,2",
		`WITH t(x) AS (VALUES (5)) SELECT group_concat(s.value) FROM t JOIN generate_series s ON s.value >= t.x AND s.value < 8`: "5,6,7",
	})
}
