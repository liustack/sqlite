<h1 align="center">sqlite</h1>

<p align="center"><b>最快的 cgo-free SQLite 驱动，给 Go 的 <code>database/sql</code> 用。<br><code>CGO_ENABLED=0</code> 就能构建，大多数场景下连 cgo 驱动也跑不过它。</b></p>

<p align="center">
  <a href="README.md">English</a> · 简体中文 ·
  <a href="docs/benchmarks.zh-CN.md">基准测试</a> ·
  <a href="docs/architecture.zh-CN.md">工作原理</a>
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/liustack/sqlite"><img src="https://pkg.go.dev/badge/github.com/liustack/sqlite.svg" alt="Go Reference"></a>
  <a href="https://github.com/liustack/sqlite/actions/workflows/test.yml"><img src="https://github.com/liustack/sqlite/actions/workflows/test.yml/badge.svg?branch=main" alt="test"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="License: MIT"></a>
  <a href="https://x.com/liustack"><img src="https://img.shields.io/badge/follow-%40liustack-black?style=flat-square&logo=x&logoColor=white" alt="Follow @liustack on X"></a>
</p>

给数据放在 SQLite 里、又想保持纯 Go 的 Go 程序用：一个静态二进制，在哪台机器上都能交叉编译，构建和容器里都不需要 C 编译器。以前这样做要牺牲速度，现在落在后面的反而是 cgo 驱动。

## 基准测试

每次操作的耗时，越低越好，取 6 轮的中位数。每行最快的加粗。全部用 WAL、`synchronous=NORMAL`，每个驱动的设置都核对过一致。

**linux/amd64**（AMD EPYC 7B13，Go 1.27.2）：

| 场景 | 本驱动 | [mattn](https://github.com/mattn/go-sqlite3)（cgo） | [modernc](https://gitlab.com/cznic/sqlite) | [ncruces](https://github.com/ncruces/go-sqlite3) |
| :-- | --: | --: | --: | --: |
| 插入一行 | **18.4 µs** | 19.7 µs | 23.5 µs | 21.5 µs |
| 一个事务插入 1000 行 | **1.25 ms** | 1.48 ms | 1.97 ms | 1.72 ms |
| 按主键读一行 | **2.20 µs** | 5.87 µs | 7.36 µs | 5.84 µs |
| 读 10 万行 | **34.8 ms** | 76.9 ms | 74.2 ms | 49.4 ms |
| 连表加分组 | 2.90 ms | **2.73 ms** | 4.50 ms | 4.43 ms |
| 按主键读，8 线程 | **1.47 µs** | 2.20 µs | 2.33 µs | 2.04 µs |
| 8 个 goroutine 同时插入 | **9.74 µs** | 19.1 µs | 25.7 µs | 22.0 µs |
| 64 个 goroutine 同时插入 | **2.97 µs** | 19.6 µs | 27.2 µs | 26.9 µs |

**darwin/arm64**（Apple M4，Go 1.27.2，测的时候机器上还有别的任务，30% 以内的差别当噪声看）：

| 场景 | 本驱动 | mattn（cgo） | modernc | ncruces |
| :-- | --: | --: | --: | --: |
| 插入一行 | **8.92 µs** | 9.16 µs | 11.0 µs | 9.27 µs |
| 一个事务插入 1000 行 | 723 µs | 837 µs | 915 µs | **634 µs** |
| 按主键读一行 | **0.81 µs** | 1.76 µs | 2.50 µs | 1.71 µs |
| 读 10 万行 | **15.3 ms** | 33.7 ms | 32.4 ms | 20.5 ms |
| 连表加分组 | 1.48 ms | **1.23 ms** | 3.00 ms | 1.98 ms |
| 按主键读，10 线程 | **1.25 µs** | 1.90 µs | 2.02 µs | 2.22 µs |
| 8 个 goroutine 同时插入 | **3.45 µs** | 9.03 µs | 10.5 µs | 48.4 µs |
| 64 个 goroutine 同时插入 | **1.42 µs** | 16.8 µs | 13.4 ms | 80.0 µs |

cgo 驱动还赢的，是全在 SQLite 内部算的活（连表）：本驱动跑的是同一份 C，只是变成了 Go 汇编。读和多写为什么更快、各驱动的版本、怎么自己再跑一遍，见 [docs/benchmarks.zh-CN.md](docs/benchmarks.zh-CN.md)。

## 安装

```sh
go get github.com/liustack/sqlite
```

需要 Go 1.27 或更新。支持 linux 和 darwin，amd64 和 arm64。

## 使用

```go
import (
	"database/sql"

	_ "github.com/liustack/sqlite"
)

db, err := sql.Open("sqlite", "file:app.db?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
```

名字可以是文件名、`:memory:` 或 [SQLite URI](https://sqlite.org/uri.html)。`_pragma=name(value)` 在每个新连接上执行一条 PRAGMA，`_txlock=immediate` 让 `BEGIN` 马上拿写锁。没有用 `busy_timeout` 另设的话，连接等锁最多 5 秒。其余见 [pkg.go.dev](https://pkg.go.dev/github.com/liustack/sqlite)。

驱动注册的名字是 `sqlite`，和 modernc.org/sqlite、glebarez/go-sqlite 一样，所以同时导入其中一个的程序启动时会 panic。这时用 `sql.OpenDB(sqlite.NewConnector(dsn))` 打开数据库。

## 对你有什么不同

- **全程没有 C。** 在 Mac 上 `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build` 就行，出来的二进制能直接跑在 `scratch` 镜像里。
- **多个写入方不再互相等。** 几个 goroutine 同时写时，驱动把它们自动提交的 `INSERT`、`UPDATE`、`DELETE` 放进一个事务，只提交一次。每次调用照样拿到自己的错误、`LastInsertId` 和 `RowsAffected`，并且提交完才返回，持久性一点不打折。你自己开的事务从不合并。[原理](docs/architecture.zh-CN.md#合并提交)
- **写入方排队，不再动不动报 `database is locked`。** 驱动里同一个文件同一时刻只有你程序的一个连接在写，其余的排队等（最多等 `busy_timeout`），不在 SQLite 的忙等里睡。先读后写的事务，如果中间有别人写过，还是会失败，这种事务用 `_txlock=immediate` 开。别的进程照样遵守 SQLite 自己的锁。
- **读更省。** 调 SQLite 就是一次普通的 Go 函数调用，把数字读进你的变量不分配内存（字符串只分配它自己那一次）。

## 凭什么信它

- **稳定和安全第一，速度第二，内存第三。** 更快但可能丢写入、返回错结果或削弱提交的改动，一律不做。合并提交给每条合并的语句都包一个 SAVEPOINT，就是这个原因，哪怕它拖慢速度。
- **C 是 SQLite 原版，一个字没改。** `engine/` 放的是 3.54.1 发布版的合并源码，按哈希核对（`engine/vendor.sh --verify`）。编译选项集中在一个文件里，每条都写了理由。
- **Go 汇编是生成的，从不手写。** LLVM 编译 C，生成器把 LLVM 的输出改写成 Go 汇编。遇到没有规则的东西就停下，还会核对链接后的二进制。仓库里的汇编和生成器按 `engine/` 现在生成的不一致时，`make check-gen` 会失败。
- **数字里留着输掉的地方。** 上面的表保留了别的驱动更快的行，[docs/benchmarks.zh-CN.md](docs/benchmarks.zh-CN.md) 写明了每次怎么测的、测的时候机器上还跑着什么。
- **每次推送都测**，linux/amd64、linux/arm64、darwin/arm64，开着竞态检测。

## 还没有的

- 还没打版本标签，`go get` 拿的是 `main` 上最新的提交。
- 暂时没编进来：FTS5、R*Tree、数学函数、`localtime`、可加载扩展。
- 没有共享缓存：`cache=shared` 会报错。想让池里所有连接看到同一个内存数据库，用 `file:/name?vfs=memdb`。
- 双引号只表示标识符（`SQLITE_DQS=0`，SQLite 官方的建议）：字符串字面量请用单引号。
- SQLite 用过的内存留着复用，进程运行期间不还给系统。
- windows 和其他平台构建不了。

## 文档

| 文档 | 什么时候读 |
| :-- | :-- |
| [基准测试](docs/benchmarks.zh-CN.md) | 想看完整数据，或者在自己机器上跑一遍 |
| [架构](docs/architecture.zh-CN.md) | 想知道 C 怎么变成 Go 汇编，或者合并提交怎么工作 |
| [上游](docs/upstream.zh-CN.md) | 想知道用的是哪个 SQLite，或者怎么升级到新版 |
| [Testing](docs/testing.md)、[Commits](docs/commit.md)（英文） | 准备改代码 |

## 致谢

感谢 D. Richard Hipp 和 SQLite 的开发者写了 SQLite，感谢 musl 提供引擎链接的字符串函数。感谢 mattn/go-sqlite3、modernc.org/sqlite、ncruces/go-sqlite3 的作者，他们探出了 SQLite 在 Go 里的几条路，本驱动也是拿它们来比的。

## 许可

本仓库的代码用 MIT 许可。SQLite 属于公有领域，musl 的代码保留它的 MIT 许可。哪个路径归哪个许可，见 [NOTICE.md](NOTICE.md)。
