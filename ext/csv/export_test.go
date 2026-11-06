// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package csv

import "testing"

// SetMaxLength makes n the most bytes of a field, for the test.
func SetMaxLength(t testing.TB, n int) {
	old := maxLength
	maxLength = n
	t.Cleanup(func() { maxLength = old })
}
