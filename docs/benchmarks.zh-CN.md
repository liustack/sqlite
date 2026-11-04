# 基准测试

[English](benchmarks.md) · 简体中文

测试内容、设置和运行方法见 [bench/README.md](../bench/README.md)。简单说：WAL，`synchronous=NORMAL`，每个基准一个 `*sql.DB`，每次用新文件，所有驱动都核对过相同的设置。数字是每次操作的耗时，越低越好，取 6 次运行的中位数。各驱动轮流跑，每轮每个基准各跑一次，这样机器上的其他负载对大家的影响一样。

| 驱动 | 版本 | 构建方式 |
| --- | --- | --- |
| 本驱动 | 提交 `beeea0d` | C 编译成 Go 汇编，`CGO_ENABLED=0` |
| mattn/go-sqlite3 | v1.14.52 | cgo |
| modernc.org/sqlite | v1.61.1 | C 翻译成 Go |
| ncruces/go-sqlite3 | v0.36.0 | SQLite 先编成 wasm，再由 wasm2go 翻译成 Go |

## linux/amd64

一台 Google Cloud 虚拟机，AMD EPYC 7B13，4 核 8 线程，Linux 7.3，Go 1.27.2。这台机器上系统调用很慢（`getppid` 要 360 ns），所以凡是加锁或写文件的操作都比桌面机更贵。

单线程（`GOMAXPROCS=1`，绑定到一个核和它的兄弟线程）：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| --- | --- | --- | --- | --- |
| Insert | 18.4 µs | 19.7 µs | 23.5 µs | 21.5 µs |
| InsertTx（1000 行） | 1.25 ms | 1.48 ms | 1.97 ms | 1.72 ms |
| Point | 2.20 µs | 5.87 µs | 7.36 µs | 5.84 µs |
| Scan（10 万行） | 34.8 ms | 76.9 ms | 74.2 ms | 49.4 ms |
| Aggregate | 2.90 ms | 2.73 ms | 4.50 ms | 4.43 ms |
| geomean | 348 µs | 514 µs | 647 µs | 543 µs |

全部 8 个线程，机器上没有其他负载：

| 基准 | 本驱动 | mattn | modernc | ncruces |
| --- | --- | --- | --- | --- |
| PointParallel | 1.47 µs | 2.20 µs | 2.33 µs | 2.04 µs |
| InsertParallel，1 个写入者 | 17.3 µs | 18.8 µs | 21.8 µs | 20.3 µs |
| InsertParallel，8 个写入者 | 9.74 µs | 19.1 µs | 25.7 µs | 22.0 µs |
| InsertParallel，64 个写入者 | 2.97 µs | 19.6 µs | 27.2 µs | 26.9 µs |
| geomean | 5.20 µs | 11.2 µs | 13.7 µs | 12.5 µs |

## darwin/arm64

一台 Apple M4（10 核），macOS 15，Go 1.27.2。测试期间机器还在跑其他工作（10 核上的平均负载是 3 到 8），所以同一个驱动的数字在多次运行间会浮动多达 30%，只有更大的差距才说明问题。四个驱动的 Insert 行都落在这个噪声之内。

| 基准 | 本驱动 | mattn | modernc | ncruces |
| --- | --- | --- | --- | --- |
| Insert | 8.92 µs | 9.16 µs | 11.0 µs | 9.27 µs |
| InsertTx（1000 行） | 723 µs | 837 µs | 915 µs | 634 µs |
| Point | 0.81 µs | 1.76 µs | 2.50 µs | 1.71 µs |
| Scan（10 万行） | 15.3 ms | 33.7 ms | 32.4 ms | 20.5 ms |
| Aggregate | 1.48 ms | 1.23 ms | 3.00 ms | 1.98 ms |
| PointParallel | 1.25 µs | 1.90 µs | 2.02 µs | 2.22 µs |
| InsertParallel，1 个写入者 | 9.08 µs | 9.71 µs | 8.66 µs | 9.09 µs |
| InsertParallel，8 个写入者 | 3.45 µs | 9.03 µs | 10.5 µs | 48.4 µs |
| InsertParallel，64 个写入者 | 1.42 µs | 16.8 µs | 13.4 ms | 80.0 µs |
| geomean | 26.6 µs | 48.8 µs | 122 µs | 68.2 µs |

## 数字说明了什么

- **读**（Point、PointParallel、Scan）比 cgo 快：调用引擎就是普通的 Go 调用，没有跨 cgo 的开销。进程里另一个读正在跑或刚结束时，读事务也不用在内核里加锁（见 `internal/vfs`）。
- **多个写入者**（InsertParallel）是合并提交发挥作用的地方：从 8 个写入者起，一次提交带上许多条插入，到 64 个写入者时，一条插入的耗时只有其他驱动的六分之一到十二分之一。在忙碌的 Mac 上，modernc 在 64 个写入者时把时间花在 SQLite 的 busy handler 里，它的休眠时间会越来越长，而持有锁的写入者在等一个线程。
- **一个写入者**（Insert）在两台机器上都和 mattn 接近。时间花在 WAL 写入和 SQLite 协议要求的锁上，所有驱动发出的系统调用都一样。
- **Aggregate** 全部工作都在引擎内部，比 cgo 慢 6%（linux）到 20%（darwin）：引擎是同一份 C，由 LLVM 编译，得到的 Go 汇编接近 clang 为这台机器生成的代码，但还不如它。
- **Scan** 把每个数字直接复制进 `Scan` 的目标变量（`driver.RowsColumnScanner`），不装箱成接口，所以分配次数只有 mattn 和 modernc 的一半。

## 重新测一遍

在安静的机器上运行 `bench/run.sh`，然后用 `benchstat`（最后会打印出表格）。把 `results/<goos>-<goarch>/header.txt` 和数字放在一起保存。在共用的机器上，单线程测试要绑定到一个核（`taskset`、`GOMAXPROCS=1`），并行测试要等机器上没有其他负载时再跑。
