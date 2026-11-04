// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The POSIX helper holds a POSIX read lock on the DMS byte of the -shm, as
// SQLite built from C (os_unix) does while it has the -shm open.
const posixHelperEnv = "VFS_TEST_POSIX_DMS_DB"

// The stdio helper closes its stdin, stdout and stderr and opens files.
const stdioHelperEnv = "VFS_TEST_STDIO_DB"

func TestMain(m *testing.M) {
	fcntlHook = func(typ int16, start, n int64) error {
		fcntlTotal.Add(1)
		if fault := fcntlFault.Load(); fault != nil {
			return (*fault)(typ, start, n)
		}
		return nil
	}
	if name := os.Getenv(helperEnv); name != "" {
		os.Exit(helper(name))
	}
	if name := os.Getenv(posixHelperEnv); name != "" {
		os.Exit(posixHelper(name))
	}
	if name := os.Getenv(stdioHelperEnv); name != "" {
		os.Exit(stdioHelper(name))
	}
	os.Exit(m.Run())
}

func posixHelper(name string) int {
	fd, err := unix.Open(name+"-shm", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0644)
	if err != nil {
		fmt.Println("open", err)
		return 1
	}
	fl := unix.Flock_t{Type: unix.F_RDLCK, Whence: 0, Start: shmDMS, Len: 1}
	if err := unix.FcntlFlock(uintptr(fd), unix.F_SETLK, &fl); err != nil {
		fmt.Println("lock", err)
		return 1
	}
	fmt.Println("ready")
	io.Copy(io.Discard, os.Stdin)
	return 0
}

// TestShmOtherProcessPOSIX: a process using POSIX locks, as os_unix, has
// the -shm open. Read slots are then released at once.
func TestShmOtherProcessPOSIX(t *testing.T) {
	idleDelay(t, time.Hour)
	name := filepath.Join(t.TempDir(), "posix.db")
	a := openT(t, name, mainDB)
	startHelper(t, posixHelperEnv, name)
	shmMapT(t, a, 0, true)
	t.Cleanup(func() { a.ShmUnmap(false) })
	lockT(t, "a shared 3", a.ShmLock(3, 1, ShmLock|ShmShared), OK)
	lockT(t, "a unlock 3", a.ShmLock(3, 1, ShmUnlock|ShmShared), OK)
	if got := heldBy(t, name, 3); got != unix.F_UNLCK {
		t.Errorf("slot 3 after the last reader left = %d, want F_UNLCK", got)
	}
}

// stdioHelper closes descriptors 0, 1 and 2, as a daemon may, then opens
// files of every kind. None of them may take one of those numbers, and the
// numbers are filled with /dev/null. The exit code tells what went wrong.
func stdioHelper(name string) int {
	for fd := range 3 {
		unix.Close(fd)
	}
	f, _, rc := Open(name, mainDB)
	if rc != OK {
		return 10
	}
	if _, rc := f.ShmMap(0, shmRegionSize, true); rc != OK {
		return 11
	}
	j, _, rc := Open(name+"-journal", mainJrnl)
	if rc != OK {
		return 12
	}
	tmp, _, rc := Open("", tempJrnl)
	if rc != OK {
		return 13
	}
	ro, _, rc := Open(name, readOnlyD)
	if rc != OK {
		return 14
	}
	for i, fd := range []int{f.fd, f.inode.fd, f.shm.node.fd, j.fd, tmp.fd, ro.fd} {
		if fd <= 2 {
			return 20 + i
		}
	}
	var null, st unix.Stat_t
	if err := unix.Stat("/dev/null", &null); err != nil {
		return 30
	}
	for fd := range 3 {
		if err := unix.Fstat(fd, &st); err != nil || st.Rdev != null.Rdev || st.Ino != null.Ino {
			return 31 + fd
		}
	}
	return 0
}

func TestOpenNotOnStdio(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), stdioHelperEnv+"="+filepath.Join(t.TempDir(), "stdio.db"))
	if err := cmd.Run(); err != nil {
		// See stdioHelper for the exit codes.
		t.Fatalf("helper: %v", err)
	}
}
