// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/liustack/sqlite/replica/internal/s3"
)

// parseURL reads a replica URL, s3://bucket/path?endpoint=...&region=...,
// into the store's config (without credentials) and the path of the replica
// in the bucket.
func parseURL(raw string) (s3.Config, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// url's errors quote the URL, which must hold no secret anyway.
		return s3.Config{}, "", errors.New("replica: the URL does not parse")
	}
	if u.Scheme != "s3" {
		return s3.Config{}, "", fmt.Errorf("replica: the URL is %s://, not s3://", u.Scheme)
	}
	if u.User != nil {
		return s3.Config{}, "", errors.New("replica: the URL holds a user: credentials go in Config.Credentials")
	}
	path := strings.Trim(u.Path, "/")
	if u.Host == "" || path == "" {
		return s3.Config{}, "", errors.New("replica: the URL needs a bucket and a path: s3://bucket/path")
	}
	cfg := s3.Config{Bucket: u.Host}
	for k, v := range u.Query() {
		if len(v) != 1 {
			return s3.Config{}, "", fmt.Errorf("replica: the URL has %s %d times", k, len(v))
		}
		switch k {
		case "endpoint":
			cfg.Endpoint = v[0]
		case "region":
			cfg.Region = v[0]
		default:
			return s3.Config{}, "", fmt.Errorf("replica: the URL has %s, which is not endpoint or region", k)
		}
	}
	if cfg.Region == "" {
		if cfg.Region = os.Getenv("AWS_REGION"); cfg.Region == "" {
			cfg.Region = "us-east-1"
		}
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://s3." + cfg.Region + ".amazonaws.com"
	}
	return cfg, path, nil
}
