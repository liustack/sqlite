// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package hash_test

import (
	"testing"

	"github.com/liustack/sqlite/ext/hash"
	"github.com/liustack/sqlite/ext/internal/sqltest"
)

// The vectors of RFC 1321, FIPS 180 and the CRC-32 check value.
func TestHash(t *testing.T) {
	db := sqltest.Open(t, hash.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT lower(hex(md5('abc')))`:            "900150983cd24fb0d6963f7d28e17f72",
		`SELECT lower(hex(sha1('abc')))`:           "a9993e364706816aba3e25717850c26c9cd0d89d",
		`SELECT lower(hex(sha256('abc')))`:         "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		`SELECT lower(hex(sha512('abc')))`:         "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f",
		`SELECT lower(hex(sha256('')))`:            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		`SELECT crc32('123456789')`:                "3421780262",
		`SELECT sha256(x'616263') = sha256('abc')`: "1",
		`SELECT sha256(123) = sha256('123')`:       "1",
		`SELECT md5(NULL)`:                         "NULL",
		`SELECT typeof(sha1('x'))`:                 "blob",
	})
	sqltest.Fails(t, db, map[string]string{
		`SELECT sha256(1.5)`: "CAST(x AS TEXT)",
	})
}
