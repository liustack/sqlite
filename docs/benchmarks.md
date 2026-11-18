---
summary: 'How fast the driver is against mattn/go-sqlite3 (cgo), modernc.org/sqlite and ncruces/go-sqlite3, on linux/amd64 and darwin/arm64, against SQLite in C, what replication costs, and how the numbers were taken'
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

A Google Cloud VM (t2d-standard-4), AMD EPYC 7B13, 4 cores with one thread each. System calls are slow on it (`getppid` takes 415 ns), so everything that locks or writes a file costs more than on a desktop. The disk is a persistent disk of the VM: a commit with its fsync takes about 1.2 to 1.3 ms.

### One thread

`GOMAXPROCS=1`, pinned to one core. The goroutines of the parallel workloads take turns on it.

<!-- bench:linux-amd64-1thread -->
Commit `18a2707`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 1 of 1 threads, 2026-11-18. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 1.65 1.50 1.61, after: 1.11 1.23 1.37.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 22.8 µs ±7% | 23.3 µs ±1% | 26.6 µs ±1% | 24.6 µs ±3% |
| InsertTx | **1.38 ms** ±2% | 1.69 ms ±2% | 2.22 ms ±1% | 1.95 ms ±0% |
| Point | **3.86 µs** ±2% | 7.56 µs ±1% | 9.44 µs ±1% | 7.13 µs ±1% |
| Scan | **34.5 ms** ±2% | 79.9 ms ±1% | 85.5 ms ±1% | 52.7 ms ±1% |
| Aggregate | 2.86 ms ±2% | 2.88 ms ±2% | 5.02 ms ±1% | 4.88 ms ±1% |
| PointParallel | **3.85 µs** ±1% | 7.48 µs ±1% | 9.35 µs ±2% | 7.16 µs ±1% |
| InsertParallel/g=1 | 22.6 µs ±1% | 23.3 µs ±4% | 26.5 µs ±12% | 24.6 µs ±2% |
| InsertParallel/g=8 | 23.1 µs ±1% | 22.0 µs ±15% | 25.9 µs ±5% | 24.3 µs ±2% |
| InsertParallel/g=64 | 20.7 µs ±1% | 22.1 µs ±11% | 29.1 µs ±8% | 29.3 µs ±3% |
| InsertParallelFull/g=1 | 1.27 ms ±8% | 1.31 ms ±7% | 1.28 ms ±14% | 1.30 ms ±18% |
| InsertParallelFull/g=8 | **334 µs** ±4% | 2.87 ms ±29% | 3.02 ms ±11% | 1.29 ms ±5% |
| InsertParallelFull/g=64 | **30.3 µs** ±15% | 4.01 ms ±58% | 6.73 ms ±41% | 1.47 ms ±5% |
| Real/insert | **1.55 ms** ±2% | 1.80 ms ±1% | 2.27 ms ±2% | 2.15 ms ±1% |
| Real/query | **523 µs** ±2% | 880 µs ±1% | 1.16 ms ±2% | 829 µs ±1% |
| Many/n=10 | **8.64 µs** ±3% | 16.8 µs ±1% | 18.7 µs ±1% | 14.2 µs ±0% |
| Many/n=100 | **57.6 µs** ±2% | 110 µs ±1% | 113 µs ±1% | 79.6 µs ±1% |
| Many/n=1000 | **557 µs** ±9% | 1.04 ms ±1% | 1.08 ms ±1% | 750 µs ±2% |
| Large/size=50k | 2.19 ms ±3% | 2.09 ms ±3% | 2.40 ms ±6% | 2.34 ms ±3% |
| Large/size=200k | 8.78 ms ±2% | 8.31 ms ±9% | 9.57 ms ±1% | 9.32 ms ±1% |
| Readers/g=2 | **35.5 ms** ±1% | 81.0 ms ±3% | 85.7 ms ±1% | 53.0 ms ±1% |
| Readers/g=4 | **35.4 ms** ±1% | 81.5 ms ±2% | 85.8 ms ±0% | 53.3 ms ±0% |
| Readers/g=8 | **35.8 ms** ±1% | 82.2 ms ±2% | 86.8 ms ±1% | 54.0 ms ±1% |
| ReadWrite | **2.83 µs** ±7% | 4.48 µs ±7% | 6.82 µs ±14% | 8.23 µs ±2% |
| geomean | 318 µs | 609 µs | 729 µs | 551 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2281 MB/s ±3% | 2392 MB/s ±3% | 2086 MB/s ±5% | 2138 MB/s ±3% |
| Large/size=200k | 2278 MB/s ±2% | 2406 MB/s ±8% | 2089 MB/s ±1% | 2147 MB/s ±2% |
| ReadWrite | 7938 writes/s ±46% | 4287 writes/s ±44% | 7871 writes/s ±30% | 5065 writes/s ±12% |

Allocations per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 6 | 6 | 7 | **5** |
| InsertTx | 6.50k | 6.51k | 7.50k | **4.76k** |
| Point | **15** | 19 | 31 | 18 |
| Scan | **400k** | 800k | 800k | **400k** |
| Aggregate | **1.01k** | 2.00k | 2.01k | **1.01k** |
| PointParallel | **14** | 18 | 30 | 18 |
| InsertParallel/g=1 | **5** | **5** | 6 | **5** |
| InsertParallel/g=8 | **6** | **6** | 7 | **6** |
| InsertParallel/g=64 | **6** | **6** | 7 | 7 |
| InsertParallelFull/g=1 | **5** | **5** | 6 | **5** |
| InsertParallelFull/g=8 | 10 | **6** | 7 | 16 |
| InsertParallelFull/g=64 | **11** | 19 | 33 | 112 |
| Real/insert | 4.63k | 4.63k | 5.04k | **2.95k** |
| Real/query | **4.44k** | 7.90k | 7.93k | **4.44k** |
| Many/n=10 | **58** | 78 | 90 | 67 |
| Many/n=100 | **508** | 708 | 720 | 517 |
| Many/n=1000 | **5.75k** | 8.50k | 8.51k | 5.76k |
| Large/size=50k | **408** | 608 | 620 | 417 |
| Large/size=200k | **408** | 608 | 620 | 417 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **15** | 19 | 31 | 19 |

<!-- /bench:linux-amd64-1thread -->

### All 4 cores

Nothing else running on the machine. Only the workloads that use several goroutines.

<!-- bench:linux-amd64-4threads -->
Commit `18a2707`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-18. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.00 0.00 0.10, after: 2.20 1.87 0.99.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **1.78 µs** ±15% | 3.02 µs ±13% | 3.92 µs ±10% | 2.82 µs ±24% |
| InsertParallel/g=1 | **22.9 µs** ±1% | **22.9 µs** ±1% | 25.5 µs ±1% | 23.5 µs ±1% |
| InsertParallel/g=8 | **10.7 µs** ±4% | 20.9 µs ±16% | 27.0 µs ±5% | 24.0 µs ±2% |
| InsertParallel/g=64 | **3.52 µs** ±10% | 22.3 µs ±13% | 30.6 µs ±5% | 29.3 µs ±5% |
| InsertParallelFull/g=1 | 1.24 ms ±4% | 1.19 ms ±10% | 1.19 ms ±8% | 1.18 ms ±13% |
| InsertParallelFull/g=8 | **311 µs** ±7% | 1.75 ms ±63% | 1.62 ms ±11% | 1.25 ms ±9% |
| InsertParallelFull/g=64 | **26.7 µs** ±5% | 23.5 ms ±13% | 19.5 ms ±62% | 1.37 ms ±11% |
| Readers/g=2 | **17.2 ms** ±2% | 39.0 ms ±1% | 40.4 ms ±4% | 27.8 ms ±1% |
| Readers/g=4 | **9.90 ms** ±14% | 21.2 ms ±5% | 23.0 ms ±8% | 14.6 ms ±5% |
| Readers/g=8 | **9.36 ms** ±2% | 21.3 ms ±3% | 21.2 ms ±6% | 14.7 ms ±2% |
| ReadWrite | **2.61 µs** ±14% | 4.75 µs ±20% | 7.94 µs ±12% | 4.54 µs ±11% |
| geomean | 118 µs | 444 µs | 500 µs | 310 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | 12401 writes/s ±8% | 14219 writes/s ±12% | 4436 writes/s ±11% | **26085 writes/s** ±3% |

Allocations per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **14** | 18 | 30 | 19 |
| InsertParallel/g=1 | **5** | **5** | 6 | **5** |
| InsertParallel/g=8 | 10 | **6** | 7 | **6** |
| InsertParallel/g=64 | 11 | **6** | 7 | 8 |
| InsertParallelFull/g=1 | **5** | **5** | 6 | **5** |
| InsertParallelFull/g=8 | 10 | **6** | 7 | 15 |
| InsertParallelFull/g=64 | **11** | 44 | 54 | 97 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **15** | 19 | 31 | 19 |

<!-- /bench:linux-amd64-4threads -->

## darwin/arm64

<!-- bench:darwin-arm64 -->
Commit `4b44e15`, Go 1.27.2, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-18. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 3.10 4.45 4.01, after: 3.13 3.80 4.16.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 6.29 µs ±1% | **5.96 µs** ±2% | 7.30 µs ±1% | 6.97 µs ±5% |
| InsertTx | **473 µs** ±6% | 555 µs ±1% | 750 µs ±3% | 582 µs ±6% |
| Point | **1.08 µs** ±2% | 1.92 µs ±2% | 2.75 µs ±1% | 2.07 µs ±2% |
| Scan | **13.7 ms** ±2% | 29.7 ms ±1% | 29.8 ms ±1% | 20.5 ms ±1% |
| Aggregate | 1.25 ms ±2% | **1.14 ms** ±2% | 2.63 ms ±3% | 1.86 ms ±3% |
| PointParallel | **1.12 µs** ±2% | 1.50 µs ±3% | 1.77 µs ±2% | 2.35 µs ±3% |
| InsertParallel/g=1 | 6.29 µs ±2% | **5.98 µs** ±3% | 7.40 µs ±2% | 7.10 µs ±2% |
| InsertParallel/g=8 | **3.12 µs** ±2% | 6.84 µs ±9% | 8.27 µs ±4% | 44.5 µs ±42% |
| InsertParallel/g=64 | **1.16 µs** ±3% | 9.78 µs ±36% | 3.42 ms ±231% | 85.8 µs ±22% |
| InsertParallelFull/g=1 | 57.7 µs ±60% | 64.3 µs ±35% | 65.2 µs ±31% | 80.2 µs ±65% |
| InsertParallelFull/g=8 | **11.5 µs** ±42% | 70.6 µs ±66% | 81.7 µs ±31% | 50.0 µs ±144% |
| InsertParallelFull/g=64 | **1.66 µs** ±88% | 114 µs ±64% | 28.4 ms ±41% | 86.1 µs ±14% |
| Real/insert | 573 µs ±5% | 575 µs ±9% | 1.02 ms ±2% | 751 µs ±9% |
| Real/query | **212 µs** ±1% | 369 µs ±1% | 559 µs ±3% | 342 µs ±1% |
| Many/n=10 | **2.70 µs** ±2% | 4.85 µs ±2% | 5.33 µs ±1% | 4.44 µs ±1% |
| Many/n=100 | **19.2 µs** ±1% | 34.3 µs ±1% | 33.3 µs ±1% | 26.7 µs ±1% |
| Many/n=1000 | **188 µs** ±1% | 339 µs ±2% | 322 µs ±1% | 253 µs ±2% |
| Large/size=50k | 826 µs ±7% | 824 µs ±3% | 924 µs ±2% | 867 µs ±3% |
| Large/size=200k | 3.41 ms ±2% | 3.38 ms ±3% | 3.75 ms ±6% | 3.56 ms ±1% |
| Readers/g=2 | **7.49 ms** ±1% | 15.8 ms ±6% | 15.8 ms ±2% | 10.9 ms ±2% |
| Readers/g=4 | **4.76 ms** ±2% | 9.74 ms ±2% | 9.83 ms ±2% | 6.98 ms ±3% |
| Readers/g=8 | **4.41 ms** ±3% | 8.58 ms ±3% | 8.50 ms ±3% | 6.07 ms ±2% |
| ReadWrite | **1.17 µs** ±1% | 2.20 µs ±3% | 2.39 µs ±2% | 5.18 µs ±3% |
| geomean | 66.8 µs | 132 µs | 256 µs | 159 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 6052 MB/s ±6% | 6067 MB/s ±3% | 5409 MB/s ±2% | 5768 MB/s ±3% |
| Large/size=200k | 5863 MB/s ±2% | 5912 MB/s ±3% | 5330 MB/s ±6% | 5621 MB/s ±1% |
| ReadWrite | **30172 writes/s** ±2% | 16355 writes/s ±2% | 28584 writes/s ±2% | 19828 writes/s ±3% |

Allocations per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 6 | 6 | 7 | **5** |
| InsertTx | 6.50k | 6.51k | 7.50k | **4.76k** |
| Point | **15** | 19 | 31 | 18 |
| Scan | **400k** | 800k | 800k | **400k** |
| Aggregate | **1.01k** | 2.00k | 2.01k | **1.01k** |
| PointParallel | **14** | 18 | 30 | 19 |
| InsertParallel/g=1 | **5** | **5** | 6 | **5** |
| InsertParallel/g=8 | 11 | **6** | 7 | **6** |
| InsertParallel/g=64 | 11 | **6** | 21 | 8 |
| InsertParallelFull/g=1 | **5** | **5** | 6 | **5** |
| InsertParallelFull/g=8 | 10 | **6** | 7 | **6** |
| InsertParallelFull/g=64 | 11 | **6** | 77 | 8 |
| Real/insert | 4.65k | 4.65k | 5.05k | **2.96k** |
| Real/query | **4.44k** | 7.90k | 7.93k | **4.44k** |
| Many/n=10 | **58** | 78 | 90 | 67 |
| Many/n=100 | **508** | 708 | 720 | 517 |
| Many/n=1000 | **5.75k** | 8.50k | 8.51k | 5.76k |
| Large/size=50k | **408** | 608 | 620 | 417 |
| Large/size=200k | **408** | 608 | 621 | 417 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **15** | 19 | 31 | 19 |

<!-- /bench:darwin-arm64 -->

## Replication

[Replication](replication.md) copies each commit inside the program, so it costs CPU in the program's process. `BenchmarkGroupCommit` in `replica` inserts one row per call from 8 or 64 goroutines at once (WAL, `synchronous=NORMAL`), with replication off and on. The store is a MinIO on two other threads of the same machine, so that its work does not fall on the benchmark's, as a store on another machine's would not.

Commit `def7170`, Go 1.27.2, an earlier VM with the same CPU, 4 cores and 8 threads, Linux 7.3.0-1003-gcp. 6 runs each, compared with benchstat (`~` is no difference it can tell from noise). Load average 1.12 before and 1.92 after. The raw output is in [bench/published/replica/](../bench/published/replica/).

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

## Against SQLite in C

The same SQLite 3.54.1 with the options of `engine/sqlite.c`, built by clang `-O2` into a C program that calls the C API itself. The workloads and the four columns are in [bench/README.md](../bench/README.md#against-sqlite-in-c). In short: SQLite(C) and liustack/sqlite(direct) prepare each statement once and run it again and again, calling the C API or the driver itself. The two columns (database/sql) read and write as a program would: a `*sql.Conn` for each reader with `QueryRowContext`, and `db.Exec` for each write. mattn runs SQLite's C through cgo.

### linux/amd64

<!-- bench:native-linux-amd64 -->
Commit `18a2707`, Go 1.27.4, Ubuntu clang version 22.1.8 (++20260714015917+ca7933e47d3a-1~exp1~20260714135927.17), AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-18. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 2.20 1.87 0.99, after: 5.05 4.89 4.07.

**How fast the engine computes.** The time each job takes on one connection to `:memory:`, lower is better. The last column is how much longer liustack/sqlite takes than C:

| Benchmark | SQLite(C) | liustack/sqlite | slower than C |
| :-- | --: | --: | --: |
| Insert 1M rows in one statement | **464 ms** ±3% | 547 ms ±1% | +18% |
| Index 1M texts | **399 ms** ±1% | 405 ms ±1% | +1% |
| Sort 1M texts | **4.21 s** ±3% | 4.71 s ±2% | +12% |
| Average 1M rows in 1000 groups | **314 ms** ±1% | 383 ms ±2% | +22% |
| Join 300k rows to a table of 1M on its key | **80.9 ms** ±3% | 87.9 ms ±1% | +9% |
| Recursive CTE of 2M steps | **454 ms** ±1% | 494 ms ±1% | +9% |
| LIKE and string functions on 1M texts | **85.2 ms** ±1% | 88.6 ms ±1% | +4% |
| Build and read 300k JSON objects | **415 ms** ±1% | 509 ms ±1% | +23% |
| sqrt, sin, log and pow of 1M numbers | **165 ms** ±3% | 196 ms ±0% | +19% |
| 4 FTS5 searches in 200k documents | **2.96 ms** ±1% | 3.41 ms ±2% | +15% |
| geomean | 209 ms | 235 ms | +13% |

**Throughput.** Reads or writes done per second by all threads together, on a file of 100k rows in WAL mode, higher is better:

| Benchmark | SQLite(C) | liustack/sqlite(direct) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 thread, each reads one row by key | 191k reads/s ±2% | **359k reads/s** ±2% | 277k reads/s ±2% | 102k reads/s ±1% |
| 8 threads, each reads one row by key | 1072k reads/s ±9% | **1307k reads/s** ±4% | 958k reads/s ±4% | 431k reads/s ±4% |
| 64 threads, each reads one row by key | 1022k reads/s ±11% | **1301k reads/s** ±2% | 939k reads/s ±4% | 432k reads/s ±5% |
| 1 thread, each reads one row by key, whole file in cache | 236k reads/s ±3% | **700k reads/s** ±4% | 458k reads/s ±5% | 115k reads/s ±2% |
| 8 threads, each reads one row by key, whole file in cache | 1394k reads/s ±10% | **2254k reads/s** ±7% | 1448k reads/s ±5% | 455k reads/s ±5% |
| 64 threads, each reads one row by key, whole file in cache | 1156k reads/s ±12% | **2016k reads/s** ±4% | 1233k reads/s ±5% | 332k reads/s ±4% |
| 8 threads read as above while 8 others write | 989k reads/s ±8% | 943k reads/s ±4% | 653k reads/s ±5% | 167k reads/s ±4% |
| 8 threads, each inserts one row, while 8 others read | 2389 writes/s ±16% | 3364 writes/s ±7% | 3042 writes/s ±6% | **15395 writes/s** ±5% |
| 64 threads, each inserts one row, `synchronous=FULL` | 862 writes/s ±3% | 56673 writes/s ±2% | 56764 writes/s ±4% | 869 writes/s ±4% |

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
Commit `18a2707`, Go 1.27.2, Homebrew clang version 22.1.8, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-18. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 4.91 6.27 6.70, after: 8.53 7.88 7.55.

**How fast the engine computes.** The time each job takes on one connection to `:memory:`, lower is better. The last column is how much longer liustack/sqlite takes than C:

| Benchmark | SQLite(C) | liustack/sqlite | slower than C |
| :-- | --: | --: | --: |
| Insert 1M rows in one statement | **188 ms** ±8% | 248 ms ±3% | +32% |
| Index 1M texts | 208 ms ±3% | 216 ms ±2% | +4% |
| Sort 1M texts | **1.57 s** ±4% | 1.76 s ±3% | +12% |
| Average 1M rows in 1000 groups | **149 ms** ±2% | 199 ms ±2% | +34% |
| Join 300k rows to a table of 1M on its key | **36.0 ms** ±1% | 45.8 ms ±1% | +27% |
| Recursive CTE of 2M steps | **199 ms** ±2% | 244 ms ±3% | +22% |
| LIKE and string functions on 1M texts | **46.8 ms** ±2% | 47.9 ms ±2% | +2% |
| Build and read 300k JSON objects | **212 ms** ±1% | 230 ms ±1% | +9% |
| sqrt, sin, log and pow of 1M numbers | **69.0 ms** ±3% | 89.4 ms ±2% | +30% |
| 4 FTS5 searches in 200k documents | **1.26 ms** ±2% | 1.82 ms ±1% | +44% |
| geomean | 94.5 ms | 114 ms | +21% |

**Throughput.** Reads or writes done per second by all threads together, on a file of 100k rows in WAL mode, higher is better:

| Benchmark | SQLite(C) | liustack/sqlite(direct) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 thread, each reads one row by key | 657k reads/s ±2% | **1044k reads/s** ±4% | 791k reads/s ±4% | 323k reads/s ±3% |
| 8 threads, each reads one row by key | 870k reads/s ±6% | 1617k reads/s ±5% | 1485k reads/s ±6% | 692k reads/s ±8% |
| 64 threads, each reads one row by key | 756k reads/s ±5% | **1545k reads/s** ±8% | 1284k reads/s ±6% | 634k reads/s ±5% |
| 1 thread, each reads one row by key, whole file in cache | 868k reads/s ±5% | **1999k reads/s** ±1% | 1409k reads/s ±2% | 395k reads/s ±1% |
| 8 threads, each reads one row by key, whole file in cache | 1364k reads/s ±6% | **3942k reads/s** ±5% | 3040k reads/s ±8% | 823k reads/s ±8% |
| 64 threads, each reads one row by key, whole file in cache | 1150k reads/s ±3% | **2996k reads/s** ±6% | 2363k reads/s ±10% | 692k reads/s ±7% |
| 8 threads read as above while 8 others write | 564k reads/s ±10% | **833k reads/s** ±9% | 703k reads/s ±9% | 287k reads/s ±14% |
| 8 threads, each inserts one row, while 8 others read | 17143 writes/s ±5% | 48904 writes/s ±3% | 46211 writes/s ±4% | 16869 writes/s ±8% |
| 64 threads, each inserts one row, `synchronous=FULL` | 22024 writes/s ±29% | 539k writes/s ±3% | 504k writes/s ±6% | 40834 writes/s ±6% |

<!-- /bench:native-darwin-arm64 -->

What the tables show:

- **Work inside the engine** takes up to 23% longer than in C on linux and up to 44% on darwin, 13% and 21% in the geometric mean, and within 4% of C for an index build or `LIKE`: the Go assembly the C becomes is close to, but not as good as, what clang makes for the machine. The engine also uses musl's string and math functions, the math ones built without fused multiply-add so that every target gives the same bits, where the C program uses the system's.
- **One reader** is faster than in C: liustack/sqlite(direct) reads 1.6 to 3.0 times as many rows a second. In C each read takes the WAL read lock with `fcntl` and gives it back with another, unless another connection of the process holds it. liustack/sqlite keeps the lock about a millisecond after the last reader leaves, so the next read makes no system call (see `internal/vfs`). A system call costs most on the linux VM.
- **Many readers** on linux read more rows than in C: liustack/sqlite(direct) reads 1.2 to 1.7 times as many. C's readers share the lock without system calls too, so part of the lead of one reader is gone. On darwin it stays further ahead, at 1.9 to 2.9 times: there C's readers spend about half their time waiting on the pthread mutex that guards the WAL locks of the process, which macOS puts to sleep in the kernel when threads contend for it. liustack/sqlite's readers join a lock the process holds with one atomic step, and wait for no mutex.
- **Through `database/sql`** a read takes 10 to 65% more time than liustack/sqlite(direct): that is the work of `database/sql` itself, and with 8 readers or more on linux it leaves liustack/sqlite at 89 to 107% of C. mattn pays the same and a cgo call on top. The connection keeps the statement of a read query, so the query is not prepared again each time (see [docs/architecture.md](architecture.md#statements-kept-for-queries)). A `*sql.Stmt` would take the lock of the `*sql.DB` on every query, even one prepared on a `*sql.Conn`, which is why the benchmark uses none.
- **Readers next to writers** on linux read 95% of the rows C reads calling liustack/sqlite directly and 66% through `database/sql`, and the writers beside them insert 1.3 to 1.4 times as many as C's. On darwin the readers read 1.2 to 1.5 times as many as C's and the writers insert 2.7 to 2.9 times as many. On the 4 cores of the linux machine the 8 readers leave the writers little: a writer is one goroutine among nine that want a core. mattn, whose calls each hold a thread, inserts 5 times as many rows there and reads a quarter as many.
- **Writes** are where group commit shows: one commit carries many inserts, so with 64 writers and `synchronous=FULL`, where each of C's writers waits for its own fsync, liustack/sqlite inserts 23 to 66 times as many rows. Next to readers it inserts 1.3 to 2.9 times as many as C.

## What the numbers say

- **Reads** are faster than with cgo: one row by key in about half the time (3.86 µs against 7.13 to 9.44 µs on linux), 100k rows in 35 ms against 53 to 86 ms, a user with its 400 comments in 523 µs against 829 µs to 1.16 ms. A call into the engine is a plain Go call, with no cgo crossing, and a read transaction takes no lock in the kernel when another read of the process is running or just ended (see `internal/vfs`).
- **Many writers** are where group commit shows: from 8 writers up, one commit carries many inserts. At 64 writers an insert costs 3.52 µs against 22 to 31 µs with the other drivers (4 threads). With `synchronous=FULL` the gap grows with the cost of a commit: at 64 writers an insert costs 26.7 µs against 1.37 to 23.5 ms. On the Mac, modernc at 64 writers spends its time in SQLite's busy handler, whose sleeps grow the longer a writer waits.
- **Readers next to a writer** (ReadWrite, 4 threads): this driver serves the readers fastest (2.61 µs a read against 4.54 to 7.94 µs), and ncruces lets the writer do the most (26.1k writes a second, against 14.2k with mattn and 12.4k with this driver).
- **One writer** (Insert) is close to mattn on both machines. The time goes to the WAL write and the locks SQLite's protocol asks for, the same system calls for every driver.
- **Join and group** (Aggregate), which is all work inside the engine, takes as long as with cgo on linux and 10% longer on darwin: the engine is the same C, compiled by LLVM, and the Go assembly it becomes is close to, but not as good as, what clang makes for the machine.
- **Large text values** are read 6% slower than with mattn on linux (100 rows of 200 KB in 8.78 ms against 8.31 ms) and faster than with modernc and ncruces. On darwin the four are even.
- **Allocations**: a number is copied straight into the `Scan` destination (`driver.RowsColumnScanner`, Go 1.27), without boxing it in an interface, so a scan allocates half as often as with mattn and modernc.
- **Against SQLite in C**, work inside the engine takes 13% (linux) and 21% (darwin) longer in the geometric mean. One reader is faster than C, as a read makes no system call for its lock. Many readers on linux read more rows than C's calling liustack/sqlite directly, and 89 to 107% through `database/sql`. With group commit, 64 writers insert 23 to 66 times as many rows. Readers next to writers on linux read 95% of what C's do calling liustack/sqlite directly and 66% through `database/sql`, with writers that insert 1.3 to 1.4 times as many.
- **Point on linux** includes one `fstat` per query, for every driver: when the WAL is empty, as it is in a fresh copy of the fixture, SQLite reads the size of the database file at the start of each read. A system call costs about 0.8 µs on this VM, which is most of the gap between linux and darwin in that row.

## Running them again

`make bench` on a quiet machine, `benchrun publish` to make a set of `bench/published/`, then `make bench-docs` to write the tables. The steps and the options are in [bench/README.md](../bench/README.md). On a shared machine, pin the single-thread runs to one core (`taskset`, `GOMAXPROCS=1`) and take the parallel runs only when nothing else runs.
