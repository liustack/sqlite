// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"
)

// A Cell is the summary of one benchmark, one unit, one driver: the median
// of the runs and its 95% confidence interval, as benchstat gives them.
type Cell struct {
	Median float64 `json:"median"`
	Lo     float64 `json:"lo"`
	Hi     float64 `json:"hi"`
	N      int     `json:"n"`
}

// A Row is one benchmark in one unit, for every driver.
type Row struct {
	Bench string           `json:"bench"`
	Unit  string           `json:"unit"`
	Cells map[string]*Cell `json:"cells"`
}

// A Set is one published run: published/<name>.json.
type Set struct {
	Name    string   `json:"name"`
	Meta    Meta     `json:"meta"`
	Drivers []string `json:"drivers"`
	Rows    []Row    `json:"rows"`
}

func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	from := fs.String("from", "", "the directory of a run")
	name := fs.String("name", "", "the name of the set, as its markers in the docs name it")
	prefix := fs.String("prefix", "", "the prefix of the result files (<prefix><driver>.txt)")
	var set setFlags
	fs.Var(&set, "set", "key=value to record about the run, again for more")
	fs.Parse(args)
	if *from == "" || *name == "" {
		return errors.New("publish needs -from and -name")
	}
	s, err := summarizePrefix(*from, *prefix)
	if err != nil {
		return err
	}
	s.Name = *name
	for _, p := range set {
		s.Meta.Set(p.K, p.V)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll("published", 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join("published", *name+".json"), append(b, '\n'), 0o644)
}

type setFlags Meta

func (s *setFlags) String() string { return "" }
func (s *setFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("-set %q: want key=value", v)
	}
	(*Meta)(s).Set(k, val)
	return nil
}

func summarize(dir, name string) (*Set, error) {
	s, err := summarizePrefix(dir, "")
	if s != nil {
		s.Name = name
	}
	return s, err
}

// procs is the -GOMAXPROCS suffix testing adds to a benchmark name.
var procs = regexp.MustCompile(`-\d+$`)

// summarizePrefix reads <prefix><driver>.txt of every driver in dir and
// meta.json (or header.txt, as bench/run.sh wrote it).
func summarizePrefix(dir, prefix string) (*Set, error) {
	s := &Set{}
	var err error
	if s.Meta, err = readMeta(dir); err != nil {
		return nil, err
	}
	type key struct{ bench, unit string }
	var order []key
	values := map[key]map[string][]float64{}
	for _, d := range drivers {
		f, err := os.Open(filepath.Join(dir, prefix+d+".txt"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		r := benchfmt.NewReader(f, f.Name())
		found := false
		for r.Scan() {
			res, ok := r.Result().(*benchfmt.Result)
			if !ok {
				continue
			}
			found = true
			bench := procs.ReplaceAllString(strings.TrimPrefix(res.Name.String(), "Benchmark"), "")
			for _, v := range res.Values {
				if v.Unit == "B/op" {
					continue // allocs/op says it more plainly
				}
				k := key{bench, v.Unit}
				if values[k] == nil {
					values[k] = map[string][]float64{}
					order = append(order, k)
				}
				values[k][d] = append(values[k][d], v.Value)
			}
		}
		f.Close()
		if err := r.Err(); err != nil {
			return nil, err
		}
		if found {
			s.Drivers = append(s.Drivers, d)
		}
	}
	if len(s.Drivers) == 0 {
		return nil, fmt.Errorf("%s: no results", dir)
	}
	for _, k := range order {
		row := Row{Bench: k.bench, Unit: k.unit, Cells: map[string]*Cell{}}
		for d, vs := range values[k] {
			sum := benchmath.AssumeNothing.Summary(benchmath.NewSample(vs, &benchmath.DefaultThresholds), 0.95)
			row.Cells[d] = &Cell{Median: sum.Center, Lo: finite(sum.Lo, sum.Center), Hi: finite(sum.Hi, sum.Center), N: len(vs)}
		}
		s.Rows = append(s.Rows, row)
	}
	return s, nil
}

// finite is v, or center when v is infinite (too few runs for an interval).
func finite(v, center float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return center
	}
	return v
}

func readMeta(dir string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err == nil {
		return m, json.Unmarshal(b, &m)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	// header.txt of bench/run.sh: "key: value" lines.
	f, err := os.Open(filepath.Join(dir, "header.txt"))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ": ")
		if !ok || strings.HasPrefix(k, " ") {
			continue
		}
		if k == "go" {
			// "go version go1.27.2 darwin/arm64"
			if f := strings.Fields(v); len(f) >= 3 {
				v = strings.TrimPrefix(f[2], "go")
			}
		}
		m.Set(k, v)
	}
	return m, sc.Err()
}
