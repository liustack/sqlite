// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/internal/csqlite"
	"github.com/liustack/sqlite/internal/replhook"
)

// SnapshotDir is the folder of the snapshots of the database path.
func SnapshotDir(path string) string { return filepath.Join(path+"-replica", "snapshot") }

// Snapshot writes a snapshot of the database, the LTX file of TXIDs 1 to
// the last staged, into SnapshotDir, and returns its name and that TXID.
// Under the driver's write lock it stages the WAL to its end and opens a
// read transaction on a connection of its own. The pages are read after
// the lock, so that writers do not wait for them, and their checksum must
// be the one of the staged files. Checkpoints cannot reset the WAL while
// the pages are read.
func (c *Capturer) Snapshot(ctx context.Context) (string, ltx.TXID, error) {
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	txid, post, err := c.pin(ctx)
	if err != nil {
		return "", 0, err
	}
	defer c.snap.Exec("ROLLBACK")
	size, err := c.snap.PageCount()
	if err != nil {
		return "", 0, err
	}
	dir := SnapshotDir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	h := ltx.Header{
		Version:   ltx.Version,
		PageSize:  uint32(c.pageSize),
		Commit:    size,
		MinTXID:   1,
		MaxTXID:   txid,
		Timestamp: time.Now().UnixMilli(),
	}
	lock := ltx.LockPgno(h.PageSize)
	page := make([]byte, c.pageSize)
	summer := newPageSummer()
	placed, err := stageFile(dir, c.mode, h, func(put func(uint32, []byte) error) (ltx.Checksum, error) {
		var sum ltx.Checksum
		for p := uint32(1); p <= size; p++ {
			if p == lock {
				continue
			}
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if err := c.snap.ReadPage(p, page); err != nil {
				return 0, err
			}
			sum ^= summer.sum(p, page)
			if err := put(p, page); err != nil {
				return 0, err
			}
		}
		sum |= ltx.ChecksumFlag
		if sum != post {
			return 0, fmt.Errorf("capture: the snapshot of TXID %s has checksum %s, the staged files %s", txid, sum, post)
		}
		return sum, nil
	})
	name := filepath.Join(dir, ltx.FormatFilename(1, txid))
	if err != nil {
		if placed {
			os.Remove(name)
		}
		return "", 0, err
	}
	return name, txid, nil
}

// pin stages the WAL to its end under the driver's write lock and opens a
// read transaction there on c.snap. It returns the TXID staged and the
// checksum of the database there.
func (c *Capturer) pin(ctx context.Context) (ltx.TXID, ltx.Checksum, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, 0, errors.New("capture: closed")
	}
	if c.snap == nil {
		conn, err := csqlite.Open(c.path, csqlite.OpenReadOnly|csqlite.OpenNoMutex, "")
		if err != nil {
			return 0, 0, err
		}
		c.snap = conn
	}
	unlock, err := replhook.Lock(ctx, c.path)
	if err != nil {
		return 0, 0, err
	}
	defer unlock()
	if _, err := c.capture(); err != nil {
		return 0, 0, err
	}
	if c.lost != nil {
		return 0, 0, fmt.Errorf("capture: no snapshot until the WAL is followed again: %w", c.lost)
	}
	// The snapshot waits for the staged files up to it: their names are
	// kept first.
	if err := syncDir(c.dir); err != nil {
		return 0, 0, err
	}
	c.setDurable(c.txid)
	if err := beginRead(c.snap); err != nil {
		return 0, 0, err
	}
	return c.txid, c.post, nil
}
