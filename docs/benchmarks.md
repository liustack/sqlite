---
summary: 'How fast the driver is against mattn/go-sqlite3 (cgo), modernc.org/sqlite and ncruces/go-sqlite3, on linux/amd64 and darwin/arm64, and how the numbers were taken'
read_when:
  - Asked how fast the driver is, or how it compares with other drivers
  - Changing something on the path of a query or a commit, to measure it again
  - Publishing numbers
---

# Benchmarks

English · [简体中文](benchmarks.zh-CN.md)

The workloads, the settings and how to run them are in [bench/README.md](../bench/README.md). In short: WAL, `synchronous=NORMAL`, one `*sql.DB` per benchmark, a fresh file each time, the same settings checked on every driver. Time per operation, lower is better, the median of 6 runs. The drivers took turns, one run of every benchmark each, so that other load on the machine fell on all of them alike.

| Driver | Version | Built with |
| --- | --- | --- |
| this driver | commit `beeea0d` | C compiled to Go assembly, `CGO_ENABLED=0` |
| mattn/go-sqlite3 | v1.14.52 | cgo |
| modernc.org/sqlite | v1.61.1 | C translated to Go |
| ncruces/go-sqlite3 | v0.36.0 | SQLite as wasm, translated to Go by wasm2go |

## linux/amd64

A Google Cloud VM, AMD EPYC 7B13, 4 cores and 8 threads, Linux 7.3, Go 1.27.2. System calls are slow on it (`getppid` takes 360 ns), so everything that locks or writes a file costs more than on a desktop.

One thread (`GOMAXPROCS=1`, pinned to one core and its sibling thread):

| Benchmark | this driver | mattn | modernc | ncruces |
| --- | --- | --- | --- | --- |
| Insert | 18.4 µs | 19.7 µs | 23.5 µs | 21.5 µs |
| InsertTx (1000 rows) | 1.25 ms | 1.48 ms | 1.97 ms | 1.72 ms |
| Point | 2.20 µs | 5.87 µs | 7.36 µs | 5.84 µs |
| Scan (100k rows) | 34.8 ms | 76.9 ms | 74.2 ms | 49.4 ms |
| Aggregate | 2.90 ms | 2.73 ms | 4.50 ms | 4.43 ms |
| geomean | 348 µs | 514 µs | 647 µs | 543 µs |

All 8 threads, nothing else running on the machine:

| Benchmark | this driver | mattn | modernc | ncruces |
| --- | --- | --- | --- | --- |
| PointParallel | 1.47 µs | 2.20 µs | 2.33 µs | 2.04 µs |
| InsertParallel, 1 writer | 17.3 µs | 18.8 µs | 21.8 µs | 20.3 µs |
| InsertParallel, 8 writers | 9.74 µs | 19.1 µs | 25.7 µs | 22.0 µs |
| InsertParallel, 64 writers | 2.97 µs | 19.6 µs | 27.2 µs | 26.9 µs |
| geomean | 5.20 µs | 11.2 µs | 13.7 µs | 12.5 µs |

With `synchronous=FULL`, where every commit waits for the disk (`InsertParallelFull`, commit `76ecfda`, one thread as above, the median of 4 runs). The disk is a persistent disk of the VM, and a commit with its fsync takes about 0.6 to 0.9 ms:

| Benchmark | this driver | mattn | modernc | ncruces |
| --- | --- | --- | --- | --- |
| InsertParallelFull, 1 writer | 881 µs | 915 µs | 948 µs | 828 µs |
| InsertParallelFull, 8 writers | 191 µs | 1.95 ms | 2.36 ms | 900 µs |
| InsertParallelFull, 64 writers | 21.0 µs | 3.03 ms | 4.37 ms | 1.09 ms |

## darwin/arm64

An Apple M4 (10 cores), macOS 15, Go 1.27.2. The machine was in use by other work during the runs (load average 3 to 8 on 10 cores), so the numbers of one driver move by up to 30% between runs, and only the larger differences mean something. The Insert rows of the four drivers are within that noise of each other.

| Benchmark | this driver | mattn | modernc | ncruces |
| --- | --- | --- | --- | --- |
| Insert | 8.92 µs | 9.16 µs | 11.0 µs | 9.27 µs |
| InsertTx (1000 rows) | 723 µs | 837 µs | 915 µs | 634 µs |
| Point | 0.81 µs | 1.76 µs | 2.50 µs | 1.71 µs |
| Scan (100k rows) | 15.3 ms | 33.7 ms | 32.4 ms | 20.5 ms |
| Aggregate | 1.48 ms | 1.23 ms | 3.00 ms | 1.98 ms |
| PointParallel | 1.25 µs | 1.90 µs | 2.02 µs | 2.22 µs |
| InsertParallel, 1 writer | 9.08 µs | 9.71 µs | 8.66 µs | 9.09 µs |
| InsertParallel, 8 writers | 3.45 µs | 9.03 µs | 10.5 µs | 48.4 µs |
| InsertParallel, 64 writers | 1.42 µs | 16.8 µs | 13.4 ms | 80.0 µs |
| geomean | 26.6 µs | 48.8 µs | 122 µs | 68.2 µs |

## What the numbers say

- **Reads** (Point, PointParallel, Scan) are faster than with cgo: a call into the engine is a plain Go call, with no cgo crossing, and a read transaction takes no lock in the kernel when another read of the process is running or just ended (see `internal/vfs`).
- **Many writers** (InsertParallel) are where group commit shows: from 8 writers up, one commit carries many inserts, and at 64 writers an insert costs a sixth to a twelfth of what it costs with the other drivers. With `synchronous=FULL` the gap grows with the cost of a commit: at 64 writers one fsync carries about 50 inserts, and an insert costs 21 µs against 1.1 to 4.4 ms. On the busy Mac, modernc at 64 writers spends its time in SQLite's busy handler, whose sleeps grow while the writer that holds the lock waits for a thread.
- **One writer** (Insert) is close to mattn on both machines. The time goes to the WAL write and the locks SQLite's protocol asks for, the same system calls for every driver.
- **Aggregate**, which is all work inside the engine, is 6% (linux) to 20% (darwin) slower than with cgo: the engine is the same C, compiled by LLVM, and the Go assembly it becomes is close to, but not as good as, what clang makes for the machine.
- **Scan** copies each number straight into the `Scan` destination (`driver.RowsColumnScanner`), without boxing it in an interface, so it allocates half as often as mattn and modernc.

## Running them again

`bench/run.sh` on a quiet machine, then `benchstat` (it prints the table at the end). Keep `results/<goos>-<goarch>/header.txt` with the numbers. On a shared machine, pin the single-thread runs to one core (`taskset`, `GOMAXPROCS=1`) and take the parallel runs only when nothing else runs.
