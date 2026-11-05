// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"errors"
	"testing"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/capture"
)

func TestStartFrom(t *testing.T) {
	const a, b, c, x = ltx.ChecksumFlag | 1, ltx.ChecksumFlag | 2, ltx.ChecksumFlag | 3, ltx.ChecksumFlag | 9
	chain := []capture.StagedFile{{Min: 3, TXID: 3, Pre: a, Post: b}, {Min: 4, TXID: 4, Pre: b, Post: c}}
	from1 := []capture.StagedFile{{Min: 1, TXID: 1, Post: a}, {Min: 2, TXID: 2, Pre: a, Post: b}}
	restart := func(txid ltx.TXID, post ltx.Checksum) *capture.Restart {
		return &capture.Restart{TXID: txid, Post: post}
	}
	for _, tc := range []struct {
		name   string
		end    remoteEnd
		staged []capture.StagedFile
		fresh  bool
		want   *capture.Restart
		err    error
	}{
		{"empty replica, nothing staged", remoteEnd{}, nil, false, nil, nil},
		{"empty replica, staged from 1", remoteEnd{}, from1, false, nil, nil},
		{"empty replica, staged for another", remoteEnd{}, chain, false, restart(0, 0), nil},
		{"replica at the last staged", remoteEnd{4, c}, chain, false, nil, nil},
		{"replica inside the staged", remoteEnd{3, b}, chain, false, nil, nil},
		{"replica just before the staged", remoteEnd{2, a}, chain, false, nil, nil},
		{"replica after the staged", remoteEnd{5, x}, chain, false, nil, ErrDiverged},
		{"replica before the staged, a gap", remoteEnd{1, x}, chain, false, nil, ErrDiverged},
		{"same TXID, another checksum", remoteEnd{4, x}, chain, false, nil, ErrDiverged},
		{"next TXID, another checksum", remoteEnd{2, x}, chain, false, nil, ErrDiverged},
		{"nothing staged", remoteEnd{4, c}, nil, false, nil, ErrDiverged},
		{"apart, fresh start", remoteEnd{5, x}, chain, true, restart(5, x), nil},
		{"nothing staged, fresh start", remoteEnd{4, c}, nil, true, restart(4, c), nil},
		{"on, with fresh start", remoteEnd{4, c}, chain, true, nil, nil},
	} {
		got, err := startFrom(tc.end, tc.staged, tc.fresh)
		if !errors.Is(err, tc.err) || (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("%s: %+v %v, want %+v %v", tc.name, got, err, tc.want, tc.err)
		}
	}
}
