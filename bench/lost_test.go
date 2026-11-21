// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build liustack

package bench

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liustack/sqlite/replica"
)

// TestLostOnKill measures what a lost machine costs a replicated database.
// A process of its own replicates a database to LOST_URL while goroutines
// insert into it, and is killed (SIGKILL) LOST after it starts writing: its
// uploads stop at that moment, as they would if the machine were lost. The
// test then restores the replica into a new directory, times the restore,
// and reports how many acknowledged inserts are not in it and how long
// before the kill the newest one there was made. It skips unless LOST and
// LOST_URL are set, and takes the credentials of the store from
// AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY:
//
//	LOST=10s LOST_URL='s3://bucket/path?endpoint=...&region=auto' \
//	  go test -tags liustack -run '^TestLostOnKill$' -v
//
// LOST_RATE caps the inserts a second (0, the default, for as many as the
// goroutines make), LOST_G sets the goroutines (64), LOST_PAD the bytes of
// each row (100) and LOST_ROUNDS how many kills (3). Each round replicates
// to a path of its own under LOST_URL.
func TestLostOnKill(t *testing.T) {
	if os.Getenv("LOST_CHILD") != "" {
		lostWrite(t)
		return
	}
	run, err := time.ParseDuration(os.Getenv("LOST"))
	base := os.Getenv("LOST_URL")
	if err != nil || base == "" {
		t.Skip("set LOST to how long the inserts run before the kill, as 10s, and LOST_URL to the store")
	}
	rounds := envInt(t, "LOST_ROUNDS", 3)
	for round := range rounds {
		url := roundURL(t, base, fmt.Sprintf("lost-%d-%d", time.Now().UnixNano(), round))
		res := lostRound(t, url, run)
		b, _ := json.Marshal(res)
		t.Logf("lost-result %s", b)
	}
}

type lostResult struct {
	Rate        int     `json:"rate"`
	G           int     `json:"g"`
	Acked       int64   `json:"acked"`
	Restored    int64   `json:"restored"`
	Lost        int64   `json:"lost_at_least"`
	NewestMs    float64 `json:"newest_before_kill_ms"`
	DBBytes     int64   `json:"db_bytes"`
	RestoreMs   float64 `json:"restore_ms"`
	StatusBytes int64   `json:"pending_bytes_at_kill"`
}

func lostRound(t *testing.T, url string, run time.Duration) lostResult {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	cmd := osexec.Command(os.Args[0], "-test.run", "^TestLostOnKill$")
	cmd.Env = append(os.Environ(), "LOST_CHILD="+path, "LOST_CHILD_URL="+url)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1024)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	if line := <-lines; line != "ready" {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("the process that writes said %q", line)
	}
	// The child reports the inserts acknowledged so far every 10 ms. The
	// last report before the kill is a lower bound of what was acknowledged.
	var acked, pending int64
	stop := time.After(run)
read:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				cmd.Wait()
				t.Fatal("the process that writes stopped")
			}
			var n, p int64
			if _, err := fmt.Sscanf(line, "acked %d pending %d", &n, &p); err != nil {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("the process that writes said %q", line)
			}
			acked, pending = n, p
		case <-stop:
			break read
		}
	}
	cmd.Process.Kill()
	killed := time.Now()
	cmd.Wait()
	for range lines {
	}

	out := filepath.Join(t.TempDir(), "restored.db")
	start := time.Now()
	if _, err := replica.Restore(context.Background(), out, replica.Config{URL: url}, replica.RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	took := time.Since(start)
	db, err := sql.Open(DriverName, out)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n, newest int64
	if err := db.QueryRow(`SELECT count(*), coalesce(max(at), 0) FROM log`).Scan(&n, &newest); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	return lostResult{
		Rate:        envInt(t, "LOST_RATE", 0),
		G:           envInt(t, "LOST_G", 64),
		Acked:       acked,
		Restored:    n,
		Lost:        max(acked-n, 0),
		NewestMs:    float64(killed.UnixNano()-newest) / 1e6,
		DBBytes:     st.Size(),
		RestoreMs:   float64(took.Microseconds()) / 1e3,
		StatusBytes: pending,
	}
}

// lostWrite runs in the child: it replicates, inserts until it is killed,
// and reports what was acknowledged.
func lostWrite(t *testing.T) {
	path, url := os.Getenv("LOST_CHILD"), os.Getenv("LOST_CHILD_URL")
	r, err := replica.New(context.Background(), path, replica.Config{
		URL:     url,
		OnError: func(err error) { fmt.Fprintln(os.Stderr, "replica:", err) },
	})
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	db, err := sql.Open(DriverName, DSN(path, "NORMAL"))
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	g, rate, pad := envInt(t, "LOST_G", 64), envInt(t, "LOST_RATE", 0), envInt(t, "LOST_PAD", 100)
	db.SetMaxOpenConns(g)
	db.SetMaxIdleConns(g)
	if _, err := db.Exec(`CREATE TABLE log(g INTEGER, at INTEGER, pad BLOB)`); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	if err := r.Sync(context.Background()); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("ready")
	var acked atomic.Int64
	// With a rate, each goroutine waits for its turn: one insert every
	// g/rate seconds, spread over the goroutines.
	var every time.Duration
	if rate > 0 {
		every = time.Duration(float64(time.Second) * float64(g) / float64(rate))
	}
	for i := range g {
		go func() {
			next := time.Now().Add(every * time.Duration(i) / time.Duration(g))
			for {
				if every > 0 {
					time.Sleep(time.Until(next))
					next = next.Add(every)
				}
				// at is taken before the insert, so a row's at is never
				// later than the moment it was acknowledged.
				if _, err := db.Exec(`INSERT INTO log VALUES (?, ?, randomblob(?))`, i, time.Now().UnixNano(), pad); err != nil {
					fmt.Println("error:", err)
					os.Exit(1)
				}
				acked.Add(1)
			}
		}()
	}
	w := bufio.NewWriter(os.Stdout)
	for range time.Tick(10 * time.Millisecond) {
		st := r.Status()
		fmt.Fprintf(w, "acked %d pending %d\n", acked.Load(), st.Pending)
		w.Flush()
	}
}

func envInt(t *testing.T, name string, def int) int {
	t.Helper()
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return n
}

// roundURL puts sub at the end of the path of an s3:// URL, before its query.
func roundURL(t *testing.T, base, sub string) string {
	t.Helper()
	u, q, _ := strings.Cut(base, "?")
	u = strings.TrimSuffix(u, "/") + "/" + sub
	if q != "" {
		u += "?" + q
	}
	return u
}
