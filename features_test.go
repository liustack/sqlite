// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// SQL that reaches many parts of SQLite, each with the answer SQLite gives.
func TestFeatures(t *testing.T) {
	db := openDB(t, "file:"+filepath.Join(t.TempDir(), "f.db")+"?_pragma=journal_mode(WAL)")
	db.SetMaxOpenConns(1)
	setup := []string{
		`CREATE TABLE emp(id INTEGER PRIMARY KEY, name TEXT NOT NULL, dept TEXT, salary REAL, doc TEXT,
			upper_name TEXT GENERATED ALWAYS AS (upper(name)) VIRTUAL) STRICT`,
		`CREATE INDEX emp_dept ON emp(dept, salary)`,
		`INSERT INTO emp(name, dept, salary, doc) VALUES
			('ann', 'eng', 120, '{"tags":["go","c"],"level":3}'),
			('bob', 'eng', 100, '{"tags":["sql"],"level":2}'),
			('cy', 'ops', 90, '{"tags":[],"level":1}'),
			('di', 'ops', 95, NULL)`,
		`CREATE TABLE kv(k TEXT PRIMARY KEY, v INT) WITHOUT ROWID`,
	}
	for _, q := range setup {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	for _, c := range []struct{ q, want string }{
		{`SELECT group_concat(name, ',') FROM (SELECT name FROM emp ORDER BY id)`, "ann,bob,cy,di"},
		{`SELECT upper_name FROM emp WHERE id = 2`, "BOB"},
		{`SELECT json_extract(doc, '$.tags[1]') FROM emp WHERE name = 'ann'`, "c"},
		{`SELECT sum(json_array_length(doc, '$.tags')) FROM emp`, "3"},
		{`SELECT json_group_array(name) FROM emp WHERE dept = 'ops'`, `["cy","di"]`},
		{`SELECT group_concat(name || ':' || r, ' ') FROM (SELECT name, rank() OVER (PARTITION BY dept ORDER BY salary DESC) r FROM emp ORDER BY id)`, "ann:1 bob:2 cy:2 di:1"},
		{`WITH RECURSIVE f(n, x) AS (SELECT 1, 1 UNION ALL SELECT n+1, x*(n+1) FROM f WHERE n < 10) SELECT x FROM f WHERE n = 10`, "3628800"},
		{`SELECT printf('%5.2f|%-4s|%03d|%x', 3.14159, 'ab', 7, 255)`, " 3.14|ab  |007|ff"},
		{`SELECT date('2026-11-04', '+1 month', 'start of month', '-1 day')`, "2026-11-30"},
		{`SELECT strftime('%Y-%j %H:%M', '2026-03-01 13:45:00')`, "2026-060 13:45"},
		{`SELECT julianday('2000-01-01') - julianday('1999-12-31')`, "1"},
		{`SELECT round(avg(salary), 2) FROM emp`, "101.25"},
		{`SELECT typeof(1e308 * 10) || ' ' || (1e308 * 10)`, "real Inf"},
		{`SELECT (9223372036854775807 + 0) || ' ' || (-9223372036854775808 + 0)`, "9223372036854775807 -9223372036854775808"},
		{`SELECT hex(zeroblob(4)) || length(randomblob(0))`, "000000001"},
		{`SELECT like('A%', 'abc'), glob('a*', 'abc'), 'x' REGEXP 'x' IS NULL`, ""},
		{`SELECT substr('héllo wörld', 2, 4) || length('héllo') || instr('abcabc', 'ca')`, "éllo53"},
		{`SELECT count(*) FROM emp WHERE dept = 'eng' AND salary > 99`, "2"},
		{`SELECT quote(x'00ff') || ' ' || quote('it''s')`, "X'00FF' 'it''s'"},
		{`SELECT sqlite_version()`, "3.54.1"},
	} {
		var got sql.NullString
		err := db.QueryRow(c.q).Scan(&got)
		if c.want == "" { // only the error matters: REGEXP has no function
			if err == nil {
				t.Errorf("%s: no error", c.q)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.q, err)
			continue
		}
		if got.String != c.want {
			t.Errorf("%s = %q, want %q", c.q, got.String, c.want)
		}
	}
	// Upsert, RETURNING, savepoints, rollback.
	var v int
	for i := 0; i < 3; i++ {
		if err := db.QueryRow(`INSERT INTO kv VALUES ('a', 1) ON CONFLICT(k) DO UPDATE SET v = v + 1 RETURNING v`).Scan(&v); err != nil {
			t.Fatal(err)
		}
	}
	if v != 3 {
		t.Errorf("upsert v = %d", v)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SAVEPOINT s`, `UPDATE kv SET v = 100`, `ROLLBACK TO s`, `UPDATE kv SET v = v * 10`, `RELEASE s`} {
		if _, err := tx.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.Begin()
	tx.Exec(`DELETE FROM kv`)
	tx.Rollback()
	if err := db.QueryRow(`SELECT v FROM kv`).Scan(&v); err != nil || v != 30 {
		t.Errorf("after savepoints and rollback v = %d, %v", v, err)
	}
	// STRICT refuses text in a REAL column.
	if _, err := db.Exec(`INSERT INTO emp(name, salary) VALUES ('x', 'lots')`); err == nil {
		t.Error("STRICT took text into a REAL column")
	}
	// The integrity of the file after all of it.
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Errorf("integrity_check: %q %v", ok, err)
	}
}
