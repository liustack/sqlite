// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const (
	mainDB    = OpenReadWrite | OpenCreate | OpenMainDB
	mainJrnl  = OpenReadWrite | OpenCreate | OpenMainJournal
	walFile   = OpenReadWrite | OpenCreate | OpenWAL
	tempJrnl  = OpenReadWrite | OpenCreate | OpenExclusive | OpenDeleteOnClose | OpenTempJournal
	readOnlyD = OpenReadOnly | OpenMainDB
)

// openT opens name and fails the test unless it opens.
func openT(t *testing.T, name string, flags int) *File {
	t.Helper()
	f, _, rc := Open(name, flags)
	if rc != OK {
		t.Fatalf("Open(%q, %#x) = %d", name, flags, rc)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func isRoot() bool { return os.Geteuid() == 0 }

// umask returns the process umask without changing it.
func umask() os.FileMode {
	m := syscall.Umask(0)
	syscall.Umask(m)
	return os.FileMode(m)
}

func modeOf(t *testing.T, name string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestOpenCreate(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.db")
	f, out, rc := Open(name, mainDB)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	if out != mainDB {
		t.Errorf("outFlags = %#x, want %#x", out, mainDB)
	}
	if got, want := modeOf(t, name), 0644&^umask(); got != want {
		t.Errorf("mode = %v, want %v", got, want)
	}
	if rc := f.WriteAt([]byte("hello"), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	if size, rc := f.Size(); rc != OK || size != 5 {
		t.Errorf("Size = %d, %d", size, rc)
	}
}

func TestOpenMissing(t *testing.T) {
	name := filepath.Join(t.TempDir(), "missing.db")
	if _, _, rc := Open(name, OpenReadWrite|OpenMainDB); rc != CantOpen {
		t.Errorf("Open read-write without create = %d, want CantOpen", rc)
	}
	if _, _, rc := Open(name, readOnlyD); rc != CantOpen {
		t.Errorf("Open read-only = %d, want CantOpen", rc)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("file was created: %v", err)
	}
}

func TestOpenDirectory(t *testing.T) {
	if _, _, rc := Open(t.TempDir(), mainDB); rc&0xff != CantOpen {
		t.Errorf("Open of a directory = %d, want CantOpen", rc)
	}
}

func TestOpenReadOnlyFallback(t *testing.T) {
	if isRoot() {
		t.Skip("root can write a read-only file")
	}
	name := filepath.Join(t.TempDir(), "ro.db")
	if err := os.WriteFile(name, []byte("data"), 0444); err != nil {
		t.Fatal(err)
	}
	f, out, rc := Open(name, mainDB)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	defer f.Close()
	if out&OpenReadOnly == 0 || out&(OpenReadWrite|OpenCreate) != 0 {
		t.Errorf("outFlags = %#x, want read-only", out)
	}
	if out&OpenMainDB == 0 {
		t.Errorf("outFlags = %#x lost the file type", out)
	}
	buf := make([]byte, 4)
	if rc := f.ReadAt(buf, 0); rc != OK || string(buf) != "data" {
		t.Errorf("ReadAt = %d, %q", rc, buf)
	}
	if rc := f.WriteAt([]byte("x"), 0); rc != IOErrWrite {
		t.Errorf("WriteAt on read-only = %d, want IOErrWrite", rc)
	}
	// A write lock cannot be taken on a read-only descriptor.
	if rc := f.Lock(LockShared); rc != OK {
		t.Fatalf("Lock(SHARED) = %d", rc)
	}
	if rc := f.Lock(LockReserved); rc == OK {
		t.Errorf("Lock(RESERVED) on read-only = OK")
	}
}

func TestOpenReadOnlyDirectory(t *testing.T) {
	if isRoot() {
		t.Skip("root can write a read-only directory")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "test.db")
	if err := os.WriteFile(db, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)

	// A new journal cannot be made in a directory that cannot be written.
	if _, _, rc := Open(db+"-journal", mainJrnl); rc != ReadOnlyDirectory {
		t.Errorf("Open journal = %d, want ReadOnlyDirectory", rc)
	}
	// The database itself still opens read-write.
	f := openT(t, db, mainDB)
	if rc := f.WriteAt([]byte("x"), 0); rc != OK {
		t.Errorf("WriteAt = %d", rc)
	}
}

func TestOpenExclusive(t *testing.T) {
	name := filepath.Join(t.TempDir(), "excl")
	flags := OpenReadWrite | OpenCreate | OpenExclusive | OpenTempDB
	openT(t, name, flags)
	if _, _, rc := Open(name, flags); rc != CantOpen {
		t.Errorf("second exclusive Open = %d, want CantOpen", rc)
	}
}

func TestOpenNoFollow(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	link := filepath.Join(dir, "link.db")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// As os_unix, the last part of the name is never a symbolic link.
	// FullPathname resolves links before SQLite opens a file.
	if _, _, rc := Open(link, mainDB); rc != CantOpen {
		t.Errorf("Open of a symbolic link = %d, want CantOpen", rc)
	}
}

func TestJournalTakesDatabaseMode(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "test.db")
	openT(t, db, mainDB)
	if err := os.Chmod(db, 0600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		suffix string
		flags  int
	}{
		{"-journal", mainJrnl},
		{"-wal", walFile},
	} {
		openT(t, db+c.suffix, c.flags)
		if got := modeOf(t, db+c.suffix); got != 0600 {
			t.Errorf("mode of %s = %v, want 0600", c.suffix, got)
		}
	}
	// The database must exist to give its mode.
	if _, _, rc := Open(filepath.Join(dir, "none.db-wal"), walFile); rc != IOErrFstat {
		t.Errorf("Open WAL without database = %d, want IOErrFstat", rc)
	}
}

func TestTempFile(t *testing.T) {
	f := openT(t, "", tempJrnl)
	data := []byte("temporary")
	if rc := f.WriteAt(data, 100); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	buf := make([]byte, len(data))
	if rc := f.ReadAt(buf, 100); rc != OK || !bytes.Equal(buf, data) {
		t.Fatalf("ReadAt = %d, %q", rc, buf)
	}
	// The file has no name left: it goes away when it is closed.
	var st syscall.Stat_t
	if err := syscall.Fstat(f.fd, &st); err != nil {
		t.Fatal(err)
	}
	if st.Nlink != 0 {
		t.Errorf("temp file has %d links, want 0", st.Nlink)
	}
	if rc := f.Close(); rc != OK {
		t.Errorf("Close = %d", rc)
	}
}

func TestTempFileDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SQLITE_TMPDIR", dir)
	f := openT(t, "", tempJrnl)
	if rc := f.WriteAt([]byte("x"), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	// Nothing is left behind in the directory, open or closed.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Errorf("temp dir holds %v", ents)
	}
}

func TestDeleteOnClose(t *testing.T) {
	name := filepath.Join(t.TempDir(), "doc")
	f, _, rc := Open(name, OpenReadWrite|OpenCreate|OpenDeleteOnClose|OpenSubjournal)
	if rc != OK {
		t.Fatalf("Open = %d", rc)
	}
	if rc := f.WriteAt([]byte("x"), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	if rc := f.Close(); rc != OK {
		t.Fatalf("Close = %d", rc)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("file still there after Close: %v", err)
	}
}

func TestShortRead(t *testing.T) {
	f := openT(t, filepath.Join(t.TempDir(), "short.db"), mainDB)
	if rc := f.WriteAt([]byte("0123456789"), 0); rc != OK {
		t.Fatalf("WriteAt = %d", rc)
	}
	buf := bytes.Repeat([]byte{0xff}, 20)
	if rc := f.ReadAt(buf, 4); rc != IOErrShortRead {
		t.Errorf("ReadAt = %d, want IOErrShortRead", rc)
	}
	want := append([]byte("456789"), make([]byte, 14)...)
	if !bytes.Equal(buf, want) {
		t.Errorf("buf = %q, want %q", buf, want)
	}

	buf = bytes.Repeat([]byte{0xff}, 8)
	if rc := f.ReadAt(buf, 1000); rc != IOErrShortRead {
		t.Errorf("ReadAt past the end = %d, want IOErrShortRead", rc)
	}
	if !bytes.Equal(buf, make([]byte, 8)) {
		t.Errorf("buf = %q, want zeros", buf)
	}
}

func TestTruncateAndSize(t *testing.T) {
	f := openT(t, filepath.Join(t.TempDir(), "trunc.db"), mainDB)
	if rc := f.WriteAt(make([]byte, 8192), 0); rc != OK {
		t.Fatal(rc)
	}
	if rc := f.Truncate(4096); rc != OK {
		t.Fatalf("Truncate = %d", rc)
	}
	if size, rc := f.Size(); rc != OK || size != 4096 {
		t.Errorf("Size = %d, %d, want 4096", size, rc)
	}
	// With a chunk size, Truncate rounds up to whole chunks.
	f.SetChunkSize(3000)
	if rc := f.Truncate(1000); rc != OK {
		t.Fatalf("Truncate = %d", rc)
	}
	if size, _ := f.Size(); size != 3000 {
		t.Errorf("Size = %d, want 3000", size)
	}
}

func TestSizeHint(t *testing.T) {
	f := openT(t, filepath.Join(t.TempDir(), "hint.db"), mainDB)
	// Without a chunk size the hint does nothing, as os_unix.
	if rc := f.SizeHint(10000); rc != OK {
		t.Fatalf("SizeHint = %d", rc)
	}
	if size, _ := f.Size(); size != 0 {
		t.Errorf("Size = %d, want 0", size)
	}
	f.SetChunkSize(4096)
	if rc := f.SizeHint(10000); rc != OK {
		t.Fatalf("SizeHint = %d", rc)
	}
	if size, _ := f.Size(); size != 12288 {
		t.Errorf("Size = %d, want 12288", size)
	}
	// A hint never makes the file smaller.
	if rc := f.SizeHint(100); rc != OK {
		t.Fatalf("SizeHint = %d", rc)
	}
	if size, _ := f.Size(); size != 12288 {
		t.Errorf("Size = %d, want 12288", size)
	}
}

func TestSync(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "sync.db")
	f := openT(t, db, mainDB)
	if f.dirSync {
		t.Error("main database marked for directory sync")
	}
	for _, flags := range []int{SyncNormal, SyncFull, SyncNormal | SyncDataOnly, SyncFull | SyncDataOnly} {
		if rc := f.Sync(flags); rc != OK {
			t.Errorf("Sync(%#x) = %d", flags, rc)
		}
	}
	j := openT(t, db+"-journal", mainJrnl)
	if !j.dirSync {
		t.Error("new journal not marked for directory sync")
	}
	if rc := j.Sync(SyncNormal); rc != OK {
		t.Errorf("Sync = %d", rc)
	}
	if j.dirSync {
		t.Error("directory sync not done on the first Sync")
	}
}

func TestHasMoved(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "moved.db")
	f := openT(t, name, mainDB)
	if f.HasMoved() {
		t.Error("HasMoved before rename")
	}
	if err := os.Rename(name, name+"2"); err != nil {
		t.Fatal(err)
	}
	if !f.HasMoved() {
		t.Error("HasMoved after rename = false")
	}
	// Another file at the old name is not this file.
	if err := os.WriteFile(name, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if !f.HasMoved() {
		t.Error("HasMoved with a new file at the name = false")
	}
}

func TestDeviceCharacteristics(t *testing.T) {
	f := openT(t, filepath.Join(t.TempDir(), "dc.db"), mainDB)
	if got := f.SectorSize(); got != 4096 {
		t.Errorf("SectorSize = %d", got)
	}
	if got := f.DeviceCharacteristics(); got != IOCapPowersafeOverwrite|IOCapSubpageRead {
		t.Errorf("DeviceCharacteristics = %#x", got)
	}
}

func TestAccess(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	full := filepath.Join(dir, "full")
	ro := filepath.Join(dir, "ro")
	os.WriteFile(empty, nil, 0644)
	os.WriteFile(full, []byte("x"), 0644)
	os.WriteFile(ro, []byte("x"), 0444)

	for _, c := range []struct {
		name  string
		flags int
		want  bool
	}{
		{filepath.Join(dir, "missing"), AccessExists, false},
		{empty, AccessExists, false}, // an empty file counts as missing, as os_unix
		{full, AccessExists, true},
		{dir, AccessExists, true},
		{full, AccessReadWrite, true},
		{ro, AccessReadWrite, isRoot()},
		{ro, AccessRead, true},
		{filepath.Join(dir, "missing"), AccessReadWrite, false},
	} {
		got, rc := Access(c.name, c.flags)
		if rc != OK || got != c.want {
			t.Errorf("Access(%s, %d) = %v, %d, want %v", filepath.Base(c.name), c.flags, got, rc, c.want)
		}
	}
}

func TestDelete(t *testing.T) {
	name := filepath.Join(t.TempDir(), "del")
	os.WriteFile(name, []byte("x"), 0644)
	if rc := Delete(name, true); rc != OK {
		t.Fatalf("Delete = %d", rc)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
	if rc := Delete(name, false); rc != IOErrDeleteNoent {
		t.Errorf("Delete of a missing file = %d, want IOErrDeleteNoent", rc)
	}
}

func TestFullPathname(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := os.MkdirAll("a/b", 0755); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ in, want string }{
		{"test.db", dir + "/test.db"},
		{"./a/./b/../test.db", dir + "/a/test.db"},
		{"a//b///test.db", dir + "/a/b/test.db"},
		{"/x/y/../z.db", "/x/z.db"},
		{"/../../z.db", "/z.db"},
	} {
		got, rc := FullPathname(c.in)
		if rc != OK || got != c.want {
			t.Errorf("FullPathname(%q) = %q, %d, want %q", c.in, got, rc, c.want)
		}
	}

	// Symbolic links are followed, and the result says so.
	if err := os.Symlink("a/b", "link"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir+"/a", "abs"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ in, want string }{
		{"link/test.db", dir + "/a/b/test.db"},
		{"abs/b/test.db", dir + "/a/b/test.db"},
		{"link/../test.db", dir + "/a/test.db"},
	} {
		got, rc := FullPathname(c.in)
		if rc != OKSymlink || got != c.want {
			t.Errorf("FullPathname(%q) = %q, %d, want %q, OKSymlink", c.in, got, rc, c.want)
		}
	}

	// A loop of links fails.
	os.Symlink("loop2", "loop1")
	os.Symlink("loop1", "loop2")
	if _, rc := FullPathname("loop1"); rc != CantOpen {
		t.Errorf("FullPathname of a link loop = %d, want CantOpen", rc)
	}

	// The root alone and a name too long for MaxPathname fail.
	if _, rc := FullPathname("/"); rc != CantOpen {
		t.Errorf("FullPathname(/) = %d, want CantOpen", rc)
	}
	if _, rc := FullPathname("/" + strings.Repeat("x", MaxPathname)); rc != CantOpen {
		t.Errorf("FullPathname of a long name = %d, want CantOpen", rc)
	}
}
