---
summary: 'How fast the driver is against mattn/go-sqlite3 (cgo), modernc.org/sqlite and ncruces/go-sqlite3, on linux/amd64 and darwin/arm64, how many writes get database is locked when many write at once (on linux and on Windows x64 and Arm), against SQLite in C, what replication costs, and how the numbers were taken'
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
Commit `91bfddb`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 1 of 1 threads, 2026-11-21. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 0.00 0.00 0.00, after: 1.19 1.17 0.81.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 22.8 µs ±1% | 23.3 µs ±3% | 27.0 µs ±2% | 24.3 µs ±3% |
| InsertTx | **1.41 ms** ±3% | 1.68 ms ±2% | 2.25 ms ±1% | 1.89 ms ±2% |
| Point | **3.85 µs** ±2% | 7.54 µs ±1% | 9.61 µs ±1% | 7.22 µs ±1% |
| Scan | **34.7 ms** ±3% | 81.4 ms ±2% | 79.7 ms ±5% | 56.0 ms ±1% |
| Aggregate | 2.83 ms ±7% | 2.89 ms ±2% | 4.91 ms ±3% | 4.88 ms ±2% |
| PointParallel | **3.82 µs** ±1% | 7.52 µs ±1% | 9.50 µs ±2% | 7.26 µs ±1% |
| InsertParallel/g=1 | **22.5 µs** ±1% | 23.4 µs ±1% | 27.1 µs ±3% | 24.6 µs ±4% |
| InsertParallel/g=8 | 23.1 µs ±1% | 21.1 µs ±9% | 26.5 µs ±6% | 24.4 µs ±7% |
| InsertParallel/g=64 | 20.8 µs ±1% | 21.7 µs ±8% | 31.3 µs ±7% | 31.0 µs ±6% |
| InsertParallelFull/g=1 | 1.31 ms ±11% | 1.25 ms ±6% | 1.33 ms ±7% | 1.27 ms ±5% |
| InsertParallelFull/g=8 | **332 µs** ±2% | 2.87 ms ±17% | 3.07 ms ±17% | 1.29 ms ±7% |
| InsertParallelFull/g=64 | **30.9 µs** ±9% | 4.60 ms ±19% | 7.15 ms ±77% | 1.44 ms ±4% |
| Real/insert | **1.55 ms** ±4% | 1.81 ms ±2% | 2.27 ms ±2% | 2.10 ms ±2% |
| Real/query | **521 µs** ±3% | 876 µs ±3% | 1.13 ms ±2% | 841 µs ±2% |
| Many/n=10 | **8.74 µs** ±2% | 17.0 µs ±2% | 18.6 µs ±2% | 14.5 µs ±1% |
| Many/n=100 | **57.5 µs** ±1% | 112 µs ±2% | 110 µs ±1% | 84.2 µs ±1% |
| Many/n=1000 | **561 µs** ±4% | 1.06 ms ±2% | 1.02 ms ±1% | 799 µs ±1% |
| Large/size=50k | 2.24 ms ±10% | 2.08 ms ±11% | 2.55 ms ±3% | 2.41 ms ±16% |
| Large/size=200k | 8.77 ms ±8% | **8.25 ms** ±3% | 9.69 ms ±5% | 9.49 ms ±14% |
| Readers/g=2 | **34.8 ms** ±2% | 82.4 ms ±2% | 80.1 ms ±2% | 56.1 ms ±2% |
| Readers/g=4 | **34.9 ms** ±2% | 83.2 ms ±2% | 80.6 ms ±1% | 56.5 ms ±3% |
| Readers/g=8 | **35.1 ms** ±2% | 84.2 ms ±1% | 81.4 ms ±2% | 56.9 ms ±2% |
| ReadWrite | **2.67 µs** ±3% | 4.41 µs ±9% | 6.76 µs ±10% | 8.43 µs ±3% |
| geomean | 317 µs | 613 µs | 728 µs | 562 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2230 MB/s ±9% | 2400 MB/s ±10% | 1962 MB/s ±3% | 2076 MB/s ±14% |
| Large/size=200k | 2281 MB/s ±7% | **2423 MB/s** ±3% | 2064 MB/s ±5% | 2108 MB/s ±12% |
| ReadWrite | 5572 writes/s ±20% | 3460 writes/s ±38% | 7226 writes/s ±51% | 4866 writes/s ±14% |

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
| InsertParallelFull/g=64 | **11** | 20 | 28 | 107 |
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
Commit `91bfddb`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-20. The median of 6 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 1.16 1.13 0.76, after: 2.18 2.14 1.45.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **1.58 µs** ±25% | 2.89 µs ±16% | 3.85 µs ±9% | 2.75 µs ±6% |
| InsertParallel/g=1 | 22.9 µs ±1% | 22.7 µs ±1% | 26.4 µs ±4% | 24.2 µs ±1% |
| InsertParallel/g=8 | **11.2 µs** ±7% | 21.7 µs ±12% | 26.2 µs ±6% | 24.7 µs ±2% |
| InsertParallel/g=64 | **3.72 µs** ±6% | 22.8 µs ±2% | 30.7 µs ±2% | 30.2 µs ±4% |
| InsertParallelFull/g=1 | 1.30 ms ±6% | 1.35 ms ±9% | 1.34 ms ±6% | 1.36 ms ±4% |
| InsertParallelFull/g=8 | **325 µs** ±13% | 1.97 ms ±7% | 1.72 ms ±84% | 1.33 ms ±9% |
| InsertParallelFull/g=64 | **30.5 µs** ±5% | 25.0 ms ±18% | 24.1 ms ±44% | 1.40 ms ±6% |
| Readers/g=2 | **17.8 ms** ±3% | 39.8 ms ±2% | 40.3 ms ±4% | 29.4 ms ±3% |
| Readers/g=4 | **9.95 ms** ±4% | 22.2 ms ±4% | 23.5 ms ±10% | 15.6 ms ±3% |
| Readers/g=8 | **9.72 ms** ±3% | 21.4 ms ±3% | 22.4 ms ±4% | 15.7 ms ±6% |
| ReadWrite | **2.67 µs** ±10% | 4.85 µs ±12% | 7.53 µs ±13% | 4.57 µs ±10% |
| geomean | 122 µs | 460 µs | 518 µs | 324 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | **39088 writes/s** ±6% | 13995 writes/s ±8% | 4270 writes/s ±13% | 25989 writes/s ±10% |

Allocations per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **14** | 18 | 30 | 19 |
| InsertParallel/g=1 | **5** | **5** | 6 | **5** |
| InsertParallel/g=8 | 10 | **6** | 7 | **6** |
| InsertParallel/g=64 | 11 | **6** | 7 | 8 |
| InsertParallelFull/g=1 | **5** | **5** | 6 | **5** |
| InsertParallelFull/g=8 | 10 | **6** | 7 | 16 |
| InsertParallelFull/g=64 | **11** | 44 | 54 | 99 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **15** | 19 | 31 | 19 |

<!-- /bench:linux-amd64-4threads -->

## darwin/arm64

<!-- bench:darwin-arm64 -->
Commit `91bfddb`, Go 1.27.2, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-20. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 2.73 2.24 2.02, after: 3.90 3.63 3.28.

Time per op, lower is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 5.98 µs ±3% | 5.90 µs ±1% | 7.28 µs ±3% | 6.83 µs ±4% |
| InsertTx | **470 µs** ±4% | 554 µs ±1% | 733 µs ±1% | 566 µs ±2% |
| Point | **1.07 µs** ±2% | 1.93 µs ±2% | 2.77 µs ±2% | 2.02 µs ±3% |
| Scan | **13.7 ms** ±1% | 29.6 ms ±2% | 29.8 ms ±3% | 20.5 ms ±3% |
| Aggregate | 1.27 ms ±3% | **1.12 ms** ±1% | 2.64 ms ±2% | 1.85 ms ±2% |
| PointParallel | **1.13 µs** ±5% | 1.51 µs ±1% | 1.78 µs ±2% | 2.32 µs ±4% |
| InsertParallel/g=1 | 5.99 µs ±3% | 5.93 µs ±2% | 7.38 µs ±4% | 6.89 µs ±1% |
| InsertParallel/g=8 | **2.98 µs** ±1% | 6.58 µs ±23% | 7.98 µs ±3% | 42.3 µs ±14% |
| InsertParallel/g=64 | **1.14 µs** ±3% | 9.36 µs ±12% | 6.35 ms ±80% | 78.7 µs ±8% |
| InsertParallelFull/g=1 | 80.2 µs ±34% | 77.5 µs ±39% | 77.4 µs ±40% | 70.2 µs ±55% |
| InsertParallelFull/g=8 | **17.0 µs** ±62% | 78.2 µs ±65% | 110 µs ±46% | 48.8 µs ±12% |
| InsertParallelFull/g=64 | **1.64 µs** ±36% | 141 µs ±42% | 28.9 ms ±21% | 86.4 µs ±3% |
| Real/insert | 568 µs ±4% | 567 µs ±12% | 1.00 ms ±16% | 747 µs ±4% |
| Real/query | **212 µs** ±1% | 363 µs ±2% | 556 µs ±1% | 339 µs ±1% |
| Many/n=10 | **2.67 µs** ±1% | 4.84 µs ±1% | 5.32 µs ±1% | 4.40 µs ±1% |
| Many/n=100 | **19.0 µs** ±1% | 34.2 µs ±1% | 33.1 µs ±1% | 26.7 µs ±3% |
| Many/n=1000 | **186 µs** ±1% | 334 µs ±2% | 321 µs ±1% | 253 µs ±2% |
| Large/size=50k | 840 µs ±2% | 822 µs ±3% | 900 µs ±2% | 870 µs ±3% |
| Large/size=200k | 3.41 ms ±11% | 3.34 ms ±4% | 3.73 ms ±3% | 3.62 ms ±5% |
| Readers/g=2 | **7.52 ms** ±1% | 15.6 ms ±1% | 15.9 ms ±2% | 11.0 ms ±1% |
| Readers/g=4 | **4.60 ms** ±3% | 9.22 ms ±1% | 9.46 ms ±1% | 6.61 ms ±2% |
| Readers/g=8 | **4.31 ms** ±5% | 8.28 ms ±1% | 8.08 ms ±4% | 5.85 ms ±3% |
| ReadWrite | **1.60 µs** ±2% | 2.56 µs ±3% | 2.38 µs ±2% | 5.16 µs ±4% |
| geomean | 69.2 µs | 135 µs | 266 µs | 155 µs |

Rates, higher is better:

| Benchmark | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 5950 MB/s ±2% | 6086 MB/s ±3% | 5555 MB/s ±2% | 5745 MB/s ±2% |
| Large/size=200k | 5857 MB/s ±10% | 5982 MB/s ±4% | 5362 MB/s ±3% | 5523 MB/s ±5% |
| ReadWrite | **47085 writes/s** ±3% | 19198 writes/s ±2% | 28764 writes/s ±2% | 19659 writes/s ±4% |

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
| InsertParallel/g=64 | 11 | **6** | 44 | 8 |
| InsertParallelFull/g=1 | **5** | **5** | 6 | **5** |
| InsertParallelFull/g=8 | 10 | **6** | 7 | **6** |
| InsertParallelFull/g=64 | 11 | **6** | 77 | 8 |
| Real/insert | 4.65k | 4.65k | 5.05k | **2.96k** |
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

## Many writers at once

`TestContention` in `bench/contention_test.go` has 64 or 256 goroutines write at once, 30 s for each case on each driver, with `busy_timeout` 5 s for every driver, and counts the writes that got `database is locked`. A transfer is a transaction that reads two rows and updates them. Work inside a transaction is time the program spends there holding the write lock, as a program that computes something between its statements does. Each case checks that the balances still add up. `benchrun contention` runs it on every driver (see [bench/README.md](../bench/README.md)).

<!-- bench:contention-linux-amd64 -->
Commit `10d5df4`, Go 1.27.4, AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-21. Load average (1, 5, 15 minutes) before: 0.00 0.00 0.10, after: 3.12 2.35 1.43.

**The slowest write** (a transaction, or an autocommit statement), and how many writes got `database is locked`, in 30 s of each:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 16.3 ms | 4.43 s | 2.93 s | 83.1 ms |
| Transactions, 64 goroutines, deferred | 1.47 s, 6657740 locked | 2.12 s, 3648767 locked | 52.9 ms, 1865060 locked | 1.38 s, 2502345 locked |
| Transactions, 64 goroutines, `_txlock=immediate` | 28.5 ms | 5.01 s | 4.13 s | 165 ms |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 165 ms | 5.01 s, 45 locked | 4.94 s, 35 locked | 1.78 s |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 162 ms | 5.01 s, 24 locked | 5.01 s, 13 locked | 1.78 s |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 96.2 ms | 5.01 s, 256 locked | 5.01 s, 241 locked | 4.63 s |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 326 ms | 5.01 s, 283 locked | 5.06 s, 216 locked | 4.45 s |

**Writes done per second**, all goroutines together:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 296k writes/s | 47307 writes/s | 32959 writes/s | 29303 writes/s |
| Transactions, 64 goroutines, deferred | 3056 writes/s | 2152 writes/s | 1684 writes/s | 1244 writes/s |
| Transactions, 64 goroutines, `_txlock=immediate` | 16040 writes/s | 13349 writes/s | 9333 writes/s | 9139 writes/s |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 435 writes/s | 378 writes/s | 373 writes/s | 357 writes/s |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 431 writes/s | 375 writes/s | 363 writes/s | 346 writes/s |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 1781 writes/s | 753 writes/s | 746 writes/s | 1759 writes/s |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 850 writes/s | 693 writes/s | 717 writes/s | 606 writes/s |

**The largest WAL** while the case ran. A WAL that keeps growing means the checkpoints copied less than was written, and the writes have not paid for the rest yet:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 3.95 MiB | 5.49 GiB | 3.83 GiB | 3.40 GiB |
| Transactions, 64 goroutines, deferred | 144 MiB | 409 MiB | 321 MiB | 229 MiB |
| Transactions, 64 goroutines, `_txlock=immediate` | 3.93 MiB | 2.48 GiB | 1.74 GiB | 1.70 GiB |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 3.93 MiB | 26.9 MiB | 39.6 MiB | 68.9 MiB |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 3.94 MiB | 69.6 MiB | 65.0 MiB | 108 MiB |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 3.93 MiB | 5.52 MiB | 4.47 MiB | 4.00 MiB |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 3.93 MiB | 134 MiB | 138 MiB | 117 MiB |

<!-- /bench:contention-linux-amd64 -->

- **Deferred transactions that read before they write** fail most of the time with every driver. In WAL mode such a transaction cannot wait for the write lock: once another writer commits, the snapshot it read is old. Start them with `_txlock=immediate`.
- **With `_txlock=immediate`, or a first statement that writes,** no write of this driver got `database is locked`, and the slowest took 29 to 326 ms. Writers of one process wait for each other in a queue, in the order they came, so a write waits for the writes ahead of it and no more. With the other drivers they race for SQLite's lock and sleep longer and longer between tries: most writes get through fast, and a few lose again and again until the busy timeout. mattn and modernc had writes fail that way in all but one such case, while most writes waited a few milliseconds. ncruces had none in this run.
- **The waits are spread evenly**, so the median is longer with this driver where writers queue: with 2 ms of work in each transaction, 147 ms against 6 ms with mattn and modernc, and the 99th percentile 163 ms against 2.9 to 3.1 s.
- **Writes per second** are 15 to 23% higher than mattn's in transactions, 2.4 times with `synchronous=FULL`, and 6.3 times for autocommit inserts, which group commit merges.
- **The WAL of the other drivers grew to gigabytes** in 30 s on this machine: 5.49 GiB for mattn with autocommit inserts and 2.48 GiB with `_txlock=immediate`, 1.70 to 3.83 GiB for modernc and ncruces. SQLite starts the WAL over only at a moment when no connection reads it, and their 64 writers are all inside SQLite, in a transaction or retrying in the busy handler, so that moment does not come. Nothing is lost, but the file keeps its disk space after the load stops. After 30 s of such inserts and a crash, the first read of mattn's database took 2.2 s with the WAL in the page cache and 33 s without, modernc's 1.7 s and 19 s, against 2.3 ms and 63 ms for this driver (`TestReopenAfterCrash`, see [bench/README.md](../bench/README.md)). This driver queues its writers before they enter SQLite and starts the WAL over each 1000 frames: it stayed under 4 MiB, and reached 144 MiB with deferred transactions, whose 64 overlapping reads keep every reader slot of SQLite taken and so can hold off the start over at the 64 MiB limit (see [architecture.md](architecture.md#a-limit-on-the-length-of-the-wal)). On an Apple M4 with 10 cores mattn's WAL stayed at 12 MiB, so how far it grows depends on the machine.

### On Windows

The same test on GitHub's Windows runners, `windows-2025` (x64) and `windows-11-arm` (Arm), every driver on one runner per architecture, run by the `bench-windows` workflow (see [bench/README.md](../bench/README.md)). mattn's cgo is built with the runner's MinGW gcc on x64 and with llvm-mingw on Arm, because the gcc of the Arm runner builds only x86-64. These runners are shared virtual machines, so their times move more from run to run than those of the linux machine above. The counts of `database is locked` are the part to read.

<!-- bench:contention-windows-amd64 -->
Commit `10d5df4`, Go 1.27.4, cgo with gcc (x86_64-posix-seh-rev1, Built by MinGW-Builds project) 15.2.0, INTEL(R) XEON(R) PLATINUM 8573C, Windows 10.0.26100.33828 amd64, GOMAXPROCS 4 of 4 threads, 2026-11-21.

**The slowest write** (a transaction, or an autocommit statement), and how many writes got `database is locked`, in 30 s of each:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 253 ms | 6.25 s, 104 locked | 5.03 s, 86 locked | 368 ms |
| Transactions, 64 goroutines, deferred | 1.22 s, 3572600 locked | 3.63 s, 1351837 locked | 67.3 ms, 345994 locked | 1.05 s, 1971729 locked |
| Transactions, 64 goroutines, `_txlock=immediate` | 201 ms | 5.53 s, 98 locked | 5.10 s, 80 locked | 1.75 s |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 331 ms | 5.52 s, 267 locked | 5.03 s, 47 locked | 2.56 s |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 363 ms | 14.6 s, 190 locked | 5.03 s, 94 locked | 2.81 s |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 628 ms | 5.67 s, 299 locked | 5.03 s, 237 locked | 2.73 s |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 798 ms | 5.63 s, 1183 locked | 5.15 s, 594 locked | 4.82 s, 4 locked |

**Writes done per second**, all goroutines together:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 85211 writes/s | 14355 writes/s | 9980 writes/s | 10194 writes/s |
| Transactions, 64 goroutines, deferred | 1039 writes/s | 2979 writes/s | 717 writes/s | 1267 writes/s |
| Transactions, 64 goroutines, `_txlock=immediate` | 3235 writes/s | 7199 writes/s | 2573 writes/s | 2374 writes/s |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 368 writes/s | 358 writes/s | 356 writes/s | 373 writes/s |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 335 writes/s | 272 writes/s | 351 writes/s | 356 writes/s |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 285 writes/s | 90.9 writes/s | 282 writes/s | 194 writes/s |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 485 writes/s | 562 writes/s | 493 writes/s | 448 writes/s |

**The largest WAL** while the case ran. A WAL that keeps growing means the checkpoints copied less than was written, and the writes have not paid for the rest yet:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 3.96 MiB | 180 MiB | 1.16 GiB | 1.18 GiB |
| Transactions, 64 goroutines, deferred | 199 MiB | 539 MiB | 132 MiB | 234 MiB |
| Transactions, 64 goroutines, `_txlock=immediate` | 3.93 MiB | 8.86 MiB | 499 MiB | 461 MiB |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 3.93 MiB | 5.55 MiB | 69.0 MiB | 72.0 MiB |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 3.94 MiB | 5.76 MiB | 109 MiB | 111 MiB |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 3.93 MiB | 3.98 MiB | 4.63 MiB | 37.2 MiB |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 3.93 MiB | 4.33 MiB | 94.9 MiB | 86.3 MiB |

<!-- /bench:contention-windows-amd64 -->

<!-- bench:contention-windows-arm64 -->
Commit `10d5df4`, Go 1.27.4, cgo with clang version 23.1.5 (https://github.com/llvm/llvm-project.git 392dfff97d29b59d77e5cac6d4b6ef2585fe7117), Cobalt 100, Windows 10.0.26200.9974 arm64, GOMAXPROCS 4 of 4 threads, 2026-11-21.

**The slowest write** (a transaction, or an autocommit statement), and how many writes got `database is locked`, in 30 s of each:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 349 ms | 5.87 s, 41 locked | 5.31 s, 103 locked | 676 ms |
| Transactions, 64 goroutines, deferred | 860 ms, 3928265 locked | 2.27 s, 2044778 locked | 46.0 ms, 285034 locked | 1.92 s, 3064735 locked |
| Transactions, 64 goroutines, `_txlock=immediate` | 160 ms | 5.56 s, 155 locked | 5.03 s, 108 locked | 1.38 s |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 501 ms | 5.58 s, 221 locked | 5.05 s, 62 locked | 4.12 s |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 303 ms | 6.51 s, 233 locked | 5.02 s, 87 locked | 3.56 s |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 708 ms | 5.56 s, 322 locked | 5.03 s, 309 locked | 4.51 s, 2 locked |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 976 ms | 5.66 s, 1226 locked | 5.12 s, 843 locked | 5.00 s, 40 locked |

**Writes done per second**, all goroutines together:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 127k writes/s | 18149 writes/s | 10932 writes/s | 9679 writes/s |
| Transactions, 64 goroutines, deferred | 1535 writes/s | 2851 writes/s | 509 writes/s | 863 writes/s |
| Transactions, 64 goroutines, `_txlock=immediate` | 4948 writes/s | 4924 writes/s | 2514 writes/s | 2137 writes/s |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 370 writes/s | 377 writes/s | 307 writes/s | 299 writes/s |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 368 writes/s | 286 writes/s | 305 writes/s | 286 writes/s |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 173 writes/s | 177 writes/s | 170 writes/s | 121 writes/s |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 460 writes/s | 546 writes/s | 420 writes/s | 367 writes/s |

**The largest WAL** while the case ran. A WAL that keeps growing means the checkpoints copied less than was written, and the writes have not paid for the rest yet:

| Workload | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert one row, 64 goroutines at once | 3.96 MiB | 1.62 GiB | 1.27 GiB | 1.12 GiB |
| Transactions, 64 goroutines, deferred | 272 MiB | 522 MiB | 89.3 MiB | 156 MiB |
| Transactions, 64 goroutines, `_txlock=immediate` | 3.93 MiB | 31.1 MiB | 488 MiB | 415 MiB |
| Transactions, 64 goroutines, immediate, 2 ms of work in each | 3.93 MiB | 4.71 MiB | 59.2 MiB | 57.5 MiB |
| Transactions, 64 goroutines, deferred, a write first, 2 ms of work in each | 3.94 MiB | 6.66 MiB | 94.8 MiB | 89.1 MiB |
| Transactions, 64 goroutines, immediate, `synchronous=FULL` | 3.93 MiB | 3.94 MiB | 5.04 MiB | 22.7 MiB |
| Transactions, 256 goroutines, immediate, 1 ms of work in each | 3.93 MiB | 4.01 MiB | 81.1 MiB | 70.6 MiB |

<!-- /bench:contention-windows-arm64 -->

- **With `_txlock=immediate`, or a first statement that writes,** no write of this driver got `database is locked` on x64 or on Arm, and the slowest took 160 to 976 ms. mattn and modernc had writes fail that way in every such case, ncruces with 256 goroutines (4 on x64, 40 on Arm) and with `synchronous=FULL` on Arm (2).
- **Short transactions are slower on Windows x64.** With `_txlock=immediate` and nothing done inside, this driver does 3235 a second on x64 and 4947 on Arm, mattn 7198 and 4923, where on linux this driver does more than mattn (16039 against 13348). The driver runs its automatic checkpoint before it hands the write lock on, and a flush on the x64 runner's disk is slow (about 67 ms for 1000 frames), so the writers behind it wait. SQLite's own checkpoint, which the other drivers use, runs after the lock is free, at the cost of a WAL that can grow without end. With 1 or 2 ms of work in each transaction the drivers are close: 335 to 485 writes a second for this driver against 272 to 562 for mattn on x64, and 367 to 459 against 286 to 545 on Arm.
- **Autocommit inserts**, which group commit merges, are still the most: 85210 a second on x64 and 127k on Arm, against 14354 and 18148 for mattn.
- **The WAL of the other drivers** reached 1.12 to 1.62 GiB in the insert case (mattn on x64 180 MiB), against 4 MiB for this driver.

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
Commit `91bfddb`, Go 1.27.4, Ubuntu clang version 22.1.8 (++20260714015917+ca7933e47d3a-1~exp1~20260714135927.17), AMD EPYC 7B13, Linux 7.0.0-1017-gcp x86_64, GOMAXPROCS 4 of 4 threads, 2026-11-21. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 1.99 1.93 1.36, after: 5.16 4.36 3.84.

**How fast the engine computes.** The time each job takes on one connection to `:memory:`, lower is better. The last column is how much longer liustack/sqlite takes than C:

| Benchmark | SQLite(C) | liustack/sqlite | slower than C |
| :-- | --: | --: | --: |
| Insert 1M rows in one statement | **462 ms** ±2% | 554 ms ±0% | +20% |
| Index 1M texts | **396 ms** ±1% | 406 ms ±1% | +2% |
| Sort 1M texts | **4.10 s** ±5% | 4.58 s ±6% | +12% |
| Average 1M rows in 1000 groups | **315 ms** ±1% | 384 ms ±2% | +22% |
| Join 300k rows to a table of 1M on its key | **79.5 ms** ±2% | 89.9 ms ±3% | +13% |
| Recursive CTE of 2M steps | **447 ms** ±1% | 498 ms ±1% | +11% |
| LIKE and string functions on 1M texts | **85.4 ms** ±1% | 88.9 ms ±1% | +4% |
| Build and read 300k JSON objects | **417 ms** ±1% | 510 ms ±1% | +22% |
| sqrt, sin, log and pow of 1M numbers | **165 ms** ±2% | 196 ms ±0% | +19% |
| 4 FTS5 searches in 200k documents | **2.96 ms** ±1% | 3.43 ms ±2% | +16% |
| geomean | 207 ms | 236 ms | +14% |

**Throughput.** Reads or writes done per second by all threads together, on a file of 100k rows in WAL mode, higher is better:

| Benchmark | SQLite(C) | liustack/sqlite(direct) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 thread, each reads one row by key | 189k reads/s ±2% | **373k reads/s** ±4% | 284k reads/s ±4% | 102k reads/s ±2% |
| 8 threads, each reads one row by key | 1075k reads/s ±5% | **1280k reads/s** ±4% | 955k reads/s ±5% | 421k reads/s ±2% |
| 64 threads, each reads one row by key | 982k reads/s ±3% | **1289k reads/s** ±4% | 914k reads/s ±3% | 429k reads/s ±4% |
| 1 thread, each reads one row by key, whole file in cache | 235k reads/s ±4% | **719k reads/s** ±9% | 481k reads/s ±11% | 115k reads/s ±2% |
| 8 threads, each reads one row by key, whole file in cache | 1395k reads/s ±7% | **2228k reads/s** ±8% | 1430k reads/s ±7% | 443k reads/s ±4% |
| 64 threads, each reads one row by key, whole file in cache | 1204k reads/s ±13% | **2003k reads/s** ±5% | 1254k reads/s ±5% | 332k reads/s ±8% |
| 8 threads read as above while 8 others write | 968k reads/s ±7% | 975k reads/s ±5% | 593k reads/s ±12% | 164k reads/s ±4% |
| 8 threads, each inserts one row, while 8 others read | 2431 writes/s ±21% | 20551 writes/s ±8% | **27319 writes/s** ±14% | 14970 writes/s ±5% |
| 64 threads, each inserts one row, `synchronous=FULL` | 845 writes/s ±3% | 56081 writes/s ±4% | 55955 writes/s ±3% | 851 writes/s ±5% |

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
Commit `91bfddb`, Go 1.27.2, Homebrew clang version 22.1.8, Apple M4, Darwin 24.3.0 arm64, GOMAXPROCS 10 of 10 threads, 2026-11-21. The median of 10 runs, ± is its 95% confidence interval. Load average (1, 5, 15 minutes) before: 2.19 2.04 1.94, after: 8.23 7.31 6.29.

**How fast the engine computes.** The time each job takes on one connection to `:memory:`, lower is better. The last column is how much longer liustack/sqlite takes than C:

| Benchmark | SQLite(C) | liustack/sqlite | slower than C |
| :-- | --: | --: | --: |
| Insert 1M rows in one statement | **187 ms** ±2% | 240 ms ±3% | +28% |
| Index 1M texts | 208 ms ±3% | 216 ms ±4% | +3% |
| Sort 1M texts | **1.59 s** ±3% | 1.75 s ±9% | +10% |
| Average 1M rows in 1000 groups | **152 ms** ±5% | 197 ms ±3% | +29% |
| Join 300k rows to a table of 1M on its key | **36.1 ms** ±2% | 46.4 ms ±0% | +29% |
| Recursive CTE of 2M steps | **199 ms** ±4% | 242 ms ±1% | +21% |
| LIKE and string functions on 1M texts | 46.2 ms ±3% | 47.8 ms ±1% | +3% |
| Build and read 300k JSON objects | **212 ms** ±1% | 230 ms ±1% | +8% |
| sqrt, sin, log and pow of 1M numbers | **67.3 ms** ±1% | 89.5 ms ±1% | +33% |
| 4 FTS5 searches in 200k documents | **1.26 ms** ±3% | 1.83 ms ±1% | +44% |
| geomean | 94.5 ms | 114 ms | +20% |

**Throughput.** Reads or writes done per second by all threads together, on a file of 100k rows in WAL mode, higher is better:

| Benchmark | SQLite(C) | liustack/sqlite(direct) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 thread, each reads one row by key | 669k reads/s ±10% | **1074k reads/s** ±12% | 828k reads/s ±17% | 325k reads/s ±10% |
| 8 threads, each reads one row by key | 867k reads/s ±3% | **1628k reads/s** ±2% | 1464k reads/s ±6% | 758k reads/s ±8% |
| 64 threads, each reads one row by key | 780k reads/s ±9% | **1550k reads/s** ±7% | 1300k reads/s ±5% | 690k reads/s ±9% |
| 1 thread, each reads one row by key, whole file in cache | 872k reads/s ±3% | **2020k reads/s** ±4% | 1438k reads/s ±7% | 396k reads/s ±4% |
| 8 threads, each reads one row by key, whole file in cache | 1409k reads/s ±5% | **3846k reads/s** ±2% | 3111k reads/s ±4% | 871k reads/s ±4% |
| 64 threads, each reads one row by key, whole file in cache | 1211k reads/s ±7% | **3191k reads/s** ±3% | 2625k reads/s ±7% | 767k reads/s ±5% |
| 8 threads read as above while 8 others write | 594k reads/s ±4% | 605k reads/s ±3% | 570k reads/s ±4% | 315k reads/s ±8% |
| 8 threads, each inserts one row, while 8 others read | 17963 writes/s ±1% | **78577 writes/s** ±8% | 67896 writes/s ±7% | 18324 writes/s ±6% |
| 64 threads, each inserts one row, `synchronous=FULL` | 16549 writes/s ±32% | 513k writes/s ±16% | 445k writes/s ±34% | 15729 writes/s ±96% |

<!-- /bench:native-darwin-arm64 -->

What the tables show:

- **Work inside the engine** takes up to 22% longer than in C on linux and up to 44% on darwin, 14% and 20% in the geometric mean, and within 4% of C for an index build or `LIKE`: the Go assembly the C becomes is close to, but not as good as, what clang makes for the machine. The engine also uses musl's string and math functions, the math ones built without fused multiply-add so that every target gives the same bits, where the C program uses the system's.
- **One reader** is faster than in C: liustack/sqlite(direct) reads 1.6 to 3.1 times as many rows a second. In C each read takes the WAL read lock with `fcntl` and gives it back with another, unless another connection of the process holds it. liustack/sqlite keeps the lock about a millisecond after the last reader leaves, so the next read makes no system call (see `internal/vfs`). A system call costs most on the linux VM.
- **Many readers** on linux read more rows than in C: liustack/sqlite(direct) reads 1.2 to 1.7 times as many. C's readers share the lock without system calls too, so part of the lead of one reader is gone. On darwin it stays further ahead, at 1.9 to 2.7 times: there C's readers spend about half their time waiting on the pthread mutex that guards the WAL locks of the process, which macOS puts to sleep in the kernel when threads contend for it. liustack/sqlite's readers join a lock the process holds with one atomic step, and wait for no mutex.
- **Through `database/sql`** a read takes 11 to 60% more time than liustack/sqlite(direct): that is the work of `database/sql` itself, and with 8 readers or more on linux it leaves liustack/sqlite at 89 to 104% of C. mattn pays the same and a cgo call on top. The connection keeps the statement of a read query, so the query is not prepared again each time (see [docs/architecture.md](architecture.md#statements-kept-for-queries)). A `*sql.Stmt` would take the lock of the `*sql.DB` on every query, even one prepared on a `*sql.Conn`, which is why the benchmark uses none.
- **Readers next to writers** read as many rows as C's when they call liustack/sqlite directly (101% on linux, 102% on darwin), and through `database/sql` 61% on linux and 96% on darwin. The writers beside them insert 8.5 to 11 times as many rows as C's on linux and 3.8 to 4.4 times on darwin. The more the writers commit, the less the readers read, with any driver: a reader that sees a new commit drops the pages it holds and reads them again (see [busy-cores.md](busy-cores.md#6-how-to-measure-it)). mattn, through `database/sql` too, inserts about half as many rows on linux and reads about a quarter as many.
- **Writes** are where group commit shows: one commit carries many inserts, so with 64 writers and `synchronous=FULL`, where each of C's writers waits for its own fsync, liustack/sqlite inserts 27 to 66 times as many rows. Next to readers it inserts 3.8 to 11 times as many as C.

## What the numbers say

- **Reads** are faster than with cgo: one row by key in about half the time (3.85 µs against 7.22 to 9.61 µs on linux), 100k rows in 35 ms against 56 to 81 ms, a user with its 400 comments in 521 µs against 841 µs to 1.13 ms. A call into the engine is a plain Go call, with no cgo crossing, and a read transaction takes no lock in the kernel when another read of the process is running or just ended (see `internal/vfs`).
- **Many writers** are where group commit shows: from 8 writers up, one commit carries many inserts. At 64 writers an insert costs 3.72 µs against 23 to 31 µs with the other drivers (4 threads). With `synchronous=FULL` the gap grows with the cost of a commit: at 64 writers an insert costs 30.5 µs against 1.40 to 25.0 ms. On the Mac, modernc at 64 writers spends its time in SQLite's busy handler, whose sleeps grow the longer a writer waits.
- **Readers next to a writer** (ReadWrite, 4 threads): this driver serves the readers fastest (2.67 µs a read against 4.57 to 7.53 µs) and its writer writes the most (39.1k writes a second, against 26.0k with ncruces and 14.0k with mattn).
- **One writer** (Insert) is close to mattn on both machines. The time goes to the WAL write and the locks SQLite's protocol asks for, the same system calls for every driver.
- **Join and group** (Aggregate), which is all work inside the engine, takes as long as with cgo on linux and 13% longer on darwin: the engine is the same C, compiled by LLVM, and the Go assembly it becomes is close to, but not as good as, what clang makes for the machine.
- **Large text values** are read 6% slower than with mattn on linux (100 rows of 200 KB in 8.77 ms against 8.25 ms) and faster than with modernc and ncruces. On darwin the four are even.
- **Allocations**: a number is copied straight into the `Scan` destination (`driver.RowsColumnScanner`, Go 1.27), without boxing it in an interface, so a scan allocates half as often as with mattn and modernc.
- **Against SQLite in C**, work inside the engine takes 14% (linux) and 20% (darwin) longer in the geometric mean. One reader is faster than C, as a read makes no system call for its lock. Many readers on linux read more rows than C's calling liustack/sqlite directly, and 89 to 104% through `database/sql`. With group commit, 64 writers insert 27 to 66 times as many rows. Readers next to writers on linux read as many rows as C's calling liustack/sqlite directly and 61% through `database/sql`, with writers that insert 8.5 to 11 times as many.
- **Point on linux** includes one `fstat` per query, for every driver: when the WAL is empty, as it is in a fresh copy of the fixture, SQLite reads the size of the database file at the start of each read. A system call costs about 0.8 µs on this VM, which is most of the gap between linux and darwin in that row.

## Running them again

`make bench` on a quiet machine, `benchrun publish` to make a set of `bench/published/`, then `make bench-docs` to write the tables. The steps and the options are in [bench/README.md](../bench/README.md). On a shared machine, pin the single-thread runs to one core (`taskset`, `GOMAXPROCS=1`) and take the parallel runs only when nothing else runs.
