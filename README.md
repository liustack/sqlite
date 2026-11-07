<h1 align="center"><img src="assets/brand/sqlite-banner.png" alt="SQLite driver for Go"></h1>

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
  <img src="https://img.shields.io/badge/CGO-off-success?style=flat-square" alt="CGO off">
  <img src="https://img.shields.io/badge/Not%20backed%20by-Y%20Combinator-FF6600?style=flat-square&logo=ycombinator&logoColor=white" alt="Not backed by Y Combinator">
  <img src="https://img.shields.io/badge/users-unknown-lightgrey?style=flat-square" alt="Users unknown">
</p>

It is for Go programs that keep their data in SQLite and want to stay plain Go: one static binary, cross-compiled from any machine, no C compiler in the build or in the container. Until now that meant giving up speed. Here the cgo driver is the one that falls behind.

> liustack/sqlite is at v0.1. The APIs may still change before v1.

## Why it exists

While building [Tifa](https://github.com/liustack/tifa), I wanted every app to deploy as one Go binary copied to a small VPS. The data had to live in SQLite, and CGO had to stay off, or there goes cross-compiling and the `scratch` image.

The pure Go drivers paid for that in speed. modernc translates SQLite's C into Go and ncruces runs it as WebAssembly, and both trailed the cgo driver. So I had LLVM compile SQLite's own C straight into Go assembly, and kept at it until the cgo driver was the one falling behind.

## Benchmarks

Time per operation, lower is better, the median of 6 runs. The best of a row is in bold when its 95% confidence interval overlaps no other driver's. WAL, `synchronous=NORMAL`, the same settings checked on every driver.

<!-- bench:readme-linux-amd64 -->
**linux/amd64** (AMD EPYC 7B13, Go 1.27.2, commit `bc87680`):

| Workload | this driver | [mattn](https://github.com/mattn/go-sqlite3) (cgo) | [modernc](https://gitlab.com/cznic/sqlite) | [ncruces](https://github.com/ncruces/go-sqlite3) |
| :-- | --: | --: | --: | --: |
| Insert one row | **18.5 µs** | 19.5 µs | 22.9 µs | 21.0 µs |
| Insert 1000 rows in a transaction | **1.30 ms** | 1.46 ms | 1.95 ms | 1.65 ms |
| Read one row by key | **3.61 µs** | 6.69 µs | 8.32 µs | 6.53 µs |
| Read 100k rows | **34.4 ms** | 72.0 ms | 71.3 ms | 50.7 ms |
| Join and group | 2.87 ms | **2.62 ms** | 4.50 ms | 4.34 ms |
| Find a user with 400 comments | **514 µs** | 836 µs | 1.01 ms | 755 µs |
| Read 100 rows of 200 KB text | 7.23 ms | **6.77 ms** | 7.89 ms | 7.81 ms |
| Read by key, 8 threads | 1.69 µs | 2.31 µs | 2.25 µs | 2.09 µs |
| Insert, 8 goroutines at once | **9.56 µs** | 19.3 µs | 24.7 µs | 21.8 µs |
| Insert, 64 goroutines at once | **3.60 µs** | 20.3 µs | 27.6 µs | 26.6 µs |
| Insert, 64 goroutines, `synchronous=FULL` | **21.3 µs** | 2.29 ms | 4.26 ms | 1.10 ms |
<!-- /bench:readme-linux-amd64 -->

<!-- bench:readme-darwin-arm64 -->
**darwin/arm64** (Apple M4, Go 1.27.2, commit `92ec4bf`, other work ran on the machine: read differences under 30% as noise):

| Workload | this driver | [mattn](https://github.com/mattn/go-sqlite3) (cgo) | [modernc](https://gitlab.com/cznic/sqlite) | [ncruces](https://github.com/ncruces/go-sqlite3) |
| :-- | --: | --: | --: | --: |
| Insert one row | 6.28 µs | 5.93 µs | 7.40 µs | 6.92 µs |
| Insert 1000 rows in a transaction | 479 µs | 556 µs | 739 µs | 570 µs |
| Read one row by key | **1.15 µs** | 1.93 µs | 2.78 µs | 2.06 µs |
| Read 100k rows | **14.2 ms** | 30.0 ms | 29.8 ms | 20.4 ms |
| Join and group | 1.34 ms | **1.14 ms** | 2.62 ms | 1.85 ms |
| Find a user with 400 comments | **226 µs** | 367 µs | 553 µs | 341 µs |
| Read 100 rows of 200 KB text | 3.39 ms | 3.36 ms | 3.73 ms | 3.62 ms |
| Read by key, 10 threads | **1.17 µs** | 1.49 µs | 1.74 µs | 2.37 µs |
| Insert, 8 goroutines at once | **3.09 µs** | 6.86 µs | 8.15 µs | 36.8 µs |
| Insert, 64 goroutines at once | **1.16 µs** | 10.0 µs | 5.65 ms | 81.1 µs |
<!-- /bench:readme-darwin-arm64 -->

Where another driver still wins: work that stays inside SQLite (the join), where this driver runs the same C but as Go assembly, and reading large text values. Why reads and many writers are faster, the versions of each driver, and how to run it all again are in [docs/benchmarks.md](docs/benchmarks.md).

## Install

```sh
go get github.com/liustack/sqlite
```

Go 1.26.6 to 1.27 (see [Moving to a new Go](docs/upstream.md#moving-to-a-new-go)). linux and darwin on amd64 and arm64, windows on amd64.

## Use

```go
import (
	"database/sql"

	_ "github.com/liustack/sqlite"
)

db, err := sql.Open("sqlite", "file:app.db?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
```

The name is a file, `:memory:` or a [SQLite URI](https://sqlite.org/uri.html). `_pragma=name(value)` runs a PRAGMA on every new connection, and `_txlock=immediate` makes `BEGIN` take the write lock at once. A connection waits 5 seconds for a lock unless `busy_timeout` says otherwise. The rest is on [pkg.go.dev](https://pkg.go.dev/github.com/liustack/sqlite).

SQL functions, aggregates, window functions, collations and virtual tables written in Go go on the connector, so that every connection of the pool has them:

```go
c, err := sqlite.NewConnector("file:app.db",
	sqlite.WithFunction("reverse", 1, reverse, sqlite.Deterministic),
	sqlite.WithAggregate("mode", 1, newMode),
	sqlite.WithCollation("fold", fold),
	sqlite.WithModule("files", connectFiles))
db := sql.OpenDB(c)
```

`sqlite.Register` gives them to every connector `sql.Open` makes after it. An error or a panic in a function or a virtual table fails its statement, not the connection.

Common functions come ready in packages under `ext/`: [`regexp`](https://pkg.go.dev/github.com/liustack/sqlite/ext/regexp) (the `REGEXP` operator, `regexp_like`, `regexp_substr`, `regexp_replace`), [`uuid`](https://pkg.go.dev/github.com/liustack/sqlite/ext/uuid), [`hash`](https://pkg.go.dev/github.com/liustack/sqlite/ext/hash) (md5, sha1, sha256, sha512, crc32), [`stats`](https://pkg.go.dev/github.com/liustack/sqlite/ext/stats) (variance and standard deviation), [`unicode`](https://pkg.go.dev/github.com/liustack/sqlite/ext/unicode) (upper and lower case and a case-blind collation for every script), [`csv`](https://pkg.go.dev/github.com/liustack/sqlite/ext/csv) (a CSV file as a table), [`series`](https://pkg.go.dev/github.com/liustack/sqlite/ext/series) (`generate_series`) and [`vec`](https://pkg.go.dev/github.com/liustack/sqlite/ext/vec) (vector distances, to find the rows nearest to an embedding). Each is one option, and `sqlite.Join` hands several over as one:

```go
sqlite.Register(sqlite.Join(regexp.Option(), uuid.Option()))
```

Full-text search ([FTS5](https://sqlite.org/fts5.html)), the spatial index ([R*Tree](https://sqlite.org/rtree.html)), the [math functions](https://sqlite.org/lang_mathfunc.html) and `median` and `percentile` ([percentile](https://sqlite.org/percentile.html)) are built in, with nothing to import. The math functions give the same bits on every target.

The driver registers as `sqlite`, as modernc.org/sqlite and glebarez/go-sqlite do, so a program that imports one of them as well panics at start. Open the database with `sql.OpenDB(sqlite.NewConnector(dsn))` then.

## What changes for you

- **No C anywhere.** `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build` from a Mac works, and the binary runs in a `scratch` image.
- **Many writers stop waiting on each other.** When goroutines write at the same time, the driver runs their autocommit `INSERT`, `UPDATE` and `DELETE` statements as one transaction and commits once. Each call still gets its own error, `LastInsertId` and `RowsAffected`, and returns only after the commit, so nothing is less durable. Transactions you open yourself are never merged. [How it works](docs/architecture.md#group-commit)
- **Writers queue instead of failing with `database is locked`.** Inside the driver one connection of your program writes a file at a time, and the others queue for it, up to `busy_timeout`, instead of sleeping in SQLite's busy handler. A transaction that reads first and writes later can still fail when another wrote in between, so start such transactions with `_txlock=immediate`. Other processes still meet SQLite's own locks.
- **Reads cost less.** A call into SQLite is a plain Go call, and on Go 1.27 scanning a number into your variable allocates nothing (a string costs one allocation, its own).
- **Losing the machine no longer means losing the database.** `replica.New` copies every commit to S3, R2 or MinIO about a second later, from inside your program, with no Litestream process beside it. `replica.Restore` brings it back on another machine, checked file by file, and `litestream restore` reads the same copy. [How it works](docs/replication.md)

## Why you can trust it

- **Tifa builds on it.** Apps made with `tifa new`, from [Tifa](https://github.com/liustack/tifa), the full-stack Go framework, keep their data in this driver.
- **Safety comes first, speed second, memory third.** A change that is faster but could lose a write, return a wrong result or weaken a commit is not taken. Group commit keeps a SAVEPOINT around every merged statement for this reason, though it costs speed.
- **The C is SQLite's, unchanged.** `engine/` holds the 3.54.1 amalgamation as released, checked against its hash (`engine/vendor.sh --verify`). The compile options sit in one file, each with its reason.
- **The Go assembly is made, never written.** LLVM compiles the C, and a generator turns what LLVM writes into Go assembly. It stops on anything it has no rule for, and checks the linked binary. `make check-gen` fails when the checked-in assembly is not what the generator makes from `engine/` today.
- **The numbers show where it loses.** The tables above keep the rows where another driver is faster, and [docs/benchmarks.md](docs/benchmarks.md) says how each run was taken and what else ran on the machine.
- **Every push is tested** on linux/amd64, linux/arm64, darwin/arm64 and windows/amd64 (with the race detector, except on windows), with the sqlite3 shell writing the same database at the same time, and with processes killed while they write.

## Not there yet

- No loadable extensions: SQLite is built without `load_extension`.
- No shared cache: `cache=shared` gives an error. For a memory database that all connections of a pool see, use `file:/name?vfs=memdb`.
- Double-quoted strings are identifiers only (`SQLITE_DQS=0`, as SQLite advises): write string literals in single quotes.
- windows/arm64 and other targets do not build.

## Documentation

| Doc | Read it when |
| :-- | :-- |
| [Benchmarks](docs/benchmarks.md) | You want the full numbers, or to run them on your machine |
| [Architecture](docs/architecture.md) | You want to know how C becomes Go assembly, or how group commit works |
| [Replication](docs/replication.md) | You want a copy of the database in S3, R2 or MinIO, or to restore one |
| [Upstream](docs/upstream.md) | You want to know which SQLite it is, or how it moves to a new one |
| [Testing](docs/testing.md), [Commits](docs/commit.md) | You are about to change the code |

## Talk to us

Issues are welcome any time: [open one](https://github.com/liustack/sqlite/issues/new/choose). Tell us what you built, where it hurt, and what should come next. Follow the liustack WeChat official account, and come find me on X: **[@liustack](https://x.com/liustack)**. What changes in the driver and what comes next are shared on WeChat and X.

The **liustack** WeChat official account: AI startup opportunities, indie-dev insights, and hands-on AI tooling, delivered as they happen. Scan the QR code in WeChat, or search for "liustack":

<p align="center">
  <img src="https://raw.githubusercontent.com/liustack/sqlite/main/assets/wechat-qrcode.png" width="420" alt="liustack WeChat official account" />
</p>

## Contributing

liustack/sqlite does not accept pull requests. A change here can reach from SQLite's C, through the generator, to group commit and the replicator, and getting it right takes a lot of context, so every line is written and reviewed by one author. Two ways to help:

- **[Open an issue](https://github.com/liustack/sqlite/issues/new/choose).** Bugs, wrong results, slow queries, confusing errors, unclear docs, ideas. A small program that reproduces it helps more than a patch, and issues shape what gets built next.
- **Fork it.** Under MIT your copy is yours to change and publish. [CONTRIBUTING.md](CONTRIBUTING.md) has the notes for working on a fork.

Security issues go through a private report, not an issue: see [SECURITY.md](SECURITY.md).

## Star History

<a href="https://www.star-history.com/#liustack/sqlite&Date">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=liustack/sqlite&type=Date&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=liustack/sqlite&type=Date" />
   <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=liustack/sqlite&type=Date" />
 </picture>
</a>

## Thanks

To D. Richard Hipp and the SQLite developers for SQLite, and to musl for the string functions the engine links. To the authors of mattn/go-sqlite3, modernc.org/sqlite and ncruces/go-sqlite3, who showed the ways SQLite can live in Go and whose drivers this one is measured against.

## License

MIT for the code of this repository. SQLite is in the public domain, and musl's code keeps its MIT license. [NOTICE.md](NOTICE.md) says which license covers which path.

SQLite is a registered trademark of Hipp, Wyrick & Company, Inc., and this project is not affiliated with it. The gopher in the banner is a redrawn [Go gopher](https://go.dev/blog/gopher), originally designed by Renee French and used under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
