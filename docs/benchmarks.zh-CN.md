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
提交 `cc1ede5`，Go 1.27.2，AMD EPYC 7B13，Linux 7.3.0-1003-gcp x86_64，GOMAXPROCS 1，共 2 个线程，2026-11-04。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 1.27 1.12 1.10，结束后 2.21 2.20 1.83。

每次操作的耗时，越低越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | **18.7 µs** ±3% | 19.6 µs ±2% | 23.4 µs ±3% | 21.5 µs ±3% |
| InsertTx | **1.30 ms** ±2% | 1.47 ms ±2% | 1.94 ms ±1% | 1.67 ms ±1% |
| Point | **3.58 µs** ±1% | 6.70 µs ±2% | 8.45 µs ±1% | 6.54 µs ±1% |
| Scan | **34.9 ms** ±1% | 73.4 ms ±3% | 76.4 ms ±4% | 50.2 ms ±1% |
| Aggregate | 2.87 ms ±1% | **2.63 ms** ±1% | 4.51 ms ±0% | 4.37 ms ±1% |
| PointParallel | **3.59 µs** ±2% | 6.66 µs ±1% | 8.36 µs ±2% | 6.59 µs ±1% |
| InsertParallel/g=1 | 18.7 µs ±2% | 19.5 µs ±3% | 23.0 µs ±3% | 21.1 µs ±2% |
| InsertParallel/g=8 | **18.5 µs** ±1% | 22.8 µs ±8% | 24.3 µs ±12% | 20.5 µs ±2% |
| InsertParallel/g=64 | **17.4 µs** ±1% | 23.6 µs ±5% | 41.0 µs ±10% | 24.5 µs ±6% |
| InsertParallelFull/g=1 | 842 µs ±8% | 974 µs ±6% | 947 µs ±13% | 888 µs ±15% |
| InsertParallelFull/g=8 | **188 µs** ±18% | 2.53 ms ±12% | 2.26 ms ±15% | 937 µs ±9% |
| InsertParallelFull/g=64 | **21.0 µs** ±7% | 2.48 ms ±23% | 5.31 ms ±23% | 1.12 ms ±8% |
| Real/insert | **1.41 ms** ±1% | 1.54 ms ±2% | 2.00 ms ±2% | 1.90 ms ±0% |
| Real/query | **509 µs** ±1% | 830 µs ±1% | 1.05 ms ±2% | 762 µs ±1% |
| Many/n=10 | **8.70 µs** ±1% | 15.2 µs ±1% | 16.7 µs ±1% | 13.5 µs ±0% |
| Many/n=100 | **58.3 µs** ±1% | 101 µs ±2% | 100 µs ±1% | 78.3 µs ±1% |
| Many/n=1000 | **584 µs** ±3% | 970 µs ±1% | 945 µs ±1% | 744 µs ±1% |
| Large/size=50k | 2.24 ms ±4% | 1.84 ms ±10% | 2.13 ms ±5% | 2.03 ms ±5% |
| Large/size=200k | 8.85 ms ±5% | **6.99 ms** ±7% | 8.13 ms ±4% | 8.02 ms ±5% |
| Readers/g=2 | **34.8 ms** ±1% | 74.3 ms ±2% | 77.9 ms ±1% | 51.5 ms ±2% |
| Readers/g=4 | **35.2 ms** ±2% | 74.0 ms ±2% | 77.9 ms ±1% | 51.6 ms ±1% |
| Readers/g=8 | **35.3 ms** ±1% | 75.1 ms ±1% | 79.6 ms ±2% | 52.1 ms ±1% |
| ReadWrite | **2.65 µs** ±7% | 4.07 µs ±10% | 5.78 µs ±10% | 7.60 µs ±2% |
| 几何平均 | 286 µs | 539 µs | 648 µs | 487 µs |

速率，越高越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 2233 MB/s ±4% | 2716 MB/s ±9% | 2344 MB/s ±4% | 2461 MB/s ±5% |
| Large/size=200k | 2260 MB/s ±5% | **2859 MB/s** ±7% | 2459 MB/s ±4% | 2493 MB/s ±5% |
| ReadWrite | 6516 writes/s ±35% | 5947 writes/s ±33% | 6367 writes/s ±27% | 6205 writes/s ±18% |

每次操作的内存分配次数，越低越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | **5** | 6 | 7 | **5** |
| InsertTx | 5.50k | 6.51k | 7.50k | **4.76k** |
| Point | **14** | 19 | 31 | 18 |
| Scan | **400k** | 800k | 800k | **400k** |
| Aggregate | **1.01k** | 2.00k | 2.01k | **1.01k** |
| PointParallel | **13** | 18 | 30 | 18 |
| InsertParallel/g=1 | **4** | 5 | 6 | 5 |
| InsertParallel/g=8 | **4** | 5 | 6 | 5 |
| InsertParallel/g=64 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 9 | **6** | 7 | 13 |
| InsertParallelFull/g=64 | **9** | 14 | 28 | 85 |
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
提交 `cc1ede5`，Go 1.27.2，AMD EPYC 7B13，Linux 7.3.0-1003-gcp x86_64，GOMAXPROCS 8，共 8 个线程，2026-11-04。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 1.45 1.78 1.75，结束后 2.83 2.50 2.10。

每次操作的耗时，越低越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **1.65 µs** ±8% | 2.00 µs ±66% | 2.18 µs ±14% | 2.29 µs ±55% |
| InsertParallel/g=1 | **17.6 µs** ±1% | 18.8 µs ±3% | 21.4 µs ±1% | 20.3 µs ±4% |
| InsertParallel/g=8 | **9.43 µs** ±14% | 19.4 µs ±8% | 24.2 µs ±7% | 21.9 µs ±1% |
| InsertParallel/g=64 | **3.40 µs** ±12% | 20.2 µs ±5% | 27.0 µs ±5% | 27.0 µs ±4% |
| InsertParallelFull/g=1 | 848 µs ±14% | 939 µs ±27% | 933 µs ±7% | 789 µs ±18% |
| InsertParallelFull/g=8 | **195 µs** ±11% | 2.21 ms ±29% | 2.49 ms ±19% | 802 µs ±17% |
| InsertParallelFull/g=64 | **19.3 µs** ±6% | 22.9 ms ±24% | 33.0 ms ±32% | 941 µs ±14% |
| Readers/g=2 | **17.9 ms** ±3% | 37.0 ms ±4% | 38.9 ms ±8% | 27.9 ms ±9% |
| Readers/g=4 | **10.7 ms** ±9% | 22.9 ms ±3% | 24.0 ms ±2% | 16.7 ms ±5% |
| Readers/g=8 | **8.30 ms** ±17% | 17.7 ms ±2% | 18.2 ms ±11% | 12.6 ms ±3% |
| ReadWrite | 2.59 µs ±39% | 4.27 µs ±20% | 5.85 µs ±14% | 3.34 µs ±3% |
| 几何平均 | 102 µs | 403 µs | 467 µs | 257 µs |

速率，越高越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| ReadWrite | 17974 writes/s ±25% | 12842 writes/s ±7% | 7439 writes/s ±6% | **28840 writes/s** ±3% |

每次操作的内存分配次数，越低越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| PointParallel | **13** | 18 | 30 | 19 |
| InsertParallel/g=1 | **4** | 5 | 6 | 5 |
| InsertParallel/g=8 | 9 | **5** | 6 | **5** |
| InsertParallel/g=64 | 9 | **5** | 6 | 6 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 9 | 5 | 7 | 11 |
| InsertParallelFull/g=64 | **9** | 44 | 55 | 72 |
| Readers/g=2 | **400k** | 800k | 800k | **400k** |
| Readers/g=4 | **400k** | 800k | 800k | **400k** |
| Readers/g=8 | **400k** | 800k | 800k | **400k** |
| ReadWrite | **14** | 19 | 31 | 19 |

<!-- /bench:linux-amd64-8threads -->

## darwin/arm64

<!-- bench:darwin-arm64 -->
提交 `90e0e83`，Go 1.27.2，Apple M4，Darwin 24.3.0 arm64，GOMAXPROCS 10，共 10 个线程，2026-11-04。取 6 轮的中位数，± 是它的 95% 置信区间。负载（1、5、15 分钟）开始前 1.74 2.04 2.23，结束后 6.79 5.38 4.28。测的时候机器上还有别的任务，30% 以内的差别当噪声看。

每次操作的耗时，越低越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | 6.32 µs ±16% | 6.21 µs ±28% | 7.96 µs ±19% | 7.00 µs ±9% |
| InsertTx | **456 µs** ±8% | 594 µs ±35% | 748 µs ±20% | 583 µs ±22% |
| Point | **1.12 µs** ±10% | 1.96 µs ±22% | 2.86 µs ±71% | 2.07 µs ±14% |
| Scan | **14.4 ms** ±19% | 31.6 ms ±18% | 30.1 ms ±23% | 20.7 ms ±17% |
| Aggregate | 1.56 ms ±15% | 1.19 ms ±18% | 2.65 ms ±33% | 1.90 ms ±17% |
| PointParallel | 1.29 µs ±36% | 1.69 µs ±34% | 1.79 µs ±73% | 2.49 µs ±9% |
| InsertParallel/g=1 | 6.29 µs ±15% | 6.26 µs ±4% | 7.33 µs ±55% | 6.91 µs ±15% |
| InsertParallel/g=8 | **3.30 µs** ±28% | 6.94 µs ±6% | 8.12 µs ±19% | 43.5 µs ±53% |
| InsertParallel/g=64 | **1.37 µs** ±31% | 9.53 µs ±12% | 8.28 ms ±100% | 83.3 µs ±24% |
| InsertParallelFull/g=1 | 61.5 µs ±35% | 83.0 µs ±31% | 64.8 µs ±71% | 61.1 µs ±17% |
| InsertParallelFull/g=8 | **19.8 µs** ±62% | 94.7 µs ±26% | 120 µs ±33% | 54.7 µs ±50% |
| InsertParallelFull/g=64 | **2.33 µs** ±19% | 158 µs ±52% | 22.0 ms ±84% | 87.3 µs ±16% |
| Real/insert | 578 µs ±16% | 569 µs ±71% | 1.05 ms ±17% | 718 µs ±12% |
| Real/query | **236 µs** ±17% | 396 µs ±16% | 594 µs ±7% | 345 µs ±4% |
| Many/n=10 | **2.93 µs** ±40% | 5.48 µs ±58% | 5.51 µs ±6% | 4.45 µs ±4% |
| Many/n=100 | 19.8 µs ±38% | 38.7 µs ±10% | 37.0 µs ±9% | 26.9 µs ±12% |
| Many/n=1000 | **196 µs** ±17% | 377 µs ±68% | 351 µs ±6% | 254 µs ±12% |
| Large/size=50k | 1.00 ms ±17% | 971 µs ±191% | 1.01 ms ±21% | 873 µs ±22% |
| Large/size=200k | 4.87 ms ±30% | 3.58 ms ±19% | 3.99 ms ±16% | 3.64 ms ±13% |
| Readers/g=2 | 8.76 ms ±112% | 19.2 ms ±17% | 16.7 ms ±22% | 11.4 ms ±26% |
| Readers/g=4 | 6.39 ms ±41% | 13.7 ms ±24% | 12.2 ms ±14% | 7.80 ms ±26% |
| Readers/g=8 | 5.59 ms ±24% | 11.5 ms ±40% | 9.68 ms ±19% | 6.37 ms ±31% |
| ReadWrite | 2.04 µs ±36% | 3.41 µs ±33% | 2.83 µs ±26% | 5.10 µs ±6% |
| 几何平均 | 78.3 µs | 152 µs | 281 µs | 159 µs |

速率，越高越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Large/size=50k | 5000 MB/s ±14% | 5170 MB/s ±66% | 4976 MB/s ±17% | 5728 MB/s ±18% |
| Large/size=200k | 4114 MB/s ±23% | 5592 MB/s ±16% | 5014 MB/s ±14% | 5490 MB/s ±12% |
| ReadWrite | 23887 writes/s ±46% | 13377 writes/s ±21% | 24145 writes/s ±23% | 19710 writes/s ±5% |

每次操作的内存分配次数，越低越好：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| Insert | **5** | 6 | 7 | **5** |
| InsertTx | 5.50k | 6.51k | 7.50k | **4.76k** |
| Point | **14** | 19 | 31 | 18 |
| Scan | **400k** | 800k | 800k | **400k** |
| Aggregate | **1.01k** | 2.00k | 2.01k | **1.01k** |
| PointParallel | **13** | 18 | 30 | 19 |
| InsertParallel/g=1 | **4** | 5 | 6 | 5 |
| InsertParallel/g=8 | 9 | **5** | 6 | **5** |
| InsertParallel/g=64 | 9 | **5** | 38 | 7 |
| InsertParallelFull/g=1 | **4** | 5 | 6 | 5 |
| InsertParallelFull/g=8 | 9 | **5** | 6 | **5** |
| InsertParallelFull/g=64 | 9 | **5** | 77 | 7 |
| Real/insert | 4.22k | 4.65k | 5.05k | **2.97k** |
| Real/query | **4.44k** | 7.90k | 7.93k | **4.44k** |
| Many/n=10 | **57** | 78 | 90 | 67 |
| Many/n=100 | **507** | 708 | 720 | 517 |
| Many/n=1000 | **5.75k** | 8.50k | 8.51k | 5.76k |
| Large/size=50k | **407** | 608 | 620 | 417 |
| Large/size=200k | **407** | 609 | 620 | 417 |
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

## 数字说明了什么

- **读**比 cgo 快：按主键读一行用一半左右的时间（linux 上 3.58 µs，别的驱动 6.54 到 8.45 µs），读 10 万行 35 ms，别的 50 到 76 ms，查一个用户和他的 400 条评论 509 µs，别的 762 µs 到 1.05 ms。调用引擎就是普通的 Go 调用，不用跨 cgo，而且同进程里另一个读正在进行或刚结束时，读事务不在内核里加锁（见 `internal/vfs`）。
- **多个写者**是组提交显出效果的地方：从 8 个写者起，一次提交带上许多条插入。64 个写者时每条插入 3.40 µs，别的驱动 20 到 27 µs（8 线程）。`synchronous=FULL` 下提交越贵差距越大：64 个写者时每条插入 19 到 21 µs，别的驱动 0.94 到 33 ms。在忙碌的 Mac 上，modernc 在 64 个写者时把时间花在 SQLite 的忙等处理里，持锁的写者等线程时，它的睡眠越睡越长。
- **写者旁边的读者**（ReadWrite，8 线程）：本驱动给读者的服务最快（每次读 2.59 µs，别的 3.34 到 5.85 µs），ncruces 让写者写得最多（每秒 2.88 万次，本驱动 1.80 万次）。
- **单个写者**（Insert）两台机器上都和 mattn 接近。时间花在写 WAL 和 SQLite 协议要求的加锁上，每个驱动的系统调用都一样。
- **连表加分组**（Aggregate）全是引擎内部的计算，比 cgo 慢 9%（linux）到 31%（darwin）：引擎是同一份 C，由 LLVM 编译，变成的 Go 汇编接近 clang 为本机生成的代码，但还不如它。
- **大文本**比别的驱动都读得慢：100 行 200 KB 的文本，linux 上 8.85 ms，别的 6.99 到 8.13 ms。时间花在哪还没量。
- **内存分配**：数字直接拷进 `Scan` 的目标（`driver.RowsColumnScanner`，Go 1.27），不装进接口，所以扫描的分配次数是 mattn 和 modernc 的一半。
- **linux 上的 Point** 每次查询都含一次 `fstat`，每个驱动都一样：WAL 为空时（夹具刚拷出来就是这样），SQLite 在每次读开始时读一次数据库文件的大小。这台虚拟机上一次系统调用约 0.8 µs，这一行 linux 和 darwin 的差距大半来自这里。

## 自己再跑一遍

在安静的机器上 `make bench`，用 `benchrun publish` 生成 `bench/published/` 里的一组数据，再 `make bench-docs` 写出表格。步骤和选项见 [bench/README.md](../bench/README.md)。在共用机器上，单线程的测试绑到一个核上跑（`taskset`、`GOMAXPROCS=1`），并发的测试只在没有别的任务时跑。
