// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package sqlite_test

import (
	"context"
	"testing"
)

func TestQueryWriteAfterPreparedPragma(t *testing.T) {
	c := oneConn(t)
	connExec(t, c, `PRAGMA foreign_keys=OFF; CREATE TABLE p(id INTEGER PRIMARY KEY); CREATE TABLE child(pid REFERENCES p(id))`)
	const q = `INSERT INTO child VALUES (?) RETURNING pid`
	if err := drainQuery(c, q, 11); err != nil {
		t.Fatal(err)
	}
	s, err := c.PrepareContext(context.Background(), `PRAGMA foreign_keys=ON`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := drainQuery(c, q, 22); err == nil {
		t.Fatal("a row with no parent went in with foreign_keys on")
	}
}

func TestQueryWriteAfterDeferForeignKeys(t *testing.T) {
	c := oneConn(t)
	connExec(t, c, `PRAGMA foreign_keys=ON; CREATE TABLE t(id INTEGER PRIMARY KEY, p REFERENCES t(id) ON DELETE RESTRICT);
		INSERT INTO t VALUES (1, NULL), (2, 1); PRAGMA defer_foreign_keys=ON`)
	const q = `DELETE FROM t RETURNING id`
	if err := drainQuery(c, q); err != nil {
		t.Fatal(err)
	}
	connExec(t, c, `INSERT INTO t VALUES (1, NULL), (2, 1)`)
	if err := drainQuery(c, q); err == nil {
		t.Fatal("DELETE removed a parent ON DELETE RESTRICT keeps")
	}
}
