// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package hash_test

import (
	"database/sql"
	"fmt"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/hash"
)

func Example() {
	c, err := sqlite.NewConnector(":memory:", hash.Option())
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	var sum string
	var crc int64
	err = db.QueryRow(`SELECT lower(hex(sha256('abc'))), crc32('hello')`).Scan(&sum, &crc)
	if err != nil {
		panic(err)
	}
	fmt.Println(sum)
	fmt.Println(crc)
	// Output:
	// ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	// 907060870
}
