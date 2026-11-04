// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package series gives SQLite generate_series, the table-valued function of
// SQLite's series extension (ext/misc/series.c), ported to Go:
//
//	sqlite.NewConnector(dsn, series.Option())
//
// Then:
//
//	SELECT value FROM generate_series(start, stop, step)
//
// gives start, start+step, ... up to stop. stop defaults to 4294967295 and
// step to 1, a step of 0 is 1, and a negative step counts down from start to
// stop. start may be left out when a WHERE clause bounds value instead:
// generate_series WHERE value BETWEEN 1 AND 10. A NULL argument gives no
// rows. The hidden columns start, stop and step give the arguments.
//
// The arguments are 64-bit integers. A REAL is cut to an integer as SQLite's
// own does. A TEXT or BLOB argument, or one compared with value in the WHERE
// clause (value = '5', or a Go string bound to value = ?), is an error where
// SQLite's would read the number at its start: write CAST(x AS INTEGER).
package series

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"math"

	"github.com/liustack/sqlite"
)

// Option adds generate_series to a connector.
func Option() sqlite.Option {
	return sqlite.WithModule("generate_series", func([]string) (sqlite.VTable, error) {
		return table{}, nil
	}, sqlite.EponymousOnly, sqlite.ModuleInnocuous)
}

// The columns.
const (
	colRowID = -1
	colValue = 0
	colStart = 1
	colStop  = 2
	colStep  = 3
)

// The bits of the plan (idxNum), and the order of Filter's arguments: start,
// stop, step, LIMIT, OFFSET, then value=, value>= or value>, then value<= or
// value<.
const (
	planStart  = 0x0001
	planStop   = 0x0002
	planStep   = 0x0004
	planDesc   = 0x0008
	planAsc    = 0x0010
	planLimit  = 0x0020
	planOffset = 0x0040
	planEQ     = 0x0080
	planGE     = 0x0100
	planGT     = 0x0200
	planLE     = 0x1000
	planLT     = 0x2000
)

type table struct{}

func (table) Schema() string {
	return "CREATE TABLE x(value,start hidden,stop hidden,step hidden)"
}

func (table) BestIndex(info *sqlite.IndexInfo) error {
	idxNum := 0
	startSeen := false
	unusable := 0
	// The constraints on start, stop, step, LIMIT, OFFSET, value= or >= or
	// >, value<= or <.
	idx := [7]int{-1, -1, -1, -1, -1, -1, -1}
	for i, c := range info.Constraints {
		op := c.Op
		if op == sqlite.IndexLimit || op == sqlite.IndexOffset {
			switch {
			case !c.Usable:
			case op == sqlite.IndexLimit:
				idx[3] = i
				idxNum |= planLimit
			default:
				idx[4] = i
				idxNum |= planOffset
			}
			continue
		}
		if c.Column < colStart {
			if (c.Column == colValue || c.Column == colRowID) && c.Usable {
				switch op {
				case sqlite.IndexEQ, sqlite.IndexIs:
					idxNum |= planEQ
					idxNum &^= planGE | planGT | planLE | planLT
					idx[5] = i
					idx[6] = -1
					startSeen = true
				case sqlite.IndexGE:
					if idxNum&planEQ == 0 {
						idxNum = idxNum&^planGT | planGE
						idx[5] = i
						startSeen = true
					}
				case sqlite.IndexGT:
					if idxNum&planEQ == 0 {
						idxNum = idxNum&^planGE | planGT
						idx[5] = i
						startSeen = true
					}
				case sqlite.IndexLE:
					if idxNum&planEQ == 0 {
						idxNum = idxNum&^planLT | planLE
						idx[6] = i
					}
				case sqlite.IndexLT:
					if idxNum&planEQ == 0 {
						idxNum = idxNum&^planLE | planLT
						idx[6] = i
					}
				}
			}
			continue
		}
		col := c.Column - colStart // 0 for start, 1 for stop, 2 for step
		mask := 1 << col
		if col == 0 && op == sqlite.IndexEQ {
			startSeen = true
		}
		if !c.Usable {
			unusable |= mask
			continue
		}
		if op == sqlite.IndexEQ {
			idxNum |= mask
			idx[col] = i
		}
	}
	if idx[3] < 0 {
		// OFFSET without LIMIT is not used.
		idxNum &^= planOffset
		idx[4] = -1
	}
	n := 0
	for _, j := range idx {
		if j >= 0 {
			n++
			info.Usage[j] = sqlite.IndexUsage{ArgvIndex: n, Omit: true}
		}
	}
	if !startSeen {
		return errors.New(`first argument to "generate_series()" missing or unusable`)
	}
	if unusable&^idxNum != 0 {
		// start, stop and step are inputs: a plan without one of them
		// that the statement gives is no plan.
		return sqlite.ErrUnusablePlan
	}
	switch {
	case idxNum&(planStart|planStop) == planStart|planStop:
		// Both ends: the plan to prefer.
		info.EstimatedCost = 2
		if idxNum&planStep != 0 {
			info.EstimatedCost = 1
		}
		info.EstimatedRows = 1000
		if len(info.OrderBy) >= 1 && info.OrderBy[0].Column == colValue {
			if info.OrderBy[0].Desc {
				idxNum |= planDesc
			} else {
				idxNum |= planAsc
			}
			info.OrderByConsumed = true
		}
	case idxNum&(planStart|planLimit) == planStart|planLimit:
		info.EstimatedRows = 2500
	default:
		// An end is missing, so a huge span: costly, so that the planner
		// finds the ends first.
		info.EstimatedRows = 2147483647
	}
	info.IdxNum = idxNum
	return nil
}

func (table) Open() (sqlite.VCursor, error) { return &cursor{}, nil }
func (table) Disconnect() error             { return nil }

// cursor is a scan. oBase, oTerm and oStep are the arguments, which the
// hidden columns give. base, term, step and desc make the sequence, which
// the constraints on value may have narrowed: from base to term, by step
// (unsigned, as it may be 1<<63), down when desc.
type cursor struct {
	oBase, oTerm, oStep int64
	base, term          int64
	step                uint64
	value               int64
	desc, done          bool
}

// span64 is a-b for a >= b, which may not fit in an int64.
func span64(a, b int64) uint64 { return uint64(a) - uint64(b) }

func add64(a int64, b uint64) int64 { return int64(uint64(a) + b) }
func sub64(a int64, b uint64) int64 { return int64(uint64(a) - b) }

// steps is the number of steps from base to term.
func (c *cursor) steps() uint64 {
	if c.desc {
		return span64(c.base, c.term) / c.step
	}
	return span64(c.term, c.base) / c.step
}

// realToInt64 is SQLite's conversion of a REAL to an integer.
func realToInt64(r float64) int64 {
	switch {
	case r < -9223372036854774784.0:
		return math.MinInt64
	case r > 9223372036854774784.0:
		return math.MaxInt64
	}
	return int64(r)
}

// toInt64 is sqlite3_value_int64 for the arguments it takes.
func toInt64(v driver.Value) (int64, error) {
	switch v := v.(type) {
	case int64:
		return v, nil
	case float64:
		return realToInt64(v), nil
	}
	return 0, argError(v)
}

func argError(v driver.Value) error {
	t := "TEXT"
	if _, ok := v.([]byte); ok {
		t = "BLOB"
	}
	return fmt.Errorf("generate_series: an argument is %s: use CAST(x AS INTEGER)", t)
}

func (c *cursor) Filter(idxNum int, _ string, args []driver.Value) error {
	min, max := int64(math.MinInt64), int64(math.MaxInt64)
	var limit, offset int64
	for _, a := range args {
		if a == nil {
			c.empty()
			return nil
		}
		switch a.(type) {
		case int64, float64:
		default:
			return argError(a)
		}
	}
	next := func() int64 {
		v, _ := toInt64(args[0]) // the types are checked above
		args = args[1:]
		return v
	}

	c.oBase, c.oTerm, c.oStep = 0, 0xffffffff, 1
	if idxNum&planStart != 0 {
		c.oBase = next()
	}
	if idxNum&planStop != 0 {
		c.oTerm = next()
	}
	if idxNum&planStep != 0 {
		if c.oStep = next(); c.oStep == 0 {
			c.oStep = 1
		}
	}
	// Constraints on value but none on start or stop: the whole range of
	// 64-bit integers, which they narrow below.
	if idxNum&(planStart|planStep) == 0 && idxNum&(planEQ|planGE|planGT) != 0 {
		c.oBase = math.MinInt64
	}
	if idxNum&(planStop|planStep) == 0 && idxNum&(planEQ|planLE|planLT) != 0 {
		c.oTerm = math.MaxInt64
	}
	c.base, c.term = c.oBase, c.oTerm
	switch {
	case c.oStep > 0:
		c.step = uint64(c.oStep)
	case c.oStep > math.MinInt64:
		c.step = uint64(-c.oStep)
	default:
		c.step = 1 << 63
	}
	c.desc = c.oStep < 0
	if !c.desc && c.base > c.term || c.desc && c.base < c.term {
		c.empty()
		return nil
	}
	// LIMIT and OFFSET apply after the constraints on value.
	if idxNum&planLimit != 0 {
		limit = next()
		if idxNum&planOffset != 0 {
			offset = next()
		}
	}

	// Narrow min and max by the constraints on value.
	if idxNum&(planEQ|planGE|planGT|planLE|planLT) != 0 {
		if idxNum&planEQ != 0 {
			if r, ok := args[0].(float64); ok {
				args = args[1:]
				if r != math.Ceil(r) || r < math.MinInt64 || r > math.MaxInt64 {
					c.empty()
					return nil
				}
				min = realToInt64(r)
				max = min
			} else {
				min = next()
				max = min
			}
		} else {
			if idxNum&(planGE|planGT) != 0 {
				if r, ok := args[0].(float64); ok {
					args = args[1:]
					switch {
					case r <= math.MinInt64:
						min = math.MinInt64
					case r > math.MaxInt64:
						c.empty()
						return nil
					default:
						min = realToInt64(math.Ceil(r))
						if idxNum&planGT != 0 && r == math.Ceil(r) {
							if min == math.MaxInt64 {
								c.empty()
								return nil
							}
							min++
						}
					}
				} else {
					min = next()
					if idxNum&planGT != 0 {
						if min == math.MaxInt64 {
							c.empty()
							return nil
						}
						min++
					}
				}
			}
			if idxNum&(planLE|planLT) != 0 {
				if r, ok := args[0].(float64); ok {
					args = args[1:]
					switch {
					case r >= math.MaxInt64:
						max = math.MaxInt64
					case r <= math.MinInt64:
						c.empty()
						return nil
					default:
						max = realToInt64(math.Floor(r))
						if idxNum&planLT != 0 && r == math.Floor(r) {
							if max == math.MinInt64 {
								c.empty()
								return nil
							}
							max--
						}
					}
				} else {
					max = next()
					if idxNum&planLT != 0 {
						if max == math.MinInt64 {
							c.empty()
							return nil
						}
						max--
					}
				}
			}
			if min > max {
				c.empty()
				return nil
			}
		}
		// Bring base and term within min and max.
		if !c.desc {
			if c.base < min {
				span := span64(min, c.base)
				c.base = add64(c.base, span/c.step*c.step)
				if c.base < min {
					if c.base > sub64(math.MaxInt64, c.step) {
						c.empty()
						return nil
					}
					c.base = add64(c.base, c.step)
				}
			}
			if c.term > max {
				c.term = max
			}
		} else {
			if c.base > max {
				span := span64(c.base, max)
				c.base = sub64(c.base, span/c.step*c.step)
				if c.base > max {
					if c.base < add64(math.MinInt64, c.step) {
						c.empty()
						return nil
					}
					c.base = sub64(c.base, c.step)
				}
			}
			if c.term < min {
				c.term = min
			}
		}
	}

	// Make term the last value of the sequence.
	if !c.desc {
		if c.base > c.term {
			c.empty()
			return nil
		}
		c.term = sub64(c.term, span64(c.term, c.base)%c.step)
	} else {
		if c.base < c.term {
			c.empty()
			return nil
		}
		c.term = add64(c.term, span64(c.base, c.term)%c.step)
	}

	// Give the values in the order asked for.
	if idxNum&planDesc != 0 && !c.desc || idxNum&planAsc != 0 && c.desc {
		c.base, c.term = c.term, c.base
		c.desc = !c.desc
	}

	if idxNum&planLimit != 0 {
		if offset > 0 {
			if c.steps() < uint64(offset) {
				c.empty()
				return nil
			}
			if c.desc {
				c.base = sub64(c.base, c.step*uint64(offset))
			} else {
				c.base = add64(c.base, c.step*uint64(offset))
			}
		}
		// series.c adds to base going down too, and LIMIT 0 makes term one
		// step before base: either way term is never reached and the scan
		// runs on for 2^64 steps. The last value is base moved limit-1
		// steps the way the sequence goes.
		switch {
		case limit == 0:
			c.empty()
			return nil
		case limit > 0 && c.steps() > uint64(limit):
			if c.desc {
				c.term = sub64(c.base, uint64(limit-1)*c.step)
			} else {
				c.term = add64(c.base, uint64(limit-1)*c.step)
			}
		}
	}
	c.value = c.base
	c.done = false
	return nil
}

// empty makes the scan give no rows.
func (c *cursor) empty() {
	c.base, c.term, c.step, c.desc, c.done = 0, 0, 1, false, true
}

func (c *cursor) Next() error {
	switch {
	case c.value == c.term:
		c.done = true
	case c.desc:
		c.value = sub64(c.value, c.step)
	default:
		c.value = add64(c.value, c.step)
	}
	return nil
}

func (c *cursor) EOF() bool { return c.done }

func (c *cursor) Column(i int) (driver.Value, error) {
	switch i {
	case colStart:
		return c.oBase, nil
	case colStop:
		return c.oTerm, nil
	case colStep:
		return c.oStep, nil
	}
	return c.value, nil
}

func (c *cursor) RowID() (int64, error) { return c.value, nil }
func (c *cursor) Close() error          { return nil }
