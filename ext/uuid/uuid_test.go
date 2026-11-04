// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package uuid_test

import (
	"regexp"
	"testing"

	"github.com/liustack/sqlite/ext/internal/sqltest"
	"github.com/liustack/sqlite/ext/uuid"
)

const canonical = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"

func TestUUID(t *testing.T) {
	db := sqltest.Open(t, uuid.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT uuid_str('a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11')`:             canonical,
		`SELECT uuid_str('{A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11}')`:           canonical,
		`SELECT uuid_str('a0eebc999c0b4ef8bb6d6bb9bd380a11')`:                 canonical,
		`SELECT uuid_str('-a0-ee-bc-99-9c-0b-4e-f8-bb-6d-6b-b9-bd-38-0a-11')`: canonical,
		`SELECT uuid_str('{a0eebc999c0b4ef8bb6d6bb9bd380a11')`:                canonical,
		`SELECT uuid_str(x'a0eebc999c0b4ef8bb6d6bb9bd380a11')`:                canonical,
		`SELECT hex(uuid_blob('` + canonical + `'))`:                          "A0EEBC999C0B4EF8BB6D6BB9BD380A11",
		`SELECT uuid_str(uuid_blob('` + canonical + `'))`:                     canonical,
		// Not UUIDs.
		`SELECT uuid_str('a0eebc999c0b4ef8bb6d6bb9bd380a1')`:    "NULL",
		`SELECT uuid_str('a0eebc999c0b4ef8bb6d6bb9bd380a111')`:  "NULL",
		`SELECT uuid_str('a0eebc99--9c0b4ef8bb6d6bb9bd380a11')`: "NULL",
		`SELECT uuid_str('a0eebc999c0b4ef8bb6d6bb9bd380a11-')`:  "NULL",
		`SELECT uuid_str('g0eebc999c0b4ef8bb6d6bb9bd380a11')`:   "NULL",
		`SELECT uuid_str(x'00')`:                                "NULL",
		`SELECT uuid_str(123)`:                                  "NULL",
		`SELECT uuid_blob(NULL)`:                                "NULL",
		`SELECT length(uuid_blob(uuid()))`:                      "16",
		`SELECT uuid() = uuid()`:                                "0",
	})
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for range 100 {
		var s string
		if err := db.QueryRow(`SELECT uuid()`).Scan(&s); err != nil || !v4.MatchString(s) {
			t.Fatalf("uuid() = %q %v", s, err)
		}
	}
}
