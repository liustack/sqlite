<!-- translatedFrom: 92d0bcccb978 -->
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
提交 `10d5df4`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-21。负载（1、5、15 分钟）开始前 0.00 0.00 0.10，结束后 3.12 2.35 1.43。

**最慢的一次写**（一个事务，或一条自动提交的语句），以及每项 30 秒里有多少次写报 `database is locked`：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 16.3 ms | 4.43 s | 2.93 s | 83.1 ms |
| 事务，64 个 goroutine，deferred | 1.47 s，6657740 次锁错误 | 2.12 s，3648767 次锁错误 | 52.9 ms，1865060 次锁错误 | 1.38 s，2502345 次锁错误 |
| 事务，64 个 goroutine，`_txlock=immediate` | 28.5 ms | 5.01 s | 4.13 s | 165 ms |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 165 ms | 5.01 s，45 次锁错误 | 4.94 s，35 次锁错误 | 1.78 s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 162 ms | 5.01 s，24 次锁错误 | 5.01 s，13 次锁错误 | 1.78 s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 96.2 ms | 5.01 s，256 次锁错误 | 5.01 s，241 次锁错误 | 4.63 s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 326 ms | 5.01 s，283 次锁错误 | 5.06 s，216 次锁错误 | 4.45 s |

**每秒完成的写**，所有 goroutine 加起来：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 296k writes/s | 47307 writes/s | 32959 writes/s | 29303 writes/s |
| 事务，64 个 goroutine，deferred | 3056 writes/s | 2152 writes/s | 1684 writes/s | 1244 writes/s |
| 事务，64 个 goroutine，`_txlock=immediate` | 16040 writes/s | 13349 writes/s | 9333 writes/s | 9139 writes/s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 435 writes/s | 378 writes/s | 373 writes/s | 357 writes/s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 431 writes/s | 375 writes/s | 363 writes/s | 346 writes/s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 1781 writes/s | 753 writes/s | 746 writes/s | 1759 writes/s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 850 writes/s | 693 writes/s | 717 writes/s | 606 writes/s |

**WAL 最大到多大**。WAL 一直涨，说明检查点拷回去的比写进来的少，剩下那部分写入还没付出代价：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 3.95 MiB | 5.49 GiB | 3.83 GiB | 3.40 GiB |
| 事务，64 个 goroutine，deferred | 144 MiB | 409 MiB | 321 MiB | 229 MiB |
| 事务，64 个 goroutine，`_txlock=immediate` | 3.93 MiB | 2.48 GiB | 1.74 GiB | 1.70 GiB |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 3.93 MiB | 26.9 MiB | 39.6 MiB | 68.9 MiB |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 3.94 MiB | 69.6 MiB | 65.0 MiB | 108 MiB |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 3.93 MiB | 5.52 MiB | 4.47 MiB | 4.00 MiB |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 3.93 MiB | 134 MiB | 138 MiB | 117 MiB |

<!-- /bench:contention-linux-amd64 -->

- **先读后写的 deferred 事务**在每个驱动上大多数都失败。WAL 模式下这种事务不能等写锁：别的写者一提交，它读到的快照就旧了。这种事务用 `_txlock=immediate` 开始。
- **用 `_txlock=immediate`，或者第一条语句就写**，本驱动没有一次写报 `database is locked`，最慢的一次 29 到 326 ms。同一个进程里的写者按到达顺序排队，一次写只等排在它前面的写。别的驱动里写者去抢 SQLite 的锁，两次尝试之间睡得越来越久：大多数写很快就过去了，少数几次一输再输，直到 busy timeout。mattn 和 modernc 在这样的场景里只有一项没有写因此失败，而同时大多数写只等了几毫秒。ncruces 这一轮没有。
- **等待分得均匀**，所以写者排队时本驱动的中位数更长：每个事务里做 2 ms 的事时，中位数 147 ms，mattn 和 modernc 6 ms，第 99 百分位 163 ms，它们 2.9 到 3.1 秒。
- **每秒完成的写**：事务比 mattn 多 15% 到 23%，`synchronous=FULL` 下是 2.4 倍，自动提交的插入是 6.3 倍，因为组提交把它们合成一次提交。
- **别的驱动的 WAL 涨到了几 GB**，在这台机器上 30 秒内：mattn 自动提交插入时 5.49 GiB，`_txlock=immediate` 时 2.48 GiB，modernc 和 ncruces 1.70 到 3.83 GiB。SQLite 只在没有连接读 WAL 的那一刻把它从头写，而它们的 64 个写者全在 SQLite 里面，要么在事务中，要么在 busy handler 里重试，这一刻等不来。数据不会丢，但负载停了文件也不缩，一直占着磁盘。这样插入 30 秒后崩溃，再打开时第一次读，mattn 的库在 WAL 已进页缓存时要 2.2 秒，不在时 33 秒，modernc 是 1.7 秒和 19 秒，本驱动是 2.3 ms 和 63 ms（`TestReopenAfterCrash`，见 [bench/README.md](../bench/README.md)）。本驱动在写者进 SQLite 之前就让它们排队，每 1000 帧把 WAL 从头写：WAL 一直不到 4 MiB，deferred 事务时到了 144 MiB，因为 64 个叠在一起的读占满了 SQLite 的读者槽，到 64 MiB 上限时从头写可能失败（见 [architecture.zh-CN.md](architecture.zh-CN.md#wal-长度的上限)）。在 10 核的 Apple M4 上 mattn 的 WAL 只到 12 MiB，所以涨到多大取决于机器。

### 在 Windows 上

同一个测试跑在 GitHub 的 Windows runner 上，`windows-2025`（x64）和 `windows-11-arm`（Arm），每种架构所有驱动在同一台 runner 上跑，由 `bench-windows` 工作流运行（见 [bench/README.md](../bench/README.md)）。mattn 的 cgo 在 x64 上用 runner 自带的 MinGW gcc 编译，在 Arm 上用 llvm-mingw，因为 Arm runner 的 gcc 只能编出 x86-64。这些 runner 是共享的虚拟机，时间在两次运行之间比上面那台 linux 机器波动大。要看的是 `database is locked` 的次数。

<!-- bench:contention-windows-amd64 -->
提交 `10d5df4`，Go 1.27.4，cgo with gcc (x86_64-posix-seh-rev1, Built by MinGW-Builds project) 15.2.0，INTEL(R) XEON(R) PLATINUM 8573C，Windows 10.0.26100.33828 amd64，GOMAXPROCS 4，共 4 个线程，2026-11-21。

**最慢的一次写**（一个事务，或一条自动提交的语句），以及每项 30 秒里有多少次写报 `database is locked`：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 253 ms | 6.25 s，104 次锁错误 | 5.03 s，86 次锁错误 | 368 ms |
| 事务，64 个 goroutine，deferred | 1.22 s，3572600 次锁错误 | 3.63 s，1351837 次锁错误 | 67.3 ms，345994 次锁错误 | 1.05 s，1971729 次锁错误 |
| 事务，64 个 goroutine，`_txlock=immediate` | 201 ms | 5.53 s，98 次锁错误 | 5.10 s，80 次锁错误 | 1.75 s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 331 ms | 5.52 s，267 次锁错误 | 5.03 s，47 次锁错误 | 2.56 s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 363 ms | 14.6 s，190 次锁错误 | 5.03 s，94 次锁错误 | 2.81 s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 628 ms | 5.67 s，299 次锁错误 | 5.03 s，237 次锁错误 | 2.73 s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 798 ms | 5.63 s，1183 次锁错误 | 5.15 s，594 次锁错误 | 4.82 s，4 次锁错误 |

**每秒完成的写**，所有 goroutine 加起来：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 85211 writes/s | 14355 writes/s | 9980 writes/s | 10194 writes/s |
| 事务，64 个 goroutine，deferred | 1039 writes/s | 2979 writes/s | 717 writes/s | 1267 writes/s |
| 事务，64 个 goroutine，`_txlock=immediate` | 3235 writes/s | 7199 writes/s | 2573 writes/s | 2374 writes/s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 368 writes/s | 358 writes/s | 356 writes/s | 373 writes/s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 335 writes/s | 272 writes/s | 351 writes/s | 356 writes/s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 285 writes/s | 90.9 writes/s | 282 writes/s | 194 writes/s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 485 writes/s | 562 writes/s | 493 writes/s | 448 writes/s |

**WAL 最大到多大**。WAL 一直涨，说明检查点拷回去的比写进来的少，剩下那部分写入还没付出代价：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 3.96 MiB | 180 MiB | 1.16 GiB | 1.18 GiB |
| 事务，64 个 goroutine，deferred | 199 MiB | 539 MiB | 132 MiB | 234 MiB |
| 事务，64 个 goroutine，`_txlock=immediate` | 3.93 MiB | 8.86 MiB | 499 MiB | 461 MiB |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 3.93 MiB | 5.55 MiB | 69.0 MiB | 72.0 MiB |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 3.94 MiB | 5.76 MiB | 109 MiB | 111 MiB |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 3.93 MiB | 3.98 MiB | 4.63 MiB | 37.2 MiB |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 3.93 MiB | 4.33 MiB | 94.9 MiB | 86.3 MiB |

<!-- /bench:contention-windows-amd64 -->

<!-- bench:contention-windows-arm64 -->
提交 `10d5df4`，Go 1.27.4，cgo with clang version 23.1.5 (https://github.com/llvm/llvm-project.git 392dfff97d29b59d77e5cac6d4b6ef2585fe7117)，Cobalt 100，Windows 10.0.26200.9974 arm64，GOMAXPROCS 4，共 4 个线程，2026-11-21。

**最慢的一次写**（一个事务，或一条自动提交的语句），以及每项 30 秒里有多少次写报 `database is locked`：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 349 ms | 5.87 s，41 次锁错误 | 5.31 s，103 次锁错误 | 676 ms |
| 事务，64 个 goroutine，deferred | 860 ms，3928265 次锁错误 | 2.27 s，2044778 次锁错误 | 46.0 ms，285034 次锁错误 | 1.92 s，3064735 次锁错误 |
| 事务，64 个 goroutine，`_txlock=immediate` | 160 ms | 5.56 s，155 次锁错误 | 5.03 s，108 次锁错误 | 1.38 s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 501 ms | 5.58 s，221 次锁错误 | 5.05 s，62 次锁错误 | 4.12 s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 303 ms | 6.51 s，233 次锁错误 | 5.02 s，87 次锁错误 | 3.56 s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 708 ms | 5.56 s，322 次锁错误 | 5.03 s，309 次锁错误 | 4.51 s，2 次锁错误 |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 976 ms | 5.66 s，1226 次锁错误 | 5.12 s，843 次锁错误 | 5.00 s，40 次锁错误 |

**每秒完成的写**，所有 goroutine 加起来：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 127k writes/s | 18149 writes/s | 10932 writes/s | 9679 writes/s |
| 事务，64 个 goroutine，deferred | 1535 writes/s | 2851 writes/s | 509 writes/s | 863 writes/s |
| 事务，64 个 goroutine，`_txlock=immediate` | 4948 writes/s | 4924 writes/s | 2514 writes/s | 2137 writes/s |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 370 writes/s | 377 writes/s | 307 writes/s | 299 writes/s |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 368 writes/s | 286 writes/s | 305 writes/s | 286 writes/s |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 173 writes/s | 177 writes/s | 170 writes/s | 121 writes/s |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 460 writes/s | 546 writes/s | 420 writes/s | 367 writes/s |

**WAL 最大到多大**。WAL 一直涨，说明检查点拷回去的比写进来的少，剩下那部分写入还没付出代价：

| 负载 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 64 个 goroutine 同时各插入一行 | 3.96 MiB | 1.62 GiB | 1.27 GiB | 1.12 GiB |
| 事务，64 个 goroutine，deferred | 272 MiB | 522 MiB | 89.3 MiB | 156 MiB |
| 事务，64 个 goroutine，`_txlock=immediate` | 3.93 MiB | 31.1 MiB | 488 MiB | 415 MiB |
| 事务，64 个 goroutine，immediate，每个事务里做 2 ms 的事 | 3.93 MiB | 4.71 MiB | 59.2 MiB | 57.5 MiB |
| 事务，64 个 goroutine，deferred，第一条就写，每个事务里做 2 ms 的事 | 3.94 MiB | 6.66 MiB | 94.8 MiB | 89.1 MiB |
| 事务，64 个 goroutine，immediate，`synchronous=FULL` | 3.93 MiB | 3.94 MiB | 5.04 MiB | 22.7 MiB |
| 事务，256 个 goroutine，immediate，每个事务里做 1 ms 的事 | 3.93 MiB | 4.01 MiB | 81.1 MiB | 70.6 MiB |

<!-- /bench:contention-windows-arm64 -->

- **用 `_txlock=immediate`，或者第一条语句就写**，本驱动在 x64 和 Arm 上都没有一次写报 `database is locked`，最慢的一次 160 到 976 ms。mattn 和 modernc 在每一项这样的场景里都有写因此失败，ncruces 在 256 个 goroutine 时有（x64 上 4 次，Arm 上 40 次），Arm 上 `synchronous=FULL` 时也有（2 次）。
- **短事务在 Windows x64 上更慢。**用 `_txlock=immediate` 且事务里不做事时，本驱动在 x64 上每秒 3235 次，Arm 上 4947 次，mattn 是 7198 和 4923 次，而在 linux 上本驱动比 mattn 多（16039 对 13348）。驱动在交出写锁之前跑自动检查点，而 x64 runner 的磁盘刷盘慢（1000 帧约 67 ms），后面的写者就得等着。别的驱动用的是 SQLite 自己的检查点，它在锁放开之后才跑，代价是 WAL 可能一直涨。事务里做 1 或 2 ms 的事时几个驱动差不多：x64 上本驱动每秒 335 到 485 次写，mattn 272 到 562 次，Arm 上 367 到 459 次对 286 到 545 次。
- **自动提交的插入**由组提交合并，仍然最多：x64 上每秒 85210 次，Arm 上 127k 次，mattn 是 14354 和 18148 次。
- **别的驱动的 WAL** 在插入用例里到了 1.12 到 1.62 GiB（mattn 在 x64 上 180 MiB），本驱动 4 MiB。用 deferred 事务时，本驱动的 WAL 在 x64 上到了 199 MiB，在 Arm 上到了 272 MiB，比 modernc（132 和 89.3 MiB）大，在 Arm 上也比 ncruces（156 MiB）大（见 [wal-growth.zh-CN.md](wal-growth.zh-CN.md)）。

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
