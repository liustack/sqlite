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
提交 `abf3e08`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 1，共 1 个线程，2026-11-17。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.22 0.05 0.02，结束后 1.15 1.19 0.81。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 320 µs | 608 µs | 723 µs | 550 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2291 MB/s ±8% | 2378 MB/s ±25% | 2064 MB/s ±6% | 2111 MB/s ±5% |
| Large/size=200k | 2309 MB/s ±3% | **2427 MB/s** ±3% | 2112 MB/s ±4% | 2136 MB/s ±3% |
| ReadWrite | 6737 writes/s ±35% | 4206 writes/s ±31% | 6042 writes/s ±36% | 5129 writes/s ±11% |

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

### 全部 4 个核

机器上没有别的任务。只跑用多个 goroutine 的场景。

<!-- bench:linux-amd64-4threads -->
提交 `abf3e08`，Go 1.27.4，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-17。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.50 1.00 0.77，结束后 2.25 2.05 1.39。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 117 µs | 440 µs | 504 µs | 304 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | 17209 writes/s ±17% | 14358 writes/s ±13% | 4463 writes/s ±11% | **26998 writes/s** ±6% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
提交 `abf3e08`，Go 1.27.2，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-17。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 3.17 2.15 1.93，结束后 2.35 2.83 2.66。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 69.1 µs | 135 µs | 274 µs | 158 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 6071 MB/s ±8% | 5973 MB/s ±5% | 5528 MB/s ±5% | 5690 MB/s ±1% |
| Large/size=200k | 5897 MB/s ±4% | 5938 MB/s ±2% | 5388 MB/s ±1% | 5639 MB/s ±6% |
| ReadWrite | **34553 writes/s** ±12% | 16321 writes/s ±4% | 28912 writes/s ±2% | 20062 writes/s ±4% |

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
提交 `81e0480`，Go 1.27.4，Ubuntu clang version 22.1.8 (++20260714015917+ca7933e47d3a-1~exp1~20260714135927.17)，AMD EPYC 7B13，Linux 7.0.0-1017-gcp x86_64，GOMAXPROCS 4，共 4 个线程，2026-11-17。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.15 1.50 2.34，结束后 5.20 4.35 4.00。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **466 ms** ±5% | 547 ms ±1% | +17% |
| 给 100 万个文本建索引 | **401 ms** ±1% | 406 ms ±1% | +1% |
| 排序 100 万个文本 | **4.23 s** ±1% | 4.74 s ±1% | +12% |
| 100 万行分 1000 组求平均 | **314 ms** ±2% | 381 ms ±1% | +21% |
| 30 万行按主键连 100 万行的表 | **81.2 ms** ±3% | 89.1 ms ±2% | +10% |
| 递归 CTE 走 200 万步 | **446 ms** ±1% | 501 ms ±2% | +12% |
| 100 万个文本做 LIKE 和字符串函数 | **85.4 ms** ±2% | 87.6 ms ±0% | +3% |
| 生成并读取 30 万个 JSON 对象 | **419 ms** ±3% | 509 ms ±1% | +22% |
| 100 万个数算 sqrt、sin、log、pow | **168 ms** ±1% | 196 ms ±0% | +17% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **2.98 ms** ±1% | 3.45 ms ±1% | +15% |
| 几何平均 | 209 ms | 236 ms | +13% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 190k reads/s ±1% | **360k reads/s** ±1% | 275k reads/s ±2% | 102k reads/s ±1% |
| 8 个线程，每次按主键读一行 | 1110k reads/s ±7% | 1132k reads/s ±4% | 871k reads/s ±3% | 418k reads/s ±2% |
| 64 个线程，每次按主键读一行 | 1013k reads/s ±5% | **1150k reads/s** ±6% | 863k reads/s ±8% | 418k reads/s ±4% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 230k reads/s ±2% | **682k reads/s** ±3% | 439k reads/s ±2% | 114k reads/s ±1% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1335k reads/s ±8% | **1971k reads/s** ±12% | 1254k reads/s ±13% | 447k reads/s ±2% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1226k reads/s ±12% | **1501k reads/s** ±6% | 1110k reads/s ±4% | 310k reads/s ±9% |
| 8 个线程照上面读，同时另有 8 个线程在写 | **966k reads/s** ±8% | 534k reads/s ±6% | 433k reads/s ±6% | 164k reads/s ±2% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 2492 writes/s ±12% | **17766 writes/s** ±2% | 14922 writes/s ±5% | 14591 writes/s ±4% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 890 writes/s ±3% | 57718 writes/s ±5% | 57543 writes/s ±3% | 880 writes/s ±4% |

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
提交 `abf3e08`，Go 1.27.2，Homebrew clang version 22.1.8，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-17。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 2.31 2.78 2.65，结束后 10.17 7.93 6.52。M4 有 4 个性能核和 6 个能效核，8 个和 64 个线程的速率随分到的核变化，最多差三分之一：这几行以 linux 为准。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **188 ms** ±2% | 242 ms ±3% | +29% |
| 给 100 万个文本建索引 | 243 ms ±14% | 239 ms ±14% | -2% |
| 排序 100 万个文本 | **1.61 s** ±4% | 1.77 s ±4% | +10% |
| 100 万行分 1000 组求平均 | **172 ms** ±14% | 200 ms ±16% | +16% |
| 30 万行按主键连 100 万行的表 | **35.8 ms** ±4% | 46.3 ms ±1% | +29% |
| 递归 CTE 走 200 万步 | **200 ms** ±2% | 243 ms ±2% | +22% |
| 100 万个文本做 LIKE 和字符串函数 | 46.8 ms ±2% | 47.6 ms ±1% | +2% |
| 生成并读取 30 万个 JSON 对象 | **211 ms** ±1% | 230 ms ±1% | +9% |
| 100 万个数算 sqrt、sin、log、pow | **67.5 ms** ±2% | 89.7 ms ±2% | +33% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **1.27 ms** ±1% | 1.83 ms ±1% | +45% |
| 几何平均 | 97.3 ms | 115 ms | +18% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 670k reads/s ±2% | **1066k reads/s** ±3% | 833k reads/s ±2% | 327k reads/s ±1% |
| 8 个线程，每次按主键读一行 | 958k reads/s ±5% | **1505k reads/s** ±2% | 1367k reads/s ±2% | 771k reads/s ±5% |
| 64 个线程，每次按主键读一行 | 787k reads/s ±8% | **1335k reads/s** ±2% | 1179k reads/s ±3% | 680k reads/s ±3% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 889k reads/s ±3% | **2039k reads/s** ±1% | 1426k reads/s ±3% | 395k reads/s ±3% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1417k reads/s ±4% | **2385k reads/s** ±3% | 1974k reads/s ±3% | 854k reads/s ±3% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1200k reads/s ±4% | **1920k reads/s** ±5% | 1594k reads/s ±6% | 760k reads/s ±5% |
| 8 个线程照上面读，同时另有 8 个线程在写 | 605k reads/s ±2% | **714k reads/s** ±3% | 631k reads/s ±2% | 316k reads/s ±3% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 17905 writes/s ±1% | **69176 writes/s** ±3% | 64019 writes/s ±6% | 18028 writes/s ±2% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 17635 writes/s ±22% | **549k writes/s** ±2% | 508k writes/s ±5% | 41378 writes/s ±10% |

<!-- /bench:native-darwin-arm64 -->

从表里能看出：

- **引擎内部的计算**在 linux 上最多比 C 多花 22% 的时间，在 darwin 上最多 45%，几何平均分别是 13% 和 18%，建索引和 `LIKE` 与 C 相差不到 3%：C 编成的 Go 汇编接近 clang 为本机生成的代码，但不如它。引擎用的还是 musl 的字符串和数学函数，数学函数编译时不开 fused multiply-add，这样每个平台算出的每一位都相同，而 C 程序用的是系统自带的。
- **单个读者**比 C 快：liustack/sqlite(直接调用) 每秒读的行数是 C 的 1.6 到 3.0 倍。C 里每次读都要用一次 `fcntl` 拿 WAL 的读锁，再用一次还回去，除非进程里另一个连接正拿着。liustack/sqlite 在最后一个读者离开后把锁多留约一毫秒，下一次读就不用系统调用（见 `internal/vfs`）。linux 虚拟机上系统调用最贵。
- **多个读者**在 linux 上读的行数和 C 持平或更多：liustack/sqlite(直接调用) 是 C 的 1.0 到 1.5 倍。这时 C 的读者之间共用读锁，也不用系统调用，所以单个读者时的优势大半没有了。darwin 上领先得更多，是 C 的 1.6 到 1.7 倍：那里 C 的读者约一半时间在等进程里管 WAL 锁的那把 pthread 互斥锁，几个线程一抢，macOS 就让它们进内核睡眠。liustack/sqlite 的锁是 Go 的互斥锁。
- **通过 `database/sql`** 读比 liustack/sqlite(直接调用) 多花 10% 到 60% 的时间，差的是 `database/sql` 自己的开销，linux 上 8 个以上读者时这让 liustack/sqlite 是 C 的 78% 到 94%。mattn 也要付这份开销，外加一次 cgo 调用。连接会留着读查询的语句，所以每次查询不用重新编译（见 [docs/architecture.zh-CN.md](architecture.zh-CN.md#查询的语句留着复用)）。用 `*sql.Stmt` 的话每次查询都要拿 `*sql.DB` 的锁，在 `*sql.Conn` 上准备的也一样，所以基准里不用它。
- **读者旁边有写者**时，linux 上读出的行数是 C 的 45% 到 55%，同时 liustack/sqlite 的写者插入的行数是 C 的 6 到 7 倍。darwin 上读的行数和 C 持平或更多。
- **写**是组提交起作用的地方：一次提交带上很多次插入，所以有读者在旁边时 liustack/sqlite 插入的行数是 C 的 3.6 到 7.1 倍，64 个写者加 `synchronous=FULL` 时是 C 的 29 到 65 倍，那时 C 的每个写者都要等自己的 fsync。

## 数字说明了什么

- **读**比 cgo 快：按主键读一行用一半左右的时间（linux 上 3.84 µs，别的驱动 7.18 到 9.36 µs），读 10 万行 36 ms，别的 52 到 82 ms，查一个用户和他的 400 条评论 515 µs，别的 831 µs 到 1.15 ms。调用引擎就是普通的 Go 调用，不用跨 cgo，而且同进程里另一个读正在进行或刚结束时，读事务不在内核里加锁（见 `internal/vfs`）。
- **多个写者**是组提交显出效果的地方：从 8 个写者起，一次提交带上许多条插入。64 个写者时每条插入 3.38 µs，别的驱动 22 到 29 µs（4 线程）。`synchronous=FULL` 下提交越贵差距越大：64 个写者时每条插入 28.0 µs，别的驱动 1.35 到 23 ms。在 Mac 上，modernc 在 64 个写者时把时间花在 SQLite 的忙等处理里，写者等得越久，它每次睡得越长。
- **写者旁边的读者**（ReadWrite，4 线程）：本驱动给读者的服务最快（每次读 2.69 µs，别的 4.18 到 6.86 µs），ncruces 让写者写得最多（每秒 2.70 万次，本驱动 1.72 万次）。
- **单个写者**（Insert）两台机器上都和 mattn 接近。时间花在写 WAL 和 SQLite 协议要求的加锁上，每个驱动的系统调用都一样。
- **连表加分组**（Aggregate）全是引擎内部的计算，在 linux 上和 cgo 用时一样，在 darwin 上多用 11%：引擎是同一份 C，由 LLVM 编译，变成的 Go 汇编接近 clang 为本机生成的代码，但还不如它。
- **大文本**在 linux 上比 mattn 慢 5%（100 行 200 KB 的文本 8.66 ms，mattn 8.24 ms），比 modernc 和 ncruces 快。darwin 上四个驱动持平。
- **内存分配**：数字直接拷进 `Scan` 的目标（`driver.RowsColumnScanner`，Go 1.27），不装进接口，所以扫描的分配次数是 mattn 和 modernc 的一半。
- **和 C 版 SQLite 比**，引擎内部的计算几何平均多花 13%（linux）和 18%（darwin）的时间。单个读者比 C 快，因为读不用为锁做系统调用。linux 上多个读者直接调用 liustack/sqlite 时读的行数和 C 持平或更多，通过 `database/sql` 是 C 的 78% 到 94%。写者靠组提交插入的行数是 C 的 3.6 到 65 倍。linux 上读者旁边有写者时，读的行数约是 C 的一半。
- **linux 上的 Point** 每次查询都含一次 `fstat`，每个驱动都一样：WAL 为空时（夹具刚拷出来就是这样），SQLite 在每次读开始时读一次数据库文件的大小。这台虚拟机上一次系统调用约 0.8 µs，这一行 linux 和 darwin 的差距大半来自这里。

## 自己再跑一遍

在安静的机器上 `make bench`，用 `benchrun publish` 生成 `bench/published/` 里的一组数据，再 `make bench-docs` 写出表格。步骤和选项见 [bench/README.md](../bench/README.md)。在共用机器上，单线程的测试绑到一个核上跑（`taskset`、`GOMAXPROCS=1`），并发的测试只在没有别的任务时跑。
