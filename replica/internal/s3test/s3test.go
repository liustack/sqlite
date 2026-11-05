// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package s3test gives tests an S3 store:
//
//   - MINIO set to a minio binary: a store started on a free port, with a
//     bucket. The version CI uses: go install github.com/minio/minio@RELEASE.2025-09-07T16-13-09Z
//   - or S3_TEST_ENDPOINT, S3_TEST_REGION, S3_TEST_BUCKET, S3_TEST_ACCESS_KEY
//     and S3_TEST_SECRET_KEY for a store that runs already (the bucket must
//     exist).
//
// Without either the tests are skipped, or fail when S3_TEST_REQUIRED is set.
// Each test gets a prefix of its own, emptied when it ends.
package s3test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/liustack/sqlite/replica/internal/s3"
)

var store struct {
	once sync.Once
	cfg  s3.Config
	cmd  *exec.Cmd
	err  error
}

// Store is the test store and a prefix of the test's own, ending in "/".
func Store(t testing.TB) (s3.Config, string) {
	t.Helper()
	store.once.Do(start)
	if store.err != nil {
		t.Fatal(store.err)
	}
	if store.cfg.Endpoint == "" {
		if os.Getenv("S3_TEST_REQUIRED") != "" {
			t.Fatal("no S3 store, and S3_TEST_REQUIRED is set")
		}
		t.Skip("no S3 store: set MINIO to a minio binary, or S3_TEST_ENDPOINT")
	}
	c, err := s3.New(store.cfg)
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("test/%s-%08x/", t.Name(), rand.Uint32())
	t.Cleanup(func() {
		ctx := context.Background()
		for o, err := range c.List(ctx, prefix, "") {
			if err != nil {
				t.Error(err)
				return
			}
			if err := c.Delete(ctx, o.Key); err != nil {
				t.Error(err)
			}
		}
	})
	return store.cfg, prefix
}

// Stop stops the store started, if any. TestMain calls it.
func Stop() {
	if store.cmd != nil {
		store.cmd.Process.Kill()
		store.cmd.Wait()
	}
}

func start() {
	if ep := os.Getenv("S3_TEST_ENDPOINT"); ep != "" {
		store.cfg = s3.Config{
			Endpoint:    ep,
			Region:      os.Getenv("S3_TEST_REGION"),
			Bucket:      os.Getenv("S3_TEST_BUCKET"),
			Credentials: s3.Static(s3.Credentials{AccessKeyID: os.Getenv("S3_TEST_ACCESS_KEY"), SecretAccessKey: os.Getenv("S3_TEST_SECRET_KEY")}),
			AllowHTTP:   true,
		}
		return
	}
	bin := os.Getenv("MINIO")
	if bin == "" {
		return
	}
	dir, err := os.MkdirTemp("", "minio")
	if err != nil {
		store.err = err
		return
	}
	addr, console := freeAddr(), freeAddr()
	cmd := exec.Command(bin, "server", "--quiet", "--address", addr, "--console-address", console, dir)
	cmd.Env = append(os.Environ(), "MINIO_ROOT_USER=testaccess", "MINIO_ROOT_PASSWORD=testsecret-0123456789")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		store.err = err
		return
	}
	store.cmd = cmd
	ep := "http://" + addr
	for start := time.Now(); ; time.Sleep(100 * time.Millisecond) {
		resp, err := http.Get(ep + "/minio/health/ready")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Since(start) > 30*time.Second {
			store.err = fmt.Errorf("minio did not start: %s", out.String())
			return
		}
	}
	store.cfg = s3.Config{Endpoint: ep, Region: "us-east-1", Bucket: "test", AllowHTTP: true,
		Credentials: s3.Static(s3.Credentials{AccessKeyID: "testaccess", SecretAccessKey: "testsecret-0123456789"})}
	c, err := s3.New(store.cfg)
	if err != nil {
		store.err = err
		return
	}
	// The S3 API can answer that the server is not ready yet after the
	// health check does.
	for start := time.Now(); ; time.Sleep(100 * time.Millisecond) {
		store.err = c.CreateBucket(context.Background())
		var e *s3.Error
		if !errors.As(store.err, &e) || e.Code != "XMinioServerNotInitialized" || time.Since(start) > 30*time.Second {
			return
		}
	}
}

func freeAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().String()
}
