# Driver benchmarks

Compares SQLite drivers for Go's `database/sql` on the same workloads and the same settings. This is its own Go module, so the root module never depends on these drivers.

| Tag       | Driver                          | Built with        |
| --------- | ------------------------------- | ----------------- |
| `liustack` | github.com/liustack/sqlite (this repository) | C compiled to Go assembly |
| `mattn`   | github.com/mattn/go-sqlite3     | cgo               |
| `modernc` | modernc.org/sqlite              | C translated to Go |
| `ncruces` | github.com/ncruces/go-sqlite3   | wasm translated to Go (wasm2go) |

Several of these register the driver name `sqlite` or `sqlite3`, so only one can be linked into a binary. A build tag picks the driver (`driver_<tag>.go`). Each file gives `DriverName`, `DriverTag` and `DSN(path)`. To add a driver, add a file with the same three things.

## Run

```sh
bench/run.sh                              # all drivers, COUNT=6 BENCHTIME=1s
COUNT=1 BENCHTIME=100x bench/run.sh       # quick check that everything runs
bench/run.sh modernc ncruces              # some drivers only
BENCH='^BenchmarkPoint$' bench/run.sh     # some workloads only
```

`run.sh` builds one test binary per driver (cgo on only for `mattn`) and checks its settings with `TestPragmas`. Then the drivers take turns, one run of every benchmark each, `COUNT` times, so that other load on the machine falls on all of them alike. It writes `results/<goos>-<goarch>/<tag>.txt` and `header.txt` (Go version, CPU, OS, driver versions), then prints a [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) table with one column per driver. The first driver is the base, and the other columns show the change against it. `results/` is not checked in.

By hand: `cd bench && CGO_ENABLED=0 go test -tags modernc -run '^$' -bench . -benchmem`.

## Same settings everywhere

Every connection gets `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000` and foreign keys off, through each driver's own DSN parameters. `TestPragmas` reads them back from three pooled connections, so a DSN that silently does nothing fails the test:

```sh
CGO_ENABLED=0 go test -tags modernc -run TestPragmas ./bench
```

With WAL and `synchronous=NORMAL`, a commit is written to the WAL file but not fsynced. A commit survives a process crash, and the last commits can be lost on power failure. This is the same for every driver.

## Workloads

Each benchmark uses a fresh database file in `b.TempDir()` and one `*sql.DB`. Schema and data are made before the timer starts.

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

In `InsertParallel`, `ns/op` is wall time divided by total inserts, so a lower number at `g=64` than at `g=1` means the driver gets more inserts done per second. Every insert is a separate autocommit and returns only after its commit.

## Reading the table

`sec/op` is time per op, `B/op` and `allocs/op` are Go heap use per op. Lower is better. A `~` means benchstat sees no real difference. With `COUNT=1` benchstat cannot say anything about noise, so use at least 6 runs for numbers you publish, on a quiet machine, and keep `header.txt` with them.
