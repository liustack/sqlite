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

一台 Google Cloud 虚拟机，AMD EPYC 7B13，4 核 8 线程。它上面系统调用很慢（`getppid` 要 360 ns），所以凡是加锁、写文件的操作都比台式机贵。磁盘是虚拟机的持久磁盘：一次带 fsync 的提交约 0.6 到 0.9 ms。

### 单线程

`GOMAXPROCS=1`，绑在一个核和它的兄弟线程上。并发场景的 goroutine 在上面轮流跑。

<!-- bench:linux-amd64-1thread -->
提交 `bc87680`，Go 1.27.2，AMD EPYC 7B13，Linux 7.3.0-1003-gcp x86_64，GOMAXPROCS 1，共 2 个线程，2026-11-06。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 0.16 0.03 0.01，结束后 1.13 1.09 0.73。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 282 µs | 530 µs | 623 µs | 484 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2672 MB/s ±3% | 2775 MB/s ±4% | 2359 MB/s ±1% | 2486 MB/s ±1% |
| Large/size=200k | 2764 MB/s ±1% | **2955 MB/s** ±3% | 2535 MB/s ±6% | 2562 MB/s ±4% |
| ReadWrite | 6438 writes/s ±51% | 6773 writes/s ±36% | 6160 writes/s ±81% | 6192 writes/s ±6% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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

### 全部 8 线程

机器上没有别的任务。只跑用多个 goroutine 的场景。

<!-- bench:linux-amd64-8threads -->
提交 `bc87680`，Go 1.27.2，AMD EPYC 7B13，Linux 7.3.0-1003-gcp x86_64，GOMAXPROCS 8，共 8 个线程，2026-11-06。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 1.13 1.09 0.73，结束后 2.77 2.51 1.60。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 103 µs | 416 µs | 470 µs | 258 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | 16992 writes/s ±12% | 12806 writes/s ±12% | 7563 writes/s ±22% | **28731 writes/s** ±25% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
提交 `92ec4bf`，Go 1.27.2，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-06。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 7.03 3.81 3.05，结束后 3.93 3.57 3.26。测的时候机器上还有别的任务，30% 以内的差别当噪声看。

每次操作的耗时，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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
| 几何平均 | 69.5 µs | 131 µs | 262 µs | 155 µs |

速率，越高越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 6094 MB/s ±8% | 6159 MB/s ±16% | 5562 MB/s ±5% | 5743 MB/s ±9% |
| Large/size=200k | 5906 MB/s ±6% | 5949 MB/s ±17% | 5365 MB/s ±12% | 5523 MB/s ±4% |
| ReadWrite | **32601 writes/s** ±9% | 16171 writes/s ±11% | 27775 writes/s ±8% | 20503 writes/s ±7% |

每次操作的内存分配次数，越低越好：

| 基准 | liustack/sqlite | mattn | modernc | ncruces |
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

## 复制

[复制](replication.zh-CN.md)在程序里复制每次提交，所以 CPU 花在程序自己的进程里。`replica` 里的 `BenchmarkGroupCommit` 让 8 个或 64 个 goroutine 同时插入，每次一行（WAL，`synchronous=NORMAL`），分别关掉和打开复制。存储是同一台机器另外两个线程上的 MinIO，它的开销不落在基准的线程上，就像存储在别的机器上时一样。

提交 `def7170`，Go 1.27.2，上面那台 linux/amd64 虚拟机，Linux 7.3.0-1003-gcp。每组跑 6 轮，用 benchstat 比较（`~` 表示和噪声分不开）。负载开始前 1.12，结束后 1.92。原始输出在 [bench/published/replica/](../bench/published/replica/)。

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
提交 `ea5dd2a`，Go 1.27.2，Ubuntu clang version 22.1.8 (1ubuntu1)，AMD EPYC 7B13，Linux 7.3.0-1003-gcp x86_64，GOMAXPROCS 8，共 8 个线程，2026-11-07。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 1.03 1.20 1.35，结束后 5.77 5.23 4.58。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **427 ms** ±4% | 588 ms ±0% | +38% |
| 给 100 万个文本建索引 | **358 ms** ±1% | 461 ms ±1% | +29% |
| 排序 100 万个文本 | **3.41 s** ±1% | 4.03 s ±1% | +18% |
| 100 万行分 1000 组求平均 | **285 ms** ±0% | 370 ms ±0% | +30% |
| 30 万行按主键连 100 万行的表 | **61.6 ms** ±2% | 78.6 ms ±5% | +28% |
| 递归 CTE 走 200 万步 | **412 ms** ±2% | 516 ms ±1% | +25% |
| 100 万个文本做 LIKE 和字符串函数 | **77.5 ms** ±1% | 109 ms ±1% | +40% |
| 生成并读取 30 万个 JSON 对象 | **383 ms** ±1% | 569 ms ±0% | +48% |
| 100 万个数算 sqrt、sin、log、pow | **151 ms** ±4% | 189 ms ±0% | +25% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **2.67 ms** ±0% | 3.34 ms ±0% | +25% |
| 几何平均 | 184 ms | 240 ms | +30% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 218k reads/s ±1% | **408k reads/s** ±1% | 314k reads/s ±1% | 116k reads/s ±0% |
| 8 个线程，每次按主键读一行 | **1933k reads/s** ±4% | 1715k reads/s ±1% | 1161k reads/s ±1% | 546k reads/s ±9% |
| 64 个线程，每次按主键读一行 | **1841k reads/s** ±8% | 1647k reads/s ±11% | 1127k reads/s ±3% | 551k reads/s ±1% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 276k reads/s ±0% | **810k reads/s** ±1% | 542k reads/s ±2% | 135k reads/s ±1% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 2525k reads/s ±9% | 2555k reads/s ±1% | 1541k reads/s ±1% | 489k reads/s ±2% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | **2401k reads/s** ±1% | 2196k reads/s ±1% | 1422k reads/s ±3% | 389k reads/s ±3% |
| 8 个线程照上面读，同时另有 8 个线程在写 | **1492k reads/s** ±8% | 563k reads/s ±1% | 452k reads/s ±4% | 242k reads/s ±2% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 6352 writes/s ±13% | 10039 writes/s ±34% | 13468 writes/s ±23% | 9029 writes/s ±1% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 1268 writes/s ±2% | 71316 writes/s ±2% | 70712 writes/s ±2% | 1237 writes/s ±7% |

<!-- /bench:native-linux-amd64 -->

### darwin/arm64

<!-- bench:native-darwin-arm64 -->
提交 `ea5dd2a`，Go 1.27.2，Homebrew clang version 22.1.8，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-07。取 10 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 3.38 3.81 3.88，结束后 8.80 8.29 7.21。M4 有 4 个性能核和 6 个能效核，8 个和 64 个线程的速率随分到的核变化，最多差三分之一：这几行以 linux 为准。

**引擎计算速度**：每项工作在一条连接、`:memory:` 上花的时间，越短越好。最后一列是 liustack/sqlite 比 C 多花的时间：

| 基准 | SQLite(C) | liustack/sqlite | 比 C 慢 |
| :-- | --: | --: | --: |
| 一条语句插入 100 万行 | **191 ms** ±4% | 263 ms ±1% | +38% |
| 给 100 万个文本建索引 | **213 ms** ±8% | 260 ms ±2% | +22% |
| 排序 100 万个文本 | **1.62 s** ±3% | 1.83 s ±2% | +13% |
| 100 万行分 1000 组求平均 | **147 ms** ±6% | 208 ms ±3% | +41% |
| 30 万行按主键连 100 万行的表 | **36.0 ms** ±0% | 48.0 ms ±1% | +34% |
| 递归 CTE 走 200 万步 | **199 ms** ±1% | 269 ms ±1% | +35% |
| 100 万个文本做 LIKE 和字符串函数 | **46.6 ms** ±2% | 54.7 ms ±1% | +17% |
| 生成并读取 30 万个 JSON 对象 | **211 ms** ±1% | 257 ms ±0% | +22% |
| 100 万个数算 sqrt、sin、log、pow | **67.3 ms** ±1% | 107 ms ±2% | +59% |
| 20 万篇文档里做 4 次 FTS5 搜索 | **1.25 ms** ±1% | 1.84 ms ±1% | +47% |
| 几何平均 | 94.6 ms | 125 ms | +32% |

**吞吐量**：所有线程加起来每秒完成多少次读或写，文件是 WAL 模式、10 万行，越高越好：

| 基准 | SQLite(C) | liustack/sqlite(直接调用) | liustack/sqlite(database/sql) | mattn(database/sql) |
| :-- | --: | --: | --: | --: |
| 1 个线程，每次按主键读一行 | 661k reads/s ±5% | **981k reads/s** ±3% | 743k reads/s ±5% | 319k reads/s ±2% |
| 8 个线程，每次按主键读一行 | 826k reads/s ±16% | 1388k reads/s ±13% | 1157k reads/s ±19% | 546k reads/s ±36% |
| 64 个线程，每次按主键读一行 | 745k reads/s ±6% | 1213k reads/s ±11% | 1101k reads/s ±14% | 498k reads/s ±19% |
| 1 个线程，每次按主键读一行，整个文件在缓存里 | 868k reads/s ±4% | **1817k reads/s** ±3% | 1286k reads/s ±3% | 383k reads/s ±3% |
| 8 个线程，每次按主键读一行，整个文件在缓存里 | 1103k reads/s ±20% | 2353k reads/s ±29% | 1820k reads/s ±32% | 617k reads/s ±30% |
| 64 个线程，每次按主键读一行，整个文件在缓存里 | 1105k reads/s ±10% | **1862k reads/s** ±10% | 1516k reads/s ±11% | 551k reads/s ±10% |
| 8 个线程照上面读，同时另有 8 个线程在写 | 419k reads/s ±13% | 502k reads/s ±15% | 421k reads/s ±11% | 216k reads/s ±9% |
| 8 个线程，每次插入一行，同时另有 8 个线程在读 | 14902 writes/s ±10% | 52404 writes/s ±11% | 48105 writes/s ±8% | 14005 writes/s ±8% |
| 64 个线程，每次插入一行，`synchronous=FULL` | 18040 writes/s ±21% | 521k writes/s ±7% | 433k writes/s ±21% | 40402 writes/s ±18% |

<!-- /bench:native-darwin-arm64 -->

从表里能看出：

- **引擎内部的计算**比 C 多花 13% 到 59% 的时间，两台机器上几何平均都约 30%：C 编成的 Go 汇编接近 clang 为本机生成的代码，但不如它。引擎用的还是 musl 的字符串和数学函数，数学函数编译时不开 fused multiply-add，这样每个平台算出的每一位都相同，而 C 程序用的是系统自带的。
- **单个读者**比 C 快：liustack/sqlite(直接调用) 每秒读的行数是 C 的 1.5 到 2.9 倍。C 里每次读都要用一次 `fcntl` 拿 WAL 的读锁，再用一次还回去，除非进程里另一个连接正拿着。liustack/sqlite 在最后一个读者离开后把锁多留约一毫秒，下一次读就不用系统调用（见 `internal/vfs`）。linux 虚拟机上系统调用最贵。
- **多个读者**在 linux 上 C 更快，这时 C 的读者之间共用读锁，也不用系统调用：liustack/sqlite(直接调用) 是 C 的 89% 到 101%。darwin 上 liustack/sqlite 仍然比 C 快：那里 C 的读者约一半时间在等进程里管 WAL 锁的那把 pthread 互斥锁，几个线程一抢，macOS 就让它们进内核睡眠。liustack/sqlite 的锁是 Go 的互斥锁。
- **通过 `database/sql`** 读比 liustack/sqlite(直接调用) 多花 30% 到 50% 的时间，差的是 `database/sql` 自己的开销，linux 上 8 个以上读者时这让 liustack/sqlite 只有 C 的 60% 左右。mattn 也要付这份开销，外加一次 cgo 调用。连接会留着读查询的语句，所以每次查询不用重新编译（见 [docs/architecture.zh-CN.md](architecture.zh-CN.md#查询的语句留着复用)）。用 `*sql.Stmt` 的话每次查询都要拿 `*sql.DB` 的锁，在 `*sql.Conn` 上准备的也一样，所以基准里不用它。
- **读者旁边有写者**时，linux 上读出的行数只有 C 的 30% 到 38%，同时 liustack/sqlite 的写者插入的行数是 C 的 1.6 到 2.1 倍。darwin 上读的行数和 C 持平或更多。
- **写**是组提交起作用的地方：一次提交带上很多次插入，所以有读者在旁边时 liustack/sqlite 插入的行数是 C 的 1.6 到 3.5 倍，64 个写者加 `synchronous=FULL` 时是 C 的 24 到 56 倍，那时 C 的每个写者都要等自己的 fsync。

## 数字说明了什么

- **读**比 cgo 快：按主键读一行用一半左右的时间（linux 上 3.61 µs，别的驱动 6.53 到 8.32 µs），读 10 万行 34 ms，别的 51 到 72 ms，查一个用户和他的 400 条评论 514 µs，别的 755 µs 到 1.01 ms。调用引擎就是普通的 Go 调用，不用跨 cgo，而且同进程里另一个读正在进行或刚结束时，读事务不在内核里加锁（见 `internal/vfs`）。
- **多个写者**是组提交显出效果的地方：从 8 个写者起，一次提交带上许多条插入。64 个写者时每条插入 3.60 µs，别的驱动 20 到 28 µs（8 线程）。`synchronous=FULL` 下提交越贵差距越大：64 个写者时每条插入 19.5 µs，别的驱动 0.94 到 27 ms。在忙碌的 Mac 上，modernc 在 64 个写者时把时间花在 SQLite 的忙等处理里，持锁的写者等线程时，它的睡眠越睡越长。
- **写者旁边的读者**（ReadWrite，8 线程）：本驱动给读者的服务最快（每次读 2.81 µs，别的 3.25 到 5.79 µs），ncruces 让写者写得最多（每秒 2.87 万次，本驱动 1.70 万次）。
- **单个写者**（Insert）两台机器上都和 mattn 接近。时间花在写 WAL 和 SQLite 协议要求的加锁上，每个驱动的系统调用都一样。
- **连表加分组**（Aggregate）全是引擎内部的计算，比 cgo 慢 10%（linux）到 18%（darwin）：引擎是同一份 C，由 LLVM 编译，变成的 Go 汇编接近 clang 为本机生成的代码，但还不如它。
- **大文本**在 linux 上比 mattn 慢 7%（100 行 200 KB 的文本 7.23 ms，mattn 6.77 ms），比 modernc 和 ncruces 快。darwin 上四个驱动持平。
- **内存分配**：数字直接拷进 `Scan` 的目标（`driver.RowsColumnScanner`，Go 1.27），不装进接口，所以扫描的分配次数是 mattn 和 modernc 的一半。
- **和 C 版 SQLite 比**，引擎内部的计算多花约 30% 的时间。单个读者比 C 快，因为读不用为锁做系统调用。linux 上多个读者直接调用 liustack/sqlite 约是 C 的 90%，通过 `database/sql` 约 60%。写者靠组提交插入的行数是 C 的 1.6 到 56 倍。linux 上读者旁边有写者时，读的行数只有 C 的三分之一。
- **linux 上的 Point** 每次查询都含一次 `fstat`，每个驱动都一样：WAL 为空时（夹具刚拷出来就是这样），SQLite 在每次读开始时读一次数据库文件的大小。这台虚拟机上一次系统调用约 0.8 µs，这一行 linux 和 darwin 的差距大半来自这里。

## 自己再跑一遍

在安静的机器上 `make bench`，用 `benchrun publish` 生成 `bench/published/` 里的一组数据，再 `make bench-docs` 写出表格。步骤和选项见 [bench/README.md](../bench/README.md)。在共用机器上，单线程的测试绑到一个核上跑（`taskset`、`GOMAXPROCS=1`），并发的测试只在没有别的任务时跑。
