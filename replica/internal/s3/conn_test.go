// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package s3

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A store that closes a connection it kept open, as stores do with idle
// ones, fails the request sent on it without an answer. It is sent again
// on a new connection, whatever its method.
func TestClosedIdleConn(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var answered atomic.Int32
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				// The first request of a connection is answered, the
				// second finds it closed.
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				io.Copy(io.Discard, req.Body)
				answered.Add(1)
				io.WriteString(conn, "HTTP/1.1 204 No Content\r\n\r\n")
				http.ReadRequest(br)
			}()
		}
	}()
	c, err := New(Config{Endpoint: "http://" + l.Addr().String(), Region: "us-east-1", Bucket: "bucket",
		Credentials: Static(Credentials{AccessKeyID: "key", SecretAccessKey: "secret"}), AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 3 {
		if err := c.Delete(ctx, "x"); err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}
	if n := answered.Load(); n != 3 {
		t.Fatalf("%d requests answered, want 3", n)
	}
}

// An answer that is an error, on a connection used before, is not sent
// again.
func TestErrorNotResent(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>no</Message></Error>`)
	}))
	defer srv.Close()
	c, err := New(Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bucket",
		Credentials: Static(Credentials{AccessKeyID: "key", SecretAccessKey: "secret"}), AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := c.Delete(context.Background(), "x"); err == nil {
			t.Fatal("no error")
		}
	}
	if got := n.Load(); got != 3 {
		t.Fatalf("%d requests for 3 calls", got)
	}
}
