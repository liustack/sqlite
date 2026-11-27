---
summary: '驱动向程序保证什么，两个目标冲突时按什么顺序取舍，哪些事更快也不做，以及改动按什么评判'
read_when:
  - 决定要不要用这个驱动在服务器上跑 SQLite
  - 提一个让驱动更快或更省内存的改动
  - 被问到驱动为什么不做别的驱动做的事（给读让路、等着凑批、让 C 在自己的栈上跑）
translatedFrom: ead35ef1a010
---

# 原则

[English](principles.md) · 简体中文

## 它为谁

SQLite 只有一个写入者，这一点不会变。变的是谁在等：同一个程序里的写入者在 Go 里按先来后到排队，一个写只等排在它前面的写，不会因为旁边的写拿到 `database is locked`。在应用和设备上，同一时刻只有一个用户在写，SQLite 到处都是。到了服务器上，很多请求同时写，很多 Go 开发者不敢把 SQLite 放上服务器，正是因为这个错误。这里并发的自动提交写还会共用一次提交，所以多个写入者一起写比一个写入者更快。程序可以从很多 goroutine 同时写，数据放在一个文件里，和一个静态 Go 二进制放在一起，全程是纯 Go。

## 取舍顺序

两个目标冲突时，顺序是：

1. **稳定和安全。** 不丢写，不给错结果，不削弱提交，不新增任何拿到 `database is locked` 或 `SQLITE_BUSY` 的途径。
2. **快。**
3. **省内存。**

更快但在第一条上有任何代价的改动不做。省内存但损失速度或安全的改动也不做。保守是有意的设计，不是等着放开的第一版。

## 程序可以依赖什么

- **调用返回时已经提交。** 合并提交不拿持久性换速度：合并执行的写和单独执行一样持久，按程序选的 `synchronous` 级别。
- **合并执行的写和单独执行结果一样。** 每条都在自己的 SAVEPOINT 里跑，有自己的 LastInsertId 和 RowsAffected，一条失败不连累其他。唯一的例外是 `changes()` 和 `total_changes()`（见 [group-commit.zh-CN.md](group-commit.zh-CN.md)）。
- **跑过的写不会再跑。** 一次失败结束整批时，已经跑过的写拿到 `SQLITE_ABORT_ROLLBACK`，只有还没进 SQLite 的写回到队列。
- **事务声明了要写时（`_txlock=immediate`，或第一条语句就是写），同一个程序的写入者之间不会拿到 `database is locked`。** 它们按到达顺序等。实测次数见 [benchmarks.zh-CN.md](benchmarks.zh-CN.md#很多写者同时写)。
- **并发写不会让 WAL 一直涨。** 驱动在持有写锁时做检查点，所以 WAL 上没有读者时，不管多少 goroutine 在写，WAL 默认每 1000 页从头开始写，崩溃后数据库也打开得快（见 [architecture.zh-CN.md](architecture.zh-CN.md#自动检查点)）。读挡着它从头写时，从 64 MiB 起驱动每次最多等 100 ms 让读让开。一个永远不结束的读仍然会让它涨，和 SQLite 一样（见 [architecture.zh-CN.md](architecture.zh-CN.md#wal-长度的上限)），很多短的读事务一个接一个、中间没有空隙时也会。怎么让它保持小，见 [wal-growth.zh-CN.md](wal-growth.zh-CN.md)。
- **任何地方都不需要 C 编译器。** SQLite 原版的 C 一字不改，编译成入库的 Go 汇编。`CGO_ENABLED=0` 就能为 linux、darwin、windows 的 amd64 和 arm64 构建。

## 它保证不了什么

- 先读后写的 deferred 事务，如果中间别的写提交了，仍然会拿到 `database is locked`。这是 SQLite 为了保证读一致定下的规则，哪个驱动都去不掉。要写的事务用 `_txlock=immediate`。
- 另一个进程写同一个文件，不在驱动的队列里。两边在 SQLite 的锁上相遇，和任何驱动一样。
- 磁盘跟不上就是跟不上。写会等更久，等过 `busy_timeout` 的写会失败：那是过载，不是运气差。

## 刻意不做的事

每一条都试过或掂量过，有意不做：

- **在读和写之间分配 CPU 核。** 谁下一个跑是 Go 调度器的事。帮写的让出或配额，会以驱动看不到的方式伤到读（一次实验让读慢了 180 倍）。驱动只去掉自己的开销：锁上的睡眠、多余的系统调用、假的 `SQLITE_BUSY`，并且读写数字一起报（见 [busy-cores.zh-CN.md](busy-cores.zh-CN.md)）。
- **为凑满一批而等待。** 提交本身就是等待：fsync 期间到达的写会进下一批。
- **把检查点挪到写锁外面。** 那样慢刷盘时写入者能继续写，但程序自己的 `PRAGMA wal_checkpoint` 会因此返回 busy，快磁盘上反而变慢。
- **改 SQLite 的 C，或手改生成的汇编。** 编译选项都写明理由。SQLite 需要的外部东西由一个小 libc 或 Go 宿主提供。
- **碰 Go 的内部实现，或让 C 在自己的栈上跑。** 两者都会随 Go 版本坏掉，而在自己的栈上出错会结束整个进程，不只是一次调用。
- **收比噪声还小的提速。** 2% 到 3% 以下的提速是代码布局的噪声，改动再干净也不收。
- **带着已知 bug 上新功能。** bug 发现就修，再罕见也修，除非每种修法都损失稳定。

## 怎么评判一个改动

- 列出调用方可能新拿到 `database is locked` 或 `SQLITE_BUSY` 的每一个地方。每一个都算这个改动的代价。
- 读和写一起量，在不止一台机器上量。一边的提速由另一边买单，不算提速。
- 新行为先写失败的测试。bug 先找根因，不用兜底值盖住它。
- 每部分必须守住的规则在 [group-commit.zh-CN.md](group-commit.zh-CN.md)、[query-cache.zh-CN.md](query-cache.zh-CN.md) 和 [busy-cores.zh-CN.md](busy-cores.zh-CN.md)。
