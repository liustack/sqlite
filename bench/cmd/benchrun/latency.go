// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// cmdLatency measures how long a request to a server takes, for each
// driver. The server (bench/latency) is a process of its own with the cores
// -procs gives it. The requests are sent from here at a fixed rate,
// whatever the server does, and each is timed from the moment it was due:
// a server that falls behind cannot slow the arrivals down, so the time it
// loses shows. The rates are parts of what the slowest driver answers at
// most, so every driver gets the same load.
func cmdLatency(args []string) error {
	fs := flag.NewFlagSet("latency", flag.ExitOnError)
	only := fs.String("drivers", strings.Join(drivers, ","), "the drivers, by build tag")
	rounds := fs.Int("rounds", 3, "rounds: each driver takes each load once per round")
	secs := fs.Float64("secs", 5, "seconds of arrivals that count")
	warm := fs.Float64("warm", 1, "seconds of arrivals before, not counted")
	procs := fs.Int("procs", 2, "GOMAXPROCS of the server")
	conns := fs.Int("conns", 32, "connections of the server's pool")
	reads := fs.Int("reads", 32, "point reads of one read request")
	writes := fs.Float64("writes", 500, "write requests a second, beside the reads")
	loads := fs.String("loads", "40,70", "read requests a second, in percent of the most the slowest driver answers")
	clients := fs.Int("clients", 256, "connections to the server at most, for reads and for writes each")
	wrap := fs.String("wrap", "", "a command to start the server with, such as \"taskset -c 0,1\"")
	out := fs.String("out", filepath.Join("results", "latency-"+runtime.GOOS+"-"+runtime.GOARCH), "where the server binaries go")
	fs.Parse(args)
	tags := strings.Split(*only, ",")
	for _, t := range tags {
		if !known(t) {
			return fmt.Errorf("unknown driver %q (want %s)", t, strings.Join(append(drivers, optional...), ", "))
		}
	}
	var percents []float64
	for _, s := range strings.Split(*loads, ",") {
		p, err := strconv.ParseFloat(s, 64)
		if err != nil || p <= 0 {
			return fmt.Errorf("-loads: %q is not a percentage", s)
		}
		percents = append(percents, p)
	}
	bin, err := filepath.Abs(filepath.Join(*out, "bin"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	for _, t := range tags {
		logf("build %s", t)
		cmd := exec.Command("go", "build", "-tags", t, "-o", filepath.Join(bin, t+"-latency"), "./latency")
		cmd.Env = append(os.Environ(), "CGO_ENABLED="+map[bool]string{false: "0", true: "1"}[cgo[t]])
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", t, err)
		}
	}
	start := func(t string) (*latencyServer, error) {
		return startLatencyServer(filepath.Join(bin, t+"-latency"), strings.Fields(*wrap), *procs, *conns, *reads)
	}

	// The most each driver answers: every client asks again as soon as it
	// has its answer. Twice, the second time in the other order, and the
	// lower counts.
	most := map[string]float64{}
	for pass := range 2 {
		order := slices.Clone(tags)
		if pass == 1 {
			slices.Reverse(order)
		}
		for _, t := range order {
			srv, err := start(t)
			if err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
			n := flood(srv.url+"/r", *clients, 3*time.Second)
			if err := srv.stop(); err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
			logf("%s answers %.0f read requests a second at most", t, n)
			if m, ok := most[t]; !ok || n < m {
				most[t] = n
			}
		}
	}
	base := 0.0
	for _, t := range tags {
		if base == 0 || most[t] < base {
			base = most[t]
		}
	}
	if base == 0 {
		return fmt.Errorf("a server answered nothing")
	}
	fmt.Printf("%s/%s, GOMAXPROCS=%d of the server, a pool of %d connections, %d point reads a read request, %.0f writes a second.\n",
		runtime.GOOS, runtime.GOARCH, *procs, *conns, *reads, *writes)
	fmt.Printf("The slowest driver answers %.0f read requests a second at most.\n\n", base)

	type key struct {
		pct    float64
		driver string
	}
	got := map[key][]loadResult{}
	for r := range *rounds {
		for i := range tags {
			t := tags[(r+i)%len(tags)]
			for _, pct := range percents {
				srv, err := start(t)
				if err != nil {
					return fmt.Errorf("%s: %w", t, err)
				}
				res := load(srv.url, base*pct/100, *writes, *secs, *warm, *clients)
				if err := srv.stop(); err != nil {
					return fmt.Errorf("%s: %w", t, err)
				}
				logf("round %d/%d: %s at %.0f%%: read %s | write %s", r+1, *rounds, t, pct, res.read, res.write)
				got[key{pct, t}] = append(got[key{pct, t}], res)
			}
		}
	}

	fmt.Println("| Load | Driver | Reads answered | Read p50 | Read p99 | Read max | Write p50 | Write p99 | Write max |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, pct := range percents {
		for _, t := range tags {
			rs := got[key{pct, t}]
			mid := func(f func(loadResult) time.Duration) string {
				var ds []time.Duration
				for _, r := range rs {
					ds = append(ds, f(r))
				}
				slices.Sort(ds)
				return fmt.Sprint(ds[len(ds)/2])
			}
			answered := rs[0].read.served
			for _, r := range rs {
				answered = min(answered, r.read.served)
			}
			fmt.Printf("| %.0f%% (%.0f/s) | %s | %.0f/s | %s | %s | %s | %s | %s | %s |\n", pct, base*pct/100, t, answered,
				mid(func(r loadResult) time.Duration { return r.read.p50 }),
				mid(func(r loadResult) time.Duration { return r.read.p99 }),
				mid(func(r loadResult) time.Duration { return r.read.max }),
				mid(func(r loadResult) time.Duration { return r.write.p50 }),
				mid(func(r loadResult) time.Duration { return r.write.p99 }),
				mid(func(r loadResult) time.Duration { return r.write.max }))
		}
	}
	fmt.Printf("\nEach time is the median of %d rounds, and the reads answered are the fewest of them. A driver that answers fewer reads than were sent is past what it can do at that load, and its times say how far behind it fell.\n", *rounds)
	return nil
}

// A latencyServer is a running bench/latency.
type latencyServer struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	url   string
}

func startLatencyServer(bin string, wrap []string, procs, conns, reads int) (*latencyServer, error) {
	argv := append(slices.Clone(wrap), bin, "-conns", strconv.Itoa(conns), "-n", strconv.Itoa(reads))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(procs))
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	addr, ok := strings.CutPrefix(strings.TrimSpace(line), "LISTEN ")
	if err != nil || !ok {
		stdin.Close()
		cmd.Wait()
		return nil, fmt.Errorf("the server did not say where it listens (%q, %v)", line, err)
	}
	go io.Copy(io.Discard, stdout)
	return &latencyServer{cmd: cmd, stdin: stdin, url: "http://" + addr}, nil
}

// stop closes the server's standard input, which ends it.
func (s *latencyServer) stop() error {
	s.stdin.Close()
	return s.cmd.Wait()
}

func latencyClient(clients int) *http.Client {
	return &http.Client{Transport: &http.Transport{MaxIdleConns: 2 * clients, MaxIdleConnsPerHost: 2 * clients}}
}

// get says whether the server answered url with 200.
func get(c *http.Client, url string) bool {
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// flood has clients ask url again and again for a second that does not
// count and for d that does, and returns the answers a second.
func flood(url string, clients int, d time.Duration) float64 {
	c := latencyClient(clients)
	defer c.CloseIdleConnections()
	from := time.Now().Add(time.Second)
	to := from.Add(d)
	var n atomic.Int64
	var wg sync.WaitGroup
	for range clients {
		wg.Go(func() {
			for {
				ok := get(c, url)
				now := time.Now()
				if now.After(to) {
					return
				}
				if ok && now.After(from) {
					n.Add(1)
				}
			}
		})
	}
	wg.Wait()
	return float64(n.Load()) / d.Seconds()
}

// A streamResult is what one kind of request got in a load.
type streamResult struct {
	served        float64 // answers a second in the seconds that count
	late, errors  int64   // never sent because the run was over, and failed
	p50, p99, max time.Duration
}

func (r streamResult) String() string {
	s := fmt.Sprintf("%.0f/s p50 %v p99 %v max %v", r.served, r.p50, r.p99, r.max)
	if r.late != 0 || r.errors != 0 {
		s += fmt.Sprintf(" (%d never sent, %d failed)", r.late, r.errors)
	}
	return s
}

type loadResult struct{ read, write streamResult }

// load sends reads and writes at their rates for warm and then secs
// seconds, and times each from the moment it was due.
func load(url string, readRate, writeRate, secs, warm float64, clients int) loadResult {
	c := latencyClient(clients)
	defer c.CloseIdleConnections()
	start := time.Now().Add(100 * time.Millisecond)
	count := start.Add(time.Duration(warm * float64(time.Second)))
	end := count.Add(time.Duration(secs * float64(time.Second)))
	var res loadResult
	var wg sync.WaitGroup
	wg.Go(func() { res.read = stream(c, url+"/r", readRate, start, count, end, clients) })
	wg.Go(func() { res.write = stream(c, url+"/w", writeRate, start, count, end, clients) })
	wg.Wait()
	return res
}

// stream sends requests to url, rate a second from start to end. An
// arrival that finds every client busy waits for one, and that wait is in
// its time. Arrivals still waiting 2 seconds after end are not sent.
func stream(c *http.Client, url string, rate float64, start, count, end time.Time, clients int) streamResult {
	var res streamResult
	if rate <= 0 {
		return res
	}
	// The channel holds every arrival of the run, so making them never
	// waits for the server.
	gap := time.Duration(float64(time.Second) / rate)
	n := int(end.Sub(start)/gap) + 1
	due := make(chan time.Time, n)
	for i := range n {
		due <- start.Add(time.Duration(i) * gap)
	}
	close(due)
	var (
		mu                 sync.Mutex
		times              []time.Duration
		served, late, errs atomic.Int64
		wg                 sync.WaitGroup
	)
	for range clients {
		wg.Go(func() {
			for at := range due {
				time.Sleep(time.Until(at))
				if time.Now().After(end.Add(2 * time.Second)) {
					late.Add(1)
					continue
				}
				if !get(c, url) {
					errs.Add(1)
					continue
				}
				now := time.Now()
				if now.After(count) && now.Before(end) {
					served.Add(1)
				}
				if at.Before(count) {
					continue
				}
				mu.Lock()
				times = append(times, now.Sub(at))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	res.served = float64(served.Load()) / end.Sub(count).Seconds()
	res.late, res.errors = late.Load(), errs.Load()
	if len(times) == 0 {
		return res
	}
	slices.Sort(times)
	at := func(p float64) time.Duration {
		return times[int(p*float64(len(times)-1))].Round(10 * time.Microsecond)
	}
	res.p50, res.p99, res.max = at(0.5), at(0.99), at(1)
	return res
}
