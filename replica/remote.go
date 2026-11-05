// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/s3"
)

// The replica is laid out as Litestream's S3 backend lays it out:
// <path>/<level, 4 hex digits>/<min>-<max>.ltx, with the time of the file
// in metadata. The replicator writes level 0.
const (
	metaTime   = "litestream-timestamp" // RFC 3339, of the header's Timestamp
	metaSHA256 = "sqlite-sha256"        // of the file, to know our own write after a 412
)

// snapshotLevel is the level of the snapshots: files from TXID 1.
const snapshotLevel = 9

func levelKey(path string, level int, min, max ltx.TXID) string {
	return fmt.Sprintf("%s/%04x/%s", path, level, ltx.FormatFilename(min, max))
}

// A remoteFile is an LTX file of the replica.
type remoteFile struct {
	level    int
	min, max ltx.TXID
	key      string
	size     int64
	modified time.Time // when the store got it
}

// listRemote lists the LTX files of the replica in key order: by level,
// then by TXID.
func listRemote(ctx context.Context, c *s3.Client, path string) ([]remoteFile, error) {
	var files []remoteFile
	for o, err := range c.List(ctx, path+"/", "") {
		if err != nil {
			return nil, err
		}
		level, name, ok := strings.Cut(strings.TrimPrefix(o.Key, path+"/"), "/")
		if !ok || len(level) != 4 {
			continue
		}
		n, err := strconv.ParseUint(level, 16, 16)
		if err != nil || n > snapshotLevel {
			continue
		}
		min, max, err := ltx.ParseFilename(name)
		if err != nil {
			continue
		}
		files = append(files, remoteFile{level: int(n), min: min, max: max, key: o.Key, size: o.Size, modified: o.LastModified})
	}
	return files, nil
}

// remoteEnd is where the replica ends: its last TXID and the checksum of
// the database there, TXID 0 for an empty replica.
type remoteEnd struct {
	txid ltx.TXID
	post ltx.Checksum
}

// readEnd reads the trailer of the last of the replica.s files.
func readEnd(ctx context.Context, c *s3.Client, files []remoteFile) (remoteEnd, error) {
	var last remoteFile
	for _, f := range files {
		if f.max > last.max {
			last = f
		}
	}
	if last.max == 0 {
		return remoteEnd{}, nil
	}
	if last.size < ltx.HeaderSize+ltx.TrailerSize {
		return remoteEnd{}, fmt.Errorf("replica: %s is too short for an LTX file", last.key)
	}
	r, err := c.Get(ctx, last.key, last.size-ltx.TrailerSize, ltx.TrailerSize)
	if err != nil {
		return remoteEnd{}, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, ltx.TrailerSize+1))
	if err != nil {
		return remoteEnd{}, err
	}
	var t ltx.Trailer
	if len(b) != ltx.TrailerSize || t.UnmarshalBinary(b) != nil || t.PostApplyChecksum&ltx.ChecksumFlag == 0 {
		return remoteEnd{}, fmt.Errorf("replica: %s has no trailer with a checksum", last.key)
	}
	return remoteEnd{txid: last.max, post: t.PostApplyChecksum}, nil
}

// ErrNoConditionalWrites is the error of New on a store that does not
// refuse a second write of a key with If-None-Match, which keeps two
// replicators from writing over each other.
var ErrNoConditionalWrites = errors.New("replica: the store does not refuse a second write of a key (If-None-Match)")

// probe checks that the store refuses a second conditional write of a key,
// with one PUT and with a multipart upload.
func probe(ctx context.Context, c *s3.Client, path string) error {
	var id [8]byte
	rand.Read(id[:])
	for _, multipart := range []bool{false, true} {
		key := fmt.Sprintf("%s/probe/%s-%t", path, hex.EncodeToString(id[:]), multipart)
		opt := s3.PutOptions{IfNoneMatch: true, Multipart: multipart}
		body := []byte("probe")
		if err := c.Put(ctx, key, bytes.NewReader(body), int64(len(body)), opt); err != nil {
			return err
		}
		err := c.Put(ctx, key, bytes.NewReader(body), int64(len(body)), opt)
		derr := c.Delete(ctx, key)
		switch {
		case err == nil:
			kind := "PUT"
			if multipart {
				kind = "multipart upload"
			}
			return fmt.Errorf("%w: a %s", ErrNoConditionalWrites, kind)
		case !errors.Is(err, s3.ErrPreconditionFailed):
			return err
		case derr != nil:
			return derr
		}
	}
	return nil
}
