# Driver benchmarks

Compares SQLite drivers for Go's `database/sql` on the same workloads and the same settings. This is its own Go module, so the root module never depends on these drivers.

| Tag       | Driver                          | Built with        |
| --------- | ------------------------------- | ----------------- |
| `liustack` | github.com/liustack/sqlite (this repository) | C compiled to Go assembly |
| `mattn`   | github.com/mattn/go-sqlite3     | cgo               |
| `modernc` | modernc.org/sqlite              | C translated to Go |
| `ncruces` | github.com/ncruces/go-sqlite3   | wasm translated to Go (wasm2go) |

Several of these register the driver name `sqlite` or `sqlite3`, so only one can be linked into a binary. A build tag picks the driver (`driver_<tag>.go`). Each file gives `DriverName`, `DriverTag` and `DSN(path, synchronous)`. To add a driver, add a file with the same three things.

## Run

```sh
make bench                                            # all drivers, 6 rounds, -benchtime 1s
make bench ARGS="-count 2 -benchtime 100ms -drivers liustack,mattn"   # a quick look
make bench ARGS="-bench '^BenchmarkPoint$'"           # some workloads only
```

`make bench` runs `go run ./cmd/benchrun run` in `bench/`. It builds one test binary per driver (cgo on only for `mattn`) and checks its settings with `TestPragmas`. Then the drivers take turns, one run of every benchmark each, `-count` times, each round starting with the next driver, so that other load on the machine falls on all of them alike. It writes `results/<goos>-<goarch>/<tag>.txt` (the format of `go test`, so `benchstat` reads it too) and `meta.json` (date, commit, Go, CPU, OS, threads, `GOMAXPROCS`, driver versions, load average before and after), then prints the tables. `results/` is not checked in.

To run on one core, start it with the CPUs and `GOMAXPROCS` it should have, for example `taskset -c 0,4 env GOMAXPROCS=1 make bench`. On a shared machine, build on other cores first with `-build-only`, then time with `-no-build`, so that no compile runs on the cores being timed.

By hand: `cd bench && CGO_ENABLED=0 go test -tags modernc -run '^$' -bench . -benchmem`.

## Publish

The tables of [docs/benchmarks.md](../docs/benchmarks.md) and of the READMEs are written by `benchrun` from `published/*.json`, never by hand:

```sh
cd bench
go run ./cmd/benchrun publish -from results/linux-amd64 -name linux-amd64-1thread
cd .. && make bench-docs
```

`publish` keeps the median of each benchmark and its 95% confidence interval (as benchstat computes them) and what `meta.json` says about the run. `-set key=value` adds to it, for example `-set note="on a busy machine"` (and `note_zh` for the Chinese side). A table in a doc sits between `<!-- bench:NAME -->` and `<!-- /bench:NAME -->`, where NAME is a set, or `readme-<platform>` for the short README table whose rows `cmd/benchrun/docs.go` lists. `make check` fails when a table is not what `published/` gives.

## Same settings everywhere

Every connection gets `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000` and foreign keys off, through each driver's own DSN parameters. `TestPragmas` reads them back from three pooled connections, so a DSN that silently does nothing fails the test:

```sh
CGO_ENABLED=0 go test -tags modernc -run TestPragmas ./bench
```

With WAL and `synchronous=NORMAL`, a commit is written to the WAL file but not fsynced. A commit survives a process crash, and the last commits can be lost on power failure. This is the same for every driver.

## Workloads

Each benchmark uses its own database file in `b.TempDir()` and one `*sql.DB`. The tables that reads start from are made once per process and copied for each benchmark (`fixture_test.go`), so `testing.B` calling a benchmark again with a larger `b.N` does not fill them again. Every workload checks what it reads, so a driver that returns wrong data fails the run.

| Name             | One op is                                                                                          |
| ---------------- | -------------------------------------------------------------------------------------------------- |
| `Insert`         | One autocommit `INSERT` with a prepared statement, one goroutine                                   |
| `InsertTx`       | 1000 prepared `INSERT`s in one transaction (divide by 1000 for a row)                              |
| `Point`          | `SELECT` one row by primary key from 100k rows, prepared, one goroutine                            |
| `Scan`           | `SELECT` all 100k rows of (id, name ~20 bytes, score, created) and scan every column               |
| `Aggregate`      | Join 10k users with 100k posts, `GROUP BY`, `ORDER BY`, 250 rows back                              |
| `PointParallel`  | `Point` from `b.RunParallel` (GOMAXPROCS goroutines) on a pool of GOMAXPROCS*2 connections         |
| `InsertParallel` | One autocommit `INSERT`, from exactly `g=1`, `g=8` or `g=64` goroutines, pool capped at `g` connections |
| `InsertParallelFull` | `InsertParallel` with `synchronous=FULL`: every commit syncs the WAL to the disk (on macOS without `fullfsync`, so the disk cache may still hold it) |
| `Real/insert`    | One user with 20 articles and 400 comments, in one transaction (the schema of go-sqlite-bench, with its indexes) |
| `Real/query`     | One user found by email with its articles and comments: two `LEFT JOIN`s, 400 rows                 |
| `Many`           | All rows of a table of `n=10`, `100` or `1000` users: the cost of one query and of each row        |
| `Large`          | All 100 rows of a table whose text is 50 or 200 KB. `MB/s` is the rate of text read                 |
| `Readers`        | One scan of all 100k items, from `g=2`, `4` or `8` goroutines at once on `g` connections          |
| `ReadWrite`      | One `Point`, from 8 goroutines while one more inserts without pause. `writes/s` is the writer's rate |

`Real`, `Many`, `Large` and `Readers` follow [cvilsmeier/go-sqlite-bench](https://github.com/cvilsmeier/go-sqlite-bench) (real, many, large, concurrent) at smaller sizes, so that six rounds of four drivers take minutes and not hours. `ReadWrite` is the mix of readers and writers of modernc's `BenchmarkConcurrent`.

In `InsertParallel`, `Readers` and `ReadWrite`, `ns/op` is wall time divided by all the ops of all goroutines, so a lower number with more goroutines means the driver gets more done per second. Every insert is a separate autocommit and returns only after its commit.

## Reading the tables

Time per op and allocations per op are lower is better. `MB/s` and `writes/s` are higher is better. Each cell is the median of the rounds, and `±` is its 95% confidence interval (benchstat's), shown from 6 rounds up. The best of a row is in bold when its interval overlaps no other driver's, and allocation counts, which do not vary, when they are the lowest. Use at least 6 rounds on a quiet machine for numbers you publish.

## Against SQLite in C

`benchrun native` times the driver against the same SQLite compiled to a C program, to show what compiling the C to Go assembly costs and what group commit gives:

```sh
go run ./cmd/benchrun native                     # 6 rounds
go run ./cmd/benchrun native -count 2 -secs 1    # a quick look
go run ./cmd/benchrun publish -from results/native-linux-amd64 -name native-linux-amd64
```

It needs clang (LLVM 22 from `scripts/llvm-bin.sh` by default, `-cc` for another), and a C compiler for cgo to build mattn/go-sqlite3. The C side is `native/c/engine.c` and `native/c/threads.c`, built at `-O2` with `engine/sqlite3.c` and the options of `engine/sqlite.c`, less the two lines that join SQLite to the Go host, so that SQLite uses its own VFS and mutexes. The Go side is `native/main.go`, built once with each driver's tag. Its columns:

| Column | How it reads |
| --- | --- |
| SQLite in C | Each thread opens a connection, prepares the query once and runs `bind`, `step`, `reset` |
| this driver, without database/sql | Each goroutine takes a `*sql.Conn` and calls the driver's own statement through `Conn.Raw`, prepared once, as the C does |
| this driver, through database/sql | Each goroutine takes a `*sql.Conn` and calls `QueryRowContext`, as a program would. No `*sql.Stmt`: each query of one takes the mutex of the `*sql.DB` |
| mattn (cgo), through database/sql | The same, with mattn/go-sqlite3, which runs SQLite's C through cgo |

Writers do the same: through database/sql each calls `db.Exec`, and without it each prepares the insert once on a connection of its own. Each round runs every workload in every column, starting with the next column each round, and the engine workloads (C and this driver) must give the same check value, or the run fails.

| Name | One op is |
| --- | --- |
| `Engine/<name>` | One block of `native/engine.sql` on `:memory:`, one connection, the median of `-reps` runs |
| `Point/g=N` | N threads read one row of 100k by a random key, each with a connection of its own. WAL, `synchronous=NORMAL`, SQLite's page cache of 2 MB, smaller than the 11 MB file |
| `PointCached/g=N` | `Point` with a page cache that holds the whole file (`cache_size=-65536`), so that a read makes no system call |
| `PointInsert/g=8` | 8 readers as in `Point` and 8 writers that insert one row each time |
| `InsertFull/g=64` | 64 writers, `synchronous=FULL` |

The threads run `-secs` seconds each time, and the rates count every thread's ops. In C the writes queue on a mutex of the process, which is faster than SQLite's busy handler. A read that does not return its 100 bytes, or a write that fails, fails the run.
