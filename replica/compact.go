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
	"slices"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/capture"
)

// maxCompactInputs is the most files one compaction merges, so that it
// opens a bounded number of files. What is left is merged next time.
const maxCompactInputs = 256

// compact merges the files of level from that come after the last file of
// level to into one file of level to, and uploads it. It returns whether it
// did: not when there is nothing to merge. It follows the files as a
// restore does, each the one that goes on and reaches furthest, so that a
// file another holds (after an upload whose answer was lost) is passed
// over. Each file is downloaded whole and checked before it is merged, and
// the merged file is checked against them before it is uploaded. Call it
// holding busy.
func (r *Replica) compact(ctx context.Context, from, to int) (bool, error) {
	done := r.last(to)
	end := done
	if done == 0 {
		// The first file of level to starts where level from does.
		first := ltx.TXID(0)
		for _, f := range r.files {
			if f.level == from && (first == 0 || f.min < first) {
				first = f.min
			}
		}
		if first > 0 {
			end = first - 1
		}
	}
	var src []remoteFile
	for len(src) < maxCompactInputs {
		var next *remoteFile
		for i := range r.files {
			f := &r.files[i]
			if f.level != from || f.min > end+1 || f.max <= end {
				continue
			}
			if next == nil || f.max > next.max || f.max == next.max && f.min < next.min {
				next = f
			}
		}
		if next == nil {
			break
		}
		src, end = append(src, *next), next.max
	}
	if len(src) == 0 {
		if r.last(from) > done {
			return false, fmt.Errorf("replica: level %d has no file from TXID %s, which level %d needs next", from, done+1, to)
		}
		return false, nil
	}

	dir := r.c.CompactDir()
	if err := makeCompactDir(dir); err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	inputs := make([]io.Reader, len(src))
	var first, last ltx.Header
	var post ltx.Checksum
	for i, f := range src {
		file, h, t, err := r.download(ctx, f, dir)
		if err != nil {
			return false, err
		}
		defer file.Close()
		inputs[i] = bufio.NewReaderSize(file, 64<<10)
		if i == 0 {
			first = h
		}
		last, post = h, t.PostApplyChecksum
	}

	name := filepath.Join(dir, ltx.FormatFilename(first.MinTXID, last.MaxTXID))
	if err := merge(ctx, name, inputs, first, last, post); err != nil {
		return false, fmt.Errorf("replica: compacting into level %d: %w", to, err)
	}
	if err := r.putFile(ctx, to, name); err != nil {
		return false, err
	}
	return true, nil
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
	h, t, err := capture.Verify(file)
	if err != nil {
		return fail(fmt.Errorf("replica: %s: %w", f.key, err))
	}
	if h.MinTXID != f.min || h.MaxTXID != f.max {
		return fail(fmt.Errorf("replica: %s holds TXIDs %s to %s, not those of its name", f.key, h.MinTXID, h.MaxTXID))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	ok = true
	return file, h, t, nil
}

// makeCompactDir makes dir, the folder a compaction downloads and writes
// its files in, empty. Compactions run one at a time. The files are copies
// of the database: only the user reads them.
func makeCompactDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Mkdir(dir, 0o700)
}

// merge merges the LTX files in inputs, in TXID order, into the file name,
// and checks it against them: it must start where first does, end where
// last does, with the checksum post and the time of last.
func merge(ctx context.Context, name string, inputs []io.Reader, first, last ltx.Header, post ltx.Checksum) error {
	inputs = slices.Clone(inputs)
	for i, in := range inputs {
		inputs[i] = ctxReader{ctx, in}
	}
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
		return fmt.Errorf("merging TXIDs %s to %s: %w", first.MinTXID, last.MaxTXID, err)
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
	h, tr, err := capture.Verify(ctxReader{ctx, out})
	if err != nil {
		return fmt.Errorf("the file merged from TXIDs %s to %s: %w", first.MinTXID, last.MaxTXID, err)
	}
	if h.MinTXID != first.MinTXID || h.MaxTXID != last.MaxTXID || h.PreApplyChecksum != first.PreApplyChecksum ||
		tr.PostApplyChecksum != post || h.Timestamp != last.Timestamp {
		return fmt.Errorf("the file merged from TXIDs %s to %s does not match the files merged", first.MinTXID, last.MaxTXID)
	}
	return nil
}

// ctxReader is r, failing once ctx is done: ltx's Compactor does not look
// at its context, and a merge of many large files would go on after it.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
