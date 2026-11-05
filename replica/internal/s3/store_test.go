// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests in this file run against a real store:
//
//   - MINIO set to a minio binary: the tests start it on a free port. The
//     version CI uses: go install github.com/minio/minio@RELEASE.2025-09-07T16-13-09Z
//   - or S3_TEST_ENDPOINT, S3_TEST_REGION, S3_TEST_BUCKET, S3_TEST_ACCESS_KEY
//     and S3_TEST_SECRET_KEY for a store that runs already (the bucket must
//     exist). Each test writes under a prefix of its own and deletes it.
//
// Without either they are skipped, or fail when S3_TEST_REQUIRED is set.

var store struct {
	once sync.Once
	cfg  Config
	cmd  *exec.Cmd
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if store.cmd != nil {
		store.cmd.Process.Kill()
		store.cmd.Wait()
	}
	os.Exit(code)
}

// testStore gives a client of the test store and the prefix of the test.
func testStore(t *testing.T) (*Client, string) {
	t.Helper()
	store.once.Do(startStore)
	if store.err != nil {
		t.Fatal(store.err)
	}
	if store.cfg.Endpoint == "" {
		if os.Getenv("S3_TEST_REQUIRED") != "" {
			t.Fatal("no S3 store, and S3_TEST_REQUIRED is set")
		}
		t.Skip("no S3 store: set MINIO to a minio binary, or S3_TEST_ENDPOINT")
	}
	c, err := New(store.cfg)
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
	return c, prefix
}

func startStore() {
	if ep := os.Getenv("S3_TEST_ENDPOINT"); ep != "" {
		store.cfg = Config{
			Endpoint:    ep,
			Region:      os.Getenv("S3_TEST_REGION"),
			Bucket:      os.Getenv("S3_TEST_BUCKET"),
			Credentials: Static(Credentials{AccessKeyID: os.Getenv("S3_TEST_ACCESS_KEY"), SecretAccessKey: os.Getenv("S3_TEST_SECRET_KEY")}),
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
	store.cfg = Config{Endpoint: ep, Region: "us-east-1", Bucket: "test", AllowHTTP: true,
		Credentials: Static(Credentials{AccessKeyID: "testaccess", SecretAccessKey: "testsecret-0123456789"})}
	c, err := New(store.cfg)
	if err != nil {
		store.err = err
		return
	}
	// The S3 API can answer that the server is not ready yet after the
	// health check does.
	for start := time.Now(); ; time.Sleep(100 * time.Millisecond) {
		store.err = c.CreateBucket(context.Background())
		var e *Error
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

func get(t *testing.T, c *Client, key string, off, n int64) ([]byte, error) {
	t.Helper()
	r, err := c.Get(context.Background(), key, off, n)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(uint64(n), 1))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func TestPutGetDelete(t *testing.T) {
	c, prefix := testStore(t)
	ctx := context.Background()
	key := prefix + "a b+c$d%25~é.ltx" // bytes the path encodes
	data := randomBytes(1000)
	if err := c.Put(ctx, key, bytes.NewReader(data), int64(len(data)), PutOptions{Metadata: map[string]string{"litestream-timestamp": "2026-11-05T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ off, n int64 }{{0, -1}, {0, 1000}, {10, 20}, {990, -1}, {990, 100}} {
		got, err := get(t, c, key, r.off, r.n)
		if err != nil {
			t.Fatalf("Get(%d, %d): %v", r.off, r.n, err)
		}
		end := int64(len(data))
		if r.n > 0 {
			end = min(end, r.off+r.n)
		}
		if !bytes.Equal(got, data[r.off:end]) {
			t.Errorf("Get(%d, %d): %d bytes, not the ones written", r.off, r.n, len(got))
		}
	}
	info, err := c.Head(ctx, key)
	if err != nil || info.Size != 1000 || info.Metadata["litestream-timestamp"] != "2026-11-05T00:00:00Z" || info.LastModified.IsZero() {
		t.Errorf("Head: %+v, %v", info, err)
	}
	if _, err := get(t, c, key, 1000, 10); err == nil {
		t.Error("Get past the end: no error")
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, c, key, 0, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: %v, want ErrNotFound", err)
	}
	if _, err := c.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Head after Delete: %v, want ErrNotFound", err)
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Errorf("Delete of a deleted key: %v", err)
	}
	// An empty object.
	if err := c.Put(ctx, key, bytes.NewReader(nil), 0, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, err := get(t, c, key, 0, -1); err != nil || len(got) != 0 {
		t.Errorf("Get of an empty object: %d bytes, %v", len(got), err)
	}
}

func TestConditionalPut(t *testing.T) {
	c, prefix := testStore(t)
	ctx := context.Background()
	key := prefix + "0000000000000001-0000000000000001.ltx"
	first, second := []byte("first"), []byte("second")
	if err := c.Put(ctx, key, bytes.NewReader(first), 5, PutOptions{IfNoneMatch: true}); err != nil {
		t.Fatal(err)
	}
	err := c.Put(ctx, key, bytes.NewReader(second), 6, PutOptions{IfNoneMatch: true})
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("second conditional Put: %v, want ErrPreconditionFailed", err)
	}
	if got, err := get(t, c, key, 0, -1); err != nil || !bytes.Equal(got, first) {
		t.Errorf("after the refused Put: %q, %v", got, err)
	}
}

func TestMultipart(t *testing.T) {
	c, prefix := testStore(t)
	c.partSize = 5 << 20 // the smallest part S3 takes
	ctx := context.Background()
	key := prefix + "snapshot.ltx"
	data := randomBytes(11<<20 + 3) // three parts
	if err := c.Put(ctx, key, bytes.NewReader(data), int64(len(data)), PutOptions{IfNoneMatch: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := get(t, c, key, 0, -1); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get: %d bytes, %v", len(got), err)
	}
	err := c.Put(ctx, key, bytes.NewReader(data), int64(len(data)), PutOptions{IfNoneMatch: true})
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("second conditional multipart Put: %v, want ErrPreconditionFailed", err)
	}
	if n := uploads(t, c, key); n != 0 {
		t.Errorf("%d multipart uploads left after the refused one", n)
	}
	// A body shorter than its size fails, and its upload is aborted.
	if err := c.Put(ctx, prefix+"short.ltx", bytes.NewReader(data[:6<<20]), int64(len(data)), PutOptions{}); err == nil {
		t.Error("Put of a short body: no error")
	}
	if n := uploads(t, c, prefix+"short.ltx"); n != 0 {
		t.Errorf("%d multipart uploads left after a failed one", n)
	}
}

// uploads counts the multipart uploads in progress of key. MinIO lists
// those of one key only.
func uploads(t *testing.T, c *Client, key string) int {
	t.Helper()
	resp, err := c.do(context.Background(), request{op: "ListMultipartUploads", method: http.MethodGet, query: query("prefix", key, "uploads", "")})
	if err != nil {
		t.Fatal(err)
	}
	var page struct{ Upload []struct{ UploadId string } }
	err = readXML(resp, 1<<20, &page)
	done(resp)
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Upload)
}

func TestList(t *testing.T) {
	c, prefix := testStore(t)
	c.listPage = 7
	ctx := context.Background()
	var keys []string
	for i := range 20 {
		k := fmt.Sprintf("%sltx/0/%016x.ltx", prefix, i)
		keys = append(keys, k)
		if err := c.Put(ctx, k, bytes.NewReader([]byte{byte(i)}), 1, PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Put(ctx, prefix+"ltx/1/x.ltx", bytes.NewReader(nil), 0, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	list := func(p, after string) []string {
		var got []string
		for o, err := range c.List(ctx, p, after) {
			if err != nil {
				t.Fatal(err)
			}
			if o.Size != 1 || o.LastModified.IsZero() || o.ETag == "" {
				t.Errorf("%+v", o)
			}
			got = append(got, o.Key)
		}
		return got
	}
	if got := list(prefix+"ltx/0/", ""); strings.Join(got, ",") != strings.Join(keys, ",") {
		t.Errorf("List: %v", got)
	}
	if got := list(prefix+"ltx/0/", keys[12]); strings.Join(got, ",") != strings.Join(keys[13:], ",") {
		t.Errorf("List after %s: %v", keys[12], got)
	}
	// Stopping early stops the pages.
	n := 0
	for range c.List(ctx, prefix+"ltx/0/", "") {
		if n++; n == 3 {
			break
		}
	}
}

// A wrong secret fails, and no error or print shows a secret.
func TestNoSecretInErrors(t *testing.T) {
	c, prefix := testStore(t)
	const secret = "wrong-secret-0123456789"
	// The store's own key ID, so that it gets as far as the store allows:
	// MinIO refuses the signature with a 403, R2 the session token it does
	// not know with a 400.
	good, err := c.creds.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bad := *c
	bad.creds = Static(Credentials{AccessKeyID: good.AccessKeyID, SecretAccessKey: secret, SessionToken: "token-0123456789"})
	err = bad.Put(context.Background(), prefix+"x", bytes.NewReader([]byte("x")), 1, PutOptions{})
	if err == nil {
		t.Fatal("Put with a wrong secret: no error")
	}
	var e *Error
	if !errors.As(err, &e) || e.StatusCode/100 != 4 {
		t.Errorf("Put with a wrong secret: %v, want the store to refuse it", err)
	}
	for _, s := range []string{
		err.Error(), fmt.Sprintf("%+v %#v", err, err),
		fmt.Sprint(bad.creds), fmt.Sprintf("%+v %#v %s %q %x %d", bad.creds, bad.creds, bad.creds, bad.creds, bad.creds, bad.creds),
	} {
		if strings.Contains(s, secret) || strings.Contains(s, "token-0123456789") || strings.Contains(s, hexOf(secret)) {
			t.Errorf("a secret in %q", s)
		}
	}
}

func hexOf(s string) string { return fmt.Sprintf("%x", s) }

// A missing bucket is not a missing key.
func TestMissingBucket(t *testing.T) {
	c, _ := testStore(t)
	other := *c
	other.bucket = fmt.Sprintf("missing-%08x", rand.Uint32())
	_, err := get(t, &other, "x", 0, -1)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Get from a missing bucket: %v, want an error that is not ErrNotFound", err)
	}
}

// Multipart sends a small object as a multipart upload of one part, whose
// completion is conditional too.
func TestMultipartSmall(t *testing.T) {
	c, prefix := testStore(t)
	ctx := context.Background()
	key := prefix + "probe"
	opt := PutOptions{IfNoneMatch: true, Multipart: true}
	if err := c.Put(ctx, key, bytes.NewReader([]byte("first")), 5, opt); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, key, bytes.NewReader([]byte("second")), 6, opt); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("second conditional Put: %v, want ErrPreconditionFailed", err)
	}
	if got, err := get(t, c, key, 0, -1); err != nil || string(got) != "first" {
		t.Errorf("Get: %q, %v", got, err)
	}
	if n := uploads(t, c, key); n != 0 {
		t.Errorf("%d multipart uploads left", n)
	}
}

// CreateBucket makes a bucket, and says when it exists already.
func TestCreateBucket(t *testing.T) {
	c, _ := testStore(t)
	if store.cmd == nil {
		t.Skip("makes buckets: only on a MinIO of the test")
	}
	other := *c
	other.bucket = fmt.Sprintf("made-%08x", rand.Uint32())
	ctx := context.Background()
	if err := other.CreateBucket(ctx); err != nil {
		t.Fatal(err)
	}
	if err := other.Put(ctx, "x", bytes.NewReader([]byte("x")), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := other.CreateBucket(ctx); err == nil {
		t.Error("CreateBucket of a bucket that exists: no error")
	}
	if err := other.Delete(ctx, "x"); err != nil {
		t.Fatal(err)
	}
}
