---
summary: 'Where the code comes from (SQLite, musl, the generator of slowjs), at which versions, how to move to a new SQLite release, and which Go releases build the driver'
read_when:
  - Moving to a new SQLite release
  - Adding a function to engine/libc
  - Asked which version of SQLite the driver has
  - Moving to a new Go release, or asked which Go releases are supported
---

# Upstream

English · [简体中文](upstream.zh-CN.md)

| Code | From | Version | Kept by |
| --- | --- | --- | --- |
| `engine/sqlite3.c`, `engine/sqlite3.h` | The SQLite amalgamation, https://sqlite.org/download.html | 3.54.1 | `engine/vendor.sh` and `engine/SQLITE.sha256` |
| `engine/libc/string/*.c` except `memcpy.c`, `engine/libc/math/*`, `engine/libc/internal/libm.h` (with `engine/libc/patches/libm.h.patch`), `engine/libc/COPYRIGHT` | musl, https://git.musl-libc.org/cgit/musl | commit `7ada6dd` (2024-03-02) | `engine/libc/vendor.sh` and `engine/libc/MUSL.sha256` |
| `internal/goasm/gen/`, the headers of `engine/libc/`, `engine/libc/string/memcpy.c`, `engine/libc/math.c`, `engine/libc/patches/libm.h.patch`, `scripts/docs-list.sh` | github.com/liustack/slowjs | commit `2445c180` | Copied once, then changed here |

## A new SQLite release

1. Take the `PRODUCT` line of the amalgamation zip from https://sqlite.org/download.html. It gives the version, the path (with the year) and the SHA3-256.
2. Put them in `version`, `year` and `zip_sha3` at the top of `engine/vendor.sh`, and run it. It downloads the zip, checks it, copies `sqlite3.c` and `sqlite3.h` and writes `engine/SQLITE.sha256`.
3. Read the release notes for changed compile options, and check `engine/sqlite.c`. `engine/os.c` uses three internals of SQLite. `sqlite3OomFault`: check that it still sets `mallocFailed` and that a statement still stops on it. `db->nVTrans`: check that it still counts the virtual tables of the open transaction. `db->pParse`, `pToplevel` and `pOuterParse` (goNested): check that a trigger body still links to its statement through `pToplevel`, and a statement prepared while another is prepared still links to it through `pOuterParse`.
4. Run `make gen` and the tests, and commit the C, the manifest and the generated files together.

## A new libc function

SQLite may call a new libc function after an upgrade. Take it from musl at the same commit: add it to the list in `engine/libc/vendor.sh`, run the script with a musl checkout at that commit, and declare the function in the header where C has it.

## Moving to a new Go

The Go assembly of `internal/csqlite` was made with Go 1.27.2 and is checked with Go 1.26 and 1.27. `go.mod` asks for 1.26.6 or later, and a newer Go stops the build with the error of `internal/goversion` (its file `newer.go` has the build line `go1.28 && !sqlite_anygo`), since the assembly was never checked with it. `-tags sqlite_anygo` builds anyway. The assembly depends on things of Go that the Go 1 promise does not cover:

- The stack check at the start of every function that is not NOSPLIT: a compare of the stack pointer with `g.stackguard0` (offset 16 of `g`, in R14 on amd64 and X28 on arm64), and a call of `runtime.morestack_noctxt.abi0` (`internal/goasm/gen/verify_insn.go`, `arm64_verify.go`).
- The room of a chain of NOSPLIT functions that the linker allows, less what a fault pushes for `runtime.sigpanic` (`internal/goasm/gen/nosplit.go`).

To move to a newer Go, with it and with `-tags sqlite_anygo`:

1. Run `make check`, `make check-gen` (its post-link check holds every generated function to the shape above in a binary the new Go linked) and `make test-gen`.
2. If one fails, read the stack check and `runtime.sigpanic` in the source of the new release, and change the generator only for what you read there.
3. When all pass, move the build line of `internal/goversion/newer.go` to the next version and the name in it to the version you checked, and write the new range here and in the README, in both languages. `TestNewerGoStops` checks that the line and the name agree.

The lowest Go is 1.26.6, the `go` line of `go.mod`. Before 1.26.6, when a goroutine's stack was copied, the frame of a function into which Go injected a call (as it injects `sigpanic` into a generated function that faulted) kept a frame pointer into the old stack, which the execution tracer and the block and mutex profiles may follow (`adjustframe` in `runtime/stack.go`). On Go 1.26, `database/sql` has no `RowsColumnScanner`, so `Scan` reads a row through `Next` and allocates more (`scan_go127.go` is built only on Go 1.27). The results are the same.

`scripts/go-versions.sh` runs the tests with the lowest Go and the newest patch release of each minor version up to the one `internal/goversion` stops. The weekly workflow `go-latest` runs it on every target, and runs the tests on the newest stable Go with `sqlite_anygo` to warn of a release that breaks the assembly.
