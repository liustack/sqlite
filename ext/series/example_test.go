// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package series_test

import (
	"database/sql"
	"fmt"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/series"
)

func Example() {
	c, err := sqlite.NewConnector(":memory:", series.Option())
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	rows, err := db.Query(`SELECT value FROM generate_series(0, 20, 5)`)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			panic(err)
		}
		fmt.Print(v, " ")
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	fmt.Println()
	// Output: 0 5 10 15 20
}
