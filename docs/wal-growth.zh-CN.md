---
summary: '数据库的 WAL 文件为什么会变得很大，驱动为此做了什么，什么情况下它还是会涨，怎么让它保持小'
read_when:
  - 数据库旁边的 -wal 文件很大，或者一直在涨
  - 程序会长时间开着读（报表、导出、没关的 rows）
  - 被问到驱动能不能让 WAL 保持小
  - 要选 journal_size_limit 或 _wal_limit
audience: users
translatedFrom: 0686ca86ed78
---

# WAL 越涨越大

[English](wal-growth.md) · 简体中文

WAL 模式下，SQLite 先把每次改动写进数据库旁边的 `-wal` 文件，之后再拷回数据库（这一步叫检查点）。只有 WAL 里的改动全都拷回去了，而且没有读者还在读它，WAL 才能从头开始写。程序在写的时候，驱动能让 WAL 保持小。读一直不停的时候，它做不到：一个一直开着的读会让 WAL 无限涨，用这个驱动和用任何 SQLite 都一样。这一页讲驱动做了什么，拦不住什么，以及你的程序能做什么。

## 驱动做了什么

- **光是写不会让它涨。** 驱动在持有写锁时做检查点，WAL 每长出 `wal_autocheckpoint` 页（默认 1000）做一次，所以 WAL 上没有读者时，不管多少 goroutine 在写，它长到这么长就从头开始写。单个事务比这还大的话，它的所有页仍然会先写进 WAL。linux/amd64 上 64 个 goroutine 连续插入 30 秒，WAL 停在 4 MiB，别的驱动涨到 3.40 到 5.49 GiB（见[基准测试](benchmarks.zh-CN.md#很多写者同时写)）。
- **过了 64 MiB 就等读者。** WAL 一旦超过 `_wal_limit`（默认 64 MiB），交还写锁的那个连接最多等 100 ms 让 WAL 的读者结束，下一个写者就从头开始写。没等到的话，等 WAL 再涨上限的四分之一时再试一次。细节见 [architecture.zh-CN.md](architecture.zh-CN.md#wal-长度的上限)。

## 什么情况下还是会涨

- **一个不结束的读。** 没读完也没关的 rows 会让一个读一直开着，一个已经读过、却既没提交也没回滚的事务也会。在 Apple M4 上，开着一个查询不关，同时插入 10 万行，WAL 涨到 595 MiB。这种情况没有哪个上限拦得住，这个驱动拦不住，SQLite 本身也拦不住。
- **很多短读一个接一个，中间没有空隙。** SQLite 给 WAL 的读者只留了四个槽。短的读事务要是一直把四个槽都占着，那 100 ms 就等不到空槽。在 Apple M4 上，64 个 goroutine 各自在 deferred 事务里先读后写，10 秒里 13 到 22 次尝试有 1 到 5 次被挡住，WAL 到了 80 到 96 MiB。基准测试里同类负载留下的 WAL，在 linux/amd64 上是 144 MiB，在 Windows 上是 199 到 272 MiB，modernc 在 Windows 上是 89 到 132 MiB（见[基准测试](benchmarks.zh-CN.md#在-windows-上)）。
- **文件大小不会缩回去。** WAL 从头开始写时，SQLite 只是从头覆盖，不会把文件变小。不设 `journal_size_limit` 的话，一个涨到过 595 MiB 的 WAL 在磁盘上一直占 595 MiB，直到一次 `TRUNCATE` 检查点，或者最后一个连接关闭。

## 怎么让它保持小

1. **读完就关。** 把 rows 读到底，或者调 `rows.Close()`（查询之后紧跟一句 `defer`），每个事务都用 `Commit` 或 `Rollback` 结束。
2. **读要短。** 报表、导出分页读，每页单独一个查询（`WHERE id > ? ORDER BY id LIMIT 1000`），不要用一个跑好几分钟的查询或事务。这样每一页看到的是它运行那一刻的数据库。如果整份导出必须是同一时刻的快照，那就只能是一个读，它跑多久 WAL 就涨多久。
3. **能用单条语句读就用单条语句。** 读事务里的每条语句都占着同一个读者槽。在 Apple M4 上，60 个 goroutine 用单条语句读，旁边 4 个写者，让 WAL 从头写的 8 次尝试一次都没被挡住。
4. **用 `_txlock=immediate` 打开。** 这样一个要写的事务会先等到写锁再读，64 个这样的事务排队时不占读者槽。在 linux/amd64 上，用 `_txlock=immediate` 时 WAL 停在 4 MiB，用 deferred 事务时到了 144 MiB。
5. **用 `journal_size_limit` 把磁盘空间还回来。** DSN 里加 `_pragma=journal_size_limit(67108864)`，WAL 从头开始写之后的第一次提交会把文件截到 64 MiB，这次提交需要更多时就截到它需要的大小。在那个开着查询不关的测试里，上限设成 4 MiB，查询关掉、写入继续之后，文件是 4 MiB，不设上限时是 598 MiB。
6. **`_wal_limit` 设小一些，会更早开始尝试。** 尝试期间写者要陪着等：最多 100 ms 等读者，然后是拷回数据库和刷盘。所以 `busy_timeout` 要比这大得多（驱动默认 5 秒）。`_wal_limit=0` 关掉这些尝试。
7. **盯着 `-wal` 文件的大小。** 看它在程序写的时候是不是一直变大，而不是看它有没有变小：不设 `journal_size_limit` 时它从来不会变小。一直涨过 `_wal_limit` 的话，通常是有一个读没有结束。

开了复制的数据库，检查点由它的复制器来做，默认每 4000 帧一次，WAL 超过 `_wal_limit` 时同样会等读者。你的程序发 `PRAGMA wal_checkpoint` 会被拒绝（见 [replication.zh-CN.md](replication.zh-CN.md)）。
