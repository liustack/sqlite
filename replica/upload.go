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
	"slices"
	"strings"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/s3"
)

// ErrConflict is under the error of an upload the store refused because
// the replica has another file of that TXID: another replicator writes to
// the same path. The replicator stops uploading. Staging and checkpoints go
// on, and the program's writes are not touched.
//
// The replicator finds another one when the store refuses one of its
// uploads, and, before an upload, when it has not checked for 5 minutes
// that the replica ends where it left it. This finds a second replicator
// in normal running and after one of them could not reach the store, but
// it is not a fence: in some races both still get files into the replica,
// such as when the level 0 files one would find were deleted in between
// its check and its upload. When a database moves to another
// machine, make sure the old replicator stopped first (Config.SingleWriter
// and a lock or lease of the program's own).
var ErrConflict = errors.New("replica: another replicator writes to the replica")

// run uploads the staged files as the capturer stages them, and then does
// the other work on the replica that is due (maintain), until ctx is done.
// Failed work is tried again later, longer after each failure up to a
// minute, and new staged files wait for that try: a store that is down gets
// few requests. After a conflict it only drops what is staged.
func (r *Replica) run(ctx context.Context) {
	defer close(r.done)
	if r.cfg.manual {
		<-ctx.Done()
		return
	}
	var retry, due <-chan time.Time
	staged := r.c.Staged()
	wait := time.Second
	reported := false // the conflict
	for {
		select {
		case <-ctx.Done():
			return
		case <-staged:
		case <-retry:
		case <-due:
		}
		retry, due, staged = nil, nil, r.c.Staged()
		next, err := r.work(ctx)
		switch {
		case err == nil:
			wait = time.Second
			if !next.IsZero() {
				due = time.After(time.Until(next))
			}
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
			staged = nil
			wait = min(2*wait, time.Minute)
		}
	}
}

// work uploads what is staged, then does what maintain does, and returns
// when to run maintain again.
func (r *Replica) work(ctx context.Context) (time.Time, error) {
	if err := r.upload(ctx); err != nil {
		return time.Time{}, err
	}
	select {
	case r.busy <- struct{}{}:
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}
	defer func() { <-r.busy }()
	if err := r.conflict(); err != nil {
		return time.Time{}, err
	}
	return r.maintain(ctx)
}

// upload uploads the staged files whose names are synced, in TXID order,
// and removes those uploaded but the last staged, which the capturer
// resumes from. It records its error in the status. ctx bounds the wait
// for another upload too.
func (r *Replica) upload(ctx context.Context) error {
	select {
	case r.busy <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.busy }()
	if r.closed {
		return errClosed
	}
	return r.uploadHeld(ctx)
}

// uploadHeld is upload, holding busy.
func (r *Replica) uploadHeld(ctx context.Context) error {
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
	err := r.conflict()
	for txid := r.uploaded() + 1; err == nil && txid <= durable; txid++ {
		if err = r.recheck(ctx); err != nil {
			break
		}
		if err = r.put(ctx, txid); err == nil {
			r.setStatus(func(s *Status) { s.Uploaded = uint64(txid) })
		}
	}
	switch {
	case err == nil:
		return r.c.Done(r.uploaded())
	case errors.Is(err, ErrConflict):
		// What is staged can no longer go to the replica, from the first
		// conflict on: only the last file is kept, which the capturer goes
		// on from. Nothing is pending, so staging does not stop.
		if derr := r.c.Done(durable); derr != nil {
			return derr
		}
	}
	return err
}

// recheck checks, when it last did longer than sched.recheck ago, that the
// replica still ends where this replicator left it. It is a check made in
// passing, not a fence: it finds another replicator that went on from the
// same history while this one could not reach the store, and deleted the
// level 0 files that an upload of this one would find, where the
// conditional upload alone would take the key. It cannot rule out every
// race, such as one that starts after the check. Files past the end it
// knows of that are its own staged files (an upload whose answer was lost)
// are not another replicator's.
func (r *Replica) recheck(ctx context.Context) error {
	if r.sched.now().Sub(r.checked) < r.sched.recheck {
		return nil
	}
	files, err := listRemote(ctx, r.client, r.path)
	if err != nil {
		return err
	}
	var end ltx.TXID
	for _, f := range files {
		end = max(end, f.max)
	}
	ok := end == r.top
	if end > r.top {
		ok = true
		for _, f := range files {
			if f.max <= r.top {
				continue
			}
			if ok, err = r.ours(ctx, f); err != nil {
				return err
			}
			if !ok {
				break
			}
		}
	}
	if !ok {
		err := fmt.Errorf("%w: the replica ends at TXID %s, not at %s where this replicator left it", ErrConflict, end, r.top)
		r.setStatus(func(s *Status) { s.Err = err })
		return err
	}
	r.checked = r.sched.now()
	return nil
}

// ours reports whether f is a staged file of this replicator, uploaded as
// level 0.
func (r *Replica) ours(ctx context.Context, f remoteFile) (bool, error) {
	if f.level != 0 || f.min != f.max {
		return false, nil
	}
	file, err := os.Open(filepath.Join(r.c.Dir(), ltx.FormatFilename(f.min, f.max)))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	defer file.Close()
	digest, err := sha256File(file)
	if err != nil {
		return false, err
	}
	info, err := r.client.Head(ctx, f.key)
	if err != nil {
		return false, err
	}
	return info.Metadata[metaSHA256] == digest, nil
}

// put uploads the staged file of txid as level 0.
func (r *Replica) put(ctx context.Context, txid ltx.TXID) error {
	return r.putFile(ctx, 0, filepath.Join(r.c.Dir(), ltx.FormatFilename(txid, txid)))
}

// putFile uploads the LTX file name to level, once: a 412 means the key
// has a file, which is the same one when an earlier try got no answer. It
// adds the file to r.files.
func (r *Replica) putFile(ctx context.Context, level int, name string) error {
	f, err := os.Open(name)
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
		return fmt.Errorf("replica: %s: %w", name, err)
	}
	digest, err := sha256File(f)
	if err != nil {
		return err
	}
	key := levelKey(r.path, level, h.MinTXID, h.MaxTXID)
	err = r.client.Put(ctx, key, f, st.Size(), s3.PutOptions{
		IfNoneMatch: true,
		Metadata: map[string]string{
			metaTime:   time.UnixMilli(h.Timestamp).UTC().Format(time.RFC3339Nano),
			metaSHA256: digest,
		},
	})
	if errors.Is(err, s3.ErrPreconditionFailed) {
		info, herr := r.client.Head(ctx, key)
		if herr != nil {
			return herr
		}
		if info.Metadata[metaSHA256] != digest {
			// At any level: the replicator stops for good.
			err := fmt.Errorf("%w: %s has another file", ErrConflict, key)
			r.setStatus(func(s *Status) { s.Err = err })
			return err
		}
	} else if err != nil {
		return err
	}
	r.addFile(remoteFile{level: level, min: h.MinTXID, max: h.MaxTXID, key: key, size: st.Size(), modified: r.sched.now()})
	r.top = max(r.top, h.MaxTXID)
	return nil
}

// addFile adds f to r.files, in key order.
func (r *Replica) addFile(f remoteFile) {
	i, found := slices.BinarySearchFunc(r.files, f.key, func(g remoteFile, key string) int { return strings.Compare(g.key, key) })
	if !found {
		r.files = slices.Insert(r.files, i, f)
	}
}

// sha256File is the SHA-256 of the file f, in hex. It reads f from its start
// and leaves its offset as it was.
func sha256File(f *os.File) (string, error) {
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(f, 0, st.Size())); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
