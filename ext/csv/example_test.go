// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package csv_test

import (
	"database/sql"
	"fmt"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/csv"
)

func Example() {
	c, err := sqlite.NewConnector(":memory:", csv.Option())
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	db.SetMaxOpenConns(1) // a temp table is on one connection
	_, err = db.Exec(`CREATE VIRTUAL TABLE temp.people USING csv(data='name,age
Ada,36
Linus,21', header=yes)`)
	if err != nil {
		panic(err)
	}
	var name string
	err = db.QueryRow(`SELECT name FROM people WHERE CAST(age AS INTEGER) > 30`).Scan(&name)
	if err != nil {
		panic(err)
	}
	fmt.Println(name)
	// Output: Ada
}
