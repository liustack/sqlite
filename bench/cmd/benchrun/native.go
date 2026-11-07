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

// The columns of a comparison with SQLite in C.
var nativeDrivers = []string{"c", "liustack-raw", "liustack", "mattn"}

// A nativeCase is one run of native/c/threads.c and of native/main.go.
type nativeCase struct {
	bench   string // the name of the benchmark, without Benchmark
	mode    string // read, write or mixed
	n       int
	sync    string
	cache   string   // cache_size, 0 for SQLite's
	drivers []string // the columns it runs in
}

var nativeCases = func() []nativeCase {
	var cs []nativeCase
	for _, n := range []int{1, 8, 64} {
		cs = append(cs, nativeCase{fmt.Sprintf("Point/g=%d", n), "read", n, "NORMAL", "0", nativeDrivers})
	}
	for _, n := range []int{1, 8, 64} {
		cs = append(cs, nativeCase{fmt.Sprintf("PointCached/g=%d", n), "read", n, "NORMAL", "-65536", nativeDrivers})
	}
	return append(cs,
		nativeCase{"PointInsert/g=8", "mixed", 8, "NORMAL", "0", nativeDrivers},
		nativeCase{"InsertFull/g=64", "write", 64, "FULL", "0", nativeDrivers})
}()

func cmdNative(args []string) error {
	fs := flag.NewFlagSet("native", flag.ExitOnError)
	count := fs.Int("count", 6, "rounds")
	reps := fs.Int("reps", 5, "runs of each engine workload in a round, of which the median counts")
	secs := fs.Int("secs", 3, "seconds of each run of threads")
	cc := fs.String("cc", "", "the C compiler (default: clang of scripts/llvm-bin.sh)")
	out := fs.String("out", filepath.Join("results", "native-"+runtime.GOOS+"-"+runtime.GOARCH), "where the results go")
	fs.Parse(args)
	if *cc == "" {
		*cc = filepath.Join(strings.TrimSpace(output("../scripts/llvm-bin.sh")), "clang")
	}
	bin := filepath.Join(*out, "bin")
	dbs := filepath.Join(*out, "db")
	for _, d := range []string{bin, dbs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := buildNative(*cc, bin); err != nil {
		return err
	}
	sql, err := filepath.Abs(filepath.Join("native", "engine.sql"))
	if err != nil {
		return err
	}

	meta := machine()
	meta.Set("cc", strings.TrimSpace(strings.SplitN(output(*cc, "--version"), "\n", 2)[0]))
	meta.Set("count", fmt.Sprint(*count))
	meta.Set("reps", fmt.Sprint(*reps))
	meta.Set("secs", fmt.Sprint(*secs))
	meta.Set("drivers", strings.Join(nativeDrivers, ","))
	meta.Set("load_before", loadAverage())
	results := map[string]*bytes.Buffer{}
	for _, d := range nativeDrivers {
		results[d] = &bytes.Buffer{}
	}
	start := time.Now()
	for r := range *count {
		logf("round %d/%d: engine", r+1, *count)
		checks := map[string]string{}
		for i := range 2 {
			d := []string{"c", "liustack"}[(r+i)%2]
			var buf bytes.Buffer
			prog, a := filepath.Join(bin, "c-engine"), []string{sql, fmt.Sprint(*reps)}
			if d == "liustack" {
				prog, a = filepath.Join(bin, "liustack"), append([]string{"engine"}, a...)
			}
			if err := runIn(".", &buf, prog, a...); err != nil {
				return fmt.Errorf("%s engine: %w", d, err)
			}
			for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				f := strings.SplitN(line, "\t", 3)
				if len(f) != 3 {
					return fmt.Errorf("%s engine: %q", d, line)
				}
				if c, ok := checks[f[0]]; ok && c != f[2] {
					return fmt.Errorf("engine %s: c gives %q, liustack %q", f[0], c, f[2])
				}
				checks[f[0]] = f[2]
				fmt.Fprintf(results[d], "BenchmarkEngine/%s 1 %s ns/op\n", f[0], f[1])
			}
		}
		for _, c := range nativeCases {
			logf("round %d/%d: %s", r+1, *count, c.bench)
			for i := range c.drivers {
				d := c.drivers[(r+i)%len(c.drivers)]
				a := []string{c.mode, fmt.Sprint(c.n), fmt.Sprint(*secs), c.sync, c.cache}
				prog := filepath.Join(bin, "c-threads")
				if d != "c" {
					build, raw := strings.TrimSuffix(d, "-raw"), "0"
					if build != d {
						raw = "1"
					}
					prog, a = filepath.Join(bin, build), append(append([]string{"threads"}, a...), raw)
				}
				a = append(a, filepath.Join(dbs, d+".db"))
				var buf bytes.Buffer
				if err := runIn(".", &buf, prog, a...); err != nil {
					return fmt.Errorf("%s %s: %w", d, c.bench, err)
				}
				f := strings.Fields(buf.String())
				if len(f) != 6 || f[0] != "reads/s" || f[2] != "writes/s" || f[4] != "errors" {
					return fmt.Errorf("%s %s: %q", d, c.bench, buf.String())
				}
				if f[5] != "0" {
					return fmt.Errorf("%s %s: %s errors", d, c.bench, f[5])
				}
				line := "Benchmark" + c.bench + " 1"
				if c.mode != "write" {
					line += " " + f[1] + " reads/s"
				}
				if c.mode != "read" {
					line += " " + f[3] + " writes/s"
				}
				results[d].WriteString(line + "\n")
			}
		}
		logf("load average: %s", loadAverage())
	}
	meta.Set("load_after", loadAverage())
	meta.Set("minutes", fmt.Sprintf("%.1f", time.Since(start).Minutes()))
	for d, b := range results {
		if err := os.WriteFile(filepath.Join(*out, d+".txt"), b.Bytes(), 0o644); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "meta.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(dbs); err != nil {
		return err
	}
	set, err := summarize(*out, "")
	if err != nil {
		return err
	}
	fmt.Print(renderSet(set, en))
	return nil
}

// buildNative builds bin/c-engine and bin/c-threads from native/c/engine.c and
// threads.c with the SQLite of engine/ and the options of engine/sqlite.c,
// and bin/liustack and bin/mattn from native/main.go with the tag of each.
func buildNative(cc, bin string) error {
	src, err := os.ReadFile("../engine/sqlite.c")
	if err != nil {
		return err
	}
	// engine/sqlite.c less what joins SQLite to the Go host: SQLite's own
	// VFS and mutexes take their place.
	opts := string(src)
	for _, host := range []string{"#define SQLITE_OS_OTHER 1\n", "#include \"os.c\"\n"} {
		if strings.Count(opts, host) != 1 {
			return fmt.Errorf("engine/sqlite.c: want one %q", strings.TrimSpace(host))
		}
		opts = strings.Replace(opts, host, "", 1)
	}
	csrc := filepath.Join(bin, "sqlite.c")
	if err := os.WriteFile(csrc, []byte(opts), 0o644); err != nil {
		return err
	}
	engine, err := filepath.Abs("../engine")
	if err != nil {
		return err
	}
	flags := []string{"-O2", "-fno-math-errno", "-ffp-contract=off", "-DHAVE_USLEEP=1", "-I", engine}
	if runtime.GOOS == "darwin" {
		flags = append(flags, "-isysroot", strings.TrimSpace(output("xcrun", "--show-sdk-path")))
	}
	logf("build c with %s", cc)
	obj := filepath.Join(bin, "sqlite.o")
	if err := runIn(".", nil, cc, append(flags, "-c", "-o", obj, csrc)...); err != nil {
		return fmt.Errorf("build sqlite: %w", err)
	}
	for _, p := range []struct{ out, src string }{{"c-engine", "engine.c"}, {"c-threads", "threads.c"}} {
		a := append(flags, "-o", filepath.Join(bin, p.out), filepath.Join("native", "c", p.src), obj, "-lpthread", "-lm")
		if err := runIn(".", nil, cc, a...); err != nil {
			return fmt.Errorf("build %s: %w", p.src, err)
		}
	}
	for _, d := range []struct {
		tag, cgo string
	}{{"liustack", "0"}, {"mattn", "1"}} {
		logf("build %s", d.tag)
		cmd := exec.Command("go", "build", "-tags", d.tag, "-o", filepath.Join(bin, d.tag), "./native")
		cmd.Env = append(os.Environ(), "CGO_ENABLED="+d.cgo)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", d.tag, err)
		}
	}
	return nil
}

// nativeWork says what each benchmark of a comparison with C does, in
// words a reader knows, for the tables of the docs: the name of an Engine
// block of native/engine.sql, or the workload of native/main.go.
var nativeWork = map[string]struct{ en, zh string }{
	"Engine/insert": {"Insert 1M rows in one statement", "一条语句插入 100 万行"},
	"Engine/index":  {"Index 1M texts", "给 100 万个文本建索引"},
	"Engine/sort":   {"Sort 1M texts", "排序 100 万个文本"},
	"Engine/group":  {"Average 1M rows in 1000 groups", "100 万行分 1000 组求平均"},
	"Engine/join":   {"Join 300k rows to a table of 1M on its key", "30 万行按主键连 100 万行的表"},
	"Engine/cte":    {"Recursive CTE of 2M steps", "递归 CTE 走 200 万步"},
	"Engine/like":   {"LIKE and string functions on 1M texts", "100 万个文本做 LIKE 和字符串函数"},
	"Engine/json":   {"Build and read 300k JSON objects", "生成并读取 30 万个 JSON 对象"},
	"Engine/math":   {"sqrt, sin, log and pow of 1M numbers", "100 万个数算 sqrt、sin、log、pow"},
	"Engine/fts5":   {"4 FTS5 searches in 200k documents", "20 万篇文档里做 4 次 FTS5 搜索"},
	"Point":         {"Read one row by key, %s threads", "按主键读一行，%s 个线程"},
	"PointCached":   {"Read one row by key, whole file in cache, %s threads", "按主键读一行，整个文件在缓存里，%s 个线程"},
	"PointInsert/r": {"8 threads read while 8 others write: the reads", "8 个线程读、另 8 个同时写：读的速率"},
	"PointInsert/w": {"8 threads read while 8 others write: the writes", "8 个线程读、另 8 个同时写：写的速率"},
	"InsertFull":    {"Insert one row, `synchronous=FULL`, %s threads", "插入一行，`synchronous=FULL`，%s 个线程"},
}

// nativeLabel is r in the words of nativeWork.
func nativeLabel(r Row, l *lang) (string, bool) {
	key, threads, _ := strings.Cut(r.Bench, "/g=")
	if strings.HasPrefix(r.Bench, "Engine/") {
		key, threads = r.Bench, ""
	} else if key == "PointInsert" {
		key += "/" + r.Unit[:1]
	}
	w, ok := nativeWork[key]
	if !ok {
		return "", false
	}
	s := w.en
	if l.zh {
		s = w.zh
	}
	if strings.Contains(s, "%s") {
		s = fmt.Sprintf(s, threads)
	}
	return s, true
}

// nativeColumns names the columns of a comparison with C by how each one
// reads, for the tables of the docs.
var nativeColumns = map[string]struct{ en, zh string }{
	"c":            {"SQLite in C", "C 版 SQLite"},
	"liustack-raw": {"this driver, without database/sql", "本驱动，不经 database/sql"},
	"liustack":     {"this driver, through database/sql", "本驱动，经 database/sql"},
	"mattn":        {"mattn (cgo), through database/sql", "mattn（cgo），经 database/sql"},
}
