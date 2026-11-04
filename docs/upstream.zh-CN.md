# 上游

[English](upstream.md) · 简体中文

| 代码 | 来源 | 版本 | 由谁维护 |
| --- | --- | --- | --- |
| `engine/sqlite3.c`、`engine/sqlite3.h` | SQLite amalgamation，https://sqlite.org/download.html | 3.54.1 | `engine/vendor.sh` 和 `engine/SQLITE.sha256` |
| `engine/libc/string/*.c`（`memcpy.c` 除外）、`engine/libc/math/*`、`engine/libc/internal/libm.h`（打上 `engine/libc/patches/libm.h.patch`）、`engine/libc/COPYRIGHT` | musl，https://git.musl-libc.org/cgit/musl | 提交 `7ada6dd`（2024-03-02） | `engine/libc/vendor.sh` 和 `engine/libc/MUSL.sha256` |
| `internal/goasm/gen/`、`engine/libc/` 的头文件、`engine/libc/string/memcpy.c`、`engine/libc/math.c`、`engine/libc/patches/libm.h.patch`、`scripts/docs-list.sh` | github.com/liustack/slowjs | 提交 `2445c180` | 复制过来一次，之后在本仓库里改 |

## 升级 SQLite

1. 从 https://sqlite.org/download.html 取 amalgamation zip 的 `PRODUCT` 那一行，里面有版本号、路径（带年份）和 SHA3-256。
2. 把它们填进 `engine/vendor.sh` 开头的 `version`、`year` 和 `zip_sha3`，然后运行它。脚本会下载 zip、校验、复制 `sqlite3.c` 和 `sqlite3.h`，并写出 `engine/SQLITE.sha256`。
3. 看发布说明里有没有编译选项的变化，对照检查 `engine/sqlite.c`。`engine/os.c` 用到 SQLite 的三处内部实现。`sqlite3OomFault`：检查它是否仍然设置 `mallocFailed`，语句是否仍然因此中止。`db->nVTrans`：检查它是否仍然是当前事务里虚表的个数。`db->pParse`、`pToplevel` 和 `pOuterParse`（goNested）：检查触发器的语句体是否仍然通过 `pToplevel` 链到所属语句，在准备另一条语句时准备的语句是否仍然通过 `pOuterParse` 链到它。
4. 运行 `make gen` 和测试，把 C 文件、清单和生成的文件放进同一个提交。

## 新增 libc 函数

升级之后，SQLite 可能会调用一个新的 libc 函数。从同一个提交的 musl 里取它：加进 `engine/libc/vendor.sh` 的列表，用那个提交的 musl 检出运行脚本，再在 C 标准规定的那个头文件里声明它（比如 `memchr` 放在 `string.h`）。

## 升级 Go

`internal/csqlite` 的 Go 汇编是用 Go 1.27.2 生成的，用 Go 1.26 和 1.27 验证过。`go.mod` 要求 1.26.6 或更新。更新的 Go 会被 `internal/goversion` 的报错拦下构建（它的 `newer.go` 的构建行是 `go1.28 && !sqlite_anygo`），因为汇编从没在那个版本上验证过。加 `-tags sqlite_anygo` 可以照样构建。汇编依赖 Go 里不在 Go 1 兼容承诺之内的东西：

- 每个非 NOSPLIT 函数开头的栈检查：栈指针和 `g.stackguard0`（`g` 的偏移 16，`g` 在 amd64 的 R14、arm64 的 X28）比较，再调用 `runtime.morestack_noctxt.abi0`（`internal/goasm/gen/verify_insn.go`、`arm64_verify.go`）。
- 链接器允许一串 NOSPLIT 函数用的栈空间，减去出错时为 `runtime.sigpanic` 压进去的部分（`internal/goasm/gen/nosplit.go`）。

升级到更新的 Go 时，用新版本并加上 `-tags sqlite_anygo`：

1. 运行 `make check`、`make check-gen`（它的链接后检查会在新 Go 链接出的二进制里，逐个核对生成函数是上面的样子）和 `make test-gen`。
2. 有失败就去读新版本源码里的栈检查和 `runtime.sigpanic`，生成器只按读到的改。
3. 全部通过后，把 `internal/goversion/newer.go` 的构建行改到下一个版本，把里面的名字改成验证过的版本，再在这里和 README 的两种语言里写上新的版本范围。`TestNewerGoStops` 会检查构建行和名字是否一致。

最低是 Go 1.26.6，即 `go.mod` 的 `go` 行。1.26.6 之前，goroutine 的栈被复制时，被 Go 注入了调用的函数（比如出错的生成函数被注入 `sigpanic`），它的栈帧会留着指向旧栈的帧指针，执行跟踪器和 block、mutex 两种 profile 可能顺着它读（`runtime/stack.go` 的 `adjustframe`）。Go 1.26 的 `database/sql` 没有 `RowsColumnScanner`，所以 `Scan` 通过 `Next` 读一行，分配的内存更多（`scan_go127.go` 只在 Go 1.27 上编译）。结果一样。

`scripts/go-versions.sh` 用最低版本，以及到 `internal/goversion` 拦下的那个版本为止每个小版本的最新补丁版，各跑一遍测试。每周的 `go-latest` 工作流在每个平台上跑它，还会用最新的稳定版 Go 加 `sqlite_anygo` 跑测试，提前发现会弄坏汇编的 Go 版本。
