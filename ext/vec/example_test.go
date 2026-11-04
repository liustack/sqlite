// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package vec_test

import (
	"database/sql"
	"fmt"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/vec"
)

func Example() {
	c, err := sqlite.NewConnector(":memory:", vec.Option())
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE doc(title TEXT, embedding BLOB);
		INSERT INTO doc VALUES
			('cats', vec_f32('[0.9, 0.1, 0.0]')),
			('dogs', vec_f32('[0.8, 0.3, 0.1]')),
			('tax law', vec_f32('[0.0, 0.2, 0.9]'))`)
	if err != nil {
		panic(err)
	}
	rows, err := db.Query(`SELECT title, vec_distance_cosine(embedding, vec_f32(?)) AS d
		FROM doc ORDER BY d LIMIT 2`, "[1, 0.2, 0]")
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		var d float64
		if err := rows.Scan(&title, &d); err != nil {
			panic(err)
		}
		fmt.Printf("%s %.3f\n", title, d)
	}
	// Output:
	// cats 0.004
	// dogs 0.020
}
