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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

var testCreds = Static(Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET"})

func TestNewChecksEndpoint(t *testing.T) {
	for _, tc := range []struct {
		endpoint  string
		allowHTTP bool
		bucket    string
		ok        bool
		host      string // with the bucket when it goes in the host
	}{
		{"https://s3.us-east-1.amazonaws.com", false, "bkt", true, "bkt.s3.us-east-1.amazonaws.com"},
		{"https://s3.us-east-1.amazonaws.com/", false, "with.dot", true, "s3.us-east-1.amazonaws.com"},
		{"https://acct.r2.cloudflarestorage.com", false, "bkt", true, "acct.r2.cloudflarestorage.com"},
		{"http://127.0.0.1:9000", true, "bkt", true, "127.0.0.1:9000"},
		{"http://127.0.0.1:9000", false, "bkt", false, ""},
		{"https://key:hunter2@example.com", false, "bkt", false, ""},
		{"https://example.com/path", false, "bkt", false, ""},
		{"https://example.com?x=1", false, "bkt", false, ""},
		{"ftp://example.com", false, "bkt", false, ""},
		{"example.com", false, "bkt", false, ""},
		{"https://example.com", false, "", false, ""},
		{"https://example.com", false, "a/b", false, ""},
		{"https://example.com", false, "Bkt", false, ""},
		{"https://example.com", false, "-bkt", false, ""},
		{"https://example.com", false, "bk", false, ""},
		{"https://example.com", false, "x@y.z", false, ""},
	} {
		c, err := New(Config{Endpoint: tc.endpoint, Region: "auto", Bucket: tc.bucket, Credentials: testCreds, AllowHTTP: tc.allowHTTP})
		if (err == nil) != tc.ok {
			t.Errorf("%s %q: %v", tc.endpoint, tc.bucket, err)
			continue
		}
		if err != nil {
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("%s: the password in %q", tc.endpoint, err)
			}
			continue
		}
		if c.host != tc.host {
			t.Errorf("%s %q: host %s, want %s", tc.endpoint, tc.bucket, c.host, tc.host)
		}
	}
}

func fakeStore(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", Credentials: testCreds, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A redirect is not followed: the signed request never goes elsewhere.
func TestNoRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	_, err := c.Get(context.Background(), "k", 0, -1)
	var e *Error
	if !errors.As(err, &e) || e.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("Get: %v, want a 307 error", err)
	}
	if elsewhere.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

// A store that answers a ranged GET with the whole object fails the call.
func TestRangeIgnored(t *testing.T) {
	c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=2-4" {
			t.Errorf("Range %q", r.Header.Get("Range"))
		}
		io.WriteString(w, "0123456789")
	})
	if _, err := c.Get(context.Background(), "k", 2, 3); err == nil {
		t.Error("Get: no error")
	}
}

// What the store sends with an error beyond its code, message and request
// ID, such as the string it signed, is left out.
func TestErrorFields(t *testing.T) {
	c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>SignatureDoesNotMatch</Code>`+
			`<Message>The signature does not match.</Message><AWSAccessKeyId>AKID</AWSAccessKeyId>`+
			`<StringToSign>AWS4-HMAC-SHA256 secret-ish</StringToSign><RequestId>R1</RequestId></Error>`)
	})
	err := c.Delete(context.Background(), "k")
	want := "s3: DELETE k: 403 SignatureDoesNotMatch: The signature does not match. (request R1)"
	if err == nil || err.Error() != want {
		t.Errorf("Delete: %v\nwant %s", err, want)
	}
}

// CompleteMultipartUpload can answer 200 with an error in its body.
func TestCompleteErrorIn200(t *testing.T) {
	var aborted atomic.Int32
	c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
		switch q := r.URL.Query(); {
		case q.Has("uploads"):
			io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>U1</UploadId></InitiateMultipartUploadResult>`)
		case q.Has("partNumber"):
			io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", `"e"`)
		case r.Method == http.MethodPost:
			io.WriteString(w, `<Error><Code>InternalError</Code><Message>try again</Message></Error>`)
		case r.Method == http.MethodDelete && q.Get("uploadId") == "U1":
			aborted.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("%s %s", r.Method, r.URL)
		}
	})
	c.partSize = 4
	err := c.Put(context.Background(), "k", strings.NewReader("0123456789"), 10, PutOptions{})
	var e *Error
	if !errors.As(err, &e) || e.Code != "InternalError" {
		t.Errorf("Put: %v, want InternalError", err)
	}
	if aborted.Load() != 1 {
		t.Errorf("aborted %d times", aborted.Load())
	}
}

// A list that goes back or never ends is an error.
func TestListChecksPages(t *testing.T) {
	for name, pages := range map[string][]string{
		"out of order": {`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>t1</NextContinuationToken><Contents><Key>p/b</Key></Contents></ListBucketResult>`,
			`<ListBucketResult><Contents><Key>p/a</Key></Contents></ListBucketResult>`},
		"same token": {`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>t1</NextContinuationToken><Contents><Key>p/a</Key></Contents></ListBucketResult>`,
			`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>t1</NextContinuationToken><Contents><Key>p/b</Key></Contents></ListBucketResult>`},
		"no token":     {`<ListBucketResult><IsTruncated>true</IsTruncated><Contents><Key>p/a</Key></Contents></ListBucketResult>`},
		"other prefix": {`<ListBucketResult><Contents><Key>q/a</Key></Contents></ListBucketResult>`},
	} {
		t.Run(name, func(t *testing.T) {
			n := 0
			c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
				if n == len(pages) {
					t.Fatal("asked for a page past the last")
				}
				io.WriteString(w, pages[n])
				n++
			})
			var err error
			for _, err = range c.List(context.Background(), "p/", "") {
				if err != nil {
					break
				}
			}
			if err == nil {
				t.Error("no error")
			}
		})
	}
}

// The body is signed by its hash: one that changes between the hash and the
// send is refused by the store.
func TestChangedBodyRefused(t *testing.T) {
	c, prefix := testStore(t)
	b := &flipper{data: []byte("0123456789")}
	err := c.Put(context.Background(), prefix+"k", b, 10, PutOptions{})
	if err == nil {
		t.Fatal("Put of a body that changed: no error")
	}
	if _, err := get(t, c, prefix+"k", 0, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("the object is there: %v", err)
	}
}

// flipper gives other bytes from its second read on.
type flipper struct {
	data  []byte
	reads atomic.Int32
}

func (f *flipper) ReadAt(p []byte, off int64) (int, error) {
	n, err := bytes.NewReader(f.data).ReadAt(p, off)
	if f.reads.Add(1) > 1 && n > 0 {
		p[0] ^= 1
	}
	return n, err
}

// No print of a client, or of a struct that holds one, shows a secret.
func TestClientPrintsNoSecret(t *testing.T) {
	c, err := New(Config{Endpoint: "https://example.com", Region: "auto", Bucket: "bkt",
		Credentials: Static(Credentials{AccessKeyID: "AKID", SecretAccessKey: "hunter2", SessionToken: "tok-42"})})
	if err != nil {
		t.Fatal(err)
	}
	type holder struct {
		c *Client
		v Client
		P *Client
	}
	h := holder{c, *c, c}
	for _, s := range []string{
		fmt.Sprintf("%v %+v %#v %s", c, c, c, c),
		fmt.Sprintf("%v %+v %#v", *c, *c, *c),
		fmt.Sprintf("%v %+v %#v", h, h, h),
		fmt.Sprintf("%v %+v %#v %d %x", c.creds, c.creds, c.creds, c.creds, c.creds),
	} {
		if strings.Contains(s, "hunter2") || strings.Contains(s, "tok-42") || strings.Contains(s, hexOf("hunter2")) {
			t.Errorf("a secret in %s", s)
		}
	}
}

func TestCheckKey(t *testing.T) {
	for key, ok := range map[string]bool{
		"ltx/0/0000000000000001-0000000000000001.ltx": true,
		"a b+c%d~é": true,
		"":          false,
		"/a":        false,
		"a/":        false,
		"a//b":      false,
		"./a":       false,
		"a/../b":    false,
		"\xff":      false,
	} {
		if err := checkKey(key); (err == nil) != ok {
			t.Errorf("checkKey(%q): %v", key, err)
		}
	}
}

// A range answer must hold all the bytes asked for, unless the object ends
// first.
func TestShortRange(t *testing.T) {
	for _, tc := range []struct {
		contentRange string
		body         string
		ok           bool
	}{
		{"bytes 0-9/100", "0123456789", true},
		{"bytes 0-4/5", "01234", true},    // the object ends
		{"bytes 0-4/100", "01234", false}, // cut short
		{"bytes 1-10/100", "0123456789", false},
		{"bytes 0-9/*", "0123456789", false},
	} {
		c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", tc.contentRange)
			w.WriteHeader(http.StatusPartialContent)
			io.WriteString(w, tc.body)
		})
		r, err := c.Get(context.Background(), "k", 0, 10)
		if err == nil {
			r.Close()
		}
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.contentRange, err)
		}
	}
}

func TestRetryable(t *testing.T) {
	for status, want := range map[int]bool{500: true, 503: true, 429: true, 409: true, 412: false, 404: false, 403: false, 400: false} {
		if got := Retryable(&Error{StatusCode: status}); got != want {
			t.Errorf("Retryable(%d) = %v", status, got)
		}
	}
	// A connection closed with no answer.
	c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	err := c.Put(context.Background(), "k", strings.NewReader("x"), 1, PutOptions{IfNoneMatch: true})
	if !errors.Is(err, ErrNoAnswer) || !Retryable(err) || errors.Is(err, ErrPreconditionFailed) {
		t.Errorf("Put to a closed connection: %v", err)
	}
}

// A multipart upload whose context ends is aborted all the same.
func TestAbortOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var aborted atomic.Int32
	c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
		switch q := r.URL.Query(); {
		case q.Has("uploads"):
			io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>U1</UploadId></InitiateMultipartUploadResult>`)
		case q.Has("partNumber"):
			io.Copy(io.Discard, r.Body)
			cancel()
			<-r.Context().Done()
		case r.Method == http.MethodDelete && q.Get("uploadId") == "U1":
			aborted.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("%s %s", r.Method, r.URL)
		}
	})
	c.partSize = 4
	err := c.Put(ctx, "k", strings.NewReader("0123456789"), 10, PutOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Put: %v, want context.Canceled", err)
	}
	if aborted.Load() != 1 {
		t.Errorf("aborted %d times", aborted.Load())
	}
}

// Leaving a list early asks for no more pages.
func TestListStops(t *testing.T) {
	var pages atomic.Int32
	c := fakeStore(t, func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>t`+
			r.URL.Query().Get("continuation-token")+`x</NextContinuationToken><Contents><Key>p/`+
			r.URL.Query().Get("continuation-token")+`a</Key></Contents><Contents><Key>p/`+
			r.URL.Query().Get("continuation-token")+`b</Key></Contents></ListBucketResult>`)
	})
	n := 0
	for _, err := range c.List(context.Background(), "p/", "") {
		if err != nil {
			t.Fatal(err)
		}
		if n++; n == 3 {
			break
		}
	}
	if pages.Load() != 2 {
		t.Errorf("%d pages asked for, want 2", pages.Load())
	}
}

// A store that puts the session token of the request in its error message:
// the error the client gives does not have it.
func TestErrorScrubsToken(t *testing.T) {
	const token = "session-token-0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.Copy(io.Discard, req.Body)
		echo := `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidToken</Code><Message>bad token ` +
			req.Header.Get("X-Amz-Security-Token") + `</Message></Error>`
		switch {
		case req.Method == http.MethodPost && req.URL.Query().Has("uploads"):
			fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>u1</UploadId></InitiateMultipartUploadResult>`)
		case req.Method == http.MethodPut && req.URL.Query().Has("partNumber"):
			w.Header().Set("ETag", `"e"`)
		case req.Method == http.MethodPost:
			fmt.Fprint(w, echo) // 200 with an error
		case req.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, echo)
		}
	}))
	defer srv.Close()
	creds := Static(Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET", SessionToken: token})
	c, err := New(Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "bkt", Credentials: creds, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("data")
	for _, multipart := range []bool{false, true} {
		err := c.Put(context.Background(), "k", bytes.NewReader(body), int64(len(body)), PutOptions{Multipart: multipart})
		if err == nil || !strings.Contains(err.Error(), "bad token") {
			t.Fatalf("multipart %t: %v", multipart, err)
		}
		if strings.Contains(err.Error(), token) {
			t.Fatalf("multipart %t: the error has the token: %v", multipart, err)
		}
	}
}
