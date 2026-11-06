// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package regexp

import "testing"

// SetMaxLength makes n the most bytes regexp_replace gives, for the test.
func SetMaxLength(t testing.TB, n int) {
	old := maxLength
	maxLength = n
	t.Cleanup(func() { maxLength = old })
}

// Fits is fits, which says whether regexp_replace may give a result.
var Fits = fits
