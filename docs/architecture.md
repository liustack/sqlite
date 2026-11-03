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

SQLite needs no edit for this. On arm64, SQLite 3.54.1 lowers whole: 1458 functions, no stub, 10 MB of assembly in about 45 seconds, and the post-link check passes on a linux/arm64 binary.

### Writable data

SQLite keeps 31 writable globals: its configuration, the state of its allocator and page cache, the tables of built-in functions it links together at start. They are shared by the whole process, as a Go global is, so they go into the one Go data symbol with the read-only data, which is then written without RODATA. The read-only data of SQLite becomes writable memory with it. SQLite never writes it.

The lowering can only rewrite a reference to data that takes an address: a direct load would have to become a copy in the body of the function, which is wrong for data that changes. So the generator makes every writable global external and preemptible in the IR before llc runs (`ExposeWritableData`). Position-independent code then reaches each one only through a load of its address from the GOT, and the lowering turns that load into the address in the data symbol. A direct reference to writable data stops the build. `SQLITE_OMIT_WSD` was tried and does not fit: it leaves 21 of 33 globals writable (3.50.4), and makes each read of the configuration look it up.

### Still to do

- **amd64.** The lowering and the post-link check of amd64 do not take a GOT load (`movq sym@GOTPCREL(%rip)`) yet.
- **The host.** `sqlite3_os_init` (the VFS) and `malloc`, `realloc` and `free` come from the Go host, which is not written. The generator's list of host callbacks is still the one of QuickJS.

## Group commit

Many goroutines that each write one row in autocommit mode each pay for a commit, and with one writer at a time in SQLite they wait for each other. The driver will take the autocommit DML of many callers, run it as one transaction and commit once. The rules are in [AGENTS.md](../AGENTS.md#hard-constraints): only autocommit DML on the main database, a SAVEPOINT per statement, each caller its own result, and the call returns only after the commit.

A prototype of it in application code, on mattn/go-sqlite3 (WAL, one writer goroutine taking whatever callers are waiting, up to 256), measured on an Apple M4:

| Writers | synchronous | One commit per write | Group commit | Gain |
| --- | --- | --- | --- | --- |
| 1 | NORMAL | 133k writes/s | 88k writes/s | 0.7x |
| 16 | NORMAL | 88k | 197k | 2.2x |
| 256 | NORMAL | 94k | 359k | 3.8x |
| 16 | FULL, fullfsync | 213 | 2.0k | 9.5x |
| 256 | FULL, fullfsync | 150 | 34.8k | 232x |

So it pays from about 16 concurrent writers up, most where a commit waits for the disk, and costs with one writer, where the queue only adds a hop: the driver must write directly when no one else is waiting. One commit per write is also unfair: some goroutines waited seconds and some got `SQLITE_BUSY`. The SAVEPOINT per statement halves the peak (it adds two calls per statement in the prototype). A failed constraint in SQLite already undoes only its own statement, so where the SAVEPOINT is really needed has yet to be shown. Each run took 3 seconds once, and runs of the same setup differed by up to 90%, so read gains under 1.5x as noise.

## Routes left out

- **cgo** (mattn/go-sqlite3): needs a C compiler for every build and every target, and cross-compiling is hard. Not having to do this is the reason for the project.
- **C translated to Go** (modernc.org/sqlite, made with ccgo): no cgo, but inserts are 3 to 4 times slower than with cgo in public benchmarks. slowjs went this way for QuickJS before and left it for the same reason.
- **WebAssembly** (ncruces/go-sqlite3, now wasm2go): no cgo, but a second machine model between SQLite and the CPU.
- **A shared library loaded with purego**: no single static binary, the SQLite version depends on the system, and each call costs more.
