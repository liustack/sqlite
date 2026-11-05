// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package s3 is the small client of the S3 protocol the replicator uses:
// PUT, multipart upload, ranged GET, ListObjectsV2 and DELETE, signed with
// SigV4, against AWS S3 and the stores that speak its protocol (Cloudflare
// R2, MinIO and the like).
//
// Every body is signed by its SHA-256, which the store checks, and no
// request asks for the checksums newer AWS SDKs add, which other stores do
// not all take. Redirects are not followed: a request goes to the endpoint
// it was given or fails. No error carries a secret.
package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Config says where the bucket is and how to sign.
type Config struct {
	// Endpoint is the scheme and host of the store, such as
	// https://s3.us-east-1.amazonaws.com or
	// https://<account>.r2.cloudflarestorage.com, with no path.
	Endpoint string
	// Region is the region requests are signed for: "auto" for R2, and
	// often "us-east-1" for a store that has no regions.
	Region string
	Bucket string
	// Credentials sign each request.
	Credentials CredentialsProvider
	// AllowHTTP lets the endpoint be plain http, such as a local MinIO.
	// Without it only https is taken.
	AllowHTTP bool
}

// Client talks to one bucket. It is safe for concurrent use.
type Client struct {
	scheme, host string
	pathStyle    bool // the bucket is the first segment of the path, not in the host
	bucket       string
	region       string
	creds        CredentialsProvider
	http         *http.Client
	now          func() time.Time
	partSize     int64 // the size of a part, and the largest object sent in one PUT
	listPage     int   // max-keys of a LIST, 0 for the store's own (1000)
	// The credentials of the last request, which errorOf takes out of what
	// the store answers.
	seen *atomic.Pointer[Credentials]
}

// New checks cfg and makes a client. It makes no request.
//
// The bucket goes in the host (virtual-hosted style) on an amazonaws.com
// endpoint when its name has no dot, and in the path otherwise, which the
// other stores all take.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, errors.New("s3: the endpoint is not a URL with a host")
	}
	switch {
	case u.User != nil:
		return nil, errors.New("s3: the endpoint has a user name or password in it: give them as credentials")
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "":
		return nil, fmt.Errorf("s3: the endpoint %s has a path or query: give the scheme and host only", u.Redacted())
	case u.Scheme == "https":
	case u.Scheme == "http" && cfg.AllowHTTP:
	case u.Scheme == "http":
		return nil, fmt.Errorf("s3: the endpoint %s is plain http, which is taken only when allowed", u.Host)
	default:
		return nil, fmt.Errorf("s3: the endpoint %s is neither https nor http", u.Host)
	}
	if cfg.Region == "" {
		return nil, errors.New("s3: no region")
	}
	if !validBucket(cfg.Bucket) {
		return nil, fmt.Errorf("s3: %q is not a bucket name", cfg.Bucket)
	}
	if cfg.Credentials == nil {
		return nil, errors.New("s3: no credentials")
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	// The bytes of an object as stored, never decompressed on the way.
	t.DisableCompression = true
	// A store that took the request and never answers. The time counts from
	// the end of the body, so a large upload does not run into it.
	t.ResponseHeaderTimeout = time.Minute
	c := &Client{
		scheme:   u.Scheme,
		host:     u.Host,
		bucket:   cfg.Bucket,
		region:   cfg.Region,
		creds:    cfg.Credentials,
		now:      time.Now,
		partSize: 64 << 20,
		seen:     new(atomic.Pointer[Credentials]),
		http: &http.Client{
			Transport: t,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	c.pathStyle = !strings.HasSuffix(u.Hostname(), ".amazonaws.com") || strings.Contains(cfg.Bucket, ".")
	if !c.pathStyle {
		c.host = cfg.Bucket + "." + u.Host
	}
	return c, nil
}

// Format prints where the client goes, and nothing of its credentials.
func (c Client) Format(f fmt.State, verb rune) {
	fmt.Fprintf(f, "s3.Client{%s://%s, bucket %s}", c.scheme, c.host, c.bucket)
}

// validBucket follows S3's rules for bucket names: 3 to 63 lower case
// letters, digits, '.' and '-', starting and ending with a letter or digit.
func validBucket(b string) bool {
	if len(b) < 3 || len(b) > 63 || strings.Trim(b, "abcdefghijklmnopqrstuvwxyz0123456789.-") != "" {
		return false
	}
	ends := func(c byte) bool { return 'a' <= c && c <= 'z' || '0' <= c && c <= '9' }
	return ends(b[0]) && ends(b[len(b)-1])
}

// ErrNoAnswer is a request that got no answer: the connection failed or
// the answer was cut. It may have taken effect or not. A PUT with
// IfNoneMatch sent again after it can fail with ErrPreconditionFailed
// because of its own first try: Head tells.
var ErrNoAnswer = errors.New("s3: no answer")

// Retryable says whether err may pass when the request is sent again: no
// answer, a 5xx, 429 (slow down), or 409 (a conditional write to the same
// key at the same time, which AWS asks to retry).
func Retryable(err error) bool {
	var e *Error
	switch {
	case errors.Is(err, ErrNoAnswer):
		return true
	case errors.As(err, &e):
		return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests || e.StatusCode == http.StatusConflict
	}
	return false
}

// ErrPreconditionFailed is a 412 answer: an object written with
// IfNoneMatch is there already.
var ErrPreconditionFailed = errors.New("s3: precondition failed")

// ErrNotFound is the answer of a GET of a key the bucket does not have. A
// missing bucket is another error.
var ErrNotFound = errors.New("s3: no such key")

// Error is an answer of the store that is not a success.
type Error struct {
	Op         string // GET, PUT, LIST, DELETE, or a step of a multipart upload
	Key        string
	StatusCode int
	Code       string // S3's error code, such as NoSuchKey, if the answer had one
	Message    string
	RequestID  string
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "s3: %s %s: %d", e.Op, e.Key, e.StatusCode)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.RequestID != "" {
		b.WriteString(" (request " + e.RequestID + ")")
	}
	return b.String()
}

// Is matches ErrPreconditionFailed and ErrNotFound.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrPreconditionFailed:
		return e.StatusCode == http.StatusPreconditionFailed
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound && e.Code == "NoSuchKey"
	}
	return false
}

// request is one request to the store.
type request struct {
	op     string // for errors
	method string
	key    string // empty for the bucket
	query  string // encoded with query
	header http.Header
	// The body: size bytes of body from off, whose SHA-256 is hash.
	body      io.ReaderAt
	off, size int64
	hash      string
}

// do sends r and gives the answer when it is a success. The caller closes
// its body.
//
// A store closes connections that wait idle in the pool, and a request
// sent on one just as it closes fails without an answer. Go sends again
// only GET and HEAD then. do sends any request again, once, on a new
// connection: a body is read again from body, the requests other than PUT
// and a multipart upload's completion change nothing when repeated, and a
// repeated conditional PUT answers 412, which the replicator reads.
func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	for try := 1; ; try++ {
		resp, reused, err := c.send(ctx, r)
		if errors.Is(err, ErrNoAnswer) && reused && try == 1 && ctx.Err() == nil {
			continue
		}
		return resp, err
	}
}

// send sends r once, and says whether it went on a connection used before.
func (c *Client) send(ctx context.Context, r request) (resp *http.Response, reused bool, err error) {
	path := "/" + r.key
	if c.pathStyle {
		path = "/" + c.bucket
		if r.key != "" {
			path += "/" + r.key
		}
	}
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), r.method, "", nil)
	if err != nil {
		return nil, false, err
	}
	req.URL = &url.URL{Scheme: c.scheme, Host: c.host, Path: path, RawPath: escape(path, false), RawQuery: r.query}
	req.Host = c.host
	if r.header != nil {
		req.Header = r.header.Clone() // signed again on each try
	}
	hash := emptySHA256
	if r.body != nil && r.size > 0 {
		req.ContentLength = r.size
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(io.NewSectionReader(r.body, r.off, r.size)), nil
		}
		req.Body, _ = req.GetBody()
		hash = r.hash
	}
	req.Header.Set("X-Amz-Content-Sha256", hash)
	cr, err := c.creds.Credentials(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("s3: %s %s: credentials: %w", r.op, r.key, err)
	}
	if p := c.seen.Load(); p == nil || *p != cr {
		c.seen.Store(&cr)
	}
	sign(req, cr, c.region, "s3", c.now(), hash)
	resp, err = c.http.Do(req)
	if err != nil {
		return nil, reused, fmt.Errorf("s3: %s %s: %w: %w", r.op, r.key, ErrNoAnswer, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, reused, c.errorOf(resp, r.op, r.key, nil)
	}
	return resp, reused, nil
}

// errorOf reads the error the store answered. body is what was read of it
// already, nil if nothing was. A store may put headers of the request in
// its message: the secret and the session token are taken out.
func (c *Client) errorOf(resp *http.Response, op, key string, body []byte) *Error {
	if body == nil {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	}
	var x struct {
		Code      string
		Message   string
		RequestID string `xml:"RequestId"`
	}
	xml.Unmarshal(body, &x) // a HEAD-like answer has no body: the status says enough
	e := &Error{Op: op, Key: key, StatusCode: resp.StatusCode, Code: x.Code, Message: x.Message, RequestID: x.RequestID}
	if e.RequestID == "" {
		e.RequestID = resp.Header.Get("X-Amz-Request-Id")
	}
	if p := c.seen.Load(); p != nil {
		for _, s := range []string{p.SecretAccessKey, p.SessionToken} {
			if s != "" {
				e.Message = strings.ReplaceAll(e.Message, s, "<hidden>")
			}
		}
	}
	if e.StatusCode/100 == 3 {
		e.Message = strings.TrimSpace(e.Message + " The endpoint or region does not match the bucket's.")
	}
	return e
}

// done reads what is left of an answer, so that its connection is used
// again, and closes it.
func done(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// query encodes the pairs of names and values kv. A pair with an empty
// value is the name alone.
func query(kv ...string) string {
	var b strings.Builder
	for i := 0; i < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(escape(kv[i], true))
		if kv[i+1] != "" {
			b.WriteByte('=')
			b.WriteString(escape(kv[i+1], true))
		}
	}
	return b.String()
}

// checkKey takes the keys every store takes: no empty, "." or ".."
// segment, which some stores refuse or read as a path.
func checkKey(key string) error {
	bad := key == "" || len(key) > 1024 || !utf8.ValidString(key)
	for s := range strings.SplitSeq(key, "/") {
		bad = bad || s == "" || s == "." || s == ".."
	}
	if bad {
		return fmt.Errorf("s3: %q is not an object key", key)
	}
	return nil
}

// hashOf is the hex SHA-256 of size bytes of r from off.
func hashOf(r io.ReaderAt, off, size int64) (string, error) {
	h := sha256.New()
	if n, err := io.Copy(h, io.NewSectionReader(r, off, size)); err != nil {
		return "", err
	} else if n != size {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readXML decodes an answer of at most limit bytes into v.
func readXML(resp *http.Response, limit int64, v any) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("an answer of more than %d bytes", limit)
	}
	return xml.Unmarshal(body, v)
}

func parseInt(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
