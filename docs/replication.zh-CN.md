---
summary: '怎样用 replica 包把数据库复制到 S3 协议存储、怎样恢复，以及复制给程序带来的变化和它的边界'
read_when:
  - 给数据库配复制，或者恢复数据库
  - 被问到复制器保证什么，或者存储断开时会怎样
  - 把复制中的数据库搬到另一台机器
  - 改 replica 面向用户的行为
---

# 复制

[English](replication.md) · 简体中文

`replica` 包把数据库复制一份到 S3 协议存储（AWS S3、Cloudflare R2、MinIO 等），比线上文件晚一秒左右。机器丢了，丢的是几秒的写入，不是整个库。它跑在你的程序里，不用另外部署进程。副本的布局和 Litestream 一样，`litestream restore` 也能把它恢复出来。

## 开始

在程序打开数据库之前启动复制器，用完关掉：

```go
r, err := replica.New(ctx, "app.db", replica.Config{
	URL: "s3://my-bucket/app.db?endpoint=https://<account>.r2.cloudflarestorage.com&region=auto",
})
if err != nil {
	return err
}
defer r.Close()

db, err := sql.Open("sqlite", "app.db")
```

`URL` 写成 `s3://bucket/path`。查询参数有 `endpoint`（默认 `https://s3.<region>.amazonaws.com`）和 `region`（默认取 `AWS_REGION`，没有就是 `us-east-1`）。R2 用 `region=auto`。明文 `http://` 的地址会被拒绝，除非设了 `AllowHTTP`，给本地 MinIO 用。

凭证默认读 `AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY` 和 `AWS_SESSION_TOKEN`，也可以用 `Config.Credentials` 给：固定密钥用 `replica.StaticCredentials`，或者自己实现 `CredentialsProvider`，比如把 AWS SDK 的实例角色凭证包一层。复制器的错误、状态和日志里都不会出现密钥和会话令牌。

最常改的几项：

| 设置 | 默认 | 作用 |
| --- | --- | --- |
| `SyncInterval` | 1s | 从提交到复制进暂存目录，最多隔多久 |
| `Retention` | 24h | 至少能恢复到多久以前。每隔 `Retention` 或每天上传一次整库快照，取更频繁的那个 |
| `StagingLimit` | 1 GiB | 暂存了还没上传的变更最多多少字节，比如存储断开的时候 |
| `OnError` | 无 | 接收后台工作的错误，记进日志 |

## 对你的程序有什么变化

- **写入从不等 S3。** 每次提交先复制进数据库旁边的暂存目录 `app.db-replica/`，再在后台上传。存储慢了或断了，写入既不变慢也不会失败。
- **数据库是 WAL 模式，**检查点由复制器来做。复制期间，`PRAGMA wal_checkpoint`、`wal_autocheckpoint`、WAL 以外的 `journal_mode` 和 `locking_mode=EXCLUSIVE` 会报错。WAL 每到 4000 帧（4 KiB 的页约 16 MB）做一次检查点。
- **只跟踪本进程里这个驱动的写入。** 在 `New` 之后用这个驱动打开数据库。如果别的进程（sqlite3 命令行、别的程序）写了文件还做了检查点，复制器会发现 WAL 的变化它没跟上，于是暂存一份整库，并报告给 `OnError`。数据不会丢，但整库要整个上传一次。
- **一个数据库同一时刻只有一个进程在复制。** 第一个还在跑时，同一个文件上的第二个 `New` 会失败。
- **磁盘：**暂存目录放着还没上传的变更，最多 `StagingLimit`，外加最近一份整库。
- **CPU：**复制每次提交，会在写入方这边多花一点。数字见[基准测试](benchmarks.zh-CN.md#复制)。

## 副本落后多少

存储正常时，副本最多比最后一次提交晚 `SyncInterval` 加一次上传。`r.Sync(ctx)` 会把到现在为止提交的内容复制并上传，用在某次写入必须先进存储才能往下走的时候。`r.Close()` 复制剩下的内容、做检查点，再花最多 10 秒上传。传不完的留在暂存目录，下次 `New` 接着传。

`r.Status()` 告诉你复制到哪了：最后暂存的 TXID、存储里最后的 TXID、等待上传的字节数，和最近一次上传的错误。TXID 是已提交写事务的编号，和 Litestream 的算法一样。

## 存储断开时

写入照常，变更在暂存目录里累积。上传失败后会重试，每次间隔变长，最长一分钟。超过 `StagingLimit` 后暂存停下，`OnError` 收到一个属于 `replica.ErrStagingFull` 的错误。检查点照做，所以 WAL 不会无限长。等存储恢复、暂存的内容传完，会跟着上传一份整库，暂存从它接着往下。暂存停下期间的那些变更，没有单独的时间点可以恢复。如果存储断开期间机器也丢了，没传上去的就跟着丢了：存储连不上的时候，任何设计都保不住它们。

## 恢复

```go
txid, err := replica.Restore(ctx, "app.db", replica.Config{URL: url}, replica.RestoreOptions{})
```

`RestoreOptions` 选恢复到哪个状态：最新（默认）、某个 `TXID`，或者某个时间之前（`At`）。恢复会下载合适的最新快照和它之后的文件，逐个核对（文件本身的校验和，以及应用前后数据库的校验和），对结果跑 `PRAGMA integrity_check`，都通过了才把数据库放到位。它从不覆盖已有的数据库，也不会写在别的数据库的 WAL、日志或暂存目录旁边。

恢复会把副本的最后一个文件留在暂存目录，让 `New` 从副本接着复制。恢复到最新状态后，`New` 直接接上，不用传整库。恢复到更早的状态后，数据库和副本已经分叉，`New` 会返回 `replica.ErrDiverged`，除非设了 `Config.FreshStart`，那样会把整库作为副本的下一个 TXID 上传。

## 边界

### 一个副本只能有一个复制器

一个副本只能有一个复制器往里写。

- **同一台机器上有硬保证。** 第二个进程的 `New` 会报错。
- **跨机器要靠你的程序保证。** S3 没有「只让持有租约的一方写」的机制，所以没有哪个复制器能在 S3 上保证这一点，Litestream 也一样。
- **复制器能发现的。** 每个文件只在键还空着时才上传，所以同一段历史出来的第二个复制器在第一次上传时就被拒绝，以 `replica.ErrConflict` 停下。复制器在上传前，如果超过 5 分钟没核对过，还会核对副本是不是还停在它离开的地方。这两项加起来，能在正常运行时和某一方连不上存储之后发现第二个复制器。
- **可能漏掉的。** 某些竞争，比如新机器接管时旧机器还在跑，而新机器恰好在不巧的时刻压缩并删掉了旧机器要撞上的文件。这时副本里可能混进两段历史。恢复会核对每个校验和，所以不会拼出一个混合的库，但可能报错，也可能恢复成旧机器那一段。
- **所以数据库搬到另一台机器时：**用你自己的锁或租约先停掉旧复制器，再启动新的，并设 `Config.SingleWriter`。

`New` 会检查存储是否拒绝同一个键的第二次写入（`If-None-Match`）。不支持的存储会以 `replica.ErrNoConditionalWrites` 被拒绝，除非用 `SingleWriter` 声明你的程序自己保证只有一个写入方。

### 按时间恢复

`At` 按文件头里的时间选文件，这个时间来自暂存它的那台机器的时钟。一个文件最多装一个 `SyncInterval` 内的提交。恢复会核对每一层里第一个被跳过的文件，之后的文件直接信它的时间，`litestream restore` 也是这样。如果那台机器的时钟往回拨过，按时间恢复的结果可能比要的时间早一点或晚一点。按 TXID 恢复和恢复到最新状态不用时间。

### 没有做的

- 没有客户端加密：加了 `litestream restore` 就读不了。数据走 HTTPS，静态加密用存储桶自己的。
- 没有只读副本，不支持别的存储类型，没有命令行工具。
- 复制是异步的：提交在到达存储之前就返回了。

## 存储里有什么

`s3://bucket/path` 的副本是 `path/` 下的 LTX 文件：第 0 层每次上传一个文件，第 1 层每分钟合并一次第 0 层，第 2 层每小时合并一次第 1 层，第 9 层放快照。第 0 层的文件在被第 1 层收进去 30 分钟后删除。超过 `Retention` 的快照会删掉（最新的一份总是留着），只有它们用得到的文件也一起删。空闲的数据库在这些工作做完后不再发请求。繁忙的数据库大约每个 `SyncInterval` 最多上传一次，外加合并。

`litestream restore` v0.5.18 能从它恢复出同样的数据库，每个 TXID 和每个时间点都一样，测试会核对这一点（[testing.md](testing.md#the-s3-client-and-the-replica)，英文）。

## 存储

复制器在 MinIO 上测试过。AWS S3 和 Cloudflare R2 支持它要的条件写入。任何讲 S3 API、用 SigV4 签名、PUT 支持 `If-None-Match: *` 的存储都一样能用。不支持的存储，设 `SingleWriter`，自己保证只有一个复制器在写。
