// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"strings"
	"testing"
)

func TestParseURL(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	for _, c := range []struct {
		url                            string
		endpoint, region, bucket, path string
	}{
		{"s3://b/app.db", "https://s3.us-east-1.amazonaws.com", "us-east-1", "b", "app.db"},
		{"s3://b/x/app.db/?region=eu-west-1", "https://s3.eu-west-1.amazonaws.com", "eu-west-1", "b", "x/app.db"},
		{"s3://b/app.db?endpoint=https://a.r2.cloudflarestorage.com&region=auto", "https://a.r2.cloudflarestorage.com", "auto", "b", "app.db"},
	} {
		cfg, path, err := parseURL(c.url)
		if err != nil || cfg.Endpoint != c.endpoint || cfg.Region != c.region || cfg.Bucket != c.bucket || path != c.path {
			t.Errorf("%s: %+v %q %v", c.url, cfg, path, err)
		}
	}
	t.Setenv("AWS_REGION", "ap-south-1")
	if cfg, _, err := parseURL("s3://b/p"); err != nil || cfg.Region != "ap-south-1" {
		t.Errorf("AWS_REGION: %+v %v", cfg, err)
	}
	for _, bad := range []string{
		"file:///x", "s3://b", "s3://b/", "s3:///p", "s3://key:secret@b/p",
		"s3://b/p?endpont=x", "s3://b/p?region=a&region=b", "%zz",
	} {
		if _, _, err := parseURL(bad); err == nil {
			t.Errorf("%s: no error", bad)
		} else if bad == "s3://key:secret@b/p" && strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the error shows the secret: %v", bad, err)
		}
	}
}
