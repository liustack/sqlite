// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// A contentionResult is one case of TestContention (contention_test.go) on
// one driver: the writes that succeeded, those that got "database is
// locked", and how long the ones that succeeded took, in nanoseconds.
type contentionResult struct {
	Case    string  `json:"case"`
	Driver  string  `json:"driver"`
	Seconds float64 `json:"seconds"`
	Done    int     `json:"done"`
	Locked  int     `json:"locked"`
	P50     int64   `json:"p50_ns"`
	P99     int64   `json:"p99_ns"`
	Max     int64   `json:"max_ns"`
}

// A contentionSet is one run of TestContention on every driver:
// published/contention/<name>.json.
type contentionSet struct {
	Name    string             `json:"name"`
	Meta    Meta               `json:"meta"`
	Drivers []string           `json:"drivers"`
	Results []contentionResult `json:"results"`
}

// resultPrefix starts the line of each result in the output of the test.
const resultPrefix = "contention-result "

// contentionCases are the cases of TestContention, in its order, in words.
// readme says the case is in the README table.
var contentionCases = []struct {
	key, en, zh string
	readme      bool
}{
	{"insert-autocommit-NORMAL-g64-hold0s", "Insert one row, 64 goroutines at once", "64 个 goroutine 同时各插入一行", true},
	{"transfer-deferred-NORMAL-g64-hold0s", "Transactions, 64 goroutines, deferred", "事务，64 个 goroutine，deferred", false},
	{"transfer-immediate-NORMAL-g64-hold0s", "Transactions, 64 goroutines, `_txlock=immediate`", "事务，64 个 goroutine，`_txlock=immediate`", true},
	{"transfer-immediate-NORMAL-g64-hold2ms", "Transactions, 64 goroutines, immediate, 2 ms of work in each", "事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事", true},
	{"writefirst-deferred-NORMAL-g64-hold2ms", "Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each", "事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事", false},
	{"transfer-immediate-FULL-g64-hold0s", "Transactions, 64 goroutines, immediate, `synchronous=FULL`", "事务，64 个 goroutine，immediate，`synchronous=FULL`", true},
	{"transfer-immediate-NORMAL-g256-hold1ms", "Transactions, 256 goroutines, immediate, 1 ms of work in each", "事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事", true},
}

func cmdContention(args []string) error {
	fs := flag.NewFlagSet("contention", flag.ExitOnError)
	run := fs.Duration("time", 30*time.Second, "how long each case runs on each driver")
	only := fs.String("drivers", strings.Join(drivers, ","), "the drivers, by build tag")
	out := fs.String("out", filepath.Join("results", runtime.GOOS+"-"+runtime.GOARCH, "contention"), "where the results go")
	samples := fs.Bool("samples", false, "write every write of each case to <out>/samples")
	name := fs.String("name", runtime.GOOS+"-"+runtime.GOARCH, "the name of the run, as its markers in the docs name it")
	buildOnly := fs.Bool("build-only", false, "build and check the test binaries, run nothing")
	noBuild := fs.Bool("no-build", false, "use the test binaries of an earlier -build-only")
	fs.Parse(args)
	tags := strings.Split(*only, ",")
	for _, t := range tags {
		if !known(t) {
			return fmt.Errorf("unknown driver %q", t)
		}
	}
	bin := filepath.Join(*out, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	if !*noBuild {
		for _, t := range tags {
			if err := buildDriver(t, bin); err != nil {
				return err
			}
		}
	}
	if *buildOnly {
		return nil
	}
	set := contentionSet{Name: *name, Meta: machine(), Drivers: tags}
	set.Meta.Set("time", run.String())
	for _, t := range tags {
		if cgo[t] {
			// The C compiler cgo built mattn with, as `go env CC` names it.
			cc := strings.TrimSpace(output("go", "env", "CC"))
			set.Meta.Set("cc", "cgo with "+strings.TrimSpace(strings.SplitN(output(cc, "--version"), "\n", 2)[0]))
			break
		}
	}
	set.Meta.Set("load_before", loadAverage())
	os.Setenv("CONTENTION", run.String())
	if *samples {
		dir, err := filepath.Abs(filepath.Join(*out, "samples"))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		os.Setenv("CONTENTION_DIR", dir)
	}
	for _, t := range tags {
		logf("contention: %s", t)
		var buf bytes.Buffer
		err := runIn(bin, &buf, "./"+testBin(t, runtime.GOOS), "-test.run=^TestContention$", "-test.v", "-test.count=1", "-test.timeout=2h")
		if werr := os.WriteFile(filepath.Join(*out, t+".txt"), buf.Bytes(), 0o644); werr != nil {
			return werr
		}
		if err != nil {
			return fmt.Errorf("%s: %w (see %s.txt)", t, err, t)
		}
		rs, err := parseContention(buf.Bytes())
		if err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		set.Results = append(set.Results, rs...)
	}
	set.Meta.Set("load_after", loadAverage())
	b, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "contention.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Print(renderContention(&set, en, false))
	return nil
}

// parseContention reads the results of TestContention from its output.
func parseContention(out []byte) ([]contentionResult, error) {
	var rs []contentionResult
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		_, js, ok := strings.Cut(sc.Text(), resultPrefix)
		if !ok {
			continue
		}
		var r contentionResult
		if err := json.Unmarshal([]byte(js), &r); err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	if len(rs) != len(contentionCases) {
		return nil, fmt.Errorf("%d results, want %d", len(rs), len(contentionCases))
	}
	return rs, sc.Err()
}

// loadContention reads published/contention/*.json.
func loadContention() (map[string]*contentionSet, error) {
	files, err := filepath.Glob(filepath.Join("published", "contention", "*.json"))
	if err != nil {
		return nil, err
	}
	sets := map[string]*contentionSet{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var s contentionSet
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if want := strings.TrimSuffix(filepath.Base(f), ".json"); s.Name != want {
			return nil, fmt.Errorf("%s: the set is named %q", f, s.Name)
		}
		sets[s.Name] = &s
	}
	return sets, nil
}

// renderContention is the table of a run of TestContention: for each case
// and driver, the slowest write that succeeded and how many got "database
// is locked". The README has some of the cases, the docs all of them with
// the writes per second too.
func renderContention(s *contentionSet, l *lang, readmeTable bool) string {
	var b strings.Builder
	res := map[[2]string]contentionResult{}
	for _, r := range s.Results {
		res[[2]string{r.Case, r.Driver}] = r
	}
	secs := s.Meta.Get("time")
	if d, err := time.ParseDuration(secs); err == nil {
		secs = strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " s"
		if l.zh {
			secs = strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " 秒"
		}
	}
	if readmeTable {
		b.WriteString(readmeHeading(s.Name, s.Meta, l))
	} else {
		b.WriteString(metaLine(s.Meta, l) + "\n\n")
	}
	table := func(intro string, cell func(r contentionResult) string, all bool) {
		b.WriteString(intro + "\n\n")
		head, rule := "| Workload", "| :--"
		if l.zh {
			head = "| 负载"
		}
		for _, d := range s.Drivers {
			name := driverName(d, l)
			if readmeTable && driverHead[d] != "" {
				name = driverHead[d]
			}
			head += " | " + name
			rule += " | --:"
		}
		b.WriteString(head + " |\n" + rule + " |\n")
		for _, c := range contentionCases {
			if !all && !c.readme {
				continue
			}
			label := c.en
			if l.zh {
				label = c.zh
			}
			b.WriteString("| " + label)
			for _, d := range s.Drivers {
				b.WriteString(" | " + cell(res[[2]string{c.key, d}]))
			}
			b.WriteString(" |\n")
		}
		b.WriteString("\n")
	}
	wait := func(r contentionResult) string {
		v := value(float64(r.Max)/1e9, "sec/op")
		if r.Locked == 0 {
			return v
		}
		if l.zh {
			return fmt.Sprintf("%s，%d 次锁错误", v, r.Locked)
		}
		return fmt.Sprintf("%s, %d locked", v, r.Locked)
	}
	waitIntro := "**The slowest write** (a transaction, or an autocommit statement), and how many writes got `database is locked`, in " + secs + " of each:"
	rateIntro := "**Writes done per second**, all goroutines together:"
	if l.zh {
		waitIntro = "**最慢的一次写**（一个事务，或一条自动提交的语句），以及每项 " + secs + "里有多少次写报 `database is locked`："
		rateIntro = "**每秒完成的写**，所有 goroutine 加起来："
	}
	table(waitIntro, wait, !readmeTable)
	if !readmeTable {
		table(rateIntro, func(r contentionResult) string { return value(float64(r.Done)/r.Seconds, "writes/s") }, true)
	}
	return b.String()
}

// readmeContention lists the runs of TestContention with a README table
// (<!-- bench:readme-contention-NAME -->).
var readmeContention = []string{"linux-amd64"}
