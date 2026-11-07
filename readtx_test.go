// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
)

// A connection in a read transaction that goes on to write while another
// connection holds the write lock is told the database is busy at once, as
// SQLite tells it (it does not call the busy handler then). In rollback
// journal mode the writer that holds the lock waits for this connection's
// read lock to go before it commits, so waiting for it would only end at
// the timeout.
func TestWriteFromReadTx(t *testing.T) {
	for _, mode := range []string{"delete", "wal"} {
		t.Run(mode, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "x.db")
			open := func() *conn {
				cn, err := (&Driver{}).Open("file:" + name + "?_pragma=journal_mode(" + mode + ")&_pragma=busy_timeout(5000)")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cn.Close() })
				return cn.(*conn)
			}
			r, w := open(), open()
			mustExec(t, r, "CREATE TABLE t(x)")
			mustExec(t, r, "INSERT INTO t VALUES (1)")
			mustExec(t, r, "BEGIN")
			queryOne(t, r, "SELECT count(*) FROM t")
			mustExec(t, w, "BEGIN")
			mustExec(t, w, "INSERT INTO t VALUES (2)")

			start := time.Now()
			_, err := r.ExecContext(context.Background(), "INSERT INTO t VALUES (3)", nil)
			if took := time.Since(start); took > time.Second {
				t.Errorf("the reader's write waited %v", took.Round(time.Millisecond))
			}
			var e *csqlite.Error
			if !errors.As(err, &e) || e.Code&0xff != 5 {
				t.Fatalf("the reader's write: %v, want database is locked", err)
			}
			mustExec(t, r, "ROLLBACK")
			mustExec(t, w, "COMMIT")
			if got := queryOne(t, r, "SELECT group_concat(x) FROM t"); got[0] != "1,2" {
				t.Fatalf("rows %v, want 1,2", got[0])
			}
		})
	}
}
