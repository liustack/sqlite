<h1 align="center">sqlite</h1>

<p align="center"><b>The fastest cgo-free SQLite driver for Go's <code>database/sql</code>.<br>It builds with <code>CGO_ENABLED=0</code>, and on most workloads it beats the cgo driver too.</b></p>

<p align="center">
  English · <a href="README.zh-CN.md">简体中文</a> ·
  <a href="docs/benchmarks.md">Benchmarks</a> ·
  <a href="docs/architecture.md">How it works</a>
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/liustack/sqlite"><img src="https://pkg.go.dev/badge/github.com/liustack/sqlite.svg" alt="Go Reference"></a>
  <a href="https://github.com/liustack/sqlite/actions/workflows/test.yml"><img src="https://github.com/liustack/sqlite/actions/workflows/test.yml/badge.svg?branch=main" alt="test"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="License: MIT"></a>
  <a href="https://x.com/liustack"><img src="https://img.shields.io/badge/follow-%40liustack-black?style=flat-square&logo=x&logoColor=white" alt="Follow @liustack on X"></a>
</p>

It is for Go programs that keep their data in SQLite and want to stay plain Go: one static binary, cross-compiled from any machine, no C compiler in the build or in the container. Until now that meant giving up speed. Here the cgo driver is the one that falls behind.

## Benchmarks

Time per operation, lower is better, the median of 6 runs. The best of a row is in bold when its 95% confidence interval overlaps no other driver's. WAL, `synchronous=NORMAL`, the same settings checked on every driver.

<!-- bench:readme-linux-amd64 -->
**linux/amd64** (AMD EPYC 7B13, Go 1.27.2, commit `cc1ede5`):

| Workload | this driver | [mattn](https://github.com/mattn/go-sqlite3) (cgo) | [modernc](https://gitlab.com/cznic/sqlite) | [ncruces](https://github.com/ncruces/go-sqlite3) |
| :-- | --: | --: | --: | --: |
| Insert one row | **18.7 µs** | 19.6 µs | 23.4 µs | 21.5 µs |
| Insert 1000 rows in a transaction | **1.30 ms** | 1.47 ms | 1.94 ms | 1.67 ms |
| Read one row by key | **3.58 µs** | 6.70 µs | 8.45 µs | 6.54 µs |
| Read 100k rows | **34.9 ms** | 73.4 ms | 76.4 ms | 50.2 ms |
| Join and group | 2.87 ms | **2.63 ms** | 4.51 ms | 4.37 ms |
| Find a user with 400 comments | **509 µs** | 830 µs | 1.05 ms | 762 µs |
| Read 100 rows of 200 KB text | 8.85 ms | **6.99 ms** | 8.13 ms | 8.02 ms |
| Read by key, 8 threads | **1.65 µs** | 2.00 µs | 2.18 µs | 2.29 µs |
| Insert, 8 goroutines at once | **9.43 µs** | 19.4 µs | 24.2 µs | 21.9 µs |
| Insert, 64 goroutines at once | **3.40 µs** | 20.2 µs | 27.0 µs | 27.0 µs |
| Insert, 64 goroutines, `synchronous=FULL` | **21.0 µs** | 2.48 ms | 5.31 ms | 1.12 ms |
<!-- /bench:readme-linux-amd64 -->

<!-- bench:readme-darwin-arm64 -->
**darwin/arm64** (Apple M4, Go 1.27.2, commit `90e0e83`, other work ran on the machine: read differences under 30% as noise):

| Workload | this driver | [mattn](https://github.com/mattn/go-sqlite3) (cgo) | [modernc](https://gitlab.com/cznic/sqlite) | [ncruces](https://github.com/ncruces/go-sqlite3) |
| :-- | --: | --: | --: | --: |
| Insert one row | 6.32 µs | 6.21 µs | 7.96 µs | 7.00 µs |
| Insert 1000 rows in a transaction | **456 µs** | 594 µs | 748 µs | 583 µs |
| Read one row by key | **1.12 µs** | 1.96 µs | 2.86 µs | 2.07 µs |
| Read 100k rows | **14.4 ms** | 31.6 ms | 30.1 ms | 20.7 ms |
| Join and group | 1.56 ms | 1.19 ms | 2.65 ms | 1.90 ms |
| Find a user with 400 comments | **236 µs** | 396 µs | 594 µs | 345 µs |
| Read 100 rows of 200 KB text | 4.87 ms | 3.58 ms | 3.99 ms | 3.64 ms |
| Read by key, 10 threads | 1.29 µs | 1.69 µs | 1.79 µs | 2.49 µs |
| Insert, 8 goroutines at once | **3.30 µs** | 6.94 µs | 8.12 µs | 43.5 µs |
| Insert, 64 goroutines at once | **1.37 µs** | 9.53 µs | 8.28 ms | 83.3 µs |
<!-- /bench:readme-darwin-arm64 -->

Where another driver still wins: work that stays inside SQLite (the join), where this driver runs the same C but as Go assembly, and reading large text values. Why reads and many writers are faster, the versions of each driver, and how to run it all again are in [docs/benchmarks.md](docs/benchmarks.md).

## Install

```sh
go get github.com/liustack/sqlite
```

Go 1.27 or later. linux and darwin, on amd64 and arm64.

## Use

```go
import (
	"database/sql"

	_ "github.com/liustack/sqlite"
)

db, err := sql.Open("sqlite", "file:app.db?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
```

The name is a file, `:memory:` or a [SQLite URI](https://sqlite.org/uri.html). `_pragma=name(value)` runs a PRAGMA on every new connection, and `_txlock=immediate` makes `BEGIN` take the write lock at once. A connection waits 5 seconds for a lock unless `busy_timeout` says otherwise. The rest is on [pkg.go.dev](https://pkg.go.dev/github.com/liustack/sqlite).

The driver registers as `sqlite`, as modernc.org/sqlite and glebarez/go-sqlite do, so a program that imports one of them as well panics at start. Open the database with `sql.OpenDB(sqlite.NewConnector(dsn))` then.

## What changes for you

- **No C anywhere.** `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build` from a Mac works, and the binary runs in a `scratch` image.
- **Many writers stop waiting on each other.** When goroutines write at the same time, the driver runs their autocommit `INSERT`, `UPDATE` and `DELETE` statements as one transaction and commits once. Each call still gets its own error, `LastInsertId` and `RowsAffected`, and returns only after the commit, so nothing is less durable. Transactions you open yourself are never merged. [How it works](docs/architecture.md#group-commit)
- **Writers queue instead of failing with `database is locked`.** Inside the driver one connection of your program writes a file at a time, and the others queue for it, up to `busy_timeout`, instead of sleeping in SQLite's busy handler. A transaction that reads first and writes later can still fail when another wrote in between, so start such transactions with `_txlock=immediate`. Other processes still meet SQLite's own locks.
- **Reads cost less.** A call into SQLite is a plain Go call, and scanning a number into your variable allocates nothing (a string costs one allocation, its own).

## Why you can trust it

- **Safety comes first, speed second, memory third.** A change that is faster but could lose a write, return a wrong result or weaken a commit is not taken. Group commit keeps a SAVEPOINT around every merged statement for this reason, though it costs speed.
- **The C is SQLite's, unchanged.** `engine/` holds the 3.54.1 amalgamation as released, checked against its hash (`engine/vendor.sh --verify`). The compile options sit in one file, each with its reason.
- **The Go assembly is made, never written.** LLVM compiles the C, and a generator turns what LLVM writes into Go assembly. It stops on anything it has no rule for, and checks the linked binary. `make check-gen` fails when the checked-in assembly is not what the generator makes from `engine/` today.
- **The numbers show where it loses.** The tables above keep the rows where another driver is faster, and [docs/benchmarks.md](docs/benchmarks.md) says how each run was taken and what else ran on the machine.
- **Every push is tested** on linux/amd64, linux/arm64 and darwin/arm64, with the race detector.

## Not there yet

- No release tag yet. `go get` takes the newest commit of `main`.
- Left out of the build for now: FTS5, R*Tree, the math functions, loadable extensions.
- No shared cache: `cache=shared` gives an error. For a memory database that all connections of a pool see, use `file:/name?vfs=memdb`.
- Double-quoted strings are identifiers only (`SQLITE_DQS=0`, as SQLite advises): write string literals in single quotes.
- Memory SQLite has used is kept for reuse and not given back to the system while the process runs.
- windows and other targets do not build.

## Documentation

| Doc | Read it when |
| :-- | :-- |
| [Benchmarks](docs/benchmarks.md) | You want the full numbers, or to run them on your machine |
| [Architecture](docs/architecture.md) | You want to know how C becomes Go assembly, or how group commit works |
| [Upstream](docs/upstream.md) | You want to know which SQLite it is, or how it moves to a new one |
| [Testing](docs/testing.md), [Commits](docs/commit.md) | You are about to change the code |

## Thanks

To D. Richard Hipp and the SQLite developers for SQLite, and to musl for the string functions the engine links. To the authors of mattn/go-sqlite3, modernc.org/sqlite and ncruces/go-sqlite3, who showed the ways SQLite can live in Go and whose drivers this one is measured against.

## License

MIT for the code of this repository. SQLite is in the public domain, and musl's code keeps its MIT license. [NOTICE.md](NOTICE.md) says which license covers which path.
