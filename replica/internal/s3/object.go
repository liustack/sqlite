// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PutOptions are the options of Put.
type PutOptions struct {
	// IfNoneMatch writes the object only if the key has none: Put fails
	// with ErrPreconditionFailed when it has.
	IfNoneMatch bool
	// Metadata is stored with the object as x-amz-meta-<name>. Names are
	// lower case letters, digits and '-', values printable ASCII.
	Metadata map[string]string
}

func (o PutOptions) header() (http.Header, error) {
	h := http.Header{}
	for name, v := range o.Metadata {
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return nil, fmt.Errorf("s3: %q is not a metadata name", name)
		}
		for i := 0; i < len(v); i++ {
			if v[i] < ' ' || v[i] > '~' {
				return nil, fmt.Errorf("s3: the value of metadata %s is not printable ASCII", name)
			}
		}
		h.Set("X-Amz-Meta-"+name, v)
	}
	return h, nil
}

// Put writes size bytes of body as the object at key. An object larger
// than a part goes up in parts (a multipart upload), each part hashed and
// sent from body in turn, so nothing is held in memory. A failed multipart
// upload is aborted, but for one whose start got no answer, which has no ID
// to abort: a lifecycle rule of the bucket (AbortIncompleteMultipartUpload)
// removes its parts. body must not change during the call.
func (c *Client) Put(ctx context.Context, key string, body io.ReaderAt, size int64, opt PutOptions) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("s3: PUT %s: a size of %d", key, size)
	}
	h, err := opt.header()
	if err != nil {
		return err
	}
	if size > c.partSize {
		return c.putParts(ctx, key, body, size, opt.IfNoneMatch, h)
	}
	hash, err := hashOf(body, 0, size)
	if err != nil {
		return fmt.Errorf("s3: PUT %s: reading the body: %w", key, err)
	}
	if opt.IfNoneMatch {
		h.Set("If-None-Match", "*")
	}
	resp, err := c.do(ctx, request{op: "PUT", method: http.MethodPut, key: key, header: h, body: body, size: size, hash: hash})
	if err != nil {
		return err
	}
	done(resp)
	return nil
}

// putParts writes the object in parts: at most 10000, of partSize or more.
func (c *Client) putParts(ctx context.Context, key string, body io.ReaderAt, size int64, ifNoneMatch bool, h http.Header) (err error) {
	partSize := max(c.partSize, (size+9999)/10000)
	resp, err := c.do(ctx, request{op: "CreateMultipartUpload", method: http.MethodPost, key: key, query: query("uploads", ""), header: h})
	if err != nil {
		return err
	}
	var created struct{ UploadId string }
	err = readXML(resp, 64<<10, &created)
	done(resp)
	if err != nil || created.UploadId == "" {
		return fmt.Errorf("s3: CreateMultipartUpload %s: no upload ID in the answer (%v)", key, err)
	}
	id := created.UploadId
	defer func() {
		if err == nil {
			return
		}
		// Parts left behind are billed until aborted, also when ctx is done.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		resp, aerr := c.do(actx, request{op: "AbortMultipartUpload", method: http.MethodDelete, key: key, query: query("uploadId", id)})
		if aerr != nil {
			err = fmt.Errorf("%w (and the upload %s was not aborted: %v)", err, id, aerr)
			return
		}
		done(resp)
	}()

	type part struct {
		PartNumber int
		ETag       string
	}
	var parts []part
	for off := int64(0); off < size; off += partSize {
		n := min(partSize, size-off)
		num := len(parts) + 1
		hash, err := hashOf(body, off, n)
		if err != nil {
			return fmt.Errorf("s3: UploadPart %s: reading the body: %w", key, err)
		}
		resp, err := c.do(ctx, request{op: "UploadPart", method: http.MethodPut, key: key,
			query: query("partNumber", strconv.Itoa(num), "uploadId", id), body: body, off: off, size: n, hash: hash})
		if err != nil {
			return err
		}
		etag := resp.Header.Get("ETag")
		done(resp)
		if etag == "" {
			return fmt.Errorf("s3: UploadPart %s: no ETag in the answer of part %d", key, num)
		}
		parts = append(parts, part{num, etag})
	}

	doc, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []part   `xml:"Part"`
	}{Parts: parts})
	if err != nil {
		return err
	}
	ch := http.Header{}
	if ifNoneMatch {
		ch.Set("If-None-Match", "*")
	}
	sum := sha256.Sum256(doc)
	resp, err = c.do(ctx, request{op: "CompleteMultipartUpload", method: http.MethodPost, key: key, query: query("uploadId", id),
		header: ch, body: bytes.NewReader(doc), size: int64(len(doc)), hash: hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	defer done(resp)
	// The store can answer 200 and still fail: the error is then the body.
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("s3: CompleteMultipartUpload %s: %w: %w", key, ErrNoAnswer, err)
	}
	var root struct{ XMLName xml.Name }
	if xml.Unmarshal(answer, &root) != nil || root.XMLName.Local != "CompleteMultipartUploadResult" {
		return errorOf(resp, "CompleteMultipartUpload", key, answer)
	}
	return nil
}

// Get reads the object at key: n bytes from byte off, or all from off when
// n < 0. The answer is shorter than n only when the object ends first. A
// range that starts at or past the end, also of an empty object, fails
// with a 416. The caller closes the reader.
func (c *Client) Get(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	if off < 0 || n == 0 {
		return nil, fmt.Errorf("s3: GET %s: %d bytes from %d", key, n, off)
	}
	h := http.Header{}
	ranged := off > 0 || n > 0
	if ranged {
		r := "bytes=" + strconv.FormatInt(off, 10) + "-"
		if n > 0 {
			r += strconv.FormatInt(off+n-1, 10)
		}
		h.Set("Range", r)
	}
	resp, err := c.do(ctx, request{op: "GET", method: http.MethodGet, key: key, header: h})
	if err != nil {
		return nil, err
	}
	if ranged {
		// A store that ignores the range, or cuts it short, would give other
		// bytes than asked.
		start, end, total, ok := contentRange(resp.Header.Get("Content-Range"))
		want := total - 1
		if n > 0 {
			want = min(off+n-1, total-1)
		}
		if resp.StatusCode != http.StatusPartialContent || !ok || start != off || end != want ||
			resp.ContentLength >= 0 && resp.ContentLength != end-start+1 {
			done(resp)
			return nil, fmt.Errorf("s3: GET %s: asked for bytes from %d, got %d %q", key, off, resp.StatusCode, resp.Header.Get("Content-Range"))
		}
	}
	return resp.Body, nil
}

// contentRange reads "bytes <start>-<end>/<total>".
func contentRange(s string) (start, end, total int64, ok bool) {
	r, ok := strings.CutPrefix(s, "bytes ")
	if !ok {
		return 0, 0, 0, false
	}
	r, t, ok := strings.Cut(r, "/")
	if !ok {
		return 0, 0, 0, false
	}
	a, b, ok := strings.Cut(r, "-")
	if !ok {
		return 0, 0, 0, false
	}
	start, err1 := parseInt(a)
	end, err2 := parseInt(b)
	total, err3 := parseInt(t)
	return start, end, total, err1 == nil && err2 == nil && err3 == nil && start <= end && end < total
}

// Delete deletes the object at key. A key with no object is no error.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	resp, err := c.do(ctx, request{op: "DELETE", method: http.MethodDelete, key: key})
	if err != nil {
		return err
	}
	done(resp)
	return nil
}

// Object is an object as a list gives it.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

// List gives the objects whose keys start with prefix and come after
// startAfter (when not empty), in key order, a page of up to 1000 at a
// time. It stops at the first error.
func (c *Client) List(ctx context.Context, prefix, startAfter string) iter.Seq2[Object, error] {
	return func(yield func(Object, error) bool) {
		last, token := startAfter, ""
		for {
			kv := []string{"list-type", "2"}
			if c.listPage > 0 {
				kv = append(kv, "max-keys", strconv.Itoa(c.listPage))
			}
			if prefix != "" {
				kv = append(kv, "prefix", prefix)
			}
			if token != "" {
				kv = append(kv, "continuation-token", token)
			} else if startAfter != "" {
				kv = append(kv, "start-after", startAfter)
			}
			resp, err := c.do(ctx, request{op: "LIST", method: http.MethodGet, key: "", query: query(kv...)})
			if err != nil {
				yield(Object{}, err)
				return
			}
			var page struct {
				IsTruncated           bool
				NextContinuationToken string
				Contents              []Object
			}
			err = readXML(resp, 16<<20, &page)
			done(resp)
			if err != nil {
				yield(Object{}, fmt.Errorf("s3: LIST %s: %w", prefix, err))
				return
			}
			for _, o := range page.Contents {
				// Checked so that a store that loops or skips is an error,
				// not a wrong list.
				if !strings.HasPrefix(o.Key, prefix) || o.Key <= last {
					yield(Object{}, fmt.Errorf("s3: LIST %s: key %q out of order after %q", prefix, o.Key, last))
					return
				}
				last = o.Key
				if !yield(o, nil) {
					return
				}
			}
			if !page.IsTruncated {
				return
			}
			if page.NextContinuationToken == "" || page.NextContinuationToken == token {
				yield(Object{}, errors.New("s3: LIST "+prefix+": a truncated page with no new continuation token"))
				return
			}
			token = page.NextContinuationToken
		}
	}
}

// Info is what Head gives of an object.
type Info struct {
	Size         int64
	LastModified time.Time
	ETag         string
	Metadata     map[string]string // by the names given to Put
}

// Head reads the size and metadata of the object at key. Its 404 is
// ErrNotFound: a HEAD answer has no body to say NoSuchKey, so a missing
// bucket reads the same.
func (c *Client) Head(ctx context.Context, key string) (Info, error) {
	if err := checkKey(key); err != nil {
		return Info{}, err
	}
	resp, err := c.do(ctx, request{op: "HEAD", method: http.MethodHead, key: key})
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.StatusCode == http.StatusNotFound && e.Code == "" {
			e.Code = "NoSuchKey"
		}
		return Info{}, err
	}
	done(resp)
	info := Info{Size: resp.ContentLength, ETag: resp.Header.Get("ETag"), Metadata: map[string]string{}}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		info.LastModified = t
	}
	for name, vs := range resp.Header {
		if m, ok := strings.CutPrefix(strings.ToLower(name), "x-amz-meta-"); ok && len(vs) > 0 {
			info.Metadata[m] = vs[0]
		}
	}
	return info, nil
}
