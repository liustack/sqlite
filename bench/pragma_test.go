// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package bench

import (
	"context"
	"strings"
	"testing"
)

// TestPragmas checks that the DSN settings reach every pooled connection.
func TestPragmas(t *testing.T) {
	db := open(t)
	db.SetMaxOpenConns(3)
	ctx := context.Background()
	// Hold three connections at once so the pool opens three distinct ones.
	var conns []interface{ Close() error }
	for i := 0; i < 3; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var mode string
		var sync, busy, fk int
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(mode, "wal") || sync != 1 || busy != 5000 || fk != 0 {
			t.Errorf("conn %d: journal_mode=%s synchronous=%d busy_timeout=%d foreign_keys=%d, want wal 1 5000 0",
				i, mode, sync, busy, fk)
		}
	}
	for _, c := range conns {
		c.Close()
	}
}
