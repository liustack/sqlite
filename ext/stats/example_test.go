// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package stats_test

import (
	"database/sql"
	"fmt"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/stats"
)

func Example() {
	c, err := sqlite.NewConnector(":memory:", stats.Option())
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	var samp, pop float64
	err = db.QueryRow(`SELECT stddev(column1), stddev_pop(column1) FROM (VALUES (2), (4), (4), (4), (5), (5), (7), (9))`).Scan(&samp, &pop)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%.4f %.4f\n", samp, pop)
	// Output: 2.1381 2.0000
}
