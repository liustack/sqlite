// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package vec gives SQLite vector functions, for similarity search over
// vectors kept in ordinary tables:
//
//	sqlite.NewConnector(dsn, vec.Option())
//
// A vector is a BLOB of float32 values in little-endian order, the bytes
// sqlite-vec and libSQL use for a float32 vector too. Each function also
// takes a vector written as a JSON array, such as '[0.1, -2, 3e4]'.
//
//	vec_f32(v)                 the vector as a BLOB
//	vec_to_json(v)             the vector as a JSON array
//	vec_length(v)              its number of dimensions
//	vec_normalize(v)           the vector of length 1 in its direction
//	vec_distance_l2(a, b)      the Euclidean distance
//	vec_distance_l1(a, b)      the sum of the absolute differences
//	vec_distance_cosine(a, b)  1 minus the cosine of the angle between them:
//	                           0 for the same direction, 2 for opposite ones
//	vec_dot(a, b)              the dot product
//
// The k rows nearest to a vector come from a full scan:
//
//	SELECT id FROM doc ORDER BY vec_distance_cosine(embedding, vec_f32(?)) LIMIT 10
//
// SQLite computes vec_f32(?) once per query. The time grows with the
// number of rows: 100000 vectors of 768 dimensions take about 0.1 s on an
// Apple M4 (BenchmarkNearest). On vectors stored normalized, vec_dot
// ordered DESC gives the cosine order.
//
// The distances are summed in float64, so float32 values never overflow,
// and every platform gives the same bits. A NULL argument gives NULL.
// Vectors of different dimensions, an empty vector, an element that is NaN
// or infinite, and the cosine or the direction of a zero vector are errors.
// One such vector in a table fails every query that measures it, so insert
// vectors through vec_f32(?), which refuses elements that are not finite,
// or vec_normalize(?), which also refuses a zero vector. vec_length takes
// any vector, to find them.
package vec

import (
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/liustack/sqlite"
	"github.com/liustack/sqlite/internal/extapi"
)

// Option adds the functions to a connector.
func Option() sqlite.Option {
	// one makes a function of one vector. check refuses elements that are
	// not finite.
	one := func(name string, check bool, f func(v []byte) (driver.Value, error)) sqlite.Option {
		return borrowed(name, 1, func(a []driver.Value) (driver.Value, error) {
			v, err := vector(name, 0, a[0])
			if err == nil && v != nil && check {
				err = finite(name, 0, v)
			}
			if err != nil || v == nil {
				return nil, err
			}
			return f(v)
		})
	}
	two := func(name string, f func(a, b []byte) (float64, error)) sqlite.Option {
		return borrowed(name, 2, func(args []driver.Value) (driver.Value, error) {
			a, err := vector(name, 0, args[0])
			if err != nil {
				return nil, err
			}
			b, err := vector(name, 1, args[1])
			if err != nil || a == nil || b == nil {
				return nil, err
			}
			if len(a) != len(b) {
				return nil, fmt.Errorf("%s: vectors of %d and %d dimensions", name, len(a)/4, len(b)/4)
			}
			d, err := f(a, b)
			// The sums of float32 values in float64 cannot overflow, so a
			// result that is not finite comes from an element that is not:
			// the elements are looked at only then.
			if err == nil && (math.IsNaN(d) || math.IsInf(d, 0)) {
				if err = finite(name, 0, a); err == nil {
					err = finite(name, 1, b)
				}
				if err != nil {
					return nil, err
				}
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			return d, nil
		})
	}
	return sqlite.Join(
		one("vec_f32", true, func(v []byte) (driver.Value, error) { return v, nil }),
		one("vec_to_json", true, func(v []byte) (driver.Value, error) { return toJSON(v), nil }),
		one("vec_length", false, func(v []byte) (driver.Value, error) { return int64(len(v) / 4), nil }),
		one("vec_normalize", true, normalize),
		two("vec_distance_l2", func(a, b []byte) (float64, error) { return math.Sqrt(l2sq(a, b)), nil }),
		two("vec_distance_l1", func(a, b []byte) (float64, error) { return l1(a, b), nil }),
		two("vec_distance_cosine", cosine),
		two("vec_dot", func(a, b []byte) (float64, error) { return dot(a, b), nil }),
	)
}

// borrowed makes a deterministic, innocuous function that reads its vectors
// in place: a copy of two vectors for each row would cost more than the
// distance.
func borrowed(name string, nArg int, fn func([]driver.Value) (driver.Value, error)) sqlite.Option {
	return extapi.BorrowedFunction(name, nArg, fn, int(sqlite.Deterministic), int(sqlite.Innocuous)).(sqlite.Option)
}

var errZero = fmt.Errorf("a zero vector has no direction")

// The loops below sum in float64. Each product is converted to float64
// before it is added, which keeps the compiler from fusing the multiply and
// the add (it does on arm64, and on amd64 v3), so that every platform gives
// the same bits. A product of two float32 values is exact in float64 anyway:
// the conversion matters for the square of a difference (l2sq).

// f32 is the float32 of the 4 bytes of p.
func f32(p []byte) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(p))) }

func l2sq(a, b []byte) float64 {
	b = b[:len(a)]
	s := 0.0
	for i := 0; i+4 <= len(a); i += 4 {
		d := f32(a[i:i+4]) - f32(b[i:i+4])
		s += float64(d * d)
	}
	return s
}

func l1(a, b []byte) float64 {
	b = b[:len(a)]
	s := 0.0
	for i := 0; i+4 <= len(a); i += 4 {
		s += math.Abs(f32(a[i:i+4]) - f32(b[i:i+4]))
	}
	return s
}

func dot(a, b []byte) float64 {
	b = b[:len(a)]
	s := 0.0
	for i := 0; i+4 <= len(a); i += 4 {
		s += float64(f32(a[i:i+4]) * f32(b[i:i+4]))
	}
	return s
}

func cosine(a, b []byte) (float64, error) {
	b = b[:len(a)]
	var ab, aa, bb float64
	for i := 0; i+4 <= len(a); i += 4 {
		x, y := f32(a[i:i+4]), f32(b[i:i+4])
		ab += float64(x * y)
		aa += float64(x * x)
		bb += float64(y * y)
	}
	if aa == 0 || bb == 0 {
		return 0, errZero
	}
	// Rounding can take the cosine a little past 1 or -1. NaN stays NaN.
	d := 1 - ab/math.Sqrt(aa*bb)
	if d < 0 {
		d = 0
	} else if d > 2 {
		d = 2
	}
	return d, nil
}

// finite checks that every element of argument i of fn is a number.
func finite(fn string, i int, v []byte) error {
	for j := 0; j+4 <= len(v); j += 4 {
		if x := f32(v[j : j+4]); math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Errorf("%s: element %d of argument %d is not finite", fn, j/4+1, i+1)
		}
	}
	return nil
}

// vector reads argument i of the function fn as the bytes of a vector: nil
// for NULL. The elements of a BLOB are not looked at (finite).
func vector(fn string, i int, a driver.Value) ([]byte, error) {
	var v []byte
	switch a := a.(type) {
	case nil:
		return nil, nil
	case []byte:
		if len(a)%4 != 0 {
			return nil, fmt.Errorf("%s: a blob of %d bytes is not float32s", fn, len(a))
		}
		v = a // SQLite's memory: read during the call only
	case string:
		var err error
		if v, err = parse(fn, a); err != nil {
			return nil, err
		}
	case int64:
		return nil, fmt.Errorf("%s: argument %d is an integer, want a vector", fn, i+1)
	case float64:
		return nil, fmt.Errorf("%s: argument %d is a real, want a vector", fn, i+1)
	default:
		return nil, fmt.Errorf("%s: argument %d is a %T, want a vector", fn, i+1, a)
	}
	if len(v) == 0 {
		return nil, fmt.Errorf("%s: an empty vector", fn)
	}
	return v, nil
}

// parse reads a JSON array of numbers into the bytes of a vector. Each
// number is rounded once, to a float32, and nothing is made per element.
func parse(fn, s string) ([]byte, error) {
	bad := fmt.Errorf("%s: not a JSON array of numbers", fn)
	i := skip(s, 0)
	if i == len(s) || s[i] != '[' {
		return nil, bad
	}
	var v []byte
	i = skip(s, i+1)
	if i < len(s) && s[i] == ']' {
		i++
	} else {
		for {
			n := number(s, i)
			if n == i {
				return nil, bad
			}
			x, err := strconv.ParseFloat(s[i:n], 32)
			if err != nil {
				// The syntax is checked: the number is out of range.
				return nil, fmt.Errorf("%s: element %d does not fit a float32", fn, len(v)/4+1)
			}
			v = binary.LittleEndian.AppendUint32(v, math.Float32bits(float32(x)))
			i = skip(s, n)
			if i < len(s) && s[i] == ',' {
				i = skip(s, i+1)
				continue
			}
			if i < len(s) && s[i] == ']' {
				i++
				break
			}
			return nil, bad
		}
	}
	if skip(s, i) != len(s) {
		return nil, bad
	}
	return v, nil
}

// skip is the index of the first byte of s from i on that is not JSON white
// space.
func skip(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// number is the end of the JSON number that starts at byte i of s, or i
// when none does: -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?
func number(s string, i int) int {
	digits := func(j int) int {
		for j < len(s) && '0' <= s[j] && s[j] <= '9' {
			j++
		}
		return j
	}
	j := i
	if j < len(s) && s[j] == '-' {
		j++
	}
	switch {
	case j < len(s) && s[j] == '0':
		j++
	case j < len(s) && '1' <= s[j] && s[j] <= '9':
		j = digits(j)
	default:
		return i
	}
	if j < len(s) && s[j] == '.' {
		k := digits(j + 1)
		if k == j+1 {
			return i
		}
		j = k
	}
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		l := digits(k)
		if l == k {
			return i
		}
		j = l
	}
	return j
}

func toJSON(v []byte) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < len(v); i += 4 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(f32(v[i:i+4]), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func normalize(v []byte) (driver.Value, error) {
	s := 0.0
	for i := 0; i < len(v); i += 4 {
		x := f32(v[i : i+4])
		s += float64(x * x)
	}
	if s == 0 {
		return nil, fmt.Errorf("vec_normalize: %w", errZero)
	}
	n := math.Sqrt(s)
	out := make([]byte, len(v))
	for i := 0; i < len(v); i += 4 {
		binary.LittleEndian.PutUint32(out[i:], math.Float32bits(float32(f32(v[i:i+4])/n)))
	}
	return out, nil
}
