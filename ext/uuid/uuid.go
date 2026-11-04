// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package uuid gives SQLite the functions of SQLite's own uuid extension:
//
//	sqlite.NewConnector(dsn, uuid.Option())
//
// Then:
//
//	uuid()          a random UUID (version 4) as text, '8f2b...-....'
//	uuid_str(x)     the UUID x, a 16-byte blob or text, as canonical text
//	uuid_blob(x)    the UUID x, a 16-byte blob or text, as a 16-byte blob
//
// uuid_str and uuid_blob take text with or without dashes and braces, hex
// digits in either case, and give NULL for NULL or for something that is not a UUID.
package uuid

import (
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"

	"github.com/liustack/sqlite"
)

// Option adds the functions to a connector.
func Option() sqlite.Option {
	return sqlite.Join(
		// Not deterministic: each call gives a new one.
		sqlite.WithFunction("uuid", 0, func([]driver.Value) (driver.Value, error) {
			return format(newV4()), nil
		}, sqlite.Innocuous),
		sqlite.WithFunction("uuid_str", 1, func(a []driver.Value) (driver.Value, error) {
			u, ok := parse(a[0])
			if !ok {
				return nil, nil
			}
			return format(u), nil
		}, sqlite.Deterministic, sqlite.Innocuous),
		sqlite.WithFunction("uuid_blob", 1, func(a []driver.Value) (driver.Value, error) {
			u, ok := parse(a[0])
			if !ok {
				return nil, nil
			}
			return u[:], nil
		}, sqlite.Deterministic, sqlite.Innocuous),
	)
}

func newV4() [16]byte {
	var u [16]byte
	rand.Read(u[:]) // never fails (crypto/rand)
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return u
}

func format(u [16]byte) string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:], u[10:])
	return string(b[:])
}

// parse reads a UUID as SQLite's uuid extension (ext/misc/uuid.c) does: a
// blob of 16 bytes, or text of 32 hex digits, each pair of which may have a
// dash before it, after an optional '{' and before an optional '}'.
func parse(v driver.Value) (u [16]byte, ok bool) {
	switch v := v.(type) {
	case []byte:
		if len(v) != 16 {
			return u, false
		}
		copy(u[:], v)
		return u, true
	case string:
		i := 0
		if i < len(v) && v[i] == '{' {
			i++
		}
		for n := range u {
			if i < len(v) && v[i] == '-' {
				i++
			}
			if i+1 >= len(v) {
				return u, false
			}
			if _, err := hex.Decode(u[n:n+1], []byte(v[i:i+2])); err != nil {
				return u, false
			}
			i += 2
		}
		if i < len(v) && v[i] == '}' {
			i++
		}
		return u, i == len(v)
	}
	return u, false
}
