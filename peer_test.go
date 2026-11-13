// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

package sqlite

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The peer helper runs the statements it reads from its standard input, one
// per line, on one connection to the database named in its environment. For
// each it prints the rows, the columns of a row joined by "|", or the
// error, and then peerDone.
const (
	peerEnv  = "SQLITE_TEST_PEER_DB"
	peerDone = "--done--"
)

// PeerEnv and PeerHelper are for TestMain, which is in the other test package.
const PeerEnv = peerEnv

var PeerHelper = peerHelper

func peerHelper(dsn string) int {
	ctx := context.Background()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	var conn *sql.Conn
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		// A connection that failed is dropped by database/sql: the next
		// statement takes a new one.
		if conn == nil {
			if conn, err = db.Conn(ctx); err != nil {
				fmt.Printf("error: %v\n%s\n", err, peerDone)
				conn = nil
				continue
			}
		}
		if err := peerRun(ctx, conn, in.Text()); err != nil {
			fmt.Printf("error: %v\n", err)
			conn.Close()
			conn = nil
		}
		fmt.Println(peerDone)
	}
	return 0
}

func peerRun(ctx context.Context, conn *sql.Conn, q string) error {
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	for i := range vals {
		vals[i] = new(sql.NullString)
	}
	for rows.Next() {
		if err := rows.Scan(vals...); err != nil {
			return err
		}
		out := make([]string, len(vals))
		for i, v := range vals {
			out[i] = v.(*sql.NullString).String
		}
		fmt.Println(strings.Join(out, "|"))
	}
	return rows.Err()
}

// A peer is another process with the database open: this driver in the
// test binary run again, or the sqlite3 shell, which is SQLite's own VFS.
type peer struct {
	t   *testing.T
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// peerKinds are the peers the machine has.
func peerKinds() []string {
	kinds := []string{"driver"}
	if _, err := exec.LookPath("sqlite3"); err == nil {
		kinds = append(kinds, "sqlite3")
	}
	return kinds
}

func startPeer(t *testing.T, kind, path string) *peer {
	t.Helper()
	var cmd *exec.Cmd
	if kind == "sqlite3" {
		cmd = exec.Command("sqlite3", "-batch", path)
	} else {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(exe)
		cmd.Env = append(os.Environ(), peerEnv+"=file:"+path)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &peer{t: t, cmd: cmd, in: in, out: bufio.NewReader(out)}
	t.Cleanup(p.stop)
	return p
}

// do runs one statement in the peer and returns what it printed.
func (p *peer) do(q string) string {
	p.t.Helper()
	end := ""
	if p.cmd.Args[0] == "sqlite3" {
		end = ";\n.print " + peerDone
	}
	if _, err := io.WriteString(p.in, q+end+"\n"); err != nil {
		p.t.Fatalf("peer: %s: %v", q, err)
	}
	var b strings.Builder
	for {
		line, err := p.out.ReadString('\n')
		if err != nil {
			p.t.Fatalf("peer: %s: %v after %q", q, err, b.String())
		}
		if line == peerDone+"\n" {
			break
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String())
}

// want runs one statement in the peer, which must print want.
func (p *peer) want(q, want string) {
	p.t.Helper()
	if got := p.do(q); got != want {
		p.t.Fatalf("peer: %s: %q, want %q", q, got, want)
	}
}

// stop ends the peer, which closes its connection.
func (p *peer) stop() {
	p.in.Close()
	p.cmd.Wait()
}
