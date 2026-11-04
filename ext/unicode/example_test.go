// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package unicode_test

import (
	"database/sql"
	"fmt"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/unicode"
)

func Example() {
	c, err := sqlite.NewConnector(":memory:", unicode.Option())
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	var upper string
	var same bool
	err = db.QueryRow(`SELECT unicode_upper('größe'), 'ÉCOLE' = 'école' COLLATE unicode_nocase`).Scan(&upper, &same)
	if err != nil {
		panic(err)
	}
	fmt.Println(upper, same)
	// Output: GRÖßE true
}
