// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package uuid_test

import (
	"database/sql"
	"fmt"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/uuid"
)

func Example() {
	c, err := sqlite.NewConnector(":memory:", uuid.Option())
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	var n int
	var canonical string
	err = db.QueryRow(`SELECT length(uuid()), uuid_str('{A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11}')`).Scan(&n, &canonical)
	if err != nil {
		panic(err)
	}
	fmt.Println(n, canonical)
	// Output: 36 a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11
}
