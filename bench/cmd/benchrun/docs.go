// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A lang is the words of one language of the docs.
type lang struct {
	zh                                    bool
	us, bench, time, allocs, rates, mean  string
	c, raw                                string // the columns of a comparison with SQLite in C
	work                                  string // the title of its engine table
	commit, threads, median, load, before string
}

var (
	en = &lang{us: "this driver", c: "SQLite in C", raw: "this driver, called directly", work: "Time for the whole job, lower is better:", bench: "Benchmark", time: "Time per op, lower is better:",
		allocs: "Allocations per op, lower is better:", rates: "Rates, higher is better:", mean: "geomean",
		commit: "Commit", threads: "GOMAXPROCS %s of %s threads",
		median: "The median of %d runs, ± is its 95%% confidence interval",
		load:   "Load average (1, 5, 15 minutes) before: %s, after: %s", before: "Load average before: %s"}
	zh = &lang{zh: true, us: "本驱动", c: "C 版 SQLite", raw: "本驱动，直接调用", work: "整项工作的耗时，越低越好：", bench: "基准", time: "每次操作的耗时，越低越好：",
		allocs: "每次操作的内存分配次数，越低越好：", rates: "速率，越高越好：", mean: "几何平均",
		commit: "提交", threads: "GOMAXPROCS %s，共 %s 个线程",
		median: "取 %d 轮的中位数，± 是它的 95%% 置信区间",
		load:   "负载（1、5、15 分钟）开始前 %s，结束后 %s", before: "开始前负载 %s"}
)

// The files whose tables benchrun writes, from bench/, and their language.
var docFiles = []struct {
	path   string
	l      *lang
	readme bool // it has the README tables, not the sets
}{
	{"../docs/benchmarks.md", en, false},
	{"../docs/benchmarks.zh-CN.md", zh, false},
	{"../README.md", en, true},
	{"../README.zh-CN.md", zh, true},
}

// A readmeRow is one line of a README table: a benchmark of a set, in
// words a reader knows.
type readmeRow struct{ set, bench, en, zh string }

// readme lists the rows of each README table (<!-- bench:readme-NAME -->).
var readme = map[string][]readmeRow{
	"linux-amd64": {
		{"linux-amd64-1thread", "Insert", "Insert one row", "插入一行"},
		{"linux-amd64-1thread", "InsertTx", "Insert 1000 rows in a transaction", "一个事务插入 1000 行"},
		{"linux-amd64-1thread", "Point", "Read one row by key", "按主键读一行"},
		{"linux-amd64-1thread", "Scan", "Read 100k rows", "读 10 万行"},
		{"linux-amd64-1thread", "Aggregate", "Join and group", "连表加分组"},
		{"linux-amd64-1thread", "Real/query", "Find a user with 400 comments", "查一个用户和他的 400 条评论"},
		{"linux-amd64-1thread", "Large/size=200k", "Read 100 rows of 200 KB text", "读 100 行 200 KB 的文本"},
		{"linux-amd64-8threads", "PointParallel", "Read by key, 8 threads", "按主键读，8 线程"},
		{"linux-amd64-8threads", "InsertParallel/g=8", "Insert, 8 goroutines at once", "8 个 goroutine 同时插入"},
		{"linux-amd64-8threads", "InsertParallel/g=64", "Insert, 64 goroutines at once", "64 个 goroutine 同时插入"},
		{"linux-amd64-1thread", "InsertParallelFull/g=64", "Insert, 64 goroutines, `synchronous=FULL`", "64 个 goroutine 同时插入，`synchronous=FULL`"},
	},
	"darwin-arm64": {
		{"darwin-arm64", "Insert", "Insert one row", "插入一行"},
		{"darwin-arm64", "InsertTx", "Insert 1000 rows in a transaction", "一个事务插入 1000 行"},
		{"darwin-arm64", "Point", "Read one row by key", "按主键读一行"},
		{"darwin-arm64", "Scan", "Read 100k rows", "读 10 万行"},
		{"darwin-arm64", "Aggregate", "Join and group", "连表加分组"},
		{"darwin-arm64", "Real/query", "Find a user with 400 comments", "查一个用户和他的 400 条评论"},
		{"darwin-arm64", "Large/size=200k", "Read 100 rows of 200 KB text", "读 100 行 200 KB 的文本"},
		{"darwin-arm64", "PointParallel", "Read by key, 10 threads", "按主键读，10 线程"},
		{"darwin-arm64", "InsertParallel/g=8", "Insert, 8 goroutines at once", "8 个 goroutine 同时插入"},
		{"darwin-arm64", "InsertParallel/g=64", "Insert, 64 goroutines at once", "64 个 goroutine 同时插入"},
	},
}

// driverHead is the column of a driver in a README table.
var driverHead = map[string]string{
	"mattn":   "[mattn](https://github.com/mattn/go-sqlite3) (cgo)",
	"modernc": "[modernc](https://gitlab.com/cznic/sqlite)",
	"ncruces": "[ncruces](https://github.com/ncruces/go-sqlite3)",
}

var marker = regexp.MustCompile(`(?s)<!-- bench:([a-z0-9-]+) -->\n.*?<!-- /bench:([a-z0-9-]+) -->`)

func cmdDocs(args []string) error {
	fs := flag.NewFlagSet("docs", flag.ExitOnError)
	check := fs.Bool("check", false, "write nothing: fail if a table is not what published/ gives")
	fs.Parse(args)
	sets, err := loadSets()
	if err != nil {
		return err
	}
	var stale []string
	for _, f := range docFiles {
		old, err := os.ReadFile(f.path)
		if err != nil {
			return err
		}
		var ferr error
		found := map[string]int{}
		new := marker.ReplaceAllFunc(old, func(m []byte) []byte {
			sub := marker.FindSubmatch(m)
			name := string(sub[1])
			found[name]++
			if name != string(sub[2]) {
				ferr = fmt.Errorf("%s: <!-- bench:%s --> ends with <!-- /bench:%s -->", f.path, name, sub[2])
				return m
			}
			body, err := renderMarker(name, sets, f.l)
			if err != nil {
				ferr = fmt.Errorf("%s: %w", f.path, err)
				return m
			}
			return []byte("<!-- bench:" + name + " -->\n" + body + "<!-- /bench:" + name + " -->")
		})
		if ferr != nil {
			return ferr
		}
		if err := complete(f.path, old, found, f.readme, sets); err != nil {
			return err
		}
		if bytes.Equal(old, new) {
			continue
		}
		if *check {
			stale = append(stale, f.path)
			continue
		}
		if err := os.WriteFile(f.path, new, 0o644); err != nil {
			return err
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("the tables of %s are not what bench/published gives: run make bench-docs", strings.Join(stale, ", "))
	}
	return nil
}

func loadSets() (map[string]*Set, error) {
	files, err := filepath.Glob(filepath.Join("published", "*.json"))
	if err != nil {
		return nil, err
	}
	sets := map[string]*Set{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var s Set
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		sets[s.Name] = &s
	}
	return sets, nil
}

func renderMarker(name string, sets map[string]*Set, l *lang) (string, error) {
	if r, ok := strings.CutPrefix(name, "readme-"); ok {
		rows, ok := readme[r]
		if !ok {
			return "", fmt.Errorf("no README table %q in benchrun", r)
		}
		return renderReadme(r, rows, sets, l)
	}
	s, ok := sets[name]
	if !ok {
		return "", fmt.Errorf("no bench/published/%s.json", name)
	}
	if strings.HasPrefix(s.Name, "native-") {
		for _, r := range s.Rows {
			if _, ok := nativeLabel(r, l); !ok {
				return "", fmt.Errorf("%s: no words for %s (%s) in nativeWork", name, r.Bench, r.Unit)
			}
		}
	}
	return renderSet(s, l), nil
}

// renderSet is the full tables of a set, for docs/benchmarks*.md.
func renderSet(s *Set, l *lang) string {
	var b strings.Builder
	b.WriteString(metaLine(s.Meta, l) + "\n\n")
	table := func(title string, keep func(unit string) bool, spread bool) {
		var rows []Row
		for _, r := range s.Rows {
			if keep(r.Unit) {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			return
		}
		// Only the drivers that ran one of these: a comparison with C runs
		// some benchmarks in more ways than others.
		var ds []string
		for _, d := range s.Drivers {
			if slices.ContainsFunc(rows, func(r Row) bool { return r.Cells[d] != nil }) {
				ds = append(ds, d)
			}
		}
		b.WriteString(title + "\n\n")
		b.WriteString("| " + l.bench)
		for _, d := range ds {
			name := driverName(d, l)
			if w, ok := nativeColumns[d]; ok && strings.HasPrefix(s.Name, "native-") {
				name = w.en
				if l.zh {
					name = w.zh
				}
			}
			b.WriteString(" | " + name)
		}
		b.WriteString(" |\n| :--" + strings.Repeat(" | --:", len(ds)) + " |\n")
		for _, r := range rows {
			name := r.Bench
			if strings.HasPrefix(s.Name, "native-") {
				if w, ok := nativeLabel(r, l); ok {
					name = w
				}
			}
			b.WriteString("| " + name)
			best := bestOf(r, ds)
			for _, d := range ds {
				b.WriteString(" | " + cell(r, d, bold(r, ds, d, best), spread))
			}
			b.WriteString(" |\n")
		}
		if keep("sec/op") {
			b.WriteString("| " + l.mean)
			for _, d := range ds {
				b.WriteString(" | " + geomean(rows, d, ds))
			}
			b.WriteString(" |\n")
		}
		b.WriteString("\n")
	}
	time := l.time
	if strings.HasPrefix(s.Name, "native-") {
		time = l.work
	}
	table(time, func(u string) bool { return u == "sec/op" }, true)
	table(l.rates, higher, true)
	table(l.allocs, func(u string) bool { return u == "allocs/op" }, false)
	return b.String()
}

func renderReadme(name string, rows []readmeRow, sets map[string]*Set, l *lang) (string, error) {
	first, ok := sets[rows[0].set]
	if !ok {
		return "", fmt.Errorf("no bench/published/%s.json", rows[0].set)
	}
	// The heading names one commit, Go and CPU for the whole table.
	for _, rr := range rows {
		s, ok := sets[rr.set]
		if !ok {
			return "", fmt.Errorf("no bench/published/%s.json", rr.set)
		}
		for _, k := range []string{"commit", "go", "cpu", "os"} {
			if s.Meta.Get(k) != first.Meta.Get(k) {
				return "", fmt.Errorf("the README table %s mixes sets of different %s (%s and %s): publish them from one run of the same build", name, k, rows[0].set, rr.set)
			}
		}
	}
	var b strings.Builder
	m := first.Meta
	plat := strings.Replace(name, "-", "/", 1)
	note := m.Get("note")
	if l.zh {
		note = m.Get("note_zh")
		fmt.Fprintf(&b, "**%s**（%s，Go %s，%s `%s`", plat, m.Get("cpu"), m.Get("go"), l.commit, m.Get("commit"))
		if note != "" {
			b.WriteString("，" + note)
		}
		b.WriteString("）：\n\n")
	} else {
		fmt.Fprintf(&b, "**%s** (%s, Go %s, %s `%s`", plat, m.Get("cpu"), m.Get("go"), strings.ToLower(l.commit), m.Get("commit"))
		if note != "" {
			b.WriteString(", " + note)
		}
		b.WriteString("):\n\n")
	}
	head := "| Workload"
	if l.zh {
		head = "| 场景"
	}
	b.WriteString(head)
	for _, d := range drivers {
		h := driverHead[d]
		if d == "liustack" {
			h = l.us
		}
		if l.zh {
			h = strings.Replace(h, " (cgo)", "（cgo）", 1)
		}
		b.WriteString(" | " + h)
	}
	b.WriteString(" |\n| :--" + strings.Repeat(" | --:", len(drivers)) + " |\n")
	for _, rr := range rows {
		s, ok := sets[rr.set]
		if !ok {
			return "", fmt.Errorf("no bench/published/%s.json", rr.set)
		}
		r := findRow(s, rr.bench, "sec/op")
		if r == nil {
			return "", fmt.Errorf("%s has no %s", rr.set, rr.bench)
		}
		label := rr.en
		if l.zh {
			label = rr.zh
		}
		b.WriteString("| " + label)
		best := bestOf(*r, drivers)
		for _, d := range drivers {
			b.WriteString(" | " + cell(*r, d, bold(*r, drivers, d, best), false))
		}
		b.WriteString(" |\n")
	}
	return b.String(), nil
}

func findRow(s *Set, bench, unit string) *Row {
	for i, r := range s.Rows {
		if r.Bench == bench && r.Unit == unit {
			return &s.Rows[i]
		}
	}
	return nil
}

func driverName(d string, l *lang) string {
	switch d {
	case "liustack":
		return l.us
	case "c":
		return l.c
	case "liustack-raw":
		return l.raw
	}
	return d
}

func metaLine(m Meta, l *lang) string {
	var parts []string
	if c := m.Get("commit"); c != "" {
		parts = append(parts, l.commit+" `"+c+"`")
	}
	if g := m.Get("go"); g != "" {
		parts = append(parts, "Go "+g)
	}
	for _, k := range []string{"cc", "cpu", "os"} {
		if v := m.Get(k); v != "" {
			parts = append(parts, v)
		}
	}
	if p, t := m.Get("gomaxprocs"), m.Get("threads"); p != "" && t != "" {
		parts = append(parts, fmt.Sprintf(l.threads, p, t))
	}
	if d := m.Get("date"); d != "" {
		parts = append(parts, strings.SplitN(d, "T", 2)[0])
	}
	sep, end := ", ", ". "
	if l.zh {
		sep, end = "，", "。"
	}
	line := strings.Join(parts, sep) + end
	if n, err := strconv.Atoi(m.Get("count")); err == nil {
		line += fmt.Sprintf(l.median, n) + end
	}
	if a, z := m.Get("load_before"), m.Get("load_after"); a != "" && z != "" {
		line += fmt.Sprintf(l.load, a, z) + end
	}
	note := m.Get("note")
	if l.zh {
		note = m.Get("note_zh")
	}
	if note != "" {
		if !l.zh {
			note = strings.ToUpper(note[:1]) + note[1:]
		}
		line += note + end
	}
	return strings.TrimSpace(line)
}

// higher says a larger value of unit is better.
func higher(unit string) bool { return strings.HasSuffix(unit, "/s") }

func bestOf(r Row, ds []string) string {
	best, bv := "", 0.0
	for _, d := range ds {
		c := r.Cells[d]
		if c == nil {
			continue
		}
		if best == "" || (higher(r.Unit) && c.Median > bv) || (!higher(r.Unit) && c.Median < bv) {
			best, bv = d, c.Median
		}
	}
	return best
}

// bold says d is in bold: it shows the best value of the row, and the 95%
// interval of the best overlaps no other driver's, so the lead is more than
// noise. Allocation counts have no spread: there the best value is enough.
func bold(r Row, ds []string, d, best string) bool {
	if !ties(r, d, best) {
		return false
	}
	b := r.Cells[best]
	for _, x := range ds {
		c := r.Cells[x]
		if c == nil || ties(r, x, best) {
			continue
		}
		if c.Lo <= b.Hi && b.Lo <= c.Hi {
			return false
		}
	}
	return true
}

// ties says d shows the same value as best, so that drivers that tie are
// all in bold.
func ties(r Row, d, best string) bool {
	c, b := r.Cells[d], r.Cells[best]
	return c != nil && b != nil && value(c.Median, r.Unit) == value(b.Median, r.Unit)
}

func cell(r Row, d string, best, spread bool) string {
	c := r.Cells[d]
	if c == nil {
		return ""
	}
	s := value(c.Median, r.Unit)
	if best {
		s = "**" + s + "**"
	}
	// Lo and Hi are the median itself when the runs were too few for an
	// interval (fewer than 6 at 95%).
	if spread && c.Median != 0 && (c.Lo != c.Median || c.Hi != c.Median) {
		p := math.Max(c.Hi-c.Median, c.Median-c.Lo) / math.Abs(c.Median) * 100
		s += fmt.Sprintf(" ±%.0f%%", p)
	}
	return s
}

// geomean is the geometric mean of d's times over the rows every driver has.
func geomean(rows []Row, d string, ds []string) string {
	sum, n := 0.0, 0
	for _, r := range rows {
		all := true
		for _, x := range ds {
			if r.Cells[x] == nil {
				all = false
			}
		}
		if all {
			sum += math.Log(r.Cells[d].Median)
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return value(math.Exp(sum/float64(n)), "sec/op")
}

// value writes v of unit with three significant digits.
func value(v float64, unit string) string {
	switch unit {
	case "sec/op":
		for _, u := range []struct {
			s string
			f float64
		}{{"ns", 1e-9}, {"µs", 1e-6}, {"ms", 1e-3}} {
			if x := v / u.f; x < 999.5 {
				return sig3(x) + " " + u.s
			}
		}
		return sig3(v) + " s"
	case "B/s":
		return sig3(v/1e6) + " MB/s"
	case "allocs/op":
		if v >= 1000 {
			return sig3(v/1000) + "k"
		}
		return strconv.FormatFloat(math.Round(v), 'f', -1, 64)
	}
	if v >= 1e5 {
		return sig3(v/1000) + "k " + unit
	}
	return sig3(v) + " " + unit
}

func sig3(x float64) string {
	switch {
	case x < 9.995:
		return strconv.FormatFloat(x, 'f', 2, 64)
	case x < 99.95:
		return strconv.FormatFloat(x, 'f', 1, 64)
	}
	return strconv.FormatFloat(math.Round(x), 'f', 0, 64)
}

// complete checks that a doc has every table it should, once, and no marker
// without its pair, which would leave a table out of date with no error.
func complete(path string, doc []byte, found map[string]int, readmeTables bool, sets map[string]*Set) error {
	n := 0
	for _, c := range found {
		n += c
	}
	if bytes.Count(doc, []byte("<!-- bench:")) != n || bytes.Count(doc, []byte("<!-- /bench:")) != n {
		return fmt.Errorf("%s: a <!-- bench:NAME --> or <!-- /bench:NAME --> without its pair", path)
	}
	var want []string
	if readmeTables {
		for name := range readme {
			want = append(want, "readme-"+name)
		}
	} else {
		for name := range sets {
			want = append(want, name)
		}
	}
	slices.Sort(want)
	for _, name := range want {
		if found[name] != 1 {
			return fmt.Errorf("%s: %d tables of %s, want 1", path, found[name], name)
		}
	}
	return nil
}
