// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// AWS Signature Version 4, as S3 takes it: the path is encoded once and not
// normalized, and the body is signed by its SHA-256.
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html

const (
	amzDate     = "20060102T150405Z"
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// sign sets X-Amz-Date, X-Amz-Security-Token when cr has a session token,
// and Authorization on req. Every header req has then is signed, and Host.
// payloadHash is the hex SHA-256 of the body.
func sign(req *http.Request, cr Credentials, region, service string, now time.Time, payloadHash string) {
	date := now.UTC().Format(amzDate)
	req.Header.Set("X-Amz-Date", date)
	if cr.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", cr.SessionToken)
	}
	canon, signed := canonicalRequest(req, payloadHash)
	scope := date[:8] + "/" + region + "/" + service + "/aws4_request"
	toSign := stringToSign(date, scope, canon)
	key := hmacSHA256([]byte("AWS4"+cr.SecretAccessKey), date[:8])
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cr.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
}

func stringToSign(date, scope, canon string) string {
	h := sha256.Sum256([]byte(canon))
	return "AWS4-HMAC-SHA256\n" + date + "\n" + scope + "\n" + hex.EncodeToString(h[:])
}

// canonicalRequest gives the canonical request of req and the list of the
// headers it signs.
func canonicalRequest(req *http.Request, payloadHash string) (canon, signed string) {
	type header struct{ name, value string }
	hs := []header{{"host", req.Host}}
	for name, values := range req.Header {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		vs := make([]string, len(values))
		for i, v := range values {
			vs[i] = strings.Join(strings.Fields(v), " ")
		}
		hs = append(hs, header{strings.ToLower(name), strings.Join(vs, ",")})
	}
	slices.SortFunc(hs, func(a, b header) int { return strings.Compare(a.name, b.name) })

	var b strings.Builder
	b.WriteString(req.Method)
	b.WriteByte('\n')
	b.WriteString(escape(req.URL.Path, false))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(req.URL.RawQuery))
	b.WriteByte('\n')
	names := make([]string, len(hs))
	for i, h := range hs {
		b.WriteString(h.name)
		b.WriteByte(':')
		b.WriteString(h.value)
		b.WriteByte('\n')
		names[i] = h.name
	}
	signed = strings.Join(names, ";")
	b.WriteByte('\n')
	b.WriteString(signed)
	b.WriteByte('\n')
	b.WriteString(payloadHash)
	return b.String(), signed
}

// canonicalQuery sorts the parameters of a raw query and encodes them as
// SigV4 wants: every byte but the unreserved ones, '=' after each name.
func canonicalQuery(raw string) string {
	type param struct{ k, v string }
	var ps []param
	for kv := range strings.SplitSeq(raw, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		if u, err := url.PathUnescape(k); err == nil {
			k = u
		}
		if u, err := url.PathUnescape(v); err == nil {
			v = u
		}
		ps = append(ps, param{escape(k, true), escape(v, true)})
	}
	slices.SortFunc(ps, func(a, b param) int {
		if c := strings.Compare(a.k, b.k); c != 0 {
			return c
		}
		return strings.Compare(a.v, b.v)
	})
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

// escape percent-encodes every byte of s but A-Z, a-z, 0-9, '-', '.', '_',
// '~', and '/' unless slash is set.
func escape(s string, slash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' || c == '/' && !slash {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
