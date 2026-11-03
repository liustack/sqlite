---
summary: 'How the driver is built: the layers from the SQLite C to database/sql, how the C becomes Go assembly, what the Go host gives SQLite, group commit, and the routes that were left out and why'
read_when:
  - Starting work in this repository
  - Changing the generator, the C tree or the compile options of SQLite
  - Deciding where a new piece of code goes
  - Asked why the driver does not use cgo, modernc, wasm or a shared library
---

# Architecture

## Layers

From the bottom up:

1. **The C tree** (`engine/`): the SQLite amalgamation as released, the compile options (`engine/sqlite.c`) and a small libc (`engine/libc/`). It is compiled freestanding, with no system headers.
2. **The generator** (`internal/goasm/gen/`): turns the C tree into Go assembly, one Go function per C function, and a Go file that declares them. Maintainers run it (`make gen`). Users never do.
3. **The generated package** (planned: `internal/csqlite`): the Go assembly and its declarations, checked in.
4. **The Go host** (planned): what SQLite asks of the system, written in Go. The memory SQLite allocates, the VFS (files, locks, time, randomness), and later the mutexes.
5. **The driver** (planned): `database/sql/driver` on top of the C API, with group commit.

## From C to Go assembly

`make gen` runs `scripts/gen-engine.sh`, which runs the generator over `engine/sqlite.c` and `engine/libc/string/*.c`:

1. clang compiles each file to LLVM IR with the flags of `engine/flags.sh` and the generator's own (target, frame pointer, no tail calls, registers Go keeps).
2. llvm-link joins the files, and the optimizer runs once more over the whole module.
3. The shadow-stack pass (`internal/goasm/gen/pass/`, an LLVM plugin) moves every local whose address is taken onto a stack of its own that the host gives, since Go's stack moves when it grows. It puts a poll in every loop, so that Go can stop the goroutine, and passes the extra arguments of a variadic call in a block on that stack.
4. llc writes the machine code. The generator rewrites each function as Go assembly: Go's prologue with its stack check, the arguments from Go's stack, each call made the way Go calls.
5. A check reads the linked binary and holds each function to what llc wrote.

SQLite needs no edit for this. What the generator still has to learn for SQLite:

- **Writable data.** SQLite keeps 33 writable globals (its configuration, the memory and page cache state, the tables of built-in functions it links together at start). The generator only knows read-only data. It will write them as a Go data symbol. SQLite's globals are meant to be shared by the whole process, as Go's are. `SQLITE_OMIT_WSD` was tried and does not fit: it leaves 21 of the 33, and makes each read of the configuration look it up.
- **arm64 jump tables.** llc stores the offsets of a jump table in one byte when the function is small enough. The rewrite makes the function longer and the offset no longer fits. llc must be run with `-aarch64-enable-compress-jump-tables=false`.
- **Format strings.** The generator holds every printf format to what the small libc of QuickJS takes. SQLite has its own printf and calls none from libc, so the check does not apply.
- **A struct copy from read-only data that holds pointers** (`fkActionTrigger`).
- **The file list.** The driver of the generator (`cmd/engine`) still names the QuickJS files and its host callbacks.

A survey of SQLite 3.50.4 on arm64 with these worked around lowered 1391 of its 1392 functions, made 10 MB of assembly in about 40 seconds, and the Go toolchain built it.

## Group commit

Many goroutines that each write one row in autocommit mode each pay for a commit, and with one writer at a time in SQLite they wait for each other. The driver will take the autocommit DML of many callers, run it as one transaction and commit once. The rules are in [AGENTS.md](../AGENTS.md#hard-constraints): only autocommit DML on the main database, a SAVEPOINT per statement, each caller its own result, and the call returns only after the commit.

## Routes left out

- **cgo** (mattn/go-sqlite3): needs a C compiler for every build and every target, and cross-compiling is hard. Not having to do this is the reason for the project.
- **C translated to Go** (modernc.org/sqlite, made with ccgo): no cgo, but inserts are 3 to 4 times slower than with cgo in public benchmarks. slowjs went this way for QuickJS before and left it for the same reason.
- **WebAssembly** (ncruces/go-sqlite3, now wasm2go): no cgo, but a second machine model between SQLite and the CPU.
- **A shared library loaded with purego**: no single static binary, the SQLite version depends on the system, and each call costs more.
