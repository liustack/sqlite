// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package powerloss builds the files a power loss could leave in a
// directory, for the tests that play one. A Disk follows what a program does
// to the files of the directory, one call for each change, in the order the
// program made them. Crash then gives files a power loss at that moment
// could leave: what was synced is there, and of what was not, any part.
package powerloss

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
)

// op is a write or a truncate of a file that no sync has followed.
type op struct {
	seq   int // its place in the log
	trunc bool
	off   int64 // where it wrote, or the new size
	data  []byte
}

// file is a file as a power loss finds it: what its last sync put on disk,
// and what was done to it since.
type file struct {
	disk    []byte
	pending []op
}

// Disk is the files of one directory, and the ones it held when it was last
// synced.
type Disk struct {
	names  map[string]*file
	synced map[string]*file
}

// Empty is the disk of a directory with no file.
func Empty() *Disk { return &Disk{names: map[string]*file{}, synced: map[string]*file{}} }

// Start is the disk before the program runs: the files of dir, all of them
// synced. The directories in dir are left out.
func Start(dir string) (*Disk, error) {
	d := &Disk{names: map[string]*file{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		d.names[e.Name()] = &file{disk: b}
	}
	d.synced = maps.Clone(d.names)
	return d, nil
}

// NoDirSync says the system has no directory to sync (Windows). A file
// made, deleted or renamed then counts as on disk at once: the tests check
// the syncs of the files there, not what the file system does with names.
const NoDirSync = runtime.GOOS == "windows"

func (d *Disk) named() {
	if NoDirSync {
		d.SyncDir()
	}
}

func (d *Disk) file(name string) (*file, error) {
	f := d.names[name]
	if f == nil {
		return nil, fmt.Errorf("%s is not there", name)
	}
	return f, nil
}

// Create makes the file name, empty, when it is not there.
func (d *Disk) Create(name string) {
	if d.names[name] == nil {
		d.names[name] = &file{}
		d.named()
	}
}

// Write writes data at off of the file name. seq is the place of the write
// among the changes of the directory, for First.
func (d *Disk) Write(seq int, name string, off int64, data []byte) error {
	f, err := d.file(name)
	if err != nil {
		return err
	}
	f.pending = append(f.pending, op{seq: seq, off: off, data: data})
	return nil
}

// Truncate sets the size of the file name. seq is as for Write.
func (d *Disk) Truncate(seq int, name string, size int64) error {
	f, err := d.file(name)
	if err != nil {
		return err
	}
	f.pending = append(f.pending, op{seq: seq, trunc: true, off: size})
	return nil
}

// Sync syncs the file name: what was done to it is on disk.
func (d *Disk) Sync(name string) error {
	f, err := d.file(name)
	if err != nil {
		return err
	}
	for _, o := range f.pending {
		f.disk = o.do(f.disk, len(o.data))
	}
	f.pending = nil
	return nil
}

// SyncDir syncs the directory: its names are on disk.
func (d *Disk) SyncDir() { d.synced = maps.Clone(d.names) }

// Delete deletes the file name.
func (d *Disk) Delete(name string) error {
	if _, err := d.file(name); err != nil {
		return err
	}
	delete(d.names, name)
	d.named()
	return nil
}

// Rename gives the file from the name to, in place of the file that had it.
func (d *Disk) Rename(from, to string) error {
	f, err := d.file(from)
	if err != nil {
		return err
	}
	d.names[to] = f
	delete(d.names, from)
	d.named()
	return nil
}

// do is b after o, or after the first n bytes of its write.
func (o op) do(b []byte, n int) []byte {
	if o.trunc {
		if int64(len(b)) >= o.off {
			return b[:o.off]
		}
		return append(b, make([]byte, o.off-int64(len(b)))...)
	}
	if end := o.off + int64(n); int64(len(b)) < end {
		b = append(b, make([]byte, end-int64(len(b)))...)
	}
	copy(b[o.off:], o.data[:n])
	return b
}

// How a power loss treats what was not synced.
const (
	None  = iota // none of it arrived
	All          // all of it did, as when the process is killed
	First        // what was done before some moment did
	Some         // any part: of each write all its sectors, none, the first ones or any of them
	Kinds
)

// KindNames says each kind in words.
var KindNames = [...]string{"nothing unsynced arrived", "all arrived", "the first part arrived", "some writes arrived"}

// sector is what a disk writes at once. Of a write that a power loss stops,
// some sectors are on disk and the others are as they were.
const sector = 512

// Which sectors of a write arrived.
const (
	sectAll = iota
	sectNone
	sectHead // the first ones
	sectAny  // each or not
)

// some is b after the sectors of o's write that arrived, as how says. With
// junk, the file is as long as the write made it, and holds anything where
// it grew and no data arrived. Without, it holds zeros there, and ends with
// the last sector that arrived.
func (o op) some(b []byte, how int, junk bool, rng *rand.Rand) []byte {
	old, end := int64(len(b)), o.off+int64(len(o.data))
	first := o.off + rng.Int64N(int64(len(o.data))/sector+1)*sector
	for at := o.off; at < end; {
		next := min((at/sector+1)*sector, end)
		got := how == sectAll || how == sectHead && next <= first || how == sectAny && rng.IntN(2) == 0
		if (got || junk) && int64(len(b)) < next {
			b = append(b, make([]byte, next-int64(len(b)))...)
		}
		switch {
		case got:
			copy(b[at:next], o.data[at-o.off:])
		case junk:
			for i := max(at, old); i < next; i++ {
				b[i] = byte(rng.Uint32())
			}
		}
		at = next
	}
	return b
}

// Crash gives the files a power loss now could leave, by name. kind says
// what arrived of what was not synced. With garbage, where a write that
// makes a file longer did not arrive, the file may be longer all the same
// and hold anything there: SQLite assumes a file system may set the size of
// a file before it writes the data.
func (d *Disk) Crash(kind int, garbage bool, rng *rand.Rand) map[string][]byte {
	files := map[string][]byte{}
	all := maps.Clone(d.synced)
	maps.Copy(all, d.names)
	names := slices.Sorted(maps.Keys(all))
	// First: what came before entry cut of the log arrived.
	cut := 0
	if kind == First {
		var seqs []int
		for _, name := range names {
			if f := d.names[name]; f != nil {
				for _, o := range f.pending {
					seqs = append(seqs, o.seq)
				}
			}
		}
		if len(seqs) > 0 {
			cut = seqs[rng.IntN(len(seqs))]
		}
	}
	for _, name := range names {
		// A name that changed since the directory was synced has the file
		// it had then, or the one it has now, or none when one was deleted
		// and another made.
		was, is := d.synced[name], d.names[name]
		f := is
		if was != is && kind != All {
			f = was
			if kind != None {
				f = []*file{was, is, nil}[rng.IntN(3)]
				if f == nil && (was == nil || is == nil) {
					f = []*file{was, is}[rng.IntN(2)]
				}
			}
		}
		if f == nil {
			continue
		}
		b := slices.Clone(f.disk)
		for _, o := range f.pending {
			how := sectAll
			switch kind {
			case None:
				how = sectNone
			case First:
				if o.seq >= cut {
					how = sectNone
				}
			case Some:
				how = []int{sectAll, sectAll, sectNone, sectHead, sectAny}[rng.IntN(5)]
			}
			switch {
			case !o.trunc:
				b = o.some(b, how, garbage && rng.IntN(2) == 0, rng)
			case how != sectNone:
				b = o.do(b, 0)
			}
		}
		files[name] = b
	}
	return files
}
