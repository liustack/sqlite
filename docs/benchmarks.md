---
summary: 'How fast the driver is against mattn/go-sqlite3 (cgo), modernc.org/sqlite and ncruces/go-sqlite3, on linux/amd64 and darwin/arm64, what replication costs, and how the numbers were taken'
read_when:
  - Asked how fast the driver is, or how it compares with other drivers
  - Changing something on the path of a query or a commit, to measure it again
  - Publishing numbers
---

# Benchmarks

English · [简体中文](benchmarks.zh-CN.md)

The workloads, the settings and how to run them are in [bench/README.md](../bench/README.md). In short: WAL, `synchronous=NORMAL` (`InsertParallelFull`: `FULL`), one `*sql.DB` per benchmark, a file of its own each time, the same settings checked on every driver. Several workloads follow [cvilsmeier/go-sqlite-bench](https://github.com/cvilsmeier/go-sqlite-bench), and every workload checks what it reads. The drivers took turns, one run of every benchmark each, each round starting with the next driver, so that other load on the machine fell on all of them alike. Each cell is the median of the rounds and `±` its 95% confidence interval. The best of a row is in bold when its interval overlaps no other driver's. The tables are written by `bench/cmd/benchrun` from `bench/published/`, never by hand.

| Driver | Version | Built with |
| --- | --- | --- |
| this driver | the commit of each section | C compiled to Go assembly, `CGO_ENABLED=0` |
| mattn/go-sqlite3 | v1.14.52 | cgo |
| modernc.org/sqlite | v1.61.1 | C translated to Go |
| ncruces/go-sqlite3 | v0.36.0 | SQLite as wasm, translated to Go by wasm2go |

## linux/amd64

A Google Cloud VM, AMD EPYC 7B13, 4 cores and 8 threads. System calls are slow on it (`getppid` takes 360 ns), so everything that locks or writes a file costs more than on a desktop. The disk is a persistent disk of the VM: a commit with its fsync takes about 0.6 to 0.9 ms.

### One thread

`GOMAXPROCS=1`, pinned to one core and its sibling thread. The goroutines of the parallel workloads take turns on it.

<!-- bench:linux-amd64-1thread -->
Commit `cc1ede5`, Go 1.27.2, AMD EPYC 7B13, Linux 7.3.0-1003-gcp x86_64, GOMAXPROCS 1 of 2 threads, 2026-11-04. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 1.27 1.12 1.10, after: 2.21 2.20 1.83.

Time per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | **18.7 µs** ±3% | 19.6 µs ±2% | 23.4 µs ±3% | 21.5 µs ±3% |
| InsertTx | **1.30 ms** ±2% | 1.47 ms ±2% | 1.94 ms ±1% | 1.67 ms ±1% |
| Point | **3.58 µs** ±1% | 6.70 µs ±2% | 8.45 µs ±1% | 6.54 µs ±1% |
| Scan | **34.9 ms** ±1% | 73.4 ms ±3% | 76.4 ms ±4% | 50.2 ms ±1% |
| Aggregate | 2.87 ms ±1% | **2.63 ms** ±1% | 4.51 ms ±0% | 4.37 ms ±1% |
| PointParallel | **3.59 µs** ±2% | 6.66 µs ±1% | 8.36 µs ±2% | 6.59 µs ±1% |
| InsertParallel/g=1 | 18.7 µs ±2% | 19.5 µs ±3% | 23.0 µs ±3% | 21.1 µs ±2% |
| InsertParallel/g=8 | **18.5 µs** ±1% | 22.8 µs ±8% | 24.3 µs ±12% | 20.5 µs ±2% |
| InsertParallel/g=64 | **17.4 µs** ±1% | 23.6 µs ±5% | 41.0 µs ±10% | 24.5 µs ±6% |
| InsertParallelFull/g=1 | 842 µs ±8% | 974 µs ±6% | 947 µs ±13% | 888 µs ±15% |
| InsertParallelFull/g=8 | **188 µs** ±18% | 2.53 ms ±12% | 2.26 ms ±15% | 937 µs ±9% |
| InsertParallelFull/g=64 | **21.0 µs** ±7% | 2.48 ms ±23% | 5.31 ms ±23% | 1.12 ms ±8% |
| Real/insert | **1.41 ms** ±1% | 1.54 ms ±2% | 2.00 ms ±2% | 1.90 ms ±0% |
| Real/query | **509 µs** ±1% | 830 µs ±1% | 1.05 ms ±2% | 762 µs ±1% |
| Many/n=10 | **8.70 µs** ±1% | 15.2 µs ±1% | 16.7 µs ±1% | 13.5 µs ±0% |
| Many/n=100 | **58.3 µs** ±1% | 101 µs ±2% | 100 µs ±1% | 78.3 µs ±1% |
| Many/n=1000 | **584 µs** ±3% | 970 µs ±1% | 945 µs ±1% | 744 µs ±1% |
| Large/size=50k | 2.24 ms ±4% | 1.84 ms ±10% | 2.13 ms ±5% | 2.03 ms ±5% |
| Large/size=200k | 8.85 ms ±5% | **6.99 ms** ±7% | 8.13 ms ±4% | 8.02 ms ±5% |
| Readers/g=2 | **34.8 ms** ±1% | 74.3 ms ±2% | 77.9 ms ±1% | 51.5 ms ±2% |
| Readers/g=4 | **35.2 ms** ±2% | 74.0 ms ±2% | 77.9 ms ±1% | 51.6 ms ±1% |
| Readers/g=8 | **35.3 ms** ±1% | 75.1 ms ±1% | 79.6 ms ±2% | 52.1 ms ±1% |
| ReadWrite | **2.65 µs** ±7% | 4.07 µs ±10% | 5.78 µs ±10% | 7.60 µs ±2% |
| geomean | 286 µs | 539 µs | 648 µs | 487 µs |

Rates, higher is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2233 MB/s ±4% | 2716 MB/s ±9% | 2344 MB/s ±4% | 2461 MB/s ±5% |
| Large/size=200k | 2260 MB/s ±5% | **2859 MB/s** ±7% | 2459 MB/s ±4% | 2493 MB/s ±5% |
| ReadWrite | 6516 writes/s ±35% | 5947 writes/s ±33% | 6367 writes/s ±27% | 6205 writes/s ±18% |

Allocations per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | **5** | 6 | 7 | **5** |
| InsertTx | 5.50k | 6.51k | 7.50k | **4.76k** |
| Point | **14** | 19 | 31 | 18 |
| Scan | **400k** | 800k | 800k | **400k** |
| Aggregate | **1.01k** | 2.00k | 2.01k | **1.01k** |
| PointParallel | **13** | 18 | 30 | 18 |
| InsertParallel/g=1 | **4** | 5 | 6 | 5 |
| InsertParallel/g=8 | **4** | 5 | 6 | 5 |
| InsertParallel/g=64 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 9 | **6** | 7 | 13 |
| InsertParallelFull/g=64 | **9** | 14 | 28 | 85 |
| Real/insert | 4.21k | 4.64k | 5.04k | **2.96k** |
| Real/query | **4.44k** | 7.90k | 7.93k | **4.44k** |
| Many/n=10 | **57** | 78 | 90 | 67 |
| Many/n=100 | **507** | 708 | 720 | 517 |
| Many/n=1000 | **5.75k** | 8.50k | 8.51k | 5.76k |
| Large/size=50k | **407** | 608 | 620 | 417 |
| Large/size=200k | **407** | 608 | 620 | 417 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **14** | 19 | 31 | 19 |

<!-- /bench:linux-amd64-1thread -->

### All 8 threads

Nothing else running on the machine. Only the workloads that use several goroutines.

<!-- bench:linux-amd64-8threads -->
Commit `cc1ede5`, Go 1.27.2, AMD EPYC 7B13, Linux 7.3.0-1003-gcp x86_64, GOMAXPROCS 8 of 8 threads, 2026-11-04. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 1.45 1.78 1.75, after: 2.83 2.50 2.10.

Time per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **1.65 µs** ±8% | 2.00 µs ±66% | 2.18 µs ±14% | 2.29 µs ±55% |
| InsertParallel/g=1 | **17.6 µs** ±1% | 18.8 µs ±3% | 21.4 µs ±1% | 20.3 µs ±4% |
| InsertParallel/g=8 | **9.43 µs** ±14% | 19.4 µs ±8% | 24.2 µs ±7% | 21.9 µs ±1% |
| InsertParallel/g=64 | **3.40 µs** ±12% | 20.2 µs ±5% | 27.0 µs ±5% | 27.0 µs ±4% |
| InsertParallelFull/g=1 | 848 µs ±14% | 939 µs ±27% | 933 µs ±7% | 789 µs ±18% |
| InsertParallelFull/g=8 | **195 µs** ±11% | 2.21 ms ±29% | 2.49 ms ±19% | 802 µs ±17% |
| InsertParallelFull/g=64 | **19.3 µs** ±6% | 22.9 ms ±24% | 33.0 ms ±32% | 941 µs ±14% |
| Readers/g=2 | **17.9 ms** ±3% | 37.0 ms ±4% | 38.9 ms ±8% | 27.9 ms ±9% |
| Readers/g=4 | **10.7 ms** ±9% | 22.9 ms ±3% | 24.0 ms ±2% | 16.7 ms ±5% |
| Readers/g=8 | **8.30 ms** ±17% | 17.7 ms ±2% | 18.2 ms ±11% | 12.6 ms ±3% |
| ReadWrite | 2.59 µs ±39% | 4.27 µs ±20% | 5.85 µs ±14% | 3.34 µs ±3% |
| geomean | 102 µs | 403 µs | 467 µs | 257 µs |

Rates, higher is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | 17974 writes/s ±25% | 12842 writes/s ±7% | 7439 writes/s ±6% | **28840 writes/s** ±3% |

Allocations per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **13** | 18 | 30 | 19 |
| InsertParallel/g=1 | **4** | 5 | 6 | 5 |
| InsertParallel/g=8 | 9 | **5** | 6 | **5** |
| InsertParallel/g=64 | 9 | **5** | 6 | 6 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 9 | 5 | 7 | 11 |
| InsertParallelFull/g=64 | **9** | 44 | 55 | 72 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **14** | 19 | 31 | 19 |

<!-- /bench:linux-amd64-8threads -->

## darwin/arm64

<!-- bench:darwin-arm64 -->
Commit `90e0e83`, Go 1.27.2, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-04. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 1.74 2.04 2.23, after: 6.79 5.38 4.28. Other work ran on the machine: read differences under 30% as noise.

Time per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 6.32 µs ±16% | 6.21 µs ±28% | 7.96 µs ±19% | 7.00 µs ±9% |
| InsertTx | **456 µs** ±8% | 594 µs ±35% | 748 µs ±20% | 583 µs ±22% |
| Point | **1.12 µs** ±10% | 1.96 µs ±22% | 2.86 µs ±71% | 2.07 µs ±14% |
| Scan | **14.4 ms** ±19% | 31.6 ms ±18% | 30.1 ms ±23% | 20.7 ms ±17% |
| Aggregate | 1.56 ms ±15% | 1.19 ms ±18% | 2.65 ms ±33% | 1.90 ms ±17% |
| PointParallel | 1.29 µs ±36% | 1.69 µs ±34% | 1.79 µs ±73% | 2.49 µs ±9% |
| InsertParallel/g=1 | 6.29 µs ±15% | 6.26 µs ±4% | 7.33 µs ±55% | 6.91 µs ±15% |
| InsertParallel/g=8 | **3.30 µs** ±28% | 6.94 µs ±6% | 8.12 µs ±19% | 43.5 µs ±53% |
| InsertParallel/g=64 | **1.37 µs** ±31% | 9.53 µs ±12% | 8.28 ms ±100% | 83.3 µs ±24% |
| InsertParallelFull/g=1 | 61.5 µs ±35% | 83.0 µs ±31% | 64.8 µs ±71% | 61.1 µs ±17% |
| InsertParallelFull/g=8 | **19.8 µs** ±62% | 94.7 µs ±26% | 120 µs ±33% | 54.7 µs ±50% |
| InsertParallelFull/g=64 | **2.33 µs** ±19% | 158 µs ±52% | 22.0 ms ±84% | 87.3 µs ±16% |
| Real/insert | 578 µs ±16% | 569 µs ±71% | 1.05 ms ±17% | 718 µs ±12% |
| Real/query | **236 µs** ±17% | 396 µs ±16% | 594 µs ±7% | 345 µs ±4% |
| Many/n=10 | **2.93 µs** ±40% | 5.48 µs ±58% | 5.51 µs ±6% | 4.45 µs ±4% |
| Many/n=100 | 19.8 µs ±38% | 38.7 µs ±10% | 37.0 µs ±9% | 26.9 µs ±12% |
| Many/n=1000 | **196 µs** ±17% | 377 µs ±68% | 351 µs ±6% | 254 µs ±12% |
| Large/size=50k | 1.00 ms ±17% | 971 µs ±191% | 1.01 ms ±21% | 873 µs ±22% |
| Large/size=200k | 4.87 ms ±30% | 3.58 ms ±19% | 3.99 ms ±16% | 3.64 ms ±13% |
| Readers/g=2 | 8.76 ms ±112% | 19.2 ms ±17% | 16.7 ms ±22% | 11.4 ms ±26% |
| Readers/g=4 | 6.39 ms ±41% | 13.7 ms ±24% | 12.2 ms ±14% | 7.80 ms ±26% |
| Readers/g=8 | 5.59 ms ±24% | 11.5 ms ±40% | 9.68 ms ±19% | 6.37 ms ±31% |
| ReadWrite | 2.04 µs ±36% | 3.41 µs ±33% | 2.83 µs ±26% | 5.10 µs ±6% |
| geomean | 78.3 µs | 152 µs | 281 µs | 159 µs |

Rates, higher is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 5000 MB/s ±14% | 5170 MB/s ±66% | 4976 MB/s ±17% | 5728 MB/s ±18% |
| Large/size=200k | 4114 MB/s ±23% | 5592 MB/s ±16% | 5014 MB/s ±14% | 5490 MB/s ±12% |
| ReadWrite | 23887 writes/s ±46% | 13377 writes/s ±21% | 24145 writes/s ±23% | 19710 writes/s ±5% |

Allocations per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | **5** | 6 | 7 | **5** |
| InsertTx | 5.50k | 6.51k | 7.50k | **4.76k** |
| Point | **14** | 19 | 31 | 18 |
| Scan | **400k** | 800k | 800k | **400k** |
| Aggregate | **1.01k** | 2.00k | 2.01k | **1.01k** |
| PointParallel | **13** | 18 | 30 | 19 |
| InsertParallel/g=1 | **4** | 5 | 6 | 5 |
| InsertParallel/g=8 | 9 | **5** | 6 | **5** |
| InsertParallel/g=64 | 9 | **5** | 38 | 7 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 9 | **5** | 6 | **5** |
| InsertParallelFull/g=64 | 9 | **5** | 77 | 7 |
| Real/insert | 4.22k | 4.65k | 5.05k | **2.97k** |
| Real/query | **4.44k** | 7.90k | 7.93k | **4.44k** |
| Many/n=10 | **57** | 78 | 90 | 67 |
| Many/n=100 | **507** | 708 | 720 | 517 |
| Many/n=1000 | **5.75k** | 8.50k | 8.51k | 5.76k |
| Large/size=50k | **407** | 608 | 620 | 417 |
| Large/size=200k | **407** | 609 | 620 | 417 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **14** | 19 | 31 | 19 |

<!-- /bench:darwin-arm64 -->

## Replication

[Replication](replication.md) copies each commit inside the program, so it costs CPU in the program's process. `BenchmarkGroupCommit` in `replica` inserts one row per call from 8 or 64 goroutines at once (WAL, `synchronous=NORMAL`), with replication off and on. The store is a MinIO on two other threads of the same machine, so that its work does not fall on the benchmark's, as a store on another machine's would not.

Commit `def7170`, Go 1.27.2, the linux/amd64 VM above, Linux 7.3.0-1003-gcp. 6 runs each, compared with benchstat (`~` is no difference it can tell from noise). Load average 1.12 before and 1.92 after.

One thread (`GOMAXPROCS=1`, cores 0 and 4, MinIO on 3 and 7):

```
              replica off    replica on
g=8           18.72µ ± 2%    19.90µ ± 2%   +6.31% (p=0.002 n=6)
g=64          17.59µ ± 3%    19.59µ ± 1%  +11.38% (p=0.002 n=6)
geomean       18.14µ         19.74µ        +8.82%
```

Six threads (`GOMAXPROCS=6`, MinIO on the other two):

```
              replica off    replica on
g=8            9.810µ ±  2%  10.893µ ± 9%  +11.04% (p=0.002 n=6)
g=64           3.577µ ± 20%   3.921µ ± 7%        ~ (p=0.065 n=6)
geomean        5.923µ         6.535µ       +10.33%
```

So an insert costs about 6 to 11% more with replication on. At 64 writers on six threads one slow run of the six without replication left the difference inside the noise. Run it with a MinIO binary, pinned away from the benchmark:

```sh
MINIO=<minio> go test ./replica -run '^$' -bench GroupCommit -benchtime 3s -count 6
```

## What the numbers say

- **Reads** are faster than with cgo: one row by key in about half the time (3.58 µs against 6.54 to 8.45 µs on linux), 100k rows in 35 ms against 50 to 76 ms, a user with its 400 comments in 509 µs against 762 µs to 1.05 ms. A call into the engine is a plain Go call, with no cgo crossing, and a read transaction takes no lock in the kernel when another read of the process is running or just ended (see `internal/vfs`).
- **Many writers** are where group commit shows: from 8 writers up, one commit carries many inserts. At 64 writers an insert costs 3.40 µs against 20 to 27 µs with the other drivers (8 threads). With `synchronous=FULL` the gap grows with the cost of a commit: at 64 writers an insert costs 19 to 21 µs against 0.94 to 33 ms. On the busy Mac, modernc at 64 writers spends its time in SQLite's busy handler, whose sleeps grow while the writer that holds the lock waits for a thread.
- **Readers next to a writer** (ReadWrite, 8 threads): this driver serves the readers fastest (2.59 µs a read against 3.34 to 5.85 µs), and ncruces lets the writer do the most (28.8k writes a second against 18.0k).
- **One writer** (Insert) is close to mattn on both machines. The time goes to the WAL write and the locks SQLite's protocol asks for, the same system calls for every driver.
- **Join and group** (Aggregate), which is all work inside the engine, is 9% (linux) to 31% (darwin) slower than with cgo: the engine is the same C, compiled by LLVM, and the Go assembly it becomes is close to, but not as good as, what clang makes for the machine.
- **Large text values** are read slower than with every other driver: 100 rows of 200 KB take 8.85 ms against 6.99 to 8.13 ms on linux. Where that time goes is not measured yet.
- **Allocations**: a number is copied straight into the `Scan` destination (`driver.RowsColumnScanner`, Go 1.27), without boxing it in an interface, so a scan allocates half as often as with mattn and modernc.
- **Point on linux** includes one `fstat` per query, for every driver: when the WAL is empty, as it is in a fresh copy of the fixture, SQLite reads the size of the database file at the start of each read. A system call costs about 0.8 µs on this VM, which is most of the gap between linux and darwin in that row.

## Running them again

`make bench` on a quiet machine, `benchrun publish` to make a set of `bench/published/`, then `make bench-docs` to write the tables. The steps and the options are in [bench/README.md](../bench/README.md). On a shared machine, pin the single-thread runs to one core (`taskset`, `GOMAXPROCS=1`) and take the parallel runs only when nothing else runs.
