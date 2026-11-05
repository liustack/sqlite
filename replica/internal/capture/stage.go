// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"bufio"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/superfly/ltx"
)

// The staging folder holds the LTX files captured and not yet uploaded, as
// Litestream lays a replica out: <db>-replica/ltx/0/<min>-<max>.ltx, so that
// `litestream restore file://<db>-replica` also reads it.

// StageDir is the folder of the level 0 files of the database path.
func StageDir(path string) string { return filepath.Join(path+"-replica", "ltx", "0") }

// lastStaged is the header and post-apply checksum of the staged file with
// the highest TXID, ok false when there is none. Files left from a write
// that did not finish are removed. The file is read whole, so that its
// checksums are checked.
func lastStaged(dir string) (hdr ltx.Header, post ltx.Checksum, ok bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return hdr, 0, false, err
	}
	var last string
	var max ltx.TXID
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return hdr, 0, false, err
			}
			continue
		}
		min, mx, err := ltx.ParseFilename(name)
		if err != nil || min != mx {
			continue
		}
		if mx > max {
			max, last = mx, name
		}
	}
	if last == "" {
		return hdr, 0, false, nil
	}
	f, err := os.Open(filepath.Join(dir, last))
	if err != nil {
		return hdr, 0, false, err
	}
	defer f.Close()
	dec := ltx.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	if err := dec.Verify(); err != nil {
		return hdr, 0, false, fmt.Errorf("the staged file %s: %w", last, err)
	}
	hdr = dec.Header()
	if hdr.MinTXID != max || hdr.MaxTXID != max {
		return hdr, 0, false, fmt.Errorf("the staged file %s holds TXIDs %s to %s", last, hdr.MinTXID, hdr.MaxTXID)
	}
	return hdr, dec.Trailer().PostApplyChecksum, true, nil
}

// walPos is where in the WAL a staged file ends: generation salt, frame end.
func walPos(h ltx.Header) (salt [8]byte, end uint32) {
	binary.BigEndian.PutUint32(salt[0:], h.WALSalt1)
	binary.BigEndian.PutUint32(salt[4:], h.WALSalt2)
	return salt, uint32((h.WALOffset - walHeaderSize + h.WALSize) / frameSize(int(h.PageSize)))
}

// setWALPos records in h that its file holds frames from+1 to to of the WAL
// generation salt (none when from == to).
func setWALPos(h *ltx.Header, salt [8]byte, from, to uint32) {
	fs := frameSize(int(h.PageSize))
	h.WALSalt1 = binary.BigEndian.Uint32(salt[0:])
	h.WALSalt2 = binary.BigEndian.Uint32(salt[4:])
	h.WALOffset = walHeaderSize + int64(from)*fs
	h.WALSize = int64(to-from) * fs
}

// stageFile writes the LTX file of header h into dir: pages calls put for
// each page in order and returns the post-apply checksum. The file is
// written to a temporary name, synced, given the time of h (which Litestream
// reads as the time of the file), renamed and its folder synced. placed is
// true once it has its name, even when the folder then fails to sync: the
// TXID is taken, and another file must not get it.
func stageFile(dir string, mode os.FileMode, h ltx.Header, pages func(put func(pgno uint32, data []byte) error) (ltx.Checksum, error)) (placed bool, err error) {
	name := filepath.Join(dir, ltx.FormatFilename(h.MinTXID, h.MaxTXID))
	tmp := name + ".tmp"
	// A file of that name left by a write that failed is written over.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return false, err
	}
	err = func() error {
		w := bufio.NewWriterSize(f, 1<<20)
		enc, err := ltx.NewEncoder(w)
		if err != nil {
			return err
		}
		if err := enc.EncodeHeader(h); err != nil {
			return err
		}
		post, err := pages(func(pgno uint32, data []byte) error {
			return enc.EncodePage(ltx.PageHeader{Pgno: pgno}, data)
		})
		if err != nil {
			return err
		}
		enc.SetPostApplyChecksum(post)
		if err := enc.Close(); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
		return f.Sync()
	}()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		t := time.UnixMilli(h.Timestamp)
		err = os.Chtimes(tmp, t, t)
	}
	if err == nil {
		err = rename(tmp, name)
	}
	if err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, syncDir(dir)
}

// syncDir makes the names in dir durable. Tests make it fail.
var syncDir = syncDirFS

// pageSummer computes ltx.ChecksumPage without allocating.
type pageSummer struct {
	h   hash.Hash64
	num [4]byte
}

func newPageSummer() *pageSummer { return &pageSummer{h: crc64.New(crc64.MakeTable(crc64.ISO))} }

func (s *pageSummer) sum(pgno uint32, data []byte) ltx.Checksum {
	s.h.Reset()
	binary.BigEndian.PutUint32(s.num[:], pgno)
	s.h.Write(s.num[:])
	s.h.Write(data)
	return ltx.ChecksumFlag | ltx.Checksum(s.h.Sum64())
}

// readFull reads len(p) bytes at off, an error when the file is shorter.
func readFull(r io.ReaderAt, p []byte, off int64) error {
	_, err := r.ReadAt(p, off)
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// stagedFiles lists the staged files of dir in TXID order, from their
// headers and trailers.
func stagedFiles(dir string) ([]StagedFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []StagedFile
	for _, e := range entries {
		min, max, err := ltx.ParseFilename(e.Name())
		if err != nil || min != max {
			continue
		}
		f, err := readStaged(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if f.TXID != max {
			return nil, fmt.Errorf("the staged file %s holds TXID %s", e.Name(), f.TXID)
		}
		files = append(files, f)
	}
	slices.SortFunc(files, func(a, b StagedFile) int { return cmp.Compare(a.TXID, b.TXID) })
	return files, nil
}

// readStaged reads the header and trailer of the staged file name.
func readStaged(name string) (StagedFile, error) {
	f, err := os.Open(name)
	if err != nil {
		return StagedFile{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return StagedFile{}, err
	}
	var hb [ltx.HeaderSize]byte
	var tb [ltx.TrailerSize]byte
	if st.Size() < ltx.HeaderSize+ltx.TrailerSize {
		return StagedFile{}, fmt.Errorf("the staged file %s is too short", name)
	}
	if err := readFull(f, hb[:], 0); err != nil {
		return StagedFile{}, err
	}
	if err := readFull(f, tb[:], st.Size()-ltx.TrailerSize); err != nil {
		return StagedFile{}, err
	}
	var h ltx.Header
	var t ltx.Trailer
	if err := h.UnmarshalBinary(hb[:]); err != nil {
		return StagedFile{}, fmt.Errorf("the staged file %s: %w", name, err)
	}
	if err := t.UnmarshalBinary(tb[:]); err != nil {
		return StagedFile{}, fmt.Errorf("the staged file %s: %w", name, err)
	}
	if h.MinTXID != h.MaxTXID {
		return StagedFile{}, fmt.Errorf("the staged file %s holds TXIDs %s to %s", name, h.MinTXID, h.MaxTXID)
	}
	return StagedFile{TXID: h.MaxTXID, Pre: h.PreApplyChecksum, Post: t.PostApplyChecksum}, nil
}

// dropStaged removes the staged files of dir whose TXID drop says, and the
// temporary ones.
func dropStaged(dir string, drop func(ltx.TXID) bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if _, max, err := ltx.ParseFilename(name); err == nil && !drop(max) || err != nil && !strings.HasSuffix(name, ".tmp") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// Stage moves the LTX file from, which holds TXID txid and leaves the
// database path as it is now, into the staging folder of path, so that
// Open goes on from it. The folder must hold no LTX file. The folders it
// makes are synced, up to the folder of path.
func Stage(path, from string, txid ltx.TXID) error {
	dir := StageDir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, _, err := ltx.ParseFilename(e.Name()); err == nil {
			return fmt.Errorf("capture: %s holds staged files already", dir)
		}
	}
	if err := rename(from, filepath.Join(dir, ltx.FormatFilename(txid, txid))); err != nil {
		return err
	}
	for d := dir; ; d = filepath.Dir(d) {
		if err := syncDir(d); err != nil {
			return err
		}
		if d == filepath.Dir(path) {
			return nil
		}
	}
}
