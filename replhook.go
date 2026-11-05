// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"context"
	"errors"
	"math"

	"github.com/liustack/sqlite/internal/replhook"
)

func init() {
	replhook.Attach = attach
	replhook.Lock = lockFile
}

// attach is replhook.Attach.
func attach(path string, committed func(frames uint32)) (detach func(), err error) {
	file, err := fileOf(path)
	if err != nil {
		return nil, err
	}
	writeLocks.Lock()
	defer writeLocks.Unlock()
	switch l := findWriteLock(file); {
	case l != nil && l.attached:
		return nil, errors.New("sqlite: " + path + " is replicated already")
	case l != nil:
		return nil, errors.New("sqlite: " + path + " is open: replication starts before the database is opened")
	}
	l := &writeLock{refs: 1, attached: true, file: file}
	if committed != nil {
		l.committed.Store(&committed)
	}
	writeLocks.all = append(writeLocks.all, l)
	return func() {
		writeLocks.Lock()
		l.attached = false
		l.committed.Store(nil)
		writeLocks.Unlock()
		closeWriteLock(l)
	}, nil
}

// lockFile is replhook.Lock.
func lockFile(ctx context.Context, path string) (unlock func(), err error) {
	file, err := fileOf(path)
	if err != nil {
		return nil, err
	}
	writeLocks.Lock()
	l := findWriteLock(file)
	writeLocks.Unlock()
	if l == nil || !l.attached {
		return nil, errors.New("sqlite: " + path + " is not replicated")
	}
	if err := l.acquire(ctx, math.MaxInt64); err != nil {
		return nil, err
	}
	return func() { l.release(nil) }, nil
}
