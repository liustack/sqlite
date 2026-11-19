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
提交 `44acf8c`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 1，共 1 个线程，2026-11-19。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.05 0.01 0.00，结束后 1.53 1.23 0.80。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 318 µs | 615 µs | 727 µs | 546 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2315 MB/s ±3% | **2419 MB/s** ±4% | 2043 MB/s ±2% | 2201 MB/s ±1% |
| Large/size=200k | 2287 MB/s ±4% | **2397 MB/s** ±2% | 2131 MB/s ±1% | 2188 MB/s ±2% |
| ReadWrite | 6951 writes/s ±23% | 3702 writes/s ±54% | 7043 writes/s ±29% | 4851 writes/s ±6% |

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

### 全部 4 个核

机器上没有别的任务。只跑用多个 goroutine 的场景。

<!-- bench:linux-amd64-4threads -->
提交 `44acf8c`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-19。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.94 1.10 0.78，结束后 2.05 1.95 1.33。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 118 µs | 455 µs | 522 µs | 309 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | **40808 writes/s** ±11% | 14839 writes/s ±6% | 4357 writes/s ±15% | 26524 writes/s ±5% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
提交 `44acf8c`，Go 1.27.2，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-19。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 2.82 3.46 3.76，结束后 3.64 3.27 3.45。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 71.3 µs | 138 µs | 270 µs | 163 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 5959 MB/s ±6% | 6081 MB/s ±5% | 5509 MB/s ±3% | 5779 MB/s ±6% |
| Large/size=200k | 5782 MB/s ±7% | 6072 MB/s ±5% | 5281 MB/s ±4% | 5638 MB/s ±2% |
| ReadWrite | **45954 writes/s** ±11% | 15858 writes/s ±3% | 28907 writes/s ±3% | 19803 writes/s ±3% |

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
提交 `44acf8c`，Go 1.27.4，Ubuntu clang version 22.1.8 (++20260714015917+ca7933e47d3a-1~exp1~20260714135927.17)，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-19。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.82 1.62 1.25，结束后 5.63 4.20 3.69。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **471 ms** ±1% | 539 ms ±1% | +15% |
| 给 100 万个文本建索引 | **392 ms** ±0% | 405 ms ±1% | +3% |
| 排序 100 万个文本 | **4.01 s** ±1% | 4.52 s ±1% | +13% |
| 100 万行分 1000 组求平均 | **312 ms** ±1% | 381 ms ±4% | +22% |
| 30 万行按主键连 100 万行的表 | **76.2 ms** ±3% | 87.1 ms ±1% | +14% |
| 递归 CTE 走 200 万步 | **439 ms** ±2% | 488 ms ±1% | +11% |
| 100 万个文本做 LIKE 和字符串函数 | 86.0 ms ±9% | 87.9 ms ±2% | +2% |
| 生成并读取 30 万个 JSON 对象 | **415 ms** ±1% | 510 ms ±0% | +23% |
| 100 万个数算 sqrt、sin、log、pow | **165 ms** ±4% | 196 ms ±0% | +19% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **2.93 ms** ±1% | 3.39 ms ±1% | +16% |
| 几何平均 | 205 ms | 233 ms | +14% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 194k reads/s ±1% | **380k reads/s** ±1% | 290k reads/s ±1% | 103k reads/s ±0% |
| 8 个线程，每次按主键读一行 | 1154k reads/s ±5% | **1320k reads/s** ±2% | 987k reads/s ±2% | 423k reads/s ±5% |
| 64 个线程，每次按主键读一行 | 1034k reads/s ±8% | **1325k reads/s** ±1% | 939k reads/s ±3% | 425k reads/s ±3% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 241k reads/s ±2% | **756k reads/s** ±2% | 487k reads/s ±2% | 117k reads/s ±1% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1499k reads/s ±8% | **2394k reads/s** ±4% | 1463k reads/s ±3% | 455k reads/s ±4% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1241k reads/s ±5% | **2123k reads/s** ±4% | 1293k reads/s ±5% | 327k reads/s ±8% |
| 8 个线程照上面读，同时另有 8 个线程在写 | 1005k reads/s ±8% | 1021k reads/s ±4% | 615k reads/s ±5% | 167k reads/s ±2% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 2796 writes/s ±25% | 19467 writes/s ±11% | **28900 writes/s** ±4% | 15437 writes/s ±2% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 819 writes/s ±6% | 56830 writes/s ±5% | 55212 writes/s ±2% | 845 writes/s ±2% |

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
提交 `44acf8c`，Go 1.27.2，Homebrew clang version 22.1.8，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-19。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 3.64 3.27 3.45，结束后 8.71 8.63 7.33。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **188 ms** ±3% | 242 ms ±4% | +29% |
| 给 100 万个文本建索引 | 211 ms ±3% | 215 ms ±6% | +2% |
| 排序 100 万个文本 | **1.58 s** ±2% | 1.75 s ±3% | +11% |
| 100 万行分 1000 组求平均 | **149 ms** ±10% | 201 ms ±2% | +34% |
| 30 万行按主键连 100 万行的表 | **35.8 ms** ±1% | 46.1 ms ±1% | +29% |
| 递归 CTE 走 200 万步 | **199 ms** ±1% | 243 ms ±1% | +22% |
| 100 万个文本做 LIKE 和字符串函数 | **46.8 ms** ±1% | 47.9 ms ±1% | +2% |
| 生成并读取 30 万个 JSON 对象 | **213 ms** ±2% | 231 ms ±0% | +8% |
| 100 万个数算 sqrt、sin、log、pow | **67.1 ms** ±2% | 89.7 ms ±0% | +34% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **1.27 ms** ±2% | 1.83 ms ±2% | +44% |
| 几何平均 | 94.5 ms | 114 ms | +21% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 673k reads/s ±1% | **1052k reads/s** ±2% | 819k reads/s ±5% | 326k reads/s ±2% |
| 8 个线程，每次按主键读一行 | 861k reads/s ±9% | **1624k reads/s** ±2% | 1542k reads/s ±5% | 761k reads/s ±4% |
| 64 个线程，每次按主键读一行 | 771k reads/s ±6% | **1560k reads/s** ±2% | 1386k reads/s ±3% | 667k reads/s ±3% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 884k reads/s ±2% | **2017k reads/s** ±2% | 1436k reads/s ±1% | 398k reads/s ±1% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1392k reads/s ±5% | **3876k reads/s** ±1% | 3078k reads/s ±2% | 849k reads/s ±4% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1202k reads/s ±6% | **3130k reads/s** ±4% | 2581k reads/s ±6% | 740k reads/s ±3% |
| 8 个线程照上面读，同时另有 8 个线程在写 | 586k reads/s ±2% | 590k reads/s ±3% | 572k reads/s ±2% | 304k reads/s ±3% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 17686 writes/s ±1% | **78104 writes/s** ±2% | 69029 writes/s ±3% | 17561 writes/s ±2% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 22800 writes/s ±62% | **546k writes/s** ±1% | 524k writes/s ±2% | 42440 writes/s ±3% |

<!-- /bench:native-darwin-arm64 -->

从表里能看出：

- **引擎内部的计算**在 linux 上最多比 C 多花 23% 的时间，在 darwin 上最多 44%，几何平均分别是 14% 和 21%，建索引和 `LIKE` 与 C 相差不超过 3%：C 编成的 Go 汇编接近 clang 为本机生成的代码，但不如它。引擎用的还是 musl 的字符串和数学函数，数学函数编译时不开 fused multiply-add，这样每个平台算出的每一位都相同，而 C 程序用的是系统自带的。
- **单个读者**比 C 快：liustack/sqlite(直接调用) 每秒读的行数是 C 的 1.6 到 3.1 倍。C 里每次读都要用一次 `fcntl` 拿 WAL 的读锁，再用一次还回去，除非进程里另一个连接正拿着。liustack/sqlite 在最后一个读者离开后把锁多留约一毫秒，下一次读就不用系统调用（见 `internal/vfs`）。linux 虚拟机上系统调用最贵。
- **多个读者**在 linux 上读的行数比 C 多：liustack/sqlite(直接调用) 是 C 的 1.1 到 1.7 倍。这时 C 的读者之间共用读锁，也不用系统调用，所以单个读者时的优势少了一部分。darwin 上领先得更多，是 C 的 1.9 到 2.8 倍：那里 C 的读者约一半时间在等进程里管 WAL 锁的那把 pthread 互斥锁，几个线程一抢，macOS 就让它们进内核睡眠。liustack/sqlite 的读者只用一步原子操作加入进程已经拿着的锁，不等任何互斥锁。
- **通过 `database/sql`** 读比 liustack/sqlite(直接调用) 多花 5% 到 64% 的时间，差的是 `database/sql` 自己的开销，linux 上 8 个以上读者时这让 liustack/sqlite 是 C 的 86% 到 104%。mattn 也要付这份开销，外加一次 cgo 调用。连接会留着读查询的语句，所以每次查询不用重新编译（见 [docs/architecture.zh-CN.md](architecture.zh-CN.md#查询的语句留着复用)）。用 `*sql.Stmt` 的话每次查询都要拿 `*sql.DB` 的锁，在 `*sql.Conn` 上准备的也一样，所以基准里不用它。
- **读者旁边有写者**时，直接调用 liustack/sqlite 读出的行数和 C 一样多（linux 上是 C 的 102%，darwin 上 101%），通过 `database/sql` 在 linux 上是 61%，darwin 上 98%。旁边的写者插入的行数在 linux 上是 C 的 7 到 10 倍，darwin 上 3.9 到 4.4 倍。写者提交得越多，读者读得越少，换哪个驱动都一样：读者看到新的提交，就把手上的页丢掉重新读（见 [busy-cores.zh-CN.md](busy-cores.zh-CN.md#6-怎么量)）。mattn 同样通过 `database/sql`，在 linux 上插入的行数约是本驱动的一半，读的行数约四分之一。
- **写**是组提交起作用的地方：一次提交带上很多次插入，所以 64 个写者加 `synchronous=FULL` 时（C 的每个写者都要等自己的 fsync），liustack/sqlite 插入的行数是 C 的 23 到 69 倍。有读者在旁边时是 C 的 3.9 到 10 倍。

## 数字说明了什么

- **读**比 cgo 快：按主键读一行用一半左右的时间（linux 上 3.79 µs，别的驱动 7.11 到 9.31 µs），读 10 万行 35 ms，别的 53 到 85 ms，查一个用户和他的 400 条评论 512 µs，别的 826 µs 到 1.15 ms。调用引擎就是普通的 Go 调用，不用跨 cgo，而且同进程里另一个读正在进行或刚结束时，读事务不在内核里加锁（见 `internal/vfs`）。
- **多个写者**是组提交显出效果的地方：从 8 个写者起，一次提交带上许多条插入。64 个写者时每条插入 3.42 µs，别的驱动 22 到 30 µs（4 线程）。`synchronous=FULL` 下提交越贵差距越大：64 个写者时每条插入 29.0 µs，别的驱动 1.37 到 25.0 ms。在 Mac 上，modernc 在 64 个写者时把时间花在 SQLite 的忙等处理里，写者等得越久，它每次睡得越长。
- **写者旁边的读者**（ReadWrite，4 线程）：本驱动给读者的服务最快（每次读 2.62 µs，别的 4.32 到 7.24 µs），写者也写得最多（每秒 4.08 万次，ncruces 2.65 万次，mattn 1.48 万次）。
- **单个写者**（Insert）两台机器上都和 mattn 接近。时间花在写 WAL 和 SQLite 协议要求的加锁上，每个驱动的系统调用都一样。
- **连表加分组**（Aggregate）全是引擎内部的计算，在 linux 上和 cgo 用时一样，在 darwin 上多用 11%：引擎是同一份 C，由 LLVM 编译，变成的 Go 汇编接近 clang 为本机生成的代码，但还不如它。
- **大文本**在 linux 上比 mattn 慢 5%（100 行 200 KB 的文本 8.75 ms，mattn 8.34 ms），比 modernc 和 ncruces 快。darwin 上四个驱动持平。
- **内存分配**：数字直接拷进 `Scan` 的目标（`driver.RowsColumnScanner`，Go 1.27），不装进接口，所以扫描的分配次数是 mattn 和 modernc 的一半。
- **和 C 版 SQLite 比**，引擎内部的计算几何平均多花 14%（linux）和 21%（darwin）的时间。单个读者比 C 快，因为读不用为锁做系统调用。linux 上多个读者直接调用 liustack/sqlite 时读的行数比 C 多，通过 `database/sql` 是 C 的 86% 到 104%。64 个写者靠组提交插入的行数是 C 的 23 到 69 倍。linux 上读者旁边有写者时，直接调用读的行数和 C 一样多，通过 `database/sql` 是 61%，写者插入的行数是 C 的 7 到 10 倍。
- **linux 上的 Point** 每次查询都含一次 `fstat`，每个驱动都一样：WAL 为空时（夹具刚拷出来就是这样），SQLite 在每次读开始时读一次数据库文件的大小。这台虚拟机上一次系统调用约 0.8 µs，这一行 linux 和 darwin 的差距大半来自这里。

## 自己再跑一遍

在安静的机器上 `make bench`，用 `benchrun publish` 生成 `bench/published/` 里的一组数据，再 `make bench-docs` 写出表格。步骤和选项见 [bench/README.md](../bench/README.md)。在共用机器上，单线程的测试绑到一个核上跑（`taskset`、`GOMAXPROCS=1`），并发的测试只在没有别的任务时跑。
