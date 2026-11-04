// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package s3

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The SigV4 test suite of AWS (testdata/sigv4, from aws-c-auth), less the
// cases that normalize the path, which S3 does not, and the one that leaves
// the session token out of the signature, which S3 signs.
func TestSigV4Suite(t *testing.T) {
	dirs, err := filepath.Glob("testdata/sigv4/*")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		n++
		t.Run(filepath.Base(dir), func(t *testing.T) {
			read := func(name string) string {
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			var ctx struct {
				Credentials struct {
					AccessKeyID     string `json:"access_key_id"`
					SecretAccessKey string `json:"secret_access_key"`
					Token           string `json:"token"`
				}
				Region    string
				Service   string
				SignBody  bool `json:"sign_body"`
				Timestamp time.Time
			}
			if err := json.Unmarshal([]byte(read("context.json")), &ctx); err != nil {
				t.Fatal(err)
			}
			req, body := parseRequest(t, read("request.txt"))
			sum := sha256.Sum256([]byte(body))
			hash := hex.EncodeToString(sum[:])
			if ctx.SignBody {
				req.Header.Set("X-Amz-Content-Sha256", hash)
			}
			cr := Credentials{ctx.Credentials.AccessKeyID, ctx.Credentials.SecretAccessKey, ctx.Credentials.Token}
			sign(req, cr, ctx.Region, ctx.Service, ctx.Timestamp, hash)

			req.Header.Del("Authorization")
			canon, _ := canonicalRequest(req, hash)
			if want := read("header-canonical-request.txt"); canon != want {
				t.Errorf("canonical request:\n%s\nwant:\n%s", canon, want)
			}
			date := ctx.Timestamp.UTC().Format(amzDate)
			toSign := stringToSign(date, date[:8]+"/"+ctx.Region+"/"+ctx.Service+"/aws4_request", canon)
			if want := read("header-string-to-sign.txt"); toSign != want {
				t.Errorf("string to sign:\n%s\nwant:\n%s", toSign, want)
			}
			sign(req, cr, ctx.Region, ctx.Service, ctx.Timestamp, hash)
			want := strings.TrimSpace(read("header-signature.txt"))
			if got := req.Header.Get("Authorization"); !strings.HasSuffix(got, "Signature="+want) {
				t.Errorf("Authorization: %s\nwant signature %s", got, want)
			}
		})
	}
	if n < 30 {
		t.Fatalf("%d cases in testdata/sigv4", n)
	}
}

// parseRequest reads a request of the suite: a request line, headers (a
// line that starts with a space goes on the one before), a blank line, the
// body.
func parseRequest(t *testing.T, s string) (*http.Request, string) {
	head, body, _ := strings.Cut(s, "\n\n")
	lines := strings.Split(head, "\n")
	method, rest, _ := strings.Cut(lines[0], " ")
	target := rest[:strings.LastIndex(rest, " HTTP/")]
	path, rawQuery, _ := strings.Cut(target, "?")
	req := &http.Request{Method: method, URL: &url.URL{Path: path, RawQuery: rawQuery}, Header: http.Header{}}
	var name string
	var values []string
	flush := func() {
		if name == "" {
			return
		}
		if strings.EqualFold(name, "Host") {
			req.Host = values[0]
		} else {
			req.Header[http.CanonicalHeaderKey(name)] = append(req.Header[http.CanonicalHeaderKey(name)], strings.Join(values, " "))
		}
		name, values = "", nil
	}
	for _, l := range lines[1:] {
		if l == "" {
			continue // the file's last newline, when there is no body
		}
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			values = append(values, l)
			continue
		}
		flush()
		n, v, ok := strings.Cut(l, ":")
		if !ok {
			t.Fatalf("header line %q", l)
		}
		name, values = n, []string{v}
	}
	flush()
	return req, body
}

// The four examples of S3's documentation of SigV4 with an Authorization
// header (AmazonS3/latest/API/sig-v4-header-based-auth.html).
func TestSigV4S3Examples(t *testing.T) {
	cr := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, method, path, query string
		header                    map[string]string
		body                      string
		signed, signature         string
	}{
		{"GET with a range", "GET", "/test.txt", "", map[string]string{"Range": "bytes=0-9"}, "",
			"host;range;x-amz-content-sha256;x-amz-date", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"PUT", "PUT", "/test$file.text", "", map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"}, "Welcome to Amazon S3.",
			"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class", "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
		{"GET lifecycle", "GET", "/", "lifecycle", nil, "",
			"host;x-amz-content-sha256;x-amz-date", "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"list", "GET", "/", "max-keys=2&prefix=J", nil, "",
			"host;x-amz-content-sha256;x-amz-date", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{Method: tc.method, Host: "examplebucket.s3.amazonaws.com",
				URL: &url.URL{Path: tc.path, RawQuery: tc.query}, Header: http.Header{}}
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			sum := sha256.Sum256([]byte(tc.body))
			hash := hex.EncodeToString(sum[:])
			req.Header.Set("X-Amz-Content-Sha256", hash)
			sign(req, cr, "us-east-1", "s3", at, hash)
			want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=" +
				tc.signed + ", Signature=" + tc.signature
			if got := req.Header.Get("Authorization"); got != want {
				t.Errorf("Authorization:\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// Parameters of one name sort by value, and an encoded query reads back as
// itself.
func TestCanonicalQuery(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"b=2&a=2&a=1", "a=1&a=2&b=2"},
		{"uploads", "uploads="},
		{query("prefix", "ltx/0/a b+c", "list-type", "2"), "list-type=2&prefix=ltx%2F0%2Fa%20b%2Bc"},
	} {
		if got := canonicalQuery(tc.raw); got != tc.want {
			t.Errorf("canonicalQuery(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
