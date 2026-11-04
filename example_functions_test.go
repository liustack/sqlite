// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/liustack/sqlite"
)

type longest struct{ s string }

func (l *longest) Step(args []driver.Value) error {
	if s, ok := args[0].(string); ok && len(s) > len(l.s) {
		l.s = s
	}
	return nil
}

func (l *longest) Final() (driver.Value, error) { return l.s, nil }

func ExampleWithFunction() {
	reverse := func(args []driver.Value) (driver.Value, error) {
		s, _ := args[0].(string)
		r := []rune(s)
		for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
			r[i], r[j] = r[j], r[i]
		}
		return string(r), nil
	}
	c, err := sqlite.NewConnector(":memory:",
		sqlite.WithFunction("reverse", 1, reverse, sqlite.Deterministic),
		sqlite.WithAggregate("longest", 1, func() sqlite.Aggregate { return &longest{} }),
		sqlite.WithCollation("nocase_go", func(a, b string) int {
			return strings.Compare(strings.ToLower(a), strings.ToLower(b))
		}))
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()

	var r, l, first string
	err = db.QueryRow(`
		WITH w(word) AS (VALUES ('Banana'), ('apple'), ('cherry'))
		SELECT reverse('stressed'), longest(word),
			(SELECT word FROM w ORDER BY word COLLATE nocase_go LIMIT 1)
		FROM w`).Scan(&r, &l, &first)
	if err != nil {
		panic(err)
	}
	fmt.Println(r, l, first)
	// Output: desserts Banana apple
}
