# liustack/sqlite (project notes for AI agents)

## Goal

`github.com/liustack/sqlite` is a SQLite driver for Go's `database/sql` that needs no cgo. SQLite's C (the amalgamation in `engine/`) is compiled by LLVM to Go assembly that is checked in, so building a program with it needs no C compiler. The driver will also merge the autocommit writes of many goroutines into one transaction (group commit), so that concurrent writers do not each pay for a commit. See [docs/architecture.md](docs/architecture.md).

## Hard constraints

- Pure Go, `CGO_ENABLED=0` for the user of the module: no cgo, no C compiler, no shared library. Targets are amd64 and arm64.
- SQLite's C is never edited. `engine/sqlite3.c` and `engine/sqlite3.h` are the amalgamation as released (`engine/vendor.sh --verify`). Compile options go in `engine/sqlite.c`, each with its reason. Anything SQLite needs from outside is given by `engine/libc` or by the Go host.
- The generated Go assembly is never edited by hand. It is always what `make gen` makes from `engine/` now, and a change to `engine/`, the generator or `scripts/gen-engine.sh` carries the files made again in the same commit (once the generated package is checked in).
- The driver registers itself as `sqlite`. modernc.org/sqlite and glebarez/go-sqlite use the same name, so a program that imports two of them panics. This is accepted. `sql.OpenDB(sqlite.NewConnector(dsn))` is the way around it.
- Group commit only merges autocommit DML on the main database. Each statement runs in its own SAVEPOINT so that one failure does not fail the others, each gets its own LastInsertId and RowsAffected, and a call returns only after the commit (durability is unchanged). User transactions, PRAGMA, ATTACH and temp tables are never merged. One database file has one writer inside the driver.

## Current state of the repository

Early. There is no driver yet.

- `engine/`: the C tree. `sqlite3.c` and `sqlite3.h` (SQLite 3.54.1), `sqlite.c` (the compile options), `flags.sh` (the clang flags, which the generator reads), `vendor.sh`. It has no Go file.
- `engine/libc/`: the small libc SQLite is compiled against: headers, and musl's string functions (`vendor.sh` copies and checks them). No stdio, no math functions: SQLite is built without them.
- `internal/goasm/gen/`: the generator, from github.com/liustack/slowjs. It compiles C to IR, moves address-taken locals to a shadow stack (`pass/`, an LLVM plugin), runs llc and rewrites each function as Go assembly. Its README and parts of its code still describe the QuickJS tree it was written for, and are being changed for SQLite (see [docs/architecture.md](docs/architecture.md#still-to-do)).
- `scripts/`: `gen-engine.sh` (`make gen`, `make pass`), `docs-list.sh`, `llvm-bin.sh`.

## Docs (`docs/`)

1. Every doc starts with front matter: `summary` (one sentence saying what question the doc answers) and `read_when` (when to read it). See the comment at the top of [scripts/docs-list.sh](scripts/docs-list.sh).
2. Before starting work, run `scripts/docs-list.sh` and read the docs whose Read when applies. Before committing, run `scripts/docs-list.sh --check`.
3. What each doc is for:
   - [docs/architecture.md](docs/architecture.md): the layers, the route from C to Go assembly, the decisions and the routes left out
   - [docs/testing.md](docs/testing.md): the checks before a commit and the tests of the generator
   - [docs/commit.md](docs/commit.md): commit rules
   - [docs/upstream.md](docs/upstream.md): where the code comes from and how to move to a new SQLite
4. Docs are written for readers: conclusions and how to do things, not reasoning or work logs. The README is a landing page and only links to the docs.
5. Docs are written in English. Keep it short and direct, use everyday words, and do not invent terms. Do not use semicolons, and do not use dashes as punctuation.

## License headers

- A file written for this repository starts with this header, in the comment syntax of its language:

  ```go
  // Copyright (c) 2026 The liustack/sqlite Authors.
  // SPDX-License-Identifier: MIT
  // Use of this source code is governed by the MIT license in LICENSE at the
  // root of this repository.
  ```

- Code from a third party keeps its own header and license file, and gets a row in [NOTICE.md](NOTICE.md): SQLite (public domain), musl (MIT, `engine/libc/COPYRIGHT`).

## Draft folder

Plans, experiment notes and raw measurements go in the repository draft folder `.issues/<YYYY-MM-DD-topic>/`, the files of one task in one folder. `.issues/` is not checked in (add it to `.git/info/exclude` in a fresh clone), and checked-in code, docs and scripts never refer to it. Settled conclusions are written into `docs/`.

## Verification and commits

- `make check` before every commit (see [docs/testing.md](docs/testing.md)), and `make test-gen` after a change to the generator. Commit rules are in [docs/commit.md](docs/commit.md).
- For new behavior, write a failing test first. For a bug, find the root cause before changing code, and do not hide it with fallback values.
- Tools are Go programs or short shell scripts. Do not add Python.
- Ask the user before creating or pushing a tag, and never push `main` on your own.
