// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Command latency is the server of the latency test (benchrun latency),
// built with the tag of a driver of bench. It answers HTTP on a port it
// prints, from one database behind a pool of connections:
//
//	/r   N point reads by primary key from 100k rows
//	/w   one autocommit INSERT
//
// It serves until its standard input closes.
//
//	latency [-conns 32] [-n 32]
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/liustack/sqlite/bench"
)

const (
	rows = 100000
	text = "some text of a row, about fifty bytes long, padding"
)

func main() {
	conns := flag.Int("conns", 32, "connections of the pool")
	perReq := flag.Int("n", 32, "point reads of one /r request")
	flag.Parse()
	if err := serve(*conns, *perReq); err != nil {
		fmt.Fprintln(os.Stderr, "latency:", err)
		os.Exit(1)
	}
}

func serve(conns, perReq int) error {
	dir, err := os.MkdirTemp("", "bench-latency-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := sql.Open(bench.DriverName, bench.DSN(filepath.Join(dir, "a.db"), "NORMAL"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	if err := fill(db); err != nil {
		return err
	}

	var seed atomic.Uint64
	rngs := sync.Pool{New: func() any { return rand.New(rand.NewPCG(seed.Add(1), 1)) }}
	mux := http.NewServeMux()
	mux.HandleFunc("/r", func(w http.ResponseWriter, r *http.Request) {
		rng := rngs.Get().(*rand.Rand)
		defer rngs.Put(rng)
		var a string
		for range perReq {
			if err := db.QueryRow("SELECT a FROM t WHERE id = ?", 1+rng.IntN(rows)).Scan(&a); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		io.WriteString(w, a)
	})
	mux.HandleFunc("/w", func(w http.ResponseWriter, r *http.Request) {
		if _, err := db.Exec("INSERT INTO w(a) VALUES(?)", text); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		io.WriteString(w, "ok")
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	fmt.Println("LISTEN", ln.Addr())
	go http.Serve(ln, mux)
	io.Copy(io.Discard, os.Stdin)
	return ln.Close()
}

// fill makes the table the reads read and the table the writes fill.
func fill(db *sql.DB) error {
	for _, q := range []string{
		"CREATE TABLE t(id INTEGER PRIMARY KEY, a TEXT)",
		"CREATE TABLE w(id INTEGER PRIMARY KEY, a TEXT)",
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare("INSERT INTO t(a) VALUES(?)")
	if err != nil {
		return err
	}
	for range rows {
		if _, err := st.Exec(text); err != nil {
			return err
		}
	}
	return tx.Commit()
}
