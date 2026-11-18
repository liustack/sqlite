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
提交 `18a2707`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 1，共 1 个线程，2026-11-18。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 1.65 1.50 1.61，结束后 1.11 1.23 1.37。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 318 µs | 609 µs | 729 µs | 551 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2281 MB/s ±3% | 2392 MB/s ±3% | 2086 MB/s ±5% | 2138 MB/s ±3% |
| Large/size=200k | 2278 MB/s ±2% | 2406 MB/s ±8% | 2089 MB/s ±1% | 2147 MB/s ±2% |
| ReadWrite | 7938 writes/s ±46% | 4287 writes/s ±44% | 7871 writes/s ±30% | 5065 writes/s ±12% |

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

### 全部 4 个核

机器上没有别的任务。只跑用多个 goroutine 的场景。

<!-- bench:linux-amd64-4threads -->
提交 `18a2707`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-18。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.00 0.00 0.10，结束后 2.20 1.87 0.99。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 118 µs | 444 µs | 500 µs | 310 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | 12401 writes/s ±8% | 14219 writes/s ±12% | 4436 writes/s ±11% | **26085 writes/s** ±3% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
提交 `4b44e15`，Go 1.27.2，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-18。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 3.10 4.45 4.01，结束后 3.13 3.80 4.16。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 66.8 µs | 132 µs | 256 µs | 159 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 6052 MB/s ±6% | 6067 MB/s ±3% | 5409 MB/s ±2% | 5768 MB/s ±3% |
| Large/size=200k | 5863 MB/s ±2% | 5912 MB/s ±3% | 5330 MB/s ±6% | 5621 MB/s ±1% |
| ReadWrite | **30172 writes/s** ±2% | 16355 writes/s ±2% | 28584 writes/s ±2% | 19828 writes/s ±3% |

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

## 复制

[复制](replication.zh-CN.md)在程序里复制每次提交，所以 CPU 花在程序自己的进程里。`replica` 里的 `BenchmarkGroupCommit` 让 8 个或 64 个 goroutine 同时插入，每次一行（WAL，`synchronous=NORMAL`），分别关掉和打开复制。存储是同一台机器另外两个线程上的 MinIO，它的开销不落在基准的线程上，就像存储在别的机器上时一样。

提交 `def7170`，Go 1.27.2，早先一台同样 CPU 的虚拟机（4 核 8 线程），Linux 7.3.0-1003-gcp。每组跑 6 轮，用 benchstat 比较（`~` 表示和噪声分不开）。负载开始前 1.12，结束后 1.92。原始输出在 [bench/published/replica/](../bench/published/replica/)。

单线程（`GOMAXPROCS=1`，核 0 和 4，MinIO 在 3 和 7）：

```
              关复制          开复制
g=8           18.72µ ± 2%    19.90µ ± 2%   +6.31% (p=0.002 n=6)
g=64          17.59µ ± 3%    19.59µ ± 1%  +11.38% (p=0.002 n=6)
geomean       18.14µ         19.74µ        +8.82%
```

六线程（`GOMAXPROCS=6`，MinIO 在另外两个线程上）：

```
              关复制          开复制
g=8            9.810µ ±  2%  10.893µ ± 9%  +11.04% (p=0.002 n=6)
g=64           3.577µ ± 20%   3.921µ ± 7%        ~ (p=0.065 n=6)
geomean        5.923µ         6.535µ       +10.33%
```

也就是说，开复制后每次插入多花约 6% 到 11%。六线程 64 个写入方那一组，关复制的 6 轮里有一轮偏慢，差距落在噪声里。用 MinIO 二进制自己跑一遍，把它绑在基准以外的核上：

```sh
MINIO=<minio> go test ./replica -run '^$' -bench GroupCommit -benchtime 3s -count 6
```

## 和 C 版 SQLite 比

同一份 SQLite 3.54.1，用 `engine/sqlite.c` 里的编译选项，由 clang `-O2` 编成直接调用 C API 的 C 程序。负载和四列各是什么见 [bench/README.md](../bench/README.md#against-sqlite-in-c)。简单说：SQLite(C) 和 liustack/sqlite(直接调用) 把每条语句编译一次，反复执行，直接调 C API 或驱动本身。两列 (database/sql) 像普通程序一样读写：每个读者一个 `*sql.Conn`，用 `QueryRowContext`，每次写用 `db.Exec`。mattn 通过 cgo 跑 SQLite 的 C。

### linux/amd64

<!-- bench:native-linux-amd64 -->
提交 `18a2707`，Go 1.27.4，Ubuntu clang version 22.1.8 (++20260714015917+ca7933e47d3a-1~exp1~20260714135927.17)，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-18。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 2.20 1.87 0.99，结束后 5.05 4.89 4.07。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **464 ms** ±3% | 547 ms ±1% | +18% |
| 给 100 万个文本建索引 | **399 ms** ±1% | 405 ms ±1% | +1% |
| 排序 100 万个文本 | **4.21 s** ±3% | 4.71 s ±2% | +12% |
| 100 万行分 1000 组求平均 | **314 ms** ±1% | 383 ms ±2% | +22% |
| 30 万行按主键连 100 万行的表 | **80.9 ms** ±3% | 87.9 ms ±1% | +9% |
| 递归 CTE 走 200 万步 | **454 ms** ±1% | 494 ms ±1% | +9% |
| 100 万个文本做 LIKE 和字符串函数 | **85.2 ms** ±1% | 88.6 ms ±1% | +4% |
| 生成并读取 30 万个 JSON 对象 | **415 ms** ±1% | 509 ms ±1% | +23% |
| 100 万个数算 sqrt、sin、log、pow | **165 ms** ±3% | 196 ms ±0% | +19% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **2.96 ms** ±1% | 3.41 ms ±2% | +15% |
| 几何平均 | 209 ms | 235 ms | +13% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 191k reads/s ±2% | **359k reads/s** ±2% | 277k reads/s ±2% | 102k reads/s ±1% |
| 8 个线程，每次按主键读一行 | 1072k reads/s ±9% | **1307k reads/s** ±4% | 958k reads/s ±4% | 431k reads/s ±4% |
| 64 个线程，每次按主键读一行 | 1022k reads/s ±11% | **1301k reads/s** ±2% | 939k reads/s ±4% | 432k reads/s ±5% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 236k reads/s ±3% | **700k reads/s** ±4% | 458k reads/s ±5% | 115k reads/s ±2% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1394k reads/s ±10% | **2254k reads/s** ±7% | 1448k reads/s ±5% | 455k reads/s ±5% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1156k reads/s ±12% | **2016k reads/s** ±4% | 1233k reads/s ±5% | 332k reads/s ±4% |
| 8 个线程照上面读，同时另有 8 个线程在写 | 989k reads/s ±8% | 943k reads/s ±4% | 653k reads/s ±5% | 167k reads/s ±4% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 2389 writes/s ±16% | 3364 writes/s ±7% | 3042 writes/s ±6% | **15395 writes/s** ±5% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 862 writes/s ±3% | 56673 writes/s ±2% | 56764 writes/s ±4% | 869 writes/s ±4% |

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
提交 `18a2707`，Go 1.27.2，Homebrew clang version 22.1.8，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-18。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 4.91 6.27 6.70，结束后 8.53 7.88 7.55。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **188 ms** ±8% | 248 ms ±3% | +32% |
| 给 100 万个文本建索引 | 208 ms ±3% | 216 ms ±2% | +4% |
| 排序 100 万个文本 | **1.57 s** ±4% | 1.76 s ±3% | +12% |
| 100 万行分 1000 组求平均 | **149 ms** ±2% | 199 ms ±2% | +34% |
| 30 万行按主键连 100 万行的表 | **36.0 ms** ±1% | 45.8 ms ±1% | +27% |
| 递归 CTE 走 200 万步 | **199 ms** ±2% | 244 ms ±3% | +22% |
| 100 万个文本做 LIKE 和字符串函数 | **46.8 ms** ±2% | 47.9 ms ±2% | +2% |
| 生成并读取 30 万个 JSON 对象 | **212 ms** ±1% | 230 ms ±1% | +9% |
| 100 万个数算 sqrt、sin、log、pow | **69.0 ms** ±3% | 89.4 ms ±2% | +30% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **1.26 ms** ±2% | 1.82 ms ±1% | +44% |
| 几何平均 | 94.5 ms | 114 ms | +21% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 657k reads/s ±2% | **1044k reads/s** ±4% | 791k reads/s ±4% | 323k reads/s ±3% |
| 8 个线程，每次按主键读一行 | 870k reads/s ±6% | 1617k reads/s ±5% | 1485k reads/s ±6% | 692k reads/s ±8% |
| 64 个线程，每次按主键读一行 | 756k reads/s ±5% | **1545k reads/s** ±8% | 1284k reads/s ±6% | 634k reads/s ±5% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 868k reads/s ±5% | **1999k reads/s** ±1% | 1409k reads/s ±2% | 395k reads/s ±1% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1364k reads/s ±6% | **3942k reads/s** ±5% | 3040k reads/s ±8% | 823k reads/s ±8% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1150k reads/s ±3% | **2996k reads/s** ±6% | 2363k reads/s ±10% | 692k reads/s ±7% |
| 8 个线程照上面读，同时另有 8 个线程在写 | 564k reads/s ±10% | **833k reads/s** ±9% | 703k reads/s ±9% | 287k reads/s ±14% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 17143 writes/s ±5% | 48904 writes/s ±3% | 46211 writes/s ±4% | 16869 writes/s ±8% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 22024 writes/s ±29% | 539k writes/s ±3% | 504k writes/s ±6% | 40834 writes/s ±6% |

<!-- /bench:native-darwin-arm64 -->

从表里能看出：

- **引擎内部的计算**在 linux 上最多比 C 多花 23% 的时间，在 darwin 上最多 44%，几何平均分别是 13% 和 21%，建索引和 `LIKE` 与 C 相差不超过 4%：C 编成的 Go 汇编接近 clang 为本机生成的代码，但不如它。引擎用的还是 musl 的字符串和数学函数，数学函数编译时不开 fused multiply-add，这样每个平台算出的每一位都相同，而 C 程序用的是系统自带的。
- **单个读者**比 C 快：liustack/sqlite(直接调用) 每秒读的行数是 C 的 1.6 到 3.0 倍。C 里每次读都要用一次 `fcntl` 拿 WAL 的读锁，再用一次还回去，除非进程里另一个连接正拿着。liustack/sqlite 在最后一个读者离开后把锁多留约一毫秒，下一次读就不用系统调用（见 `internal/vfs`）。linux 虚拟机上系统调用最贵。
- **多个读者**在 linux 上读的行数比 C 多：liustack/sqlite(直接调用) 是 C 的 1.2 到 1.7 倍。这时 C 的读者之间共用读锁，也不用系统调用，所以单个读者时的优势少了一部分。darwin 上领先得更多，是 C 的 1.9 到 2.9 倍：那里 C 的读者约一半时间在等进程里管 WAL 锁的那把 pthread 互斥锁，几个线程一抢，macOS 就让它们进内核睡眠。liustack/sqlite 的读者只用一步原子操作加入进程已经拿着的锁，不等任何互斥锁。
- **通过 `database/sql`** 读比 liustack/sqlite(直接调用) 多花 10% 到 65% 的时间，差的是 `database/sql` 自己的开销，linux 上 8 个以上读者时这让 liustack/sqlite 是 C 的 89% 到 107%。mattn 也要付这份开销，外加一次 cgo 调用。连接会留着读查询的语句，所以每次查询不用重新编译（见 [docs/architecture.zh-CN.md](architecture.zh-CN.md#查询的语句留着复用)）。用 `*sql.Stmt` 的话每次查询都要拿 `*sql.DB` 的锁，在 `*sql.Conn` 上准备的也一样，所以基准里不用它。
- **读者旁边有写者**时，linux 上直接调用 liustack/sqlite 读出的行数是 C 的 95%，通过 `database/sql` 是 66%，旁边的写者插入的行数是 C 的 1.3 到 1.4 倍。darwin 上读的行数是 C 的 1.2 到 1.5 倍，写是 2.7 到 2.9 倍。在 linux 那台 4 核机器上，8 个读者留给写者的不多：写者只是九个要抢核的 goroutine 之一。mattn 每次调用占一个线程，它在这里插入的行数是本驱动的 5 倍，读的行数是四分之一。
- **写**是组提交起作用的地方：一次提交带上很多次插入，所以 64 个写者加 `synchronous=FULL` 时（C 的每个写者都要等自己的 fsync），liustack/sqlite 插入的行数是 C 的 23 到 66 倍。有读者在旁边时是 C 的 1.3 到 2.9 倍。

## 数字说明了什么

- **读**比 cgo 快：按主键读一行用一半左右的时间（linux 上 3.86 µs，别的驱动 7.13 到 9.44 µs），读 10 万行 35 ms，别的 53 到 86 ms，查一个用户和他的 400 条评论 523 µs，别的 829 µs 到 1.16 ms。调用引擎就是普通的 Go 调用，不用跨 cgo，而且同进程里另一个读正在进行或刚结束时，读事务不在内核里加锁（见 `internal/vfs`）。
- **多个写者**是组提交显出效果的地方：从 8 个写者起，一次提交带上许多条插入。64 个写者时每条插入 3.52 µs，别的驱动 22 到 31 µs（4 线程）。`synchronous=FULL` 下提交越贵差距越大：64 个写者时每条插入 26.7 µs，别的驱动 1.37 到 23.5 ms。在 Mac 上，modernc 在 64 个写者时把时间花在 SQLite 的忙等处理里，写者等得越久，它每次睡得越长。
- **写者旁边的读者**（ReadWrite，4 线程）：本驱动给读者的服务最快（每次读 2.61 µs，别的 4.54 到 7.94 µs），ncruces 让写者写得最多（每秒 2.61 万次，mattn 1.42 万次，本驱动 1.24 万次）。
- **单个写者**（Insert）两台机器上都和 mattn 接近。时间花在写 WAL 和 SQLite 协议要求的加锁上，每个驱动的系统调用都一样。
- **连表加分组**（Aggregate）全是引擎内部的计算，在 linux 上和 cgo 用时一样，在 darwin 上多用 10%：引擎是同一份 C，由 LLVM 编译，变成的 Go 汇编接近 clang 为本机生成的代码，但还不如它。
- **大文本**在 linux 上比 mattn 慢 6%（100 行 200 KB 的文本 8.78 ms，mattn 8.31 ms），比 modernc 和 ncruces 快。darwin 上四个驱动持平。
- **内存分配**：数字直接拷进 `Scan` 的目标（`driver.RowsColumnScanner`，Go 1.27），不装进接口，所以扫描的分配次数是 mattn 和 modernc 的一半。
- **和 C 版 SQLite 比**，引擎内部的计算几何平均多花 13%（linux）和 21%（darwin）的时间。单个读者比 C 快，因为读不用为锁做系统调用。linux 上多个读者直接调用 liustack/sqlite 时读的行数比 C 多，通过 `database/sql` 是 C 的 89% 到 107%。64 个写者靠组提交插入的行数是 C 的 23 到 66 倍。linux 上读者旁边有写者时，直接调用读的行数是 C 的 95%，通过 `database/sql` 是 66%，写者插入的行数是 C 的 1.3 到 1.4 倍。
- **linux 上的 Point** 每次查询都含一次 `fstat`，每个驱动都一样：WAL 为空时（夹具刚拷出来就是这样），SQLite 在每次读开始时读一次数据库文件的大小。这台虚拟机上一次系统调用约 0.8 µs，这一行 linux 和 darwin 的差距大半来自这里。

## 自己再跑一遍

在安静的机器上 `make bench`，用 `benchrun publish` 生成 `bench/published/` 里的一组数据，再 `make bench-docs` 写出表格。步骤和选项见 [bench/README.md](../bench/README.md)。在共用机器上，单线程的测试绑到一个核上跑（`taskset`、`GOMAXPROCS=1`），并发的测试只在没有别的任务时跑。
