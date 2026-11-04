// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package hash gives SQLite hash functions:
//
//	sqlite.NewConnector(dsn, hash.Option())
//
// Then md5(x), sha1(x), sha256(x), sha512(x) and crc32(x). Each hashes the
// bytes of a text or a blob (an integer as its decimal text) and gives the
// hash as a blob, crc32 as an integer. Use hex() for text:
// hex(sha256('abc')). NULL gives NULL. md5 and sha1 are broken for
// security: use them to find copies, not to check what someone sent.
package hash

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql/driver"
	"hash/crc32"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/ext/internal/arg"
)

// Option adds the functions to a connector.
func Option() sqlite.Option {
	return sqlite.Join(
		digest("md5", func(b []byte) []byte { h := md5.Sum(b); return h[:] }),
		digest("sha1", func(b []byte) []byte { h := sha1.Sum(b); return h[:] }),
		digest("sha256", func(b []byte) []byte { h := sha256.Sum256(b); return h[:] }),
		digest("sha512", func(b []byte) []byte { h := sha512.Sum512(b); return h[:] }),
		sqlite.WithFunction("crc32", 1, func(a []driver.Value) (driver.Value, error) {
			b, null, err := arg.Bytes("crc32", 0, a[0])
			if null || err != nil {
				return nil, err
			}
			return int64(crc32.ChecksumIEEE(b)), nil
		}, sqlite.Deterministic, sqlite.Innocuous),
	)
}

func digest(name string, sum func([]byte) []byte) sqlite.Option {
	return sqlite.WithFunction(name, 1, func(a []driver.Value) (driver.Value, error) {
		b, null, err := arg.Bytes(name, 0, a[0])
		if null || err != nil {
			return nil, err
		}
		return sum(b), nil
	}, sqlite.Deterministic, sqlite.Innocuous)
}
