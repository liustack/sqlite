// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

package capture

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/superfly/ltx"
)

// The staging folder holds the LTX files captured and not yet uploaded, as
// Litestream lays a replica out: <db>-replica/ltx/0/<min>-<max>.ltx, so that
// `litestream restore file://<db>-replica` also reads it.

// stageDir is the folder of the level 0 files of the database path.
func stageDir(path string) string { return filepath.Join(path+"-replica", "ltx", "0") }

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
// written to a temporary name, synced, renamed and its folder synced, then
// given the time of h, which Litestream reads as the time of the file.
func stageFile(dir string, mode os.FileMode, h ltx.Header, pages func(put func(pgno uint32, data []byte) error) (ltx.Checksum, error)) error {
	name := filepath.Join(dir, ltx.FormatFilename(h.MinTXID, h.MaxTXID))
	tmp := name + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
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
		err = os.Rename(tmp, name)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	t := time.UnixMilli(h.Timestamp)
	return os.Chtimes(name, t, t)
}

// syncDir makes the names in dir durable. Windows has no way to, and needs
// none: NTFS logs its metadata.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

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
