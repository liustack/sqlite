// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package unicode_test

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/liustack/sqlite/ext/internal/sqltest"
	"github.com/liustack/sqlite/ext/unicode"
)

func TestCase(t *testing.T) {
	db := sqltest.Open(t, unicode.Option())
	sqltest.Want(t, db, map[string]string{
		`SELECT unicode_upper('straße éa Σσς ǆ')`: "STRAßE ÉA ΣΣΣ Ǆ",
		`SELECT unicode_lower('ÄÖÜ İ ΣΑΣ Ǆ ABC')`: "äöü i σασ ǆ abc",
		`SELECT unicode_upper(NULL) IS NULL`:      "1",
		`SELECT unicode_upper(42)`:                "42",
		`SELECT unicode_upper(x'61ff62')`:         "A\xffB",
		`SELECT hex(unicode_lower(x'C3' || 'A'))`: "C361",
		// SQLite's own stay as they are.
		`SELECT upper('straße é')`: "STRAßE é",
	})
	sqltest.Fails(t, db, map[string]string{
		`SELECT unicode_upper(1.5)`: "use CAST(x AS TEXT)",
	})
}

func TestNoCase(t *testing.T) {
	db := sqltest.Open(t, unicode.Option())
	if _, err := db.Exec(`CREATE TABLE w(s TEXT COLLATE unicode_nocase);
		CREATE INDEX w_s ON w(s);
		INSERT INTO w VALUES ('Äpfel'), ('zebra'), ('apple'), ('ÉCOLE'), ('école'), ('Zürich'), ('_x'), ('Ωmega'), ('ωmega'), ('İstanbul'), ('istanbul')`); err != nil {
		t.Fatal(err)
	}
	sqltest.Want(t, db, map[string]string{
		`SELECT group_concat(s, ' ') FROM (SELECT s FROM w ORDER BY s, rowid)`: "_x apple istanbul zebra Zürich Äpfel ÉCOLE école İstanbul Ωmega ωmega",
		`SELECT count(*) FROM w WHERE s = 'äPFEL'`:                             "1",
		`SELECT count(*) FROM w WHERE s = 'École'`:                             "2",
		`SELECT count(*) FROM w WHERE s = 'ΩMEGA'`:                             "2",
		`SELECT count(*) FROM w WHERE s = 'ISTANBUL'`:                          "1", // İ folds only with itself
		`SELECT 'K' = 'k' COLLATE unicode_nocase`:                              "1", // the Kelvin sign
		`SELECT 'ſ' = 'S' COLLATE unicode_nocase`:                              "1",
		`SELECT 'Σ' = 'ς' COLLATE unicode_nocase`:                              "1",
		`SELECT 'ß' = 'ẞ' COLLATE unicode_nocase`:                              "1",
		`SELECT '_' < 'a' COLLATE unicode_nocase`:                              "1", // as NOCASE
		`SELECT '_' < 'a' COLLATE NOCASE`:                                      "1",
		`SELECT 'ab' < 'ABC' COLLATE unicode_nocase`:                           "1",
		`PRAGMA integrity_check`:                                               "ok",
	})
}

// Compare is an order, and equal exactly when strings.EqualFold is.
func TestCompare(t *testing.T) {
	alpha := []string{"a", "A", "k", "K", "K", "s", "S", "ſ", "_", "z", "Z", "é", "É", "σ", "ς", "Σ", "ß", "ẞ", "İ", "ı", "i", "I", "ǅ", "ǆ", "Ǆ", "日", "\xff", "\xc3"}
	r := rand.New(rand.NewPCG(3, 4))
	gen := func() string {
		var b strings.Builder
		for range r.IntN(4) {
			b.WriteString(alpha[r.IntN(len(alpha))])
		}
		return b.String()
	}
	for range 20000 {
		a, b, c := gen(), gen(), gen()
		ab, ba := unicode.Compare(a, b), unicode.Compare(b, a)
		if ab != -ba {
			t.Fatalf("Compare(%q, %q) = %d, reversed %d", a, b, ab, ba)
		}
		if ab <= 0 && unicode.Compare(b, c) <= 0 && unicode.Compare(a, c) > 0 {
			t.Fatalf("not transitive: %q <= %q <= %q", a, b, c)
		}
		if utf8.ValidString(a) && utf8.ValidString(b) && (ab == 0) != strings.EqualFold(a, b) {
			t.Fatalf("Compare(%q, %q) = %d, EqualFold %v", a, b, ab, strings.EqualFold(a, b))
		}
		ascii := func(s string) bool {
			for i := range len(s) {
				if s[i] >= utf8.RuneSelf {
					return false
				}
			}
			return true
		}
		if ascii(a) && ascii(b) {
			want := strings.Compare(strings.ToLower(a), strings.ToLower(b))
			if ab != want {
				t.Fatalf("ASCII Compare(%q, %q) = %d, NOCASE %d", a, b, ab, want)
			}
		}
	}
}
