// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/s3"
)

// ErrConflict is under the error of an upload the store refused because
// the replica has another file of that TXID: another replicator writes to
// the same path. The replicator stops uploading. Staging and checkpoints go
// on, and the program's writes are not touched.
var ErrConflict = errors.New("replica: another replicator writes to the replica")

// run uploads the staged files as the capturer stages them, until ctx is
// done. A failed upload is tried again later, longer after each failure up
// to a minute. After a conflict it only drops what is staged.
func (r *Replica) run(ctx context.Context) {
	defer close(r.done)
	var retry <-chan time.Time
	wait := time.Second
	reported := false // the conflict
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.c.Staged():
		case <-retry:
		}
		retry = nil
		err := r.upload(ctx)
		switch {
		case err == nil:
			wait = time.Second
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrConflict):
			if !reported {
				r.report(err)
				reported = true
			}
		default:
			r.report(err)
			retry = time.After(wait)
			wait = min(2*wait, time.Minute)
		}
	}
}

// upload uploads the staged files whose names are synced, in TXID order,
// and removes those uploaded but the last staged, which the capturer
// resumes from. It records its error in the status. ctx bounds the wait
// for another upload too.
func (r *Replica) upload(ctx context.Context) error {
	select {
	case r.uploading <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.uploading }()
	err := r.uploadFiles(ctx)
	r.setStatus(func(s *Status) {
		if !errors.Is(s.Err, ErrConflict) {
			s.Err = err
		}
	})
	return err
}

func (r *Replica) uploadFiles(ctx context.Context) error {
	// Durable is 1 or more once capture.Open returns.
	durable := r.c.Durable()
	if err := r.conflict(); err != nil {
		// What is staged can no longer go to the replica: only the last
		// file is kept, which the capturer goes on from.
		if derr := r.dropUploaded(durable - 1); derr != nil {
			return derr
		}
		return err
	}
	for txid := r.uploaded() + 1; txid <= durable; txid++ {
		if err := r.put(ctx, txid); err != nil {
			if errors.Is(err, ErrConflict) {
				r.setStatus(func(s *Status) { s.Err = err })
			}
			return err
		}
		r.setStatus(func(s *Status) { s.Uploaded = uint64(txid) })
	}
	return r.dropUploaded(min(r.uploaded(), durable-1))
}

// put uploads the staged file of txid, once: a 412 means the key has a
// file, which is the same one when an earlier try got no answer.
func (r *Replica) put(ctx context.Context, txid ltx.TXID) error {
	f, err := os.Open(filepath.Join(r.c.Dir(), ltx.FormatFilename(txid, txid)))
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	var hb [ltx.HeaderSize]byte
	if _, err := f.ReadAt(hb[:], 0); err != nil {
		return err
	}
	var h ltx.Header
	if err := h.UnmarshalBinary(hb[:]); err != nil {
		return fmt.Errorf("replica: the staged file of TXID %s: %w", txid, err)
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(f, 0, st.Size())); err != nil {
		return err
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	key := levelKey(r.path, 0, txid)
	err = r.client.Put(ctx, key, f, st.Size(), s3.PutOptions{
		IfNoneMatch: true,
		Metadata: map[string]string{
			metaTime:   time.UnixMilli(h.Timestamp).UTC().Format(time.RFC3339Nano),
			metaSHA256: digest,
		},
	})
	if !errors.Is(err, s3.ErrPreconditionFailed) {
		return err
	}
	info, err := r.client.Head(ctx, key)
	if err != nil {
		return err
	}
	if info.Metadata[metaSHA256] != digest {
		return fmt.Errorf("%w: %s has another file", ErrConflict, key)
	}
	return nil
}

// dropUploaded removes the staged files up to txid.
func (r *Replica) dropUploaded(txid ltx.TXID) error {
	entries, err := os.ReadDir(r.c.Dir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		min, max, err := ltx.ParseFilename(e.Name())
		if err != nil || min != max || max > txid {
			continue
		}
		if err := os.Remove(filepath.Join(r.c.Dir(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}
