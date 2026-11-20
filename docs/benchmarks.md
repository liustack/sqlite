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
Commit `44acf8c`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 1 of 1 threads, 2026-11-19. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.05 0.01 0.00, after: 1.53 1.23 0.80.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 22.9 µs ±1% | 23.3 µs ±1% | 26.7 µs ±1% | 24.2 µs ±2% |
| InsertTx | 1.40 ms ±105% | 1.67 ms ±1% | 2.19 ms ±2% | 1.91 ms ±1% |
| Point | **3.79 µs** ±1% | 7.43 µs ±1% | 9.31 µs ±1% | 7.11 µs ±1% |
| Scan | **35.1 ms** ±1% | 79.9 ms ±3% | 84.5 ms ±0% | 52.5 ms ±0% |
| Aggregate | 2.88 ms ±2% | 2.87 ms ±1% | 4.93 ms ±2% | 4.74 ms ±1% |
| PointParallel | **3.77 µs** ±1% | 7.41 µs ±1% | 9.29 µs ±1% | 7.13 µs ±1% |
| InsertParallel/g=1 | 22.7 µs ±1% | 23.2 µs ±2% | 26.7 µs ±2% | 24.3 µs ±2% |
| InsertParallel/g=8 | 23.2 µs ±1% | 22.1 µs ±5% | 26.0 µs ±7% | 24.5 µs ±2% |
| InsertParallel/g=64 | 21.1 µs ±2% | 22.0 µs ±7% | 28.6 µs ±7% | 29.2 µs ±2% |
| InsertParallelFull/g=1 | 1.26 ms ±9% | 1.30 ms ±9% | 1.28 ms ±4% | 1.30 ms ±7% |
| InsertParallelFull/g=8 | **331 µs** ±8% | 3.66 ms ±36% | 3.34 ms ±29% | 1.29 ms ±5% |
| InsertParallelFull/g=64 | **30.4 µs** ±9% | 4.53 ms ±23% | 7.11 ms ±64% | 1.42 ms ±8% |
| Real/insert | **1.55 ms** ±2% | 1.81 ms ±1% | 2.27 ms ±1% | 2.12 ms ±1% |
| Real/query | **512 µs** ±2% | 880 µs ±1% | 1.15 ms ±2% | 826 µs ±1% |
| Many/n=10 | **8.75 µs** ±1% | 16.5 µs ±3% | 18.5 µs ±1% | 14.0 µs ±1% |
| Many/n=100 | **57.6 µs** ±2% | 107 µs ±2% | 111 µs ±1% | 79.0 µs ±0% |
| Many/n=1000 | **572 µs** ±1% | 1.03 ms ±1% | 1.05 ms ±1% | 749 µs ±0% |
| Large/size=50k | 2.16 ms ±3% | **2.07 ms** ±4% | 2.45 ms ±2% | 2.27 ms ±1% |
| Large/size=200k | 8.75 ms ±4% | **8.34 ms** ±2% | 9.38 ms ±1% | 9.14 ms ±2% |
| Readers/g=2 | **35.8 ms** ±1% | 80.5 ms ±2% | 85.1 ms ±1% | 53.0 ms ±0% |
| Readers/g=4 | **36.0 ms** ±1% | 81.3 ms ±2% | 85.0 ms ±1% | 53.1 ms ±0% |
| Readers/g=8 | **35.9 ms** ±0% | 81.9 ms ±2% | 85.5 ms ±0% | 53.8 ms ±0% |
| ReadWrite | **2.71 µs** ±3% | 4.30 µs ±6% | 6.40 µs ±17% | 8.20 µs ±3% |
| geomean | 318 µs | 615 µs | 727 µs | 546 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2315 MB/s ±3% | **2419 MB/s** ±4% | 2043 MB/s ±2% | 2201 MB/s ±1% |
| Large/size=200k | 2287 MB/s ±4% | **2397 MB/s** ±2% | 2131 MB/s ±1% | 2188 MB/s ±2% |
| ReadWrite | 6951 writes/s ±23% | 3702 writes/s ±54% | 7043 writes/s ±29% | 4851 writes/s ±6% |

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
| InsertParallelFull/g=8 | 10 | **6** | 8 | 16 |
| InsertParallelFull/g=64 | **11** | 21 | 34 | 105 |
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
Commit `44acf8c`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-19. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.94 1.10 0.78, after: 2.05 1.95 1.33.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **1.47 µs** ±5% | 2.97 µs ±13% | 3.86 µs ±15% | 2.79 µs ±14% |
| InsertParallel/g=1 | 23.1 µs ±0% | **22.7 µs** ±1% | 25.1 µs ±2% | 23.4 µs ±1% |
| InsertParallel/g=8 | **10.4 µs** ±3% | 22.9 µs ±11% | 26.0 µs ±7% | 23.4 µs ±2% |
| InsertParallel/g=64 | **3.42 µs** ±4% | 22.2 µs ±8% | 30.1 µs ±4% | 28.6 µs ±3% |
| InsertParallelFull/g=1 | 1.26 ms ±7% | 1.30 ms ±4% | 1.31 ms ±4% | 1.29 ms ±6% |
| InsertParallelFull/g=8 | **323 µs** ±7% | 1.86 ms ±15% | 2.13 ms ±13% | 1.30 ms ±5% |
| InsertParallelFull/g=64 | **29.0 µs** ±6% | 25.0 ms ±10% | 24.6 ms ±29% | 1.37 ms ±8% |
| Readers/g=2 | **17.4 ms** ±2% | 39.2 ms ±2% | 40.7 ms ±3% | 27.1 ms ±2% |
| Readers/g=4 | **9.86 ms** ±3% | 20.8 ms ±6% | 23.3 ms ±6% | 14.4 ms ±10% |
| Readers/g=8 | **9.72 ms** ±4% | 20.6 ms ±2% | 22.0 ms ±4% | 14.2 ms ±4% |
| ReadWrite | **2.62 µs** ±18% | 4.98 µs ±11% | 7.24 µs ±15% | 4.32 µs ±28% |
| geomean | 118 µs | 455 µs | 522 µs | 309 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | **40808 writes/s** ±11% | 14839 writes/s ±6% | 4357 writes/s ±15% | 26524 writes/s ±5% |

Allocations per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **14** | 18 | 30 | 19 |
| InsertParallel/g=1 | **5** | **5** | 6 | **5** |
| InsertParallel/g=8 | 10 | **6** | 7 | **6** |
| InsertParallel/g=64 | 11 | **6** | 7 | 7 |
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
Commit `44acf8c`, Go 1.27.2, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-19. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 2.82 3.46 3.76, after: 3.64 3.27 3.45.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 5.95 µs ±5% | 5.99 µs ±14% | 7.34 µs ±1% | 7.01 µs ±2% |
| InsertTx | **483 µs** ±11% | 557 µs ±11% | 765 µs ±5% | 584 µs ±4% |
| Point | **1.08 µs** ±3% | 1.93 µs ±3% | 2.76 µs ±1% | 2.02 µs ±2% |
| Scan | **13.7 ms** ±4% | 29.7 ms ±1% | 29.9 ms ±1% | 20.6 ms ±1% |
| Aggregate | 1.27 ms ±2% | **1.14 ms** ±9% | 2.64 ms ±3% | 1.87 ms ±2% |
| PointParallel | **1.13 µs** ±4% | 1.52 µs ±3% | 1.77 µs ±4% | 2.33 µs ±3% |
| InsertParallel/g=1 | 6.14 µs ±5% | 6.04 µs ±13% | 7.47 µs ±6% | 7.10 µs ±3% |
| InsertParallel/g=8 | **3.04 µs** ±19% | 6.75 µs ±53% | 8.19 µs ±9% | 45.0 µs ±27% |
| InsertParallel/g=64 | **1.19 µs** ±15% | 9.94 µs ±46% | 5.67 ms ±86% | 85.8 µs ±20% |
| InsertParallelFull/g=1 | 98.5 µs ±46% | 80.5 µs ±45% | 71.4 µs ±64% | 83.6 µs ±42% |
| InsertParallelFull/g=8 | **16.1 µs** ±56% | 75.1 µs ±63% | 125 µs ±29% | 79.8 µs ±68% |
| InsertParallelFull/g=64 | **2.22 µs** ±55% | 206 µs ±380% | 34.0 ms ±27% | 96.0 µs ±19% |
| Real/insert | 569 µs ±4% | 570 µs ±4% | 1.03 ms ±7% | 726 µs ±3% |
| Real/query | **212 µs** ±1% | 371 µs ±2% | 554 µs ±1% | 342 µs ±4% |
| Many/n=10 | **2.68 µs** ±1% | 4.87 µs ±2% | 5.33 µs ±2% | 4.41 µs ±1% |
| Many/n=100 | **19.2 µs** ±1% | 34.4 µs ±1% | 33.4 µs ±2% | 26.7 µs ±1% |
| Many/n=1000 | **187 µs** ±1% | 338 µs ±2% | 324 µs ±1% | 254 µs ±1% |
| Large/size=50k | 839 µs ±5% | 822 µs ±4% | 908 µs ±3% | 865 µs ±6% |
| Large/size=200k | 3.46 ms ±7% | 3.29 ms ±5% | 3.79 ms ±4% | 3.55 ms ±2% |
| Readers/g=2 | **7.50 ms** ±4% | 15.9 ms ±2% | 16.0 ms ±2% | 10.9 ms ±2% |
| Readers/g=4 | **4.83 ms** ±3% | 9.74 ms ±3% | 9.93 ms ±7% | 7.06 ms ±3% |
| Readers/g=8 | **4.53 ms** ±10% | 8.67 ms ±4% | 8.41 ms ±6% | 6.08 ms ±2% |
| ReadWrite | **1.56 µs** ±7% | 2.23 µs ±3% | 2.40 µs ±4% | 5.21 µs ±4% |
| geomean | 71.3 µs | 138 µs | 270 µs | 163 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 5959 MB/s ±6% | 6081 MB/s ±5% | 5509 MB/s ±3% | 5779 MB/s ±6% |
| Large/size=200k | 5782 MB/s ±7% | 6072 MB/s ±5% | 5281 MB/s ±4% | 5638 MB/s ±2% |
| ReadWrite | **45954 writes/s** ±11% | 15858 writes/s ±3% | 28907 writes/s ±3% | 19803 writes/s ±3% |

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
| InsertParallel/g=64 | 11 | **6** | 33 | 8 |
| InsertParallelFull/g=1 | **5** | **5** | 6 | **5** |
| InsertParallelFull/g=8 | 10 | **6** | 7 | **6** |
| InsertParallelFull/g=64 | 11 | 6 | 77 | 9 |
| Real/insert | 4.65k | 4.65k | 5.05k | **2.97k** |
| Real/query | **4.44k** | 7.91k | 7.93k | **4.44k** |
| Many/n=10 | **58** | 78 | 90 | 67 |
| Many/n=100 | **508** | 708 | 720 | 517 |
| Many/n=1000 | **5.75k** | 8.50k | 8.51k | 5.76k |
| Large/size=50k | **408** | 608 | 620 | 417 |
| Large/size=200k | **408** | 608 | 620 | 417 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **15** | 19 | 31 | 19 |

<!-- /bench:darwin-arm64 -->

## Replication

[Replication](replication.md) copies each commit inside the program, so it costs CPU in the program's process. `BenchmarkGroupCommit` in `replica` inserts one row per call from 8 or 64 goroutines at once (WAL, `synchronous=NORMAL`), with replication off and on. The store is a MinIO on another core of the same machine, so that its work does not fall on the benchmark's, as a store on another machine's would not.

Commit `8671819`, Go 1.27.4, AMD EPYC 7B13 (4 cores, no SMT), Linux 7.0.0-1017-gcp, 2026-11-20. 6 runs each, compared with benchstat (`~` is no difference it can tell from noise). Load average 0.00 before and 2.42 after. The raw output is in [bench/published/replica/](../bench/published/replica/).

One core (`GOMAXPROCS=1` on core 0, MinIO on core 3):

```
              replica off    replica on
g=8           24.27µ ± 3%    24.13µ ± 3%        ~ (p=0.818 n=6)
g=64          22.28µ ± 2%    23.08µ ± 1%   +3.57% (p=0.002 n=6)
geomean       23.25µ         23.60µ        +1.48%
```

Three cores (`GOMAXPROCS=3` on cores 0 to 2, MinIO on core 3):

```
              replica off    replica on
g=8           12.20µ ±  2%   12.33µ ±  2%        ~ (p=0.180 n=6)
g=64           4.154µ ± 4%    4.098µ ± 10%       ~ (p=0.937 n=6)
geomean        7.118µ         7.107µ        -0.15%
```

So an insert costs at most about 4% more with replication on, and on three cores no difference shows. Run it with a MinIO binary, pinned away from the benchmark (on linux, `MINIO` can name a script that runs `taskset -c 3 minio "$@"`, and the test runs under `taskset -c 0`):

```sh
MINIO=<minio> go test ./replica -run '^$' -bench GroupCommit -benchtime 3s -count 6
```

## Against SQLite in C

The same SQLite 3.54.1 with the options of `engine/sqlite.c`, built by clang `-O2` into a C program that calls the C API itself. The workloads and the four columns are in [bench/README.md](../bench/README.md#against-sqlite-in-c). In short: SQLite(C) and liustack/sqlite(direct) prepare each statement once and run it again and again, calling the C API or the driver itself. The two columns (database/sql) read and write as a program would: a `*sql.Conn` for each reader with `QueryRowContext`, and `db.Exec` for each write. mattn runs SQLite's C through cgo.

### linux/amd64

<!-- bench:native-linux-amd64 -->
Commit `44acf8c`, Go 1.27.4, Ubuntu clang version 22.1.8 (++20260714015917+ca7933e47d3a-1~exp1~20260714135927.17), AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-19. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.82 1.62 1.25, after: 5.63 4.20 3.69.

**How fast the engine computes.** The time each job takes on one connection to `:memory:`, lower is better. The last column is how much longer liustack/sqlite takes than C:

| Benchmark | SQLite(C) | liustack/sqlite | slower than C |
| :-- | --: | --: | --: |
| Insert 1M rows in one statement | **471 ms** ±1% | 539 ms ±1% | +15% |
| Index 1M texts | **392 ms** ±0% | 405 ms ±1% | +3% |
| Sort 1M texts | **4.01 s** ±1% | 4.52 s ±1% | +13% |
| Average 1M rows in 1000 groups | **312 ms** ±1% | 381 ms ±4% | +22% |
| Join 300k rows to a table of 1M on its key | **76.2 ms** ±3% | 87.1 ms ±1% | +14% |
| Recursive CTE of 2M steps | **439 ms** ±2% | 488 ms ±1% | +11% |
| LIKE and string functions on 1M texts | 86.0 ms ±9% | 87.9 ms ±2% | +2% |
| Build and read 300k JSON objects | **415 ms** ±1% | 510 ms ±0% | +23% |
| sqrt, sin, log and pow of 1M numbers | **165 ms** ±4% | 196 ms ±0% | +19% |
| 4 FTS5 searches in 200k documents | **2.93 ms** ±1% | 3.39 ms ±1% | +16% |
| geomean | 205 ms | 233 ms | +14% |

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

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
Commit `44acf8c`, Go 1.27.2, Homebrew clang version 22.1.8, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-19. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 3.64 3.27 3.45, after: 8.71 8.63 7.33.

**How fast the engine computes.** The time each job takes on one connection to `:memory:`, lower is better. The last column is how much longer liustack/sqlite takes than C:

| Benchmark | SQLite(C) | liustack/sqlite | slower than C |
| :-- | --: | --: | --: |
| Insert 1M rows in one statement | **188 ms** ±3% | 242 ms ±4% | +29% |
| Index 1M texts | 211 ms ±3% | 215 ms ±6% | +2% |
| Sort 1M texts | **1.58 s** ±2% | 1.75 s ±3% | +11% |
| Average 1M rows in 1000 groups | **149 ms** ±10% | 201 ms ±2% | +34% |
| Join 300k rows to a table of 1M on its key | **35.8 ms** ±1% | 46.1 ms ±1% | +29% |
| Recursive CTE of 2M steps | **199 ms** ±1% | 243 ms ±1% | +22% |
| LIKE and string functions on 1M texts | **46.8 ms** ±1% | 47.9 ms ±1% | +2% |
| Build and read 300k JSON objects | **213 ms** ±2% | 231 ms ±0% | +8% |
| sqrt, sin, log and pow of 1M numbers | **67.1 ms** ±2% | 89.7 ms ±0% | +34% |
| 4 FTS5 searches in 200k documents | **1.27 ms** ±2% | 1.83 ms ±2% | +44% |
| geomean | 94.5 ms | 114 ms | +21% |

**Throughput.** Reads or writes done per second by all threads together, on a file of 100k rows in WAL mode, higher is better:

| Benchmark | SQLite(C) | liustack/sqlite(direct) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 thread, each reads one row by key | 673k reads/s ±1% | **1052k reads/s** ±2% | 819k reads/s ±5% | 326k reads/s ±2% |
| 8 threads, each reads one row by key | 861k reads/s ±9% | **1624k reads/s** ±2% | 1542k reads/s ±5% | 761k reads/s ±4% |
| 64 threads, each reads one row by key | 771k reads/s ±6% | **1560k reads/s** ±2% | 1386k reads/s ±3% | 667k reads/s ±3% |
| 1 thread, each reads one row by key, whole file in cache | 884k reads/s ±2% | **2017k reads/s** ±2% | 1436k reads/s ±1% | 398k reads/s ±1% |
| 8 threads, each reads one row by key, whole file in cache | 1392k reads/s ±5% | **3876k reads/s** ±1% | 3078k reads/s ±2% | 849k reads/s ±4% |
| 64 threads, each reads one row by key, whole file in cache | 1202k reads/s ±6% | **3130k reads/s** ±4% | 2581k reads/s ±6% | 740k reads/s ±3% |
| 8 threads read as above while 8 others write | 586k reads/s ±2% | 590k reads/s ±3% | 572k reads/s ±2% | 304k reads/s ±3% |
| 8 threads, each inserts one row, while 8 others read | 17686 writes/s ±1% | **78104 writes/s** ±2% | 69029 writes/s ±3% | 17561 writes/s ±2% |
| 64 threads, each inserts one row, `synchronous=FULL` | 22800 writes/s ±62% | **546k writes/s** ±1% | 524k writes/s ±2% | 42440 writes/s ±3% |

<!-- /bench:native-darwin-arm64 -->

What the tables show:

- **Work inside the engine** takes up to 23% longer than in C on linux and up to 44% on darwin, 14% and 21% in the geometric mean, and within 3% of C for an index build or `LIKE`: the Go assembly the C becomes is close to, but not as good as, what clang makes for the machine. The engine also uses musl's string and math functions, the math ones built without fused multiply-add so that every target gives the same bits, where the C program uses the system's.
- **One reader** is faster than in C: liustack/sqlite(direct) reads 1.6 to 3.1 times as many rows a second. In C each read takes the WAL read lock with `fcntl` and gives it back with another, unless another connection of the process holds it. liustack/sqlite keeps the lock about a millisecond after the last reader leaves, so the next read makes no system call (see `internal/vfs`). A system call costs most on the linux VM.
- **Many readers** on linux read more rows than in C: liustack/sqlite(direct) reads 1.1 to 1.7 times as many. C's readers share the lock without system calls too, so part of the lead of one reader is gone. On darwin it stays further ahead, at 1.9 to 2.8 times: there C's readers spend about half their time waiting on the pthread mutex that guards the WAL locks of the process, which macOS puts to sleep in the kernel when threads contend for it. liustack/sqlite's readers join a lock the process holds with one atomic step, and wait for no mutex.
- **Through `database/sql`** a read takes 5 to 64% more time than liustack/sqlite(direct): that is the work of `database/sql` itself, and with 8 readers or more on linux it leaves liustack/sqlite at 86 to 104% of C. mattn pays the same and a cgo call on top. The connection keeps the statement of a read query, so the query is not prepared again each time (see [docs/architecture.md](architecture.md#statements-kept-for-queries)). A `*sql.Stmt` would take the lock of the `*sql.DB` on every query, even one prepared on a `*sql.Conn`, which is why the benchmark uses none.
- **Readers next to writers** read as many rows as C's when they call liustack/sqlite directly (102% on linux, 101% on darwin), and through `database/sql` 61% on linux and 98% on darwin. The writers beside them insert 7 to 10 times as many rows as C's on linux and 3.9 to 4.4 times on darwin. The more the writers commit, the less the readers read, with any driver: a reader that sees a new commit drops the pages it holds and reads them again (see [busy-cores.md](busy-cores.md#6-how-to-measure-it)). mattn, through `database/sql` too, inserts about half as many rows on linux and reads about a quarter as many.
- **Writes** are where group commit shows: one commit carries many inserts, so with 64 writers and `synchronous=FULL`, where each of C's writers waits for its own fsync, liustack/sqlite inserts 23 to 69 times as many rows. Next to readers it inserts 3.9 to 10 times as many as C.

## What the numbers say

- **Reads** are faster than with cgo: one row by key in about half the time (3.79 µs against 7.11 to 9.31 µs on linux), 100k rows in 35 ms against 53 to 85 ms, a user with its 400 comments in 512 µs against 826 µs to 1.15 ms. A call into the engine is a plain Go call, with no cgo crossing, and a read transaction takes no lock in the kernel when another read of the process is running or just ended (see `internal/vfs`).
- **Many writers** are where group commit shows: from 8 writers up, one commit carries many inserts. At 64 writers an insert costs 3.42 µs against 22 to 30 µs with the other drivers (4 threads). With `synchronous=FULL` the gap grows with the cost of a commit: at 64 writers an insert costs 29.0 µs against 1.37 to 25.0 ms. On the Mac, modernc at 64 writers spends its time in SQLite's busy handler, whose sleeps grow the longer a writer waits.
- **Readers next to a writer** (ReadWrite, 4 threads): this driver serves the readers fastest (2.62 µs a read against 4.32 to 7.24 µs) and its writer writes the most (40.8k writes a second, against 26.5k with ncruces and 14.8k with mattn).
- **One writer** (Insert) is close to mattn on both machines. The time goes to the WAL write and the locks SQLite's protocol asks for, the same system calls for every driver.
- **Join and group** (Aggregate), which is all work inside the engine, takes as long as with cgo on linux and 11% longer on darwin: the engine is the same C, compiled by LLVM, and the Go assembly it becomes is close to, but not as good as, what clang makes for the machine.
- **Large text values** are read 5% slower than with mattn on linux (100 rows of 200 KB in 8.75 ms against 8.34 ms) and faster than with modernc and ncruces. On darwin the four are even.
- **Allocations**: a number is copied straight into the `Scan` destination (`driver.RowsColumnScanner`, Go 1.27), without boxing it in an interface, so a scan allocates half as often as with mattn and modernc.
- **Against SQLite in C**, work inside the engine takes 14% (linux) and 21% (darwin) longer in the geometric mean. One reader is faster than C, as a read makes no system call for its lock. Many readers on linux read more rows than C's calling liustack/sqlite directly, and 86 to 104% through `database/sql`. With group commit, 64 writers insert 23 to 69 times as many rows. Readers next to writers on linux read as many rows as C's calling liustack/sqlite directly and 61% through `database/sql`, with writers that insert 7 to 10 times as many.
- **Point on linux** includes one `fstat` per query, for every driver: when the WAL is empty, as it is in a fresh copy of the fixture, SQLite reads the size of the database file at the start of each read. A system call costs about 0.8 µs on this VM, which is most of the gap between linux and darwin in that row.

## Running them again

`make bench` on a quiet machine, `benchrun publish` to make a set of `bench/published/`, then `make bench-docs` to write the tables. The steps and the options are in [bench/README.md](../bench/README.md). On a shared machine, pin the single-thread runs to one core (`taskset`, `GOMAXPROCS=1`) and take the parallel runs only when nothing else runs.
