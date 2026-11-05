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
	"io"
)

// The WAL file format (https://sqlite.org/fileformat2.html#walformat): a
// 32-byte header, then frames of a 24-byte header and a page.
const (
	walHeaderSize   = 32
	frameHeaderSize = 24
	walMagic        = 0x377f0682 // | 1 when the checksums use big-endian words
	walVersion      = 3007000
)

// walHeader is the header of a WAL file.
type walHeader struct {
	bigEndian bool // the checksums are computed on big-endian words
	pageSize  int
	salt      [8]byte
	cksum     [2]uint32 // the checksum of the header, where the frames' start
}

// errNoWAL is the error of a WAL file too short to hold a header.
var errNoWAL = errors.New("no WAL header")

// readWALHeader reads and checks the header of the WAL r.
func readWALHeader(r io.ReaderAt) (walHeader, error) {
	var b [walHeaderSize]byte
	if _, err := r.ReadAt(b[:], 0); err != nil {
		if errors.Is(err, io.EOF) {
			return walHeader{}, errNoWAL
		}
		return walHeader{}, err
	}
	be := binary.BigEndian
	magic := be.Uint32(b[0:])
	if magic&^1 != walMagic || be.Uint32(b[4:]) != walVersion {
		return walHeader{}, errors.New("not a WAL header")
	}
	h := walHeader{bigEndian: magic&1 != 0, pageSize: int(be.Uint32(b[8:]))}
	if h.pageSize == 1 {
		h.pageSize = 65536
	}
	if h.pageSize < 512 || h.pageSize > 65536 || h.pageSize&(h.pageSize-1) != 0 {
		return walHeader{}, fmt.Errorf("a WAL header with a page size of %d", h.pageSize)
	}
	copy(h.salt[:], b[16:24])
	h.cksum = walChecksum(h.bigEndian, [2]uint32{}, b[:24])
	if h.cksum != [2]uint32{be.Uint32(b[24:]), be.Uint32(b[28:])} {
		return walHeader{}, errors.New("the checksum of the WAL header is wrong")
	}
	return h, nil
}

// walChecksum is SQLite's walChecksumBytes: the checksum s carried over the
// 32-bit words of b, whose length is a multiple of 8.
func walChecksum(bigEndian bool, s [2]uint32, b []byte) [2]uint32 {
	var order binary.ByteOrder = binary.LittleEndian
	if bigEndian {
		order = binary.BigEndian
	}
	s1, s2 := s[0], s[1]
	for i := 0; i+8 <= len(b); i += 8 {
		s1 += order.Uint32(b[i:]) + s2
		s2 += order.Uint32(b[i+4:]) + s1
	}
	return [2]uint32{s1, s2}
}

// frameSize is the size of a frame of a WAL of pages of pageSize.
func frameSize(pageSize int) int64 { return frameHeaderSize + int64(pageSize) }

// frameOffset is where frame n (from 1) starts.
func frameOffset(n uint32, pageSize int) int64 {
	return walHeaderSize + int64(n-1)*frameSize(pageSize)
}

// A frame of the WAL.
type frame struct {
	n      uint32    // its index, from 1
	pgno   uint32    // the page it holds
	commit uint32    // the size of the database in pages after it, for the last frame of a transaction, else 0
	prev   [2]uint32 // the checksum it carries on from
	data   []byte    // the page, valid until the next frame
}

// errFrame is the error of a frame that is not one of the WAL: its salts or
// its checksum do not match.
var errFrame = errors.New("a frame of the WAL does not match its header or the frames before it")

// scanFrames reads frames from+1 to to of the WAL r, whose header is h, in
// order, and calls fn for each. cksum is the checksum of frame from, or
// h.cksum for from 0. Each frame is checked as SQLite's recovery checks it:
// its salts are the header's and its checksum carries on from the frame
// before. It returns the checksum of frame to.
func scanFrames(r io.ReaderAt, h walHeader, from, to uint32, cksum [2]uint32, fn func(*frame) error) ([2]uint32, error) {
	if to <= from {
		return cksum, nil
	}
	fs := frameSize(h.pageSize)
	off := walHeaderSize + int64(from)*fs
	br := bufio.NewReaderSize(io.NewSectionReader(r, off, int64(to-from)*fs), 1<<20)
	buf := make([]byte, fs)
	f := &frame{data: buf[frameHeaderSize:]}
	for n := from + 1; n <= to; n++ {
		if _, err := io.ReadFull(br, buf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return cksum, fmt.Errorf("the WAL ends before frame %d: %w", n, errFrame)
			}
			return cksum, err
		}
		if err := checkFrame(h, cksum, buf); err != nil {
			return cksum, fmt.Errorf("frame %d: %w", n, err)
		}
		be := binary.BigEndian
		f.n, f.pgno, f.commit, f.prev = n, be.Uint32(buf[0:]), be.Uint32(buf[4:]), cksum
		cksum = [2]uint32{be.Uint32(buf[16:]), be.Uint32(buf[20:])}
		if err := fn(f); err != nil {
			return cksum, err
		}
	}
	return cksum, nil
}

// checkFrame checks the frame b (header and page) of the WAL whose header is
// h, the checksum of the frame before being prev.
func checkFrame(h walHeader, prev [2]uint32, b []byte) error {
	be := binary.BigEndian
	if be.Uint32(b[0:]) == 0 || [8]byte(b[8:16]) != h.salt {
		return errFrame
	}
	s := walChecksum(h.bigEndian, prev, b[:8])
	s = walChecksum(h.bigEndian, s, b[frameHeaderSize:])
	if s != [2]uint32{be.Uint32(b[16:]), be.Uint32(b[20:])} {
		return errFrame
	}
	return nil
}

// readFrame reads frame n of the WAL r, whose header is h, into buf (a
// frame long) and checks it again, prev being the checksum of the frame
// before it, as scanFrames found. It returns the page.
func readFrame(r io.ReaderAt, h walHeader, n uint32, prev [2]uint32, buf []byte) ([]byte, error) {
	if _, err := r.ReadAt(buf, frameOffset(n, h.pageSize)); err != nil {
		return nil, err
	}
	if err := checkFrame(h, prev, buf); err != nil {
		return nil, fmt.Errorf("frame %d: %w", n, err)
	}
	return buf[frameHeaderSize:], nil
}
