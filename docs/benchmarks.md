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
Commit `bc87680`, Go 1.27.2, AMD EPYC 7B13, Linux 7.3.0-1003-gcp x86_64, GOMAXPROCS 1 of 2 threads, 2026-11-06. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.16 0.03 0.01, after: 1.13 1.09 0.73.

Time per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | **18.5 µs** ±2% | 19.5 µs ±3% | 22.9 µs ±4% | 21.0 µs ±4% |
| InsertTx | **1.30 ms** ±1% | 1.46 ms ±1% | 1.95 ms ±1% | 1.65 ms ±1% |
| Point | **3.61 µs** ±1% | 6.69 µs ±1% | 8.32 µs ±2% | 6.53 µs ±1% |
| Scan | **34.4 ms** ±3% | 72.0 ms ±3% | 71.3 ms ±2% | 50.7 ms ±1% |
| Aggregate | 2.87 ms ±0% | **2.62 ms** ±1% | 4.50 ms ±2% | 4.34 ms ±1% |
| PointParallel | **3.60 µs** ±0% | 6.59 µs ±2% | 8.34 µs ±2% | 6.54 µs ±1% |
| InsertParallel/g=1 | **18.5 µs** ±1% | 19.3 µs ±3% | 22.9 µs ±5% | 21.1 µs ±1% |
| InsertParallel/g=8 | **18.6 µs** ±1% | 22.9 µs ±3% | 24.0 µs ±6% | 20.4 µs ±4% |
| InsertParallel/g=64 | **17.5 µs** ±0% | 23.0 µs ±3% | 38.7 µs ±17% | 24.3 µs ±2% |
| InsertParallelFull/g=1 | 849 µs ±6% | 945 µs ±11% | 957 µs ±11% | 869 µs ±10% |
| InsertParallelFull/g=8 | **194 µs** ±14% | 2.28 ms ±26% | 2.14 ms ±29% | 1.01 ms ±15% |
| InsertParallelFull/g=64 | **21.3 µs** ±6% | 2.29 ms ±12% | 4.26 ms ±28% | 1.10 ms ±8% |
| Real/insert | **1.41 ms** ±1% | 1.50 ms ±1% | 1.98 ms ±1% | 1.87 ms ±2% |
| Real/query | **514 µs** ±1% | 836 µs ±1% | 1.01 ms ±1% | 755 µs ±0% |
| Many/n=10 | **8.71 µs** ±1% | 15.1 µs ±1% | 16.0 µs ±1% | 13.3 µs ±2% |
| Many/n=100 | **59.3 µs** ±0% | 100 µs ±0% | 93.9 µs ±1% | 78.3 µs ±0% |
| Many/n=1000 | **601 µs** ±2% | 957 µs ±1% | 910 µs ±4% | 739 µs ±0% |
| Large/size=50k | 1.87 ms ±3% | 1.80 ms ±4% | 2.12 ms ±1% | 2.01 ms ±1% |
| Large/size=200k | 7.23 ms ±1% | **6.77 ms** ±4% | 7.89 ms ±6% | 7.81 ms ±4% |
| Readers/g=2 | **35.0 ms** ±1% | 73.9 ms ±2% | 72.7 ms ±2% | 51.1 ms ±0% |
| Readers/g=4 | **35.2 ms** ±1% | 74.8 ms ±3% | 73.1 ms ±1% | 51.4 ms ±0% |
| Readers/g=8 | **35.5 ms** ±1% | 75.4 ms ±1% | 74.0 ms ±1% | 51.8 ms ±0% |
| ReadWrite | **2.68 µs** ±8% | 4.16 µs ±7% | 5.56 µs ±15% | 7.54 µs ±2% |
| geomean | 282 µs | 530 µs | 623 µs | 484 µs |

Rates, higher is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2672 MB/s ±3% | 2775 MB/s ±4% | 2359 MB/s ±1% | 2486 MB/s ±1% |
| Large/size=200k | 2764 MB/s ±1% | **2955 MB/s** ±3% | 2535 MB/s ±6% | 2562 MB/s ±4% |
| ReadWrite | 6438 writes/s ±51% | 6773 writes/s ±36% | 6160 writes/s ±81% | 6192 writes/s ±6% |

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
| InsertParallel/g=8 | **5** | 6 | 7 | 6 |
| InsertParallel/g=64 | **5** | 6 | 7 | 6 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 10 | **6** | 7 | 14 |
| InsertParallelFull/g=64 | **10** | 13 | 24 | 84 |
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
Commit `bc87680`, Go 1.27.2, AMD EPYC 7B13, Linux 7.3.0-1003-gcp x86_64, GOMAXPROCS 8 of 8 threads, 2026-11-06. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 1.13 1.09 0.73, after: 2.77 2.51 1.60.

Time per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | 1.69 µs ±81% | 2.31 µs ±30% | 2.25 µs ±35% | 2.09 µs ±52% |
| InsertParallel/g=1 | 18.0 µs ±3% | 19.0 µs ±3% | 22.1 µs ±2% | 20.3 µs ±3% |
| InsertParallel/g=8 | **9.56 µs** ±15% | 19.3 µs ±6% | 24.7 µs ±4% | 21.8 µs ±1% |
| InsertParallel/g=64 | **3.60 µs** ±16% | 20.3 µs ±20% | 27.6 µs ±6% | 26.6 µs ±3% |
| InsertParallelFull/g=1 | 804 µs ±15% | 882 µs ±14% | 924 µs ±9% | 840 µs ±4% |
| InsertParallelFull/g=8 | **183 µs** ±10% | 2.43 ms ±89% | 2.64 ms ±19% | 898 µs ±15% |
| InsertParallelFull/g=64 | **19.5 µs** ±11% | 25.4 ms ±20% | 27.0 ms ±28% | 939 µs ±17% |
| Readers/g=2 | **18.1 ms** ±3% | 38.5 ms ±5% | 41.5 ms ±2% | 28.5 ms ±3% |
| Readers/g=4 | **10.9 ms** ±6% | 23.0 ms ±16% | 25.0 ms ±7% | 16.7 ms ±6% |
| Readers/g=8 | **8.42 ms** ±8% | 18.1 ms ±35% | 18.2 ms ±2% | 12.8 ms ±5% |
| ReadWrite | 2.81 µs ±23% | 4.35 µs ±27% | 5.79 µs ±31% | 3.25 µs ±51% |
| geomean | 103 µs | 416 µs | 470 µs | 258 µs |

Rates, higher is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | 16992 writes/s ±12% | 12806 writes/s ±12% | 7563 writes/s ±22% | **28731 writes/s** ±25% |

Allocations per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **13** | 18 | 30 | 19 |
| InsertParallel/g=1 | **4** | 5 | 6 | 5 |
| InsertParallel/g=8 | 10 | **6** | 7 | **6** |
| InsertParallel/g=64 | 10 | **6** | 7 | 7 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 10 | **6** | 7 | 13 |
| InsertParallelFull/g=64 | **10** | 44 | 55 | 74 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **14** | 19 | 31 | 19 |

<!-- /bench:linux-amd64-8threads -->

## darwin/arm64

<!-- bench:darwin-arm64 -->
Commit `92ec4bf`, Go 1.27.2, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-06. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 7.03 3.81 3.05, after: 3.93 3.57 3.26. Other work ran on the machine: read differences under 30% as noise.

Time per op, lower is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 6.28 µs ±4% | 5.93 µs ±17% | 7.40 µs ±7% | 6.92 µs ±2% |
| InsertTx | 479 µs ±22% | 556 µs ±12% | 739 µs ±3% | 570 µs ±8% |
| Point | **1.15 µs** ±57% | 1.93 µs ±18% | 2.78 µs ±2% | 2.06 µs ±12% |
| Scan | **14.2 ms** ±9% | 30.0 ms ±40% | 29.8 ms ±3% | 20.4 ms ±11% |
| Aggregate | 1.34 ms ±13% | **1.14 ms** ±15% | 2.62 ms ±1% | 1.85 ms ±19% |
| PointParallel | **1.17 µs** ±7% | 1.49 µs ±4% | 1.74 µs ±5% | 2.37 µs ±12% |
| InsertParallel/g=1 | 6.24 µs ±10% | 6.01 µs ±4% | 7.31 µs ±3% | 7.01 µs ±12% |
| InsertParallel/g=8 | **3.09 µs** ±5% | 6.86 µs ±84% | 8.15 µs ±10% | 36.8 µs ±19% |
| InsertParallel/g=64 | **1.16 µs** ±14% | 10.0 µs ±17% | 5.65 ms ±77% | 81.1 µs ±19% |
| InsertParallelFull/g=1 | 64.2 µs ±63% | 52.0 µs ±58% | 53.9 µs ±39% | 58.0 µs ±191% |
| InsertParallelFull/g=8 | **12.4 µs** ±102% | 55.5 µs ±56% | 106 µs ±57% | 51.3 µs ±275% |
| InsertParallelFull/g=64 | **1.66 µs** ±144% | 108 µs ±86% | 29.5 ms ±26% | 88.2 µs ±71% |
| Real/insert | 570 µs ±2% | 586 µs ±6% | 1.01 ms ±1% | 742 µs ±7% |
| Real/query | **226 µs** ±1% | 367 µs ±3% | 553 µs ±5% | 341 µs ±9% |
| Many/n=10 | **2.75 µs** ±15% | 4.94 µs ±2% | 5.30 µs ±2% | 4.37 µs ±7% |
| Many/n=100 | **19.6 µs** ±19% | 34.6 µs ±3% | 33.1 µs ±0% | 26.7 µs ±7% |
| Many/n=1000 | **193 µs** ±11% | 339 µs ±2% | 319 µs ±1% | 252 µs ±2% |
| Large/size=50k | 820 µs ±8% | 812 µs ±20% | 899 µs ±5% | 871 µs ±10% |
| Large/size=200k | 3.39 ms ±6% | 3.36 ms ±20% | 3.73 ms ±14% | 3.62 ms ±4% |
| Readers/g=2 | **7.74 ms** ±5% | 16.0 ms ±18% | 15.8 ms ±1% | 11.4 ms ±16% |
| Readers/g=4 | **5.27 ms** ±18% | 10.7 ms ±20% | 10.2 ms ±74% | 7.67 ms ±68% |
| Readers/g=8 | **4.68 ms** ±17% | 9.12 ms ±24% | 8.65 ms ±22% | 6.45 ms ±22% |
| ReadWrite | **1.43 µs** ±16% | 2.28 µs ±11% | 2.54 µs ±11% | 4.86 µs ±8% |
| geomean | 69.5 µs | 131 µs | 262 µs | 155 µs |

Rates, higher is better:

| Benchmark | this driver | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 6094 MB/s ±8% | 6159 MB/s ±16% | 5562 MB/s ±5% | 5743 MB/s ±9% |
| Large/size=200k | 5906 MB/s ±6% | 5949 MB/s ±17% | 5365 MB/s ±12% | 5523 MB/s ±4% |
| ReadWrite | **32601 writes/s** ±9% | 16171 writes/s ±11% | 27775 writes/s ±8% | 20503 writes/s ±7% |

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
| InsertParallel/g=8 | 11 | **6** | 7 | **6** |
| InsertParallel/g=64 | 10 | **6** | 32 | 8 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 10 | **6** | 7 | **6** |
| InsertParallelFull/g=64 | 10 | **6** | 77 | 8 |
| Real/insert | 4.22k | 4.65k | 5.05k | **2.97k** |
| Real/query | **4.44k** | 7.91k | 7.93k | **4.44k** |
| Many/n=10 | **57** | 78 | 90 | 67 |
| Many/n=100 | **507** | 708 | 720 | 517 |
| Many/n=1000 | **5.75k** | 8.50k | 8.51k | 5.76k |
| Large/size=50k | **407** | 608 | 620 | 417 |
| Large/size=200k | **407** | 608 | 621 | 417 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **14** | 19 | 31 | 19 |

<!-- /bench:darwin-arm64 -->

## Replication

[Replication](replication.md) copies each commit inside the program, so it costs CPU in the program's process. `BenchmarkGroupCommit` in `replica` inserts one row per call from 8 or 64 goroutines at once (WAL, `synchronous=NORMAL`), with replication off and on. The store is a MinIO on two other threads of the same machine, so that its work does not fall on the benchmark's, as a store on another machine's would not.

Commit `def7170`, Go 1.27.2, the linux/amd64 VM above, Linux 7.3.0-1003-gcp. 6 runs each, compared with benchstat (`~` is no difference it can tell from noise). Load average 1.12 before and 1.92 after. The raw output is in [bench/published/replica/](../bench/published/replica/).

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

- **Reads** are faster than with cgo: one row by key in about half the time (3.61 µs against 6.53 to 8.32 µs on linux), 100k rows in 34 ms against 51 to 72 ms, a user with its 400 comments in 514 µs against 755 µs to 1.01 ms. A call into the engine is a plain Go call, with no cgo crossing, and a read transaction takes no lock in the kernel when another read of the process is running or just ended (see `internal/vfs`).
- **Many writers** are where group commit shows: from 8 writers up, one commit carries many inserts. At 64 writers an insert costs 3.60 µs against 20 to 28 µs with the other drivers (8 threads). With `synchronous=FULL` the gap grows with the cost of a commit: at 64 writers an insert costs 19.5 µs against 0.94 to 27 ms. On the busy Mac, modernc at 64 writers spends its time in SQLite's busy handler, whose sleeps grow while the writer that holds the lock waits for a thread.
- **Readers next to a writer** (ReadWrite, 8 threads): this driver serves the readers fastest (2.81 µs a read against 3.25 to 5.79 µs), and ncruces lets the writer do the most (28.7k writes a second against 17.0k).
- **One writer** (Insert) is close to mattn on both machines. The time goes to the WAL write and the locks SQLite's protocol asks for, the same system calls for every driver.
- **Join and group** (Aggregate), which is all work inside the engine, is 10% (linux) to 18% (darwin) slower than with cgo: the engine is the same C, compiled by LLVM, and the Go assembly it becomes is close to, but not as good as, what clang makes for the machine.
- **Large text values** are read 7% slower than with mattn on linux (100 rows of 200 KB in 7.23 ms against 6.77 ms) and faster than with modernc and ncruces. On darwin the four are even.
- **Allocations**: a number is copied straight into the `Scan` destination (`driver.RowsColumnScanner`, Go 1.27), without boxing it in an interface, so a scan allocates half as often as with mattn and modernc.
- **Point on linux** includes one `fstat` per query, for every driver: when the WAL is empty, as it is in a fresh copy of the fixture, SQLite reads the size of the database file at the start of each read. A system call costs about 0.8 µs on this VM, which is most of the gap between linux and darwin in that row.

## Running them again

`make bench` on a quiet machine, `benchrun publish` to make a set of `bench/published/`, then `make bench-docs` to write the tables. The steps and the options are in [bench/README.md](../bench/README.md). On a shared machine, pin the single-thread runs to one core (`taskset`, `GOMAXPROCS=1`) and take the parallel runs only when nothing else runs.
