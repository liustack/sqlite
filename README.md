<h1 align="center"><img src="assets/brand/sqlite-banner.png" alt="SQLite driver for Go"></h1>

<p align="center"><b>The fastest cgo-free SQLite driver out there, fully compatible with Go's <code>database/sql</code>.<br>Real-time backup to Cloudflare R2 built in keeps your app stable and reliable on a single VPS.</b></p>

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
**linux/amd64** (AMD EPYC 7B13, Go 1.27.4, commit `44acf8c`):

| Workload | liustack/sqlite | [mattn](https://github.com/mattn/go-sqlite3) (cgo) | [modernc](https://gitlab.com/cznic/sqlite) | [ncruces](https://github.com/ncruces/go-sqlite3) |
| :-- | --: | --: | --: | --: |
| Insert one row | 22.9 µs | 23.3 µs | 26.7 µs | 24.2 µs |
| Insert 1000 rows in a transaction | 1.40 ms | 1.67 ms | 2.19 ms | 1.91 ms |
| Read one row by key | **3.79 µs** | 7.43 µs | 9.31 µs | 7.11 µs |
| Read 100k rows | **35.1 ms** | 79.9 ms | 84.5 ms | 52.5 ms |
| Join and group | 2.88 ms | 2.87 ms | 4.93 ms | 4.74 ms |
| Find a user with 400 comments | **512 µs** | 880 µs | 1.15 ms | 826 µs |
| Read 100 rows of 200 KB text | 8.75 ms | **8.34 ms** | 9.38 ms | 9.14 ms |
| Read by key, 4 threads | **1.47 µs** | 2.97 µs | 3.86 µs | 2.79 µs |
| Insert, 8 goroutines at once | **10.4 µs** | 22.9 µs | 26.0 µs | 23.4 µs |
| Insert, 64 goroutines at once | **3.42 µs** | 22.2 µs | 30.1 µs | 28.6 µs |
| Insert, 64 goroutines, `synchronous=FULL` | **30.4 µs** | 4.53 ms | 7.11 ms | 1.42 ms |
<!-- /bench:readme-linux-amd64 -->

<!-- bench:readme-darwin-arm64 -->
**darwin/arm64** (Apple M4, Go 1.27.2, commit `44acf8c`):

| Workload | liustack/sqlite | [mattn](https://github.com/mattn/go-sqlite3) (cgo) | [modernc](https://gitlab.com/cznic/sqlite) | [ncruces](https://github.com/ncruces/go-sqlite3) |
| :-- | --: | --: | --: | --: |
| Insert one row | 5.95 µs | 5.99 µs | 7.34 µs | 7.01 µs |
| Insert 1000 rows in a transaction | **483 µs** | 557 µs | 765 µs | 584 µs |
| Read one row by key | **1.08 µs** | 1.93 µs | 2.76 µs | 2.02 µs |
| Read 100k rows | **13.7 ms** | 29.7 ms | 29.9 ms | 20.6 ms |
| Join and group | 1.27 ms | **1.14 ms** | 2.64 ms | 1.87 ms |
| Find a user with 400 comments | **212 µs** | 371 µs | 554 µs | 342 µs |
| Read 100 rows of 200 KB text | 3.46 ms | 3.29 ms | 3.79 ms | 3.55 ms |
| Read by key, 10 threads | **1.13 µs** | 1.52 µs | 1.77 µs | 2.33 µs |
| Insert, 8 goroutines at once | **3.04 µs** | 6.75 µs | 8.19 µs | 45.0 µs |
| Insert, 64 goroutines at once | **1.19 µs** | 9.94 µs | 5.67 ms | 85.8 µs |
<!-- /bench:readme-darwin-arm64 -->

Against SQLite in C on the same machine: SQLite(C) is a C program calling SQLite itself, liustack/sqlite(direct) calls the driver without `database/sql`, and the two `database/sql` columns read and write as a program would. Many writers at once go faster because the driver merges their commits into one.

<!-- bench:readme-through-linux-amd64 -->
**linux/amd64** (AMD EPYC 7B13, Go 1.27.4, commit `44acf8c`):

**Throughput.** Reads or writes done per second by all threads together, on a file of 100k rows in WAL mode, higher is better:

| Benchmark | SQLite(C) | liustack/sqlite(direct) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 thread, each reads one row by key | 194k reads/s ±1% | **380k reads/s** ±1% | 290k reads/s ±1% | 103k reads/s ±0% |
| 8 threads, each reads one row by key | 1154k reads/s ±5% | **1320k reads/s** ±2% | 987k reads/s ±2% | 423k reads/s ±5% |
| 64 threads, each reads one row by key | 1034k reads/s ±8% | **1325k reads/s** ±1% | 939k reads/s ±3% | 425k reads/s ±3% |
| 1 thread, each reads one row by key, whole file in cache | 241k reads/s ±2% | **756k reads/s** ±2% | 487k reads/s ±2% | 117k reads/s ±1% |
| 8 threads, each reads one row by key, whole file in cache | 1499k reads/s ±8% | **2394k reads/s** ±4% | 1463k reads/s ±3% | 455k reads/s ±4% |
| 64 threads, each reads one row by key, whole file in cache | 1241k reads/s ±5% | **2123k reads/s** ±4% | 1293k reads/s ±5% | 327k reads/s ±8% |
| 8 threads read as above while 8 others write | 1005k reads/s ±8% | 1021k reads/s ±4% | 615k reads/s ±5% | 167k reads/s ±2% |
| 8 threads, each inserts one row, while 8 others read | 2796 writes/s ±25% | 19467 writes/s ±11% | **28900 writes/s** ±4% | 15437 writes/s ±2% |
| 64 threads, each inserts one row, `synchronous=FULL` | 819 writes/s ±6% | 56830 writes/s ±5% | 55212 writes/s ±2% | 845 writes/s ±2% |

<!-- /bench:readme-through-linux-amd64 -->

Where another driver still wins: work that stays inside SQLite (the join), where this driver runs the same C but as Go assembly, and reading large text values. Why reads and many writers are faster, the versions of each driver, and how to run it all again are in [docs/benchmarks.md](docs/benchmarks.md).

## Real-time backup to S3

Start the replicator before you open the database, and every commit goes to S3, Cloudflare R2 or MinIO about a second later:

```go
cfg := replica.Config{
	URL: "s3://my-bucket/app.db?endpoint=https://<account>.r2.cloudflarestorage.com&region=auto",
}
r, err := replica.New(ctx, "app.db", cfg)
if err != nil {
	return err
}
defer r.Close()

db, err := sql.Open("sqlite", "app.db")
```

On a new machine one call brings the database back, as it is now or as it was at an earlier time, at least a day back by default:

```go
txid, err := replica.Restore(ctx, "app.db", cfg, replica.RestoreOptions{})
```

- **Writes never wait for the upload.** A slow or unreachable store never slows a write or makes it fail. Changes wait on disk next to the database and go up once the store answers.
- **Nothing else to deploy.** It is a package in your program, so the deploy stays one binary, with no Litestream process beside it.
- **Every file is checked on restore**, then `PRAGMA integrity_check` runs, and a database already there is never written over.
- **Litestream reads the same copy.** It is laid out as Litestream lays it out, and the tests have `litestream restore` rebuild it at every point and check it gives the same bytes.
- **`r.Sync(ctx)` waits until what you committed is in the store**, for the writes that must be safe before you go on.

Settings, how far behind the copy is, and its limits: [Replication](docs/replication.md).

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

Each connection keeps SQLite's page cache, up to 2 MB by default, until it closes, and `database/sql` opens a connection for every query that runs at the same time. Give the pool a size, and let it keep that many idle so that connections are not closed and opened again:

```go
db.SetMaxOpenConns(16)
db.SetMaxIdleConns(16)
```

SQLite picks how to run a query from statistics that only `ANALYZE` gathers, and it does not update them as the data changes. Let `PRAGMA optimize` keep them: run `db.Exec("PRAGMA optimize=0x10002")` once at start, and `db.Exec("PRAGMA optimize")` now and then (once a day, and after `CREATE INDEX`). It analyzes only the tables that need it, from a sample of each index, and holds the write lock while it does, so its first run on a large database that never had statistics can keep writes waiting for seconds. A full `ANALYZE` gives the best statistics and reads the whole database under the write lock: run it when nothing writes. `_pragma=optimize` is refused, as it would run each time a connection opens.

An error from SQLite is a `*sqlite.Error`. `errors.As` gets it, and `Code()` and `ExtendedCode()` give its [result code](https://sqlite.org/rescode.html), as `sqlite.BUSY` or `sqlite.CONSTRAINT_UNIQUE`.

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
- **Many writers stop waiting on each other.** When goroutines write at the same time, the driver runs their autocommit `INSERT`, `UPDATE` and `DELETE` statements as one transaction and commits once. Each call still gets its own error, `LastInsertId` and `RowsAffected`, and returns only after the commit, so nothing is less durable. When one statement fails so badly that SQLite rolls back the whole transaction (an IO error, `ON CONFLICT ROLLBACK`, a context that ends while it runs), the statements merged before it fail with `ABORT_ROLLBACK`, and none runs twice. Transactions you open yourself are never merged. [The rules](docs/group-commit.md)
- **Writers queue instead of failing with `database is locked`.** Inside the driver one connection of your program writes a file at a time, and the others queue for it, up to `busy_timeout`, instead of sleeping in SQLite's busy handler. A transaction that reads first and writes later can still fail when another wrote in between, so start such transactions with `_txlock=immediate`. Other processes still meet SQLite's own locks.
- **Reads cost less.** A call into SQLite is a plain Go call, and on Go 1.27 scanning a number into your variable allocates nothing (a string costs one allocation, its own).
- **Real-time backup is built in.** Every commit reaches S3, R2 or MinIO about a second later, from inside your program. See [Real-time backup to S3](#real-time-backup-to-s3).

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
| [Group commit](docs/group-commit.md) | You want to know which concurrent writes merge, and what error each gets when one fails |
| [Statement cache](docs/query-cache.md) | You want to know which statements the driver keeps for you, and why a kept one never reads something stale |
| [Replication](docs/replication.md) | You want a copy of the database in S3, R2 or MinIO, or to restore one |
| [When every core is busy](docs/busy-cores.md) | A read or a write waits tens of milliseconds or more now and then, or you run on one core or two |
| [Upstream](docs/upstream.md) | You want to know which SQLite it is, or how it moves to a new one |
| [Testing](docs/testing.md), [Commits](docs/commit.md) | You are about to change the code |

## Talk to us

Issues are welcome any time: [open one](https://github.com/liustack/sqlite/issues/new/choose). Tell us what you built, where it hurt, and what should come next. What changes in the driver and what comes next are shared on X: **[@liustack](https://x.com/liustack)**.

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
