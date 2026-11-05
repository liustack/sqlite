// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package replica

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/superfly/ltx"
)

// maxCompactInputs is the most files one compaction merges, so that it
// opens a bounded number of files. What is left is merged next time.
const maxCompactInputs = 256

// compact merges the files of level from that come after the last file of
// level to into one file of level to, and uploads it. It does nothing when
// there is none. Each file is downloaded whole and checked before it is
// merged, and the merged file is checked against them before it is
// uploaded. Call it holding busy.
func (r *Replica) compact(ctx context.Context, from, to int) error {
	var done ltx.TXID // the last TXID of level to
	for _, f := range r.files {
		if f.level == to {
			done = max(done, f.max)
		}
	}
	var src []remoteFile
	for _, f := range r.files {
		if f.level != from || f.min <= done {
			continue
		}
		if len(src) > 0 && f.min != src[len(src)-1].max+1 || len(src) == 0 && done > 0 && f.min != done+1 {
			// A gap: the files after it wait for the file before them.
			break
		}
		if src = append(src, f); len(src) == maxCompactInputs {
			break
		}
	}
	if len(src) == 0 {
		return nil
	}

	dir, err := os.MkdirTemp(filepath.Dir(r.c.Dir()), "compact-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	inputs := make([]io.Reader, len(src))
	var first, last ltx.Header
	var post ltx.Checksum
	for i, f := range src {
		file, h, t, err := r.download(ctx, f, dir)
		if err != nil {
			return err
		}
		defer file.Close()
		inputs[i] = bufio.NewReaderSize(file, 64<<10)
		if i == 0 {
			first = h
		}
		last, post = h, t.PostApplyChecksum
	}

	name := filepath.Join(dir, ltx.FormatFilename(first.MinTXID, last.MaxTXID))
	out, err := os.Create(name)
	if err != nil {
		return err
	}
	defer out.Close()
	w := bufio.NewWriterSize(out, 1<<20)
	comp, err := ltx.NewCompactor(w, inputs)
	if err != nil {
		return err
	}
	if err := comp.Compact(ctx); err != nil {
		return fmt.Errorf("replica: compacting TXIDs %s to %s into level %d: %w", first.MinTXID, last.MaxTXID, to, err)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dec := ltx.NewDecoder(bufio.NewReaderSize(out, 1<<20))
	if err := dec.Verify(); err != nil {
		return fmt.Errorf("replica: the file compacted into level %d: %w", to, err)
	}
	h := dec.Header()
	if h.MinTXID != first.MinTXID || h.MaxTXID != last.MaxTXID || h.PreApplyChecksum != first.PreApplyChecksum ||
		dec.Trailer().PostApplyChecksum != post || h.Timestamp != last.Timestamp {
		return fmt.Errorf("replica: the file compacted into level %d does not match the files merged", to)
	}
	return r.putFile(ctx, to, name)
}

// download downloads the file f into dir, checks it, and returns it open
// at its start, with its header and trailer.
func (r *Replica) download(ctx context.Context, f remoteFile, dir string) (*os.File, ltx.Header, ltx.Trailer, error) {
	fail := func(err error) (*os.File, ltx.Header, ltx.Trailer, error) {
		return nil, ltx.Header{}, ltx.Trailer{}, err
	}
	body, err := r.client.Get(ctx, f.key, 0, -1)
	if err != nil {
		return fail(err)
	}
	defer body.Close()
	file, err := os.Create(filepath.Join(dir, fmt.Sprintf("%d-%s", f.level, ltx.FormatFilename(f.min, f.max))))
	if err != nil {
		return fail(err)
	}
	ok := false
	defer func() {
		if !ok {
			file.Close()
		}
	}()
	w := bufio.NewWriterSize(file, 1<<20)
	if _, err := io.Copy(w, body); err != nil {
		return fail(err)
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	dec := ltx.NewDecoder(bufio.NewReaderSize(file, 1<<20))
	if err := dec.Verify(); err != nil {
		return fail(fmt.Errorf("replica: %s: %w", f.key, err))
	}
	h, t := dec.Header(), dec.Trailer()
	if h.MinTXID != f.min || h.MaxTXID != f.max {
		return fail(fmt.Errorf("replica: %s holds TXIDs %s to %s, not those of its name", f.key, h.MinTXID, h.MaxTXID))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	ok = true
	return file, h, t, nil
}
