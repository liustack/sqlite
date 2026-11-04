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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// drivers are the build tags of bench/, in the order of the table columns.
var drivers = []string{"liustack", "mattn", "modernc", "ncruces"}

// cgo says which drivers need cgo.
var cgo = map[string]bool{"mattn": true}

// A Pair is one line of what a run records about itself.
type Pair struct{ K, V string }

// Meta is what a run records about itself, in order.
type Meta []Pair

func (m Meta) Get(k string) string {
	for _, p := range m {
		if p.K == k {
			return p.V
		}
	}
	return ""
}

func (m *Meta) Set(k, v string) {
	for i, p := range *m {
		if p.K == k {
			(*m)[i].V = v
			return
		}
	}
	*m = append(*m, Pair{k, v})
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	count := fs.Int("count", 6, "rounds: each driver runs every benchmark once per round")
	benchtime := fs.String("benchtime", "1s", "-test.benchtime of each benchmark")
	bench := fs.String("bench", ".", "-test.bench: the benchmarks to run")
	only := fs.String("drivers", strings.Join(drivers, ","), "the drivers, by build tag")
	out := fs.String("out", filepath.Join("results", runtime.GOOS+"-"+runtime.GOARCH), "where the results go")
	fs.Parse(args)
	tags := strings.Split(*only, ",")
	for _, t := range tags {
		if !contains(drivers, t) {
			return fmt.Errorf("unknown driver %q (want %s)", t, strings.Join(drivers, ", "))
		}
	}
	bin := filepath.Join(*out, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}

	meta := machine()
	meta.Set("count", fmt.Sprint(*count))
	meta.Set("benchtime", *benchtime)
	meta.Set("bench", *bench)
	meta.Set("drivers", strings.Join(tags, ","))
	meta.Set("load_before", loadAverage())

	for _, t := range tags {
		logf("build %s", t)
		cmd := exec.Command("go", "test", "-c", "-tags", t, "-o", filepath.Join(bin, t+".test"), ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED="+map[bool]string{false: "0", true: "1"}[cgo[t]])
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", t, err)
		}
		// The settings every driver must run with (pragma_test.go).
		if err := runIn(bin, nil, "./"+t+".test", "-test.count=1", "-test.run=^TestPragmas$"); err != nil {
			return fmt.Errorf("%s: settings: %w", t, err)
		}
		if err := os.WriteFile(filepath.Join(*out, t+".txt"), nil, 0o644); err != nil {
			return err
		}
	}

	start := time.Now()
	for r := range *count {
		// Each round starts with the next driver, so that none always runs
		// first or always after the same one.
		for i := range tags {
			t := tags[(r+i)%len(tags)]
			logf("round %d/%d: %s", r+1, *count, t)
			var buf bytes.Buffer
			if err := runIn(bin, &buf, "./"+t+".test", "-test.run=^$", "-test.bench="+*bench,
				"-test.benchmem", "-test.count=1", "-test.benchtime="+*benchtime); err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
			if err := appendFile(filepath.Join(*out, t+".txt"), buf.Bytes()); err != nil {
				return err
			}
		}
		logf("load average: %s", loadAverage())
	}
	meta.Set("load_after", loadAverage())
	meta.Set("minutes", fmt.Sprintf("%.1f", time.Since(start).Minutes()))
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "meta.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	set, err := summarize(*out, "")
	if err != nil {
		return err
	}
	fmt.Print(renderSet(set, en))
	return nil
}

// machine describes the machine and the code under test.
func machine() Meta {
	var m Meta
	m.Set("date", time.Now().UTC().Format("2006-01-02"))
	commit := strings.TrimSpace(output("git", "rev-parse", "--short", "HEAD"))
	if strings.TrimSpace(output("git", "status", "--porcelain", "--untracked-files=no")) != "" {
		commit += " (with changes not committed)"
	}
	m.Set("commit", commit)
	m.Set("go", strings.TrimPrefix(runtime.Version(), "go"))
	m.Set("os", strings.TrimSpace(output("uname", "-srm")))
	m.Set("cpu", cpuModel())
	m.Set("threads", fmt.Sprint(runtime.NumCPU()))
	if g := os.Getenv("GOMAXPROCS"); g != "" {
		m.Set("gomaxprocs", g)
	} else {
		m.Set("gomaxprocs", fmt.Sprint(runtime.NumCPU()))
	}
	m.Set("modules", strings.Join(strings.Fields(output("go", "list", "-m",
		"github.com/mattn/go-sqlite3", "modernc.org/sqlite", "github.com/ncruces/go-sqlite3")), " "))
	return m
}

func cpuModel() string {
	if s := strings.TrimSpace(output("sysctl", "-n", "machdep.cpu.brand_string")); s != "" {
		return s
	}
	b, _ := os.ReadFile("/proc/cpuinfo")
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return "unknown"
}

// loadAverage is the 1, 5 and 15 minute load averages.
func loadAverage() string {
	s := output("uptime")
	if _, after, ok := strings.Cut(s, "load average"); ok {
		after = strings.TrimLeft(after, "s: ")
		return strings.Join(strings.Fields(strings.ReplaceAll(after, ",", " ")), " ")
	}
	return strings.TrimSpace(s)
}

// output is the standard output of a command, "" when it fails.
func output(name string, args ...string) string {
	b, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(b)
}

// runIn runs a command in dir, its standard output into w (or standard
// error when w is nil).
func runIn(dir string, w *bytes.Buffer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	if w != nil {
		cmd.Stdout = w
	} else {
		cmd.Stdout = os.Stderr
	}
	return cmd.Run()
}

func appendFile(name string, b []byte) error {
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, ">> "+format+"\n", args...)
}
