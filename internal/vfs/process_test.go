// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build (linux || darwin) && (amd64 || arm64)

package vfs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The helper process takes the locks named in its environment, says
// "ready" and holds them until its stdin is closed.
const helperEnv = "VFS_TEST_HELPER_DB"

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

func helper(name string) int {
	f, _, rc := Open(name, mainDB)
	if rc != OK {
		fmt.Println("open", rc)
		return 1
	}
	steps := []struct {
		what string
		rc   int
	}{
		{"SHARED", f.Lock(LockShared)},
		{"RESERVED", f.Lock(LockReserved)},
	}
	addr, rc := f.ShmMap(0, shmRegionSize, true)
	steps = append(steps, struct {
		what string
		rc   int
	}{"ShmMap", rc})
	if rc == OK {
		region(addr)[0] = 99
		steps = append(steps, struct {
			what string
			rc   int
		}{"ShmLock", f.ShmLock(0, 1, ShmLock|ShmExclusive)})
	}
	for _, s := range steps {
		if s.rc != OK {
			fmt.Println(s.what, s.rc)
			return 1
		}
	}
	fmt.Println("ready")
	io.Copy(io.Discard, os.Stdin)
	return 0
}

func TestCrossProcess(t *testing.T) {
	name := filepath.Join(t.TempDir(), "proc.db")
	f := openT(t, name, mainDB)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+name)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer stdin.Close()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper said %q, %v", line, err)
	}

	// The helper holds SHARED and RESERVED on the database, has the -shm
	// mapped and holds the WAL write lock.
	lockT(t, "SHARED", f.Lock(LockShared), OK)
	if r, rc := f.CheckReservedLock(); rc != OK || !r {
		t.Errorf("CheckReservedLock = %v, %d, want true", r, rc)
	}
	lockT(t, "RESERVED", f.Lock(LockReserved), Busy)
	lockT(t, "EXCLUSIVE", f.Lock(LockExclusive), Busy)

	addr := shmMapT(t, f, 0, false)
	if addr == 0 || region(addr)[0] != 99 {
		t.Fatal("the -shm of the helper was not kept")
	}
	lockT(t, "shm excl 0", f.ShmLock(0, 1, ShmLock|ShmExclusive), Busy)
	lockT(t, "shm shared 3", f.ShmLock(3, 1, ShmLock|ShmShared), OK)

	// When the helper exits, its locks go with it.
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	lockT(t, "shm excl 0", f.ShmLock(0, 1, ShmLock|ShmExclusive), OK)
	lockT(t, "RESERVED", f.Lock(LockReserved), OK)
	lockT(t, "EXCLUSIVE", f.Lock(LockExclusive), OK)
	f.ShmUnmap(false)
}

// startHelper runs the test binary as a helper of kind env on name and
// waits for it to say "ready". The helper ends when the test does, or
// when stop is called.
func startHelper(t *testing.T, env, name string) (stop func()) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), env+"="+name)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			stdin.Close()
			cmd.Wait()
		}
	}
	t.Cleanup(stop)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("helper said %q, %v", line, err)
	}
	return stop
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
