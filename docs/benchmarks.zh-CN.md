# 基准测试

[English](benchmarks.md) · 简体中文

测试内容、设置和运行方法见 [bench/README.md](../bench/README.md)。简单说：WAL，`synchronous=NORMAL`（`InsertParallelFull` 用 `FULL`），每个基准一个 `*sql.DB`，每次用自己的新文件，所有驱动都核对过相同的设置。有几项照搬 [cvilsmeier/go-sqlite-bench](https://github.com/cvilsmeier/go-sqlite-bench)，每一项都核对读出来的结果。各驱动轮流跑，每轮每个基准各跑一次，每轮换下一个驱动先跑，这样机器上的其他负载对大家的影响一样。每格是各轮的中位数，`±` 是它的 95% 置信区间。每行最好的那个，区间和别的驱动都不重叠时才加粗。表格由 `bench/cmd/benchrun` 从 `bench/published/` 生成，不手写。

| 驱动 | 版本 | 构建方式 |
| --- | --- | --- |
| 本驱动 | 见每节的提交 | C 编译成 Go 汇编，`CGO_ENABLED=0` |
| mattn/go-sqlite3 | v1.14.52 | cgo |
| modernc.org/sqlite | v1.61.1 | C 翻译成 Go |
| ncruces/go-sqlite3 | v0.36.0 | SQLite 先编成 wasm，再由 wasm2go 翻译成 Go |

## linux/amd64

一台 Google Cloud 虚拟机（t2d-standard-4），AMD EPYC 7B13，4 个核，每核一个线程。它上面系统调用很慢（`getppid` 要 415 ns），所以凡是加锁、写文件的操作都比台式机贵。磁盘是虚拟机的持久磁盘：一次带 fsync 的提交约 1.2 到 1.3 ms。

### 单线程

`GOMAXPROCS=1`，绑在一个核上。并发场景的 goroutine 在上面轮流跑。

<!-- bench:linux-amd64-1thread -->
提交 `91bfddb`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 1，共 1 个线程，2026-11-21。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.00 0.00 0.00，结束后 1.19 1.17 0.81。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 317 µs | 613 µs | 728 µs | 562 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2230 MB/s ±9% | 2400 MB/s ±10% | 1962 MB/s ±3% | 2076 MB/s ±14% |
| Large/size=200k | 2281 MB/s ±7% | **2423 MB/s** ±3% | 2064 MB/s ±5% | 2108 MB/s ±12% |
| ReadWrite | 5572 writes/s ±20% | 3460 writes/s ±38% | 7226 writes/s ±51% | 4866 writes/s ±14% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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

### 全部 4 个核

机器上没有别的任务。只跑用多个 goroutine 的场景。

<!-- bench:linux-amd64-4threads -->
提交 `91bfddb`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-20。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 1.16 1.13 0.76，结束后 2.18 2.14 1.45。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 122 µs | 460 µs | 518 µs | 324 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | **39088 writes/s** ±6% | 13995 writes/s ±8% | 4270 writes/s ±13% | 25989 writes/s ±10% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
提交 `91bfddb`，Go 1.27.2，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-20。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 2.73 2.24 2.02，结束后 3.90 3.63 3.28。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 69.2 µs | 135 µs | 266 µs | 155 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 5950 MB/s ±2% | 6086 MB/s ±3% | 5555 MB/s ±2% | 5745 MB/s ±2% |
| Large/size=200k | 5857 MB/s ±10% | 5982 MB/s ±4% | 5362 MB/s ±3% | 5523 MB/s ±5% |
| ReadWrite | **47085 writes/s** ±3% | 19198 writes/s ±2% | 28764 writes/s ±2% | 19659 writes/s ±4% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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

## 很多写者同时写

`bench/contention_test.go` 里的 `TestContention` 让 64 或 256 个 goroutine 同时写，每个驱动每项跑 30 秒，每个驱动的 `busy_timeout` 都是 5 秒，数一数有多少次写报了 `database is locked`。转账是一个事务，先读两行再改它们。事务里做的事，是程序拿着写锁在事务里花的时间，就像程序在两条语句之间算点东西。每一项都检查余额加起来没变。`benchrun contention` 在每个驱动上跑它（见 [bench/README.md](../bench/README.md)）。

<!-- bench:contention-linux-amd64 -->
提交 `e5fe295`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-20。负载（1、5、15 分钟）开始前 0.07 0.32 0.80，结束后 3.27 2.55 1.80。

**最慢的一次写**（一个事务，或一条自动提交的语句），以及每项 30 秒里有多少次写报 `database is locked`：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 23.9 ms | 3.93 s | 3.63 s | 86.4 ms |
| 事务，64 个 goroutine，deferred | 610 ms，5289455 次锁错误 | 1.78 s，3034824 次锁错误 | 39.6 ms，935625 次锁错误 | 1.34 s，1663455 次锁错误 |
| 事务，64 个 goroutine，`_txlock=immediate` | 22.6 ms | 4.83 s，3 次锁错误 | 3.84 s | 252 ms |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 173 ms | 5.01 s，70 次锁错误 | 5.06 s，18 次锁错误 | 2.22 s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 172 ms | 5.02 s，18 次锁错误 | 5.02 s，37 次锁错误 | 2.06 s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 215 ms | 5.01 s，77 次锁错误 | 5.04 s，125 次锁错误 | 1.41 s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 329 ms | 5.01 s，273 次锁错误 | 5.11 s，232 次锁错误 | 5.01 s，7 次锁错误 |

**每秒完成的写**，所有 goroutine 加起来：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 240k writes/s | 46957 writes/s | 20563 writes/s | 14560 writes/s |
| 事务，64 个 goroutine，deferred | 3668 writes/s | 2739 writes/s | 1061 writes/s | 1007 writes/s |
| 事务，64 个 goroutine，`_txlock=immediate` | 14148 writes/s | 11267 writes/s | 6030 writes/s | 5787 writes/s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 426 writes/s | 330 writes/s | 325 writes/s | 338 writes/s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 423 writes/s | 314 writes/s | 315 writes/s | 330 writes/s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 1610 writes/s | 480 writes/s | 486 writes/s | 1109 writes/s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 831 writes/s | 610 writes/s | 528 writes/s | 354 writes/s |

<!-- /bench:contention-linux-amd64 -->

- **先读后写的 deferred 事务**在每个驱动上大多数都失败。WAL 模式下这种事务不能等写锁：别的写者一提交，它读到的快照就旧了。这种事务用 `_txlock=immediate` 开始。
- **用 `_txlock=immediate`，或者第一条语句就写**，本驱动没有一次写报 `database is locked`，最慢的一次 23 到 329 ms。同一个进程里的写者按到达顺序排队，一次写只等排在它前面的写。别的驱动里写者去抢 SQLite 的锁，两次尝试之间睡得越来越久：大多数写很快就过去了，少数几次一输再输，直到 busy timeout。mattn 在每一项这样的场景里都有写因此失败，modernc 只有一项没有，ncruces 有一项，而同时大多数写只等了几毫秒。
- **等待分得均匀**，所以写者排队时本驱动的中位数更长：每个事务里做 2 ms 的事时，中位数 149 ms，mattn 和 modernc 7 ms，第 99 百分位 166 ms，它们 2.8 到 3.0 秒。
- **每秒完成的写**：事务比 mattn 多 25% 到 36%，`synchronous=FULL` 下是 3.4 倍，自动提交的插入是 5 倍，因为组提交把它们合成一次提交。

### 在 Windows 上

同一个测试跑在 GitHub 的 Windows runner 上，`windows-2025`（x64）和 `windows-11-arm`（Arm），每种架构所有驱动在同一台 runner 上跑，由 `bench-windows` 工作流运行（见 [bench/README.md](../bench/README.md)）。mattn 的 cgo 在 x64 上用 runner 自带的 MinGW gcc 编译，在 Arm 上用 llvm-mingw，因为 Arm runner 的 gcc 只能编出 x86-64。这些 runner 是共享的虚拟机，时间在两次运行之间比上面那台 linux 机器波动大。要看的是 `database is locked` 的次数。

<!-- bench:contention-windows-amd64 -->
提交 `9b06a41`，Go 1.27.4，cgo with gcc (x86_64-posix-seh-rev1, Built by MinGW-Builds project) 15.2.0，AMD EPYC 9V45 96-Core Processor，Windows 10.0.26100.33828 amd64，GOMAXPROCS 4，共 4 个线程，2026-11-21。

**最慢的一次写**（一个事务，或一条自动提交的语句），以及每项 30 秒里有多少次写报 `database is locked`：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 1.06 s | 42.3 s，197 次锁错误 | 31.6 s，139 次锁错误 | 851 ms |
| 事务，64 个 goroutine，deferred | 1.37 s，6240772 次锁错误 | 2.03 s，3657227 次锁错误 | 3.73 s，1795547 次锁错误 | 1.80 s，3920556 次锁错误 |
| 事务，64 个 goroutine，`_txlock=immediate` | 192 ms | 5.51 s，139 次锁错误 | 5.07 s，89 次锁错误 | 2.23 s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 471 ms | 5.49 s，289 次锁错误 | 5.03 s，84 次锁错误 | 2.74 s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 399 ms | 5.51 s，261 次锁错误 | 5.03 s，88 次锁错误 | 2.55 s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 279 ms | 5.52 s，291 次锁错误 | 5.02 s，314 次锁错误 | 3.75 s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 1.02 s | 5.60 s，1150 次锁错误 | 5.04 s，654 次锁错误 | 4.85 s，3 次锁错误 |

**每秒完成的写**，所有 goroutine 加起来：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 79446 writes/s | 58649 writes/s | 24843 writes/s | 21223 writes/s |
| 事务，64 个 goroutine，deferred | 1325 writes/s | 6731 writes/s | 790 writes/s | 1441 writes/s |
| 事务，64 个 goroutine，`_txlock=immediate` | 5503 writes/s | 16346 writes/s | 5742 writes/s | 5151 writes/s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 354 writes/s | 390 writes/s | 324 writes/s | 378 writes/s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 340 writes/s | 379 writes/s | 351 writes/s | 371 writes/s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 340 writes/s | 372 writes/s | 319 writes/s | 242 writes/s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 459 writes/s | 409 writes/s | 552 writes/s | 557 writes/s |

<!-- /bench:contention-windows-amd64 -->

<!-- bench:contention-windows-arm64 -->
提交 `9b06a41`，Go 1.27.4，cgo with clang version 23.1.5 (https://github.com/llvm/llvm-project.git 392dfff97d29b59d77e5cac6d4b6ef2585fe7117)，Cobalt 100，Windows 10.0.26200.9974 arm64，GOMAXPROCS 4，共 4 个线程，2026-11-21。

**最慢的一次写**（一个事务，或一条自动提交的语句），以及每项 30 秒里有多少次写报 `database is locked`：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 416 ms | 19.8 s，2 次锁错误 | 5.02 s，66 次锁错误 | 413 ms |
| 事务，64 个 goroutine，deferred | 1.83 s，4246078 次锁错误 | 1.98 s，2154308 次锁错误 | 451 ms，390655 次锁错误 | 2.18 s，3113389 次锁错误 |
| 事务，64 个 goroutine，`_txlock=immediate` | 142 ms | 5.53 s，144 次锁错误 | 5.05 s，53 次锁错误 | 1.02 s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 397 ms | 5.46 s，138 次锁错误 | 5.02 s，41 次锁错误 | 2.70 s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 385 ms | 5.46 s，143 次锁错误 | 5.03 s，54 次锁错误 | 2.75 s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 573 ms | 5.50 s，296 次锁错误 | 5.03 s，189 次锁错误 | 3.53 s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 721 ms | 5.59 s，852 次锁错误 | 5.06 s，597 次锁错误 | 4.77 s，9 次锁错误 |

**每秒完成的写**，所有 goroutine 加起来：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 167k writes/s | 34905 writes/s | 11372 writes/s | 10092 writes/s |
| 事务，64 个 goroutine，deferred | 1333 writes/s | 3489 writes/s | 871 writes/s | 1054 writes/s |
| 事务，64 个 goroutine，`_txlock=immediate` | 5692 writes/s | 9614 writes/s | 2549 writes/s | 2320 writes/s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 384 writes/s | 387 writes/s | 381 writes/s | 393 writes/s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 349 writes/s | 380 writes/s | 379 writes/s | 388 writes/s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 214 writes/s | 270 writes/s | 210 writes/s | 191 writes/s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 546 writes/s | 631 writes/s | 552 writes/s | 476 writes/s |

<!-- /bench:contention-windows-arm64 -->

- **用 `_txlock=immediate`，或者第一条语句就写**，本驱动在 x64 和 Arm 上都没有一次写报 `database is locked`，最慢的一次 142 ms 到 1.02 秒。mattn 和 modernc 在每一项这样的场景里都有写因此失败，ncruces 只在 256 个 goroutine 时有（x64 上 3 次，Arm 上 9 次）。
- **短事务在 Windows 上更慢。**用 `_txlock=immediate` 且事务里不做事时，本驱动在 x64 上每秒 5503 次，Arm 上 5692 次，mattn 是 16346 和 9614 次，而在 linux 上本驱动比 mattn 多（14148 对 11267）。原因还没找到。事务里做 1 或 2 ms 的事时几个驱动差不多：x64 上本驱动每秒 340 到 459 次写，mattn 379 到 409 次，Arm 上 349 到 546 次对 380 到 631 次。
- **自动提交的插入**由组提交合并，仍然最多：x64 上每秒 79446 次，Arm 上 167k 次，mattn 是 58649 和 34905 次。

## 复制

[复制](replication.zh-CN.md)在程序里复制每次提交，所以 CPU 花在程序自己的进程里。`replica` 里的 `BenchmarkGroupCommit` 让 8 个或 64 个 goroutine 同时插入，每次一行（WAL，`synchronous=NORMAL`），分别关掉和打开复制。存储是同一台机器另一个核上的 MinIO，它的开销不落在基准的核上，就像存储在别的机器上时一样。

提交 `8671819`，Go 1.27.4，AMD EPYC 7B13（4 核，没有超线程），Linux 7.0.0-1017-gcp，2026-11-20。每组跑 6 轮，用 benchstat 比较（`~` 表示和噪声分不开）。负载开始前 0.00，结束后 2.42。原始输出在 [bench/published/replica/](../bench/published/replica/)。

单核（`GOMAXPROCS=1`，在核 0 上，MinIO 在核 3）：

```
              关复制          开复制
g=8           24.27µ ± 3%    24.13µ ± 3%        ~ (p=0.818 n=6)
g=64          22.28µ ± 2%    23.08µ ± 1%   +3.57% (p=0.002 n=6)
geomean       23.25µ         23.60µ        +1.48%
```

三核（`GOMAXPROCS=3`，在核 0 到 2 上，MinIO 在核 3）：

```
              关复制          开复制
g=8           12.20µ ±  2%   12.33µ ±  2%        ~ (p=0.180 n=6)
g=64           4.154µ ± 4%    4.098µ ± 10%       ~ (p=0.937 n=6)
geomean        7.118µ         7.107µ        -0.15%
```

也就是说，开复制后每次插入最多多花约 4%，三核上看不出差别。用 MinIO 二进制自己跑一遍，把它绑在基准以外的核上（linux 上 `MINIO` 可以指向一个执行 `taskset -c 3 minio "$@"` 的脚本，测试本身用 `taskset -c 0` 跑）：

```sh
MINIO=<minio> go test ./replica -run '^$' -bench GroupCommit -benchtime 3s -count 6
```

## 和 C 版 SQLite 比

同一份 SQLite 3.54.1，用 `engine/sqlite.c` 里的编译选项，由 clang `-O2` 编成直接调用 C API 的 C 程序。负载和四列各是什么见 [bench/README.md](../bench/README.md#against-sqlite-in-c)。简单说：SQLite(C) 和 liustack/sqlite(直接调用) 把每条语句编译一次，反复执行，直接调 C API 或驱动本身。两列 (database/sql) 像普通程序一样读写：每个读者一个 `*sql.Conn`，用 `QueryRowContext`，每次写用 `db.Exec`。mattn 通过 cgo 跑 SQLite 的 C。

### linux/amd64

<!-- bench:native-linux-amd64 -->
提交 `91bfddb`，Go 1.27.4，Ubuntu clang version 22.1.8 (++20260714015917+ca7933e47d3a-1~exp1~20260714135927.17)，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-21。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 1.99 1.93 1.36，结束后 5.16 4.36 3.84。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **462 ms** ±2% | 554 ms ±0% | +20% |
| 给 100 万个文本建索引 | **396 ms** ±1% | 406 ms ±1% | +2% |
| 排序 100 万个文本 | **4.10 s** ±5% | 4.58 s ±6% | +12% |
| 100 万行分 1000 组求平均 | **315 ms** ±1% | 384 ms ±2% | +22% |
| 30 万行按主键连 100 万行的表 | **79.5 ms** ±2% | 89.9 ms ±3% | +13% |
| 递归 CTE 走 200 万步 | **447 ms** ±1% | 498 ms ±1% | +11% |
| 100 万个文本做 LIKE 和字符串函数 | **85.4 ms** ±1% | 88.9 ms ±1% | +4% |
| 生成并读取 30 万个 JSON 对象 | **417 ms** ±1% | 510 ms ±1% | +22% |
| 100 万个数算 sqrt、sin、log、pow | **165 ms** ±2% | 196 ms ±0% | +19% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **2.96 ms** ±1% | 3.43 ms ±2% | +16% |
| 几何平均 | 207 ms | 236 ms | +14% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 189k reads/s ±2% | **373k reads/s** ±4% | 284k reads/s ±4% | 102k reads/s ±2% |
| 8 个线程，每次按主键读一行 | 1075k reads/s ±5% | **1280k reads/s** ±4% | 955k reads/s ±5% | 421k reads/s ±2% |
| 64 个线程，每次按主键读一行 | 982k reads/s ±3% | **1289k reads/s** ±4% | 914k reads/s ±3% | 429k reads/s ±4% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 235k reads/s ±4% | **719k reads/s** ±9% | 481k reads/s ±11% | 115k reads/s ±2% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1395k reads/s ±7% | **2228k reads/s** ±8% | 1430k reads/s ±7% | 443k reads/s ±4% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1204k reads/s ±13% | **2003k reads/s** ±5% | 1254k reads/s ±5% | 332k reads/s ±8% |
| 8 个线程照上面读，同时另有 8 个线程在写 | 968k reads/s ±7% | 975k reads/s ±5% | 593k reads/s ±12% | 164k reads/s ±4% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 2431 writes/s ±21% | 20551 writes/s ±8% | **27319 writes/s** ±14% | 14970 writes/s ±5% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 845 writes/s ±3% | 56081 writes/s ±4% | 55955 writes/s ±3% | 851 writes/s ±5% |

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
提交 `91bfddb`，Go 1.27.2，Homebrew clang version 22.1.8，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-21。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 2.19 2.04 1.94，结束后 8.23 7.31 6.29。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **187 ms** ±2% | 240 ms ±3% | +28% |
| 给 100 万个文本建索引 | 208 ms ±3% | 216 ms ±4% | +3% |
| 排序 100 万个文本 | **1.59 s** ±3% | 1.75 s ±9% | +10% |
| 100 万行分 1000 组求平均 | **152 ms** ±5% | 197 ms ±3% | +29% |
| 30 万行按主键连 100 万行的表 | **36.1 ms** ±2% | 46.4 ms ±0% | +29% |
| 递归 CTE 走 200 万步 | **199 ms** ±4% | 242 ms ±1% | +21% |
| 100 万个文本做 LIKE 和字符串函数 | 46.2 ms ±3% | 47.8 ms ±1% | +3% |
| 生成并读取 30 万个 JSON 对象 | **212 ms** ±1% | 230 ms ±1% | +8% |
| 100 万个数算 sqrt、sin、log、pow | **67.3 ms** ±1% | 89.5 ms ±1% | +33% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **1.26 ms** ±3% | 1.83 ms ±1% | +44% |
| 几何平均 | 94.5 ms | 114 ms | +20% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 669k reads/s ±10% | **1074k reads/s** ±12% | 828k reads/s ±17% | 325k reads/s ±10% |
| 8 个线程，每次按主键读一行 | 867k reads/s ±3% | **1628k reads/s** ±2% | 1464k reads/s ±6% | 758k reads/s ±8% |
| 64 个线程，每次按主键读一行 | 780k reads/s ±9% | **1550k reads/s** ±7% | 1300k reads/s ±5% | 690k reads/s ±9% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 872k reads/s ±3% | **2020k reads/s** ±4% | 1438k reads/s ±7% | 396k reads/s ±4% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1409k reads/s ±5% | **3846k reads/s** ±2% | 3111k reads/s ±4% | 871k reads/s ±4% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1211k reads/s ±7% | **3191k reads/s** ±3% | 2625k reads/s ±7% | 767k reads/s ±5% |
| 8 个线程照上面读，同时另有 8 个线程在写 | 594k reads/s ±4% | 605k reads/s ±3% | 570k reads/s ±4% | 315k reads/s ±8% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 17963 writes/s ±1% | **78577 writes/s** ±8% | 67896 writes/s ±7% | 18324 writes/s ±6% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 16549 writes/s ±32% | 513k writes/s ±16% | 445k writes/s ±34% | 15729 writes/s ±96% |

<!-- /bench:native-darwin-arm64 -->

从表里能看出：

- **引擎内部的计算**在 linux 上最多比 C 多花 22% 的时间，在 darwin 上最多 44%，几何平均分别是 14% 和 20%，建索引和 `LIKE` 与 C 相差不超过 4%：C 编成的 Go 汇编接近 clang 为本机生成的代码，但不如它。引擎用的还是 musl 的字符串和数学函数，数学函数编译时不开 fused multiply-add，这样每个平台算出的每一位都相同，而 C 程序用的是系统自带的。
- **单个读者**比 C 快：liustack/sqlite(直接调用) 每秒读的行数是 C 的 1.6 到 3.1 倍。C 里每次读都要用一次 `fcntl` 拿 WAL 的读锁，再用一次还回去，除非进程里另一个连接正拿着。liustack/sqlite 在最后一个读者离开后把锁多留约一毫秒，下一次读就不用系统调用（见 `internal/vfs`）。linux 虚拟机上系统调用最贵。
- **多个读者**在 linux 上读的行数比 C 多：liustack/sqlite(直接调用) 是 C 的 1.2 到 1.7 倍。这时 C 的读者之间共用读锁，也不用系统调用，所以单个读者时的优势少了一部分。darwin 上领先得更多，是 C 的 1.9 到 2.7 倍：那里 C 的读者约一半时间在等进程里管 WAL 锁的那把 pthread 互斥锁，几个线程一抢，macOS 就让它们进内核睡眠。liustack/sqlite 的读者只用一步原子操作加入进程已经拿着的锁，不等任何互斥锁。
- **通过 `database/sql`** 读比 liustack/sqlite(直接调用) 多花 11% 到 60% 的时间，差的是 `database/sql` 自己的开销，linux 上 8 个以上读者时这让 liustack/sqlite 是 C 的 89% 到 104%。mattn 也要付这份开销，外加一次 cgo 调用。连接会留着读查询的语句，所以每次查询不用重新编译（见 [docs/architecture.zh-CN.md](architecture.zh-CN.md#查询的语句留着复用)）。用 `*sql.Stmt` 的话每次查询都要拿 `*sql.DB` 的锁，在 `*sql.Conn` 上准备的也一样，所以基准里不用它。
- **读者旁边有写者**时，直接调用 liustack/sqlite 读出的行数和 C 一样多（linux 上是 C 的 101%，darwin 上 102%），通过 `database/sql` 在 linux 上是 61%，darwin 上 96%。旁边的写者插入的行数在 linux 上是 C 的 8.5 到 11 倍，darwin 上 3.8 到 4.4 倍。写者提交得越多，读者读得越少，换哪个驱动都一样：读者看到新的提交，就把手上的页丢掉重新读（见 [busy-cores.zh-CN.md](busy-cores.zh-CN.md#6-怎么量)）。mattn 同样通过 `database/sql`，在 linux 上插入的行数约是本驱动的一半，读的行数约四分之一。
- **写**是组提交起作用的地方：一次提交带上很多次插入，所以 64 个写者加 `synchronous=FULL` 时（C 的每个写者都要等自己的 fsync），liustack/sqlite 插入的行数是 C 的 27 到 66 倍。有读者在旁边时是 C 的 3.8 到 11 倍。

## 数字说明了什么

- **读**比 cgo 快：按主键读一行用一半左右的时间（linux 上 3.85 µs，别的驱动 7.22 到 9.61 µs），读 10 万行 35 ms，别的 56 到 81 ms，查一个用户和他的 400 条评论 521 µs，别的 841 µs 到 1.13 ms。调用引擎就是普通的 Go 调用，不用跨 cgo，而且同进程里另一个读正在进行或刚结束时，读事务不在内核里加锁（见 `internal/vfs`）。
- **多个写者**是组提交显出效果的地方：从 8 个写者起，一次提交带上许多条插入。64 个写者时每条插入 3.72 µs，别的驱动 23 到 31 µs（4 线程）。`synchronous=FULL` 下提交越贵差距越大：64 个写者时每条插入 30.5 µs，别的驱动 1.40 到 25.0 ms。在 Mac 上，modernc 在 64 个写者时把时间花在 SQLite 的忙等处理里，写者等得越久，它每次睡得越长。
- **写者旁边的读者**（ReadWrite，4 线程）：本驱动给读者的服务最快（每次读 2.67 µs，别的 4.57 到 7.53 µs），写者也写得最多（每秒 3.91 万次，ncruces 2.60 万次，mattn 1.40 万次）。
- **单个写者**（Insert）两台机器上都和 mattn 接近。时间花在写 WAL 和 SQLite 协议要求的加锁上，每个驱动的系统调用都一样。
- **连表加分组**（Aggregate）全是引擎内部的计算，在 linux 上和 cgo 用时一样，在 darwin 上多用 13%：引擎是同一份 C，由 LLVM 编译，变成的 Go 汇编接近 clang 为本机生成的代码，但还不如它。
- **大文本**在 linux 上比 mattn 慢 6%（100 行 200 KB 的文本 8.77 ms，mattn 8.25 ms），比 modernc 和 ncruces 快。darwin 上四个驱动持平。
- **内存分配**：数字直接拷进 `Scan` 的目标（`driver.RowsColumnScanner`，Go 1.27），不装进接口，所以扫描的分配次数是 mattn 和 modernc 的一半。
- **和 C 版 SQLite 比**，引擎内部的计算几何平均多花 14%（linux）和 20%（darwin）的时间。单个读者比 C 快，因为读不用为锁做系统调用。linux 上多个读者直接调用 liustack/sqlite 时读的行数比 C 多，通过 `database/sql` 是 C 的 89% 到 104%。64 个写者靠组提交插入的行数是 C 的 27 到 66 倍。linux 上读者旁边有写者时，直接调用读的行数和 C 一样多，通过 `database/sql` 是 61%，写者插入的行数是 C 的 8.5 到 11 倍。
- **linux 上的 Point** 每次查询都含一次 `fstat`，每个驱动都一样：WAL 为空时（夹具刚拷出来就是这样），SQLite 在每次读开始时读一次数据库文件的大小。这台虚拟机上一次系统调用约 0.8 µs，这一行 linux 和 darwin 的差距大半来自这里。

## 自己再跑一遍

在安静的机器上 `make bench`，用 `benchrun publish` 生成 `bench/published/` 里的一组数据，再 `make bench-docs` 写出表格。步骤和选项见 [bench/README.md](../bench/README.md)。在共用机器上，单线程的测试绑到一个核上跑（`taskset`、`GOMAXPROCS=1`），并发的测试只在没有别的任务时跑。
