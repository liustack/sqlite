// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import "context"

// lock is a mutex whose wait a context can end. Make it with newLock.
type lock chan struct{}

func newLock() lock { return make(lock, 1) }

func (l lock) Lock() { l <- struct{}{} }

func (l lock) Unlock() { <-l }

func (l lock) TryLock() bool {
	select {
	case l <- struct{}{}:
		return true
	default:
		return false
	}
}

// LockCtx takes l, or gives ctx's error when ctx ends first.
func (l lock) LockCtx(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
