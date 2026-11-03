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
)

// The helper process takes the locks named in its environment, says
// "ready" and holds them until its stdin is closed.
const helperEnv = "VFS_TEST_HELPER_DB"

func TestMain(m *testing.M) {
	if name := os.Getenv(helperEnv); name != "" {
		os.Exit(helper(name))
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
