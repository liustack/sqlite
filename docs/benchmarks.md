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
Commit `abf3e08`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 1 of 1 threads, 2026-11-17. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.22 0.05 0.02, after: 1.15 1.19 0.81.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 22.7 µs ±2% | 22.9 µs ±4% | 26.5 µs ±2% | 24.2 µs ±2% |
| InsertTx | **1.40 ms** ±1% | 1.70 ms ±2% | 2.21 ms ±3% | 1.95 ms ±1% |
| Point | **3.84 µs** ±1% | 7.52 µs ±2% | 9.36 µs ±1% | 7.18 µs ±1% |
| Scan | **35.9 ms** ±2% | 82.0 ms ±4% | 80.8 ms ±1% | 52.3 ms ±1% |
| Aggregate | 2.89 ms ±1% | 2.90 ms ±2% | 4.93 ms ±3% | 4.80 ms ±2% |
| PointParallel | **3.83 µs** ±2% | 7.44 µs ±2% | 9.28 µs ±2% | 7.16 µs ±1% |
| InsertParallel/g=1 | 22.5 µs ±1% | 22.7 µs ±2% | 26.4 µs ±2% | 24.2 µs ±2% |
| InsertParallel/g=8 | 23.1 µs ±2% | **20.9 µs** ±11% | 25.2 µs ±4% | 24.4 µs ±3% |
| InsertParallel/g=64 | 21.0 µs ±3% | 21.3 µs ±8% | 28.8 µs ±9% | 29.2 µs ±2% |
| InsertParallelFull/g=1 | 1.30 ms ±11% | 1.26 ms ±3% | 1.29 ms ±11% | 1.28 ms ±12% |
| InsertParallelFull/g=8 | **328 µs** ±6% | 2.81 ms ±20% | 3.62 ms ±27% | 1.30 ms ±45% |
| InsertParallelFull/g=64 | **30.4 µs** ±8% | 4.01 ms ±26% | 7.16 ms ±30% | 1.41 ms ±5% |
| Real/insert | **1.54 ms** ±2% | 1.80 ms ±1% | 2.26 ms ±2% | 2.14 ms ±2% |
| Real/query | **515 µs** ±18% | 883 µs ±1% | 1.15 ms ±6% | 831 µs ±2% |
| Many/n=10 | **8.85 µs** ±4% | 17.0 µs ±1% | 18.6 µs ±2% | 14.3 µs ±1% |
| Many/n=100 | **58.2 µs** ±4% | 112 µs ±2% | 111 µs ±1% | 80.4 µs ±1% |
| Many/n=1000 | **584 µs** ±4% | 1.08 ms ±1% | 1.05 ms ±4% | 760 µs ±1% |
| Large/size=50k | 2.18 ms ±9% | 2.10 ms ±34% | 2.42 ms ±6% | 2.37 ms ±4% |
| Large/size=200k | 8.66 ms ±3% | **8.24 ms** ±3% | 9.47 ms ±4% | 9.36 ms ±4% |
| Readers/g=2 | **36.4 ms** ±1% | 82.4 ms ±2% | 80.5 ms ±2% | 53.0 ms ±1% |
| Readers/g=4 | **36.3 ms** ±1% | 83.4 ms ±2% | 81.1 ms ±1% | 53.3 ms ±1% |
| Readers/g=8 | **36.8 ms** ±1% | 84.9 ms ±4% | 82.0 ms ±6% | 53.7 ms ±1% |
| ReadWrite | **2.75 µs** ±8% | 4.52 µs ±10% | 6.47 µs ±9% | 8.28 µs ±2% |
| geomean | 320 µs | 608 µs | 723 µs | 550 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2291 MB/s ±8% | 2378 MB/s ±25% | 2064 MB/s ±6% | 2111 MB/s ±5% |
| Large/size=200k | 2309 MB/s ±3% | **2427 MB/s** ±3% | 2112 MB/s ±4% | 2136 MB/s ±3% |
| ReadWrite | 6737 writes/s ±35% | 4206 writes/s ±31% | 6042 writes/s ±36% | 5129 writes/s ±11% |

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
| InsertParallelFull/g=64 | **11** | 18 | 31 | 106 |
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
Commit `abf3e08`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-17. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.50 1.00 0.77, after: 2.25 2.05 1.39.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **1.51 µs** ±11% | 2.85 µs ±13% | 3.50 µs ±27% | 2.51 µs ±15% |
| InsertParallel/g=1 | 22.9 µs ±0% | 22.8 µs ±1% | 25.3 µs ±4% | 23.5 µs ±2% |
| InsertParallel/g=8 | **10.2 µs** ±4% | 21.5 µs ±18% | 25.7 µs ±4% | 23.7 µs ±2% |
| InsertParallel/g=64 | **3.38 µs** ±6% | 22.2 µs ±8% | 29.0 µs ±7% | 28.6 µs ±3% |
| InsertParallelFull/g=1 | 1.22 ms ±9% | 1.25 ms ±7% | 1.25 ms ±5% | 1.26 ms ±19% |
| InsertParallelFull/g=8 | **312 µs** ±9% | 1.74 ms ±17% | 2.12 ms ±20% | 1.30 ms ±12% |
| InsertParallelFull/g=64 | **28.0 µs** ±11% | 23.0 ms ±11% | 20.6 ms ±53% | 1.35 ms ±6% |
| Readers/g=2 | **17.6 ms** ±3% | 38.6 ms ±2% | 41.1 ms ±1% | 26.7 ms ±2% |
| Readers/g=4 | **10.0 ms** ±7% | 20.8 ms ±4% | 23.0 ms ±7% | 14.6 ms ±4% |
| Readers/g=8 | **9.70 ms** ±9% | 21.3 ms ±7% | 22.7 ms ±4% | 14.4 ms ±3% |
| ReadWrite | **2.69 µs** ±19% | 4.52 µs ±19% | 6.86 µs ±25% | 4.18 µs ±18% |
| geomean | 117 µs | 440 µs | 504 µs | 304 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | 17209 writes/s ±17% | 14358 writes/s ±13% | 4463 writes/s ±11% | **26998 writes/s** ±6% |

Allocations per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **14** | 18 | 30 | 19 |
| InsertParallel/g=1 | **5** | **5** | 6 | **5** |
| InsertParallel/g=8 | 10 | **6** | 7 | **6** |
| InsertParallel/g=64 | 11 | **6** | 7 | 7 |
| InsertParallelFull/g=1 | **5** | **5** | 6 | **5** |
| InsertParallelFull/g=8 | 10 | **6** | 7 | 15 |
| InsertParallelFull/g=64 | **11** | 44 | 54 | 96 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **15** | 19 | 31 | 19 |

<!-- /bench:linux-amd64-4threads -->

## darwin/arm64

<!-- bench:darwin-arm64 -->
Commit `abf3e08`, Go 1.27.2, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-17. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 3.17 2.15 1.93, after: 2.35 2.83 2.66.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 6.41 µs ±2% | 5.99 µs ±7% | 7.46 µs ±5% | 7.03 µs ±3% |
| InsertTx | 495 µs ±42% | 552 µs ±5% | 750 µs ±6% | 578 µs ±3% |
| Point | **1.08 µs** ±7% | 1.93 µs ±1% | 2.76 µs ±1% | 2.05 µs ±28% |
| Scan | **13.7 ms** ±2% | 29.7 ms ±1% | 29.8 ms ±1% | 20.6 ms ±10% |
| Aggregate | 1.25 ms ±3% | **1.13 ms** ±2% | 2.64 ms ±2% | 1.86 ms ±12% |
| PointParallel | **1.13 µs** ±7% | 1.52 µs ±2% | 1.75 µs ±6% | 2.37 µs ±6% |
| InsertParallel/g=1 | 6.19 µs ±8% | 6.07 µs ±1% | 7.40 µs ±3% | 7.02 µs ±4% |
| InsertParallel/g=8 | **3.20 µs** ±3% | 6.74 µs ±6% | 8.11 µs ±15% | 48.8 µs ±35% |
| InsertParallel/g=64 | **1.14 µs** ±13% | 10.3 µs ±15% | 10.6 ms ±77% | 86.2 µs ±32% |
| InsertParallelFull/g=1 | 74.8 µs ±43% | 92.7 µs ±29% | 80.2 µs ±16% | 72.0 µs ±67% |
| InsertParallelFull/g=8 | **16.0 µs** ±61% | 77.6 µs ±46% | 130 µs ±71% | 51.5 µs ±10% |
| InsertParallelFull/g=64 | **1.65 µs** ±25% | 112 µs ±134% | 26.9 ms ±42% | 89.3 µs ±11% |
| Real/insert | 573 µs ±6% | 567 µs ±10% | 1.01 ms ±3% | 724 µs ±3% |
| Real/query | **213 µs** ±9% | 368 µs ±10% | 554 µs ±2% | 342 µs ±2% |
| Many/n=10 | **2.69 µs** ±3% | 4.88 µs ±2% | 5.35 µs ±1% | 4.40 µs ±1% |
| Many/n=100 | **19.3 µs** ±1% | 34.4 µs ±2% | 33.2 µs ±1% | 26.7 µs ±1% |
| Many/n=1000 | **190 µs** ±1% | 337 µs ±1% | 320 µs ±2% | 252 µs ±4% |
| Large/size=50k | 824 µs ±9% | 837 µs ±5% | 905 µs ±5% | 879 µs ±1% |
| Large/size=200k | 3.39 ms ±4% | 3.37 ms ±2% | 3.71 ms ±1% | 3.55 ms ±7% |
| Readers/g=2 | **7.48 ms** ±12% | 15.8 ms ±2% | 15.8 ms ±2% | 10.9 ms ±0% |
| Readers/g=4 | **4.67 ms** ±6% | 9.49 ms ±3% | 9.61 ms ±3% | 6.86 ms ±2% |
| Readers/g=8 | **4.44 ms** ±4% | 8.49 ms ±4% | 8.32 ms ±4% | 5.93 ms ±3% |
| ReadWrite | **1.32 µs** ±4% | 2.29 µs ±4% | 2.35 µs ±2% | 5.07 µs ±3% |
| geomean | 69.1 µs | 135 µs | 274 µs | 158 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 6071 MB/s ±8% | 5973 MB/s ±5% | 5528 MB/s ±5% | 5690 MB/s ±1% |
| Large/size=200k | 5897 MB/s ±4% | 5938 MB/s ±2% | 5388 MB/s ±1% | 5639 MB/s ±6% |
| ReadWrite | **34553 writes/s** ±12% | 16321 writes/s ±4% | 28912 writes/s ±2% | 20062 writes/s ±4% |

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
| InsertParallel/g=64 | 11 | **6** | 66 | 8 |
| InsertParallelFull/g=1 | **5** | **5** | 6 | **5** |
| InsertParallelFull/g=8 | 10 | **6** | 7 | **6** |
| InsertParallelFull/g=64 | 11 | **6** | 77 | 8 |
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
Commit `81e0480`, Go 1.27.4, Ubuntu clang version 22.1.8 (++20260714015917+ca7933e47d3a-1~exp1~20260714135927.17), AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-17. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.15 1.50 2.34, after: 5.20 4.35 4.00.

**How fast the engine computes.** The time each job takes on one connection to `:memory:`, lower is better. The last column is how much longer liustack/sqlite takes than C:

| Benchmark | SQLite(C) | liustack/sqlite | slower than C |
| :-- | --: | --: | --: |
| Insert 1M rows in one statement | **466 ms** ±5% | 547 ms ±1% | +17% |
| Index 1M texts | **401 ms** ±1% | 406 ms ±1% | +1% |
| Sort 1M texts | **4.23 s** ±1% | 4.74 s ±1% | +12% |
| Average 1M rows in 1000 groups | **314 ms** ±2% | 381 ms ±1% | +21% |
| Join 300k rows to a table of 1M on its key | **81.2 ms** ±3% | 89.1 ms ±2% | +10% |
| Recursive CTE of 2M steps | **446 ms** ±1% | 501 ms ±2% | +12% |
| LIKE and string functions on 1M texts | **85.4 ms** ±2% | 87.6 ms ±0% | +3% |
| Build and read 300k JSON objects | **419 ms** ±3% | 509 ms ±1% | +22% |
| sqrt, sin, log and pow of 1M numbers | **168 ms** ±1% | 196 ms ±0% | +17% |
| 4 FTS5 searches in 200k documents | **2.98 ms** ±1% | 3.45 ms ±1% | +15% |
| geomean | 209 ms | 236 ms | +13% |

**Throughput.** Reads or writes done per second by all threads together, on a file of 100k rows in WAL mode, higher is better:

| Benchmark | SQLite(C) | liustack/sqlite(direct) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 thread, each reads one row by key | 190k reads/s ±1% | **360k reads/s** ±1% | 275k reads/s ±2% | 102k reads/s ±1% |
| 8 threads, each reads one row by key | 1110k reads/s ±7% | 1132k reads/s ±4% | 871k reads/s ±3% | 418k reads/s ±2% |
| 64 threads, each reads one row by key | 1013k reads/s ±5% | **1150k reads/s** ±6% | 863k reads/s ±8% | 418k reads/s ±4% |
| 1 thread, each reads one row by key, whole file in cache | 230k reads/s ±2% | **682k reads/s** ±3% | 439k reads/s ±2% | 114k reads/s ±1% |
| 8 threads, each reads one row by key, whole file in cache | 1335k reads/s ±8% | **1971k reads/s** ±12% | 1254k reads/s ±13% | 447k reads/s ±2% |
| 64 threads, each reads one row by key, whole file in cache | 1226k reads/s ±12% | **1501k reads/s** ±6% | 1110k reads/s ±4% | 310k reads/s ±9% |
| 8 threads read as above while 8 others write | **966k reads/s** ±8% | 534k reads/s ±6% | 433k reads/s ±6% | 164k reads/s ±2% |
| 8 threads, each inserts one row, while 8 others read | 2492 writes/s ±12% | **17766 writes/s** ±2% | 14922 writes/s ±5% | 14591 writes/s ±4% |
| 64 threads, each inserts one row, `synchronous=FULL` | 890 writes/s ±3% | 57718 writes/s ±5% | 57543 writes/s ±3% | 880 writes/s ±4% |

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
Commit `abf3e08`, Go 1.27.2, Homebrew clang version 22.1.8, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-17. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 2.31 2.78 2.65, after: 10.17 7.93 6.52. The M4 has 4 performance and 6 efficiency cores, and the rates of 8 and 64 threads change with the cores they get, by up to a third: read those rows on linux.

**How fast the engine computes.** The time each job takes on one connection to `:memory:`, lower is better. The last column is how much longer liustack/sqlite takes than C:

| Benchmark | SQLite(C) | liustack/sqlite | slower than C |
| :-- | --: | --: | --: |
| Insert 1M rows in one statement | **188 ms** ±2% | 242 ms ±3% | +29% |
| Index 1M texts | 243 ms ±14% | 239 ms ±14% | -2% |
| Sort 1M texts | **1.61 s** ±4% | 1.77 s ±4% | +10% |
| Average 1M rows in 1000 groups | **172 ms** ±14% | 200 ms ±16% | +16% |
| Join 300k rows to a table of 1M on its key | **35.8 ms** ±4% | 46.3 ms ±1% | +29% |
| Recursive CTE of 2M steps | **200 ms** ±2% | 243 ms ±2% | +22% |
| LIKE and string functions on 1M texts | 46.8 ms ±2% | 47.6 ms ±1% | +2% |
| Build and read 300k JSON objects | **211 ms** ±1% | 230 ms ±1% | +9% |
| sqrt, sin, log and pow of 1M numbers | **67.5 ms** ±2% | 89.7 ms ±2% | +33% |
| 4 FTS5 searches in 200k documents | **1.27 ms** ±1% | 1.83 ms ±1% | +45% |
| geomean | 97.3 ms | 115 ms | +18% |

**Throughput.** Reads or writes done per second by all threads together, on a file of 100k rows in WAL mode, higher is better:

| Benchmark | SQLite(C) | liustack/sqlite(direct) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 thread, each reads one row by key | 670k reads/s ±2% | **1066k reads/s** ±3% | 833k reads/s ±2% | 327k reads/s ±1% |
| 8 threads, each reads one row by key | 958k reads/s ±5% | **1505k reads/s** ±2% | 1367k reads/s ±2% | 771k reads/s ±5% |
| 64 threads, each reads one row by key | 787k reads/s ±8% | **1335k reads/s** ±2% | 1179k reads/s ±3% | 680k reads/s ±3% |
| 1 thread, each reads one row by key, whole file in cache | 889k reads/s ±3% | **2039k reads/s** ±1% | 1426k reads/s ±3% | 395k reads/s ±3% |
| 8 threads, each reads one row by key, whole file in cache | 1417k reads/s ±4% | **2385k reads/s** ±3% | 1974k reads/s ±3% | 854k reads/s ±3% |
| 64 threads, each reads one row by key, whole file in cache | 1200k reads/s ±4% | **1920k reads/s** ±5% | 1594k reads/s ±6% | 760k reads/s ±5% |
| 8 threads read as above while 8 others write | 605k reads/s ±2% | **714k reads/s** ±3% | 631k reads/s ±2% | 316k reads/s ±3% |
| 8 threads, each inserts one row, while 8 others read | 17905 writes/s ±1% | **69176 writes/s** ±3% | 64019 writes/s ±6% | 18028 writes/s ±2% |
| 64 threads, each inserts one row, `synchronous=FULL` | 17635 writes/s ±22% | **549k writes/s** ±2% | 508k writes/s ±5% | 41378 writes/s ±10% |

<!-- /bench:native-darwin-arm64 -->

What the tables show:

- **Work inside the engine** takes up to 22% longer than in C on linux and up to 45% on darwin, 13% and 18% in the geometric mean, and within 3% of C for an index build or `LIKE`: the Go assembly the C becomes is close to, but not as good as, what clang makes for the machine. The engine also uses musl's string and math functions, the math ones built without fused multiply-add so that every target gives the same bits, where the C program uses the system's.
- **One reader** is faster than in C: liustack/sqlite(direct) reads 1.6 to 3.0 times as many rows a second. In C each read takes the WAL read lock with `fcntl` and gives it back with another, unless another connection of the process holds it. liustack/sqlite keeps the lock about a millisecond after the last reader leaves, so the next read makes no system call (see `internal/vfs`). A system call costs most on the linux VM.
- **Many readers** on linux read as many rows as in C or more: liustack/sqlite(direct) reads 1.0 to 1.5 times as many. C's readers share the lock without system calls too, so most of the lead of one reader is gone. On darwin it stays further ahead, at 1.6 to 1.7 times: there C's readers spend about half their time waiting on the pthread mutex that guards the WAL locks of the process, which macOS puts to sleep in the kernel when threads contend for it. liustack/sqlite's locks are Go mutexes.
- **Through `database/sql`** a read takes 10 to 60% more time than liustack/sqlite(direct): that is the work of `database/sql` itself, and with 8 readers or more on linux it brings liustack/sqlite to 78 to 94% of C. mattn pays the same and a cgo call on top. The connection keeps the statement of a read query, so the query is not prepared again each time (see [docs/architecture.md](architecture.md#statements-kept-for-queries)). A `*sql.Stmt` would take the lock of the `*sql.DB` on every query, even one prepared on a `*sql.Conn`, which is why the benchmark uses none.
- **Readers next to writers** on linux read 45 to 55% of the rows C reads, while liustack/sqlite's writers insert 6 to 7 times as many. On darwin the readers read as many as C's, or more.
- **Writes** are where group commit shows: one commit carries many inserts, so liustack/sqlite inserts 3.6 to 7.1 times as many rows as C next to readers, and 29 to 65 times as many with 64 writers and `synchronous=FULL`, where each of C's writers waits for its own fsync.

## What the numbers say

- **Reads** are faster than with cgo: one row by key in about half the time (3.84 µs against 7.18 to 9.36 µs on linux), 100k rows in 36 ms against 52 to 82 ms, a user with its 400 comments in 515 µs against 831 µs to 1.15 ms. A call into the engine is a plain Go call, with no cgo crossing, and a read transaction takes no lock in the kernel when another read of the process is running or just ended (see `internal/vfs`).
- **Many writers** are where group commit shows: from 8 writers up, one commit carries many inserts. At 64 writers an insert costs 3.38 µs against 22 to 29 µs with the other drivers (4 threads). With `synchronous=FULL` the gap grows with the cost of a commit: at 64 writers an insert costs 28.0 µs against 1.35 to 23 ms. On the Mac, modernc at 64 writers spends its time in SQLite's busy handler, whose sleeps grow the longer a writer waits.
- **Readers next to a writer** (ReadWrite, 4 threads): this driver serves the readers fastest (2.69 µs a read against 4.18 to 6.86 µs), and ncruces lets the writer do the most (27.0k writes a second against 17.2k).
- **One writer** (Insert) is close to mattn on both machines. The time goes to the WAL write and the locks SQLite's protocol asks for, the same system calls for every driver.
- **Join and group** (Aggregate), which is all work inside the engine, takes as long as with cgo on linux and 11% longer on darwin: the engine is the same C, compiled by LLVM, and the Go assembly it becomes is close to, but not as good as, what clang makes for the machine.
- **Large text values** are read 5% slower than with mattn on linux (100 rows of 200 KB in 8.66 ms against 8.24 ms) and faster than with modernc and ncruces. On darwin the four are even.
- **Allocations**: a number is copied straight into the `Scan` destination (`driver.RowsColumnScanner`, Go 1.27), without boxing it in an interface, so a scan allocates half as often as with mattn and modernc.
- **Against SQLite in C**, work inside the engine takes 13% (linux) and 18% (darwin) longer in the geometric mean. One reader is faster than C, as a read makes no system call for its lock. Many readers on linux read as many rows as C's or more calling liustack/sqlite directly, and 78 to 94% through `database/sql`. Writers insert 3.6 to 65 times as many rows with group commit. Readers next to writers on linux read about half of what C's do.
- **Point on linux** includes one `fstat` per query, for every driver: when the WAL is empty, as it is in a fresh copy of the fixture, SQLite reads the size of the database file at the start of each read. A system call costs about 0.8 µs on this VM, which is most of the gap between linux and darwin in that row.

## Running them again

`make bench` on a quiet machine, `benchrun publish` to make a set of `bench/published/`, then `make bench-docs` to write the tables. The steps and the options are in [bench/README.md](../bench/README.md). On a shared machine, pin the single-thread runs to one core (`taskset`, `GOMAXPROCS=1`) and take the parallel runs only when nothing else runs.
