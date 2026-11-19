# 上游

[English](upstream.md) · 简体中文

| 代码 | 来源 | 版本 | 由谁维护 |
| --- | --- | --- | --- |
| `engine/sqlite3.c`、`engine/sqlite3.h` | SQLite amalgamation，https://sqlite.org/download.html | 3.54.1 | `engine/vendor.sh` 和 `engine/SQLITE.sha256` |
| `engine/libc/string/*.c`（`memcpy.c` 除外）、`engine/libc/math/*`、`engine/libc/internal/libm.h`、`engine/libc/COPYRIGHT`，打上 `engine/libc/patches/` 里的补丁（`libm.h`，以及为了速度改的 `memcmp.c` 和 `strcspn.c`） | musl，https://git.musl-libc.org/cgit/musl | 提交 `7ada6dd`（2024-03-02） | `engine/libc/vendor.sh` 和 `engine/libc/MUSL.sha256` |
| `internal/goasm/gen/`、`engine/libc/` 的头文件、`engine/libc/string/memcpy.c`、`engine/libc/math.c`、`engine/libc/patches/libm.h.patch`、`scripts/docs-list.sh` | github.com/liustack/slowjs | 提交 `2445c180` | 复制过来一次，之后在本仓库里改 |

## 升级 SQLite

1. 从 https://sqlite.org/download.html 取 amalgamation zip 的 `PRODUCT` 那一行，里面有版本号、路径（带年份）和 SHA3-256。
2. 把它们填进 `engine/vendor.sh` 开头的 `version`、`year` 和 `zip_sha3`，然后运行它。脚本会下载 zip、校验、复制 `sqlite3.c` 和 `sqlite3.h`，并写出 `engine/SQLITE.sha256`。再把同一版本源码 zip（`sqlite-src-*.zip`）的 SHA3-256 填进 `scripts/fuzzdata.go` 的 `srcSHA3`，运行它取这个版本的 fuzz 用例（见 [testing.md](testing.md#sqlites-fuzz-cases)）。
3. 看发布说明里有没有编译选项的变化，对照检查 `engine/sqlite.c`。`engine/os.c` 用到 SQLite 的三处内部实现。`sqlite3OomFault`：检查它是否仍然设置 `mallocFailed`，语句是否仍然因此中止。`db->nVTrans`：检查它是否仍然是当前事务里虚表的个数。`db->pParse`、`pToplevel` 和 `pOuterParse`（goNested）：检查触发器的语句体是否仍然通过 `pToplevel` 链到所属语句，在准备另一条语句时准备的语句是否仍然通过 `pOuterParse` 链到它。
4. 重新训练 profile（`engine/pgo/train.sh`，见 [profile](#profile)）。
5. 运行 `make gen` 和测试，把 C 文件、清单、profile 和生成的文件放进同一个提交。

### profile

`make gen` 编译 C 时带着 `engine/pgo/profile.txt`（[architecture.zh-CN.md](architecture.zh-CN.md#profile)）。profile 只记每个分支走了多少次，所以旧的 profile 不会编出错的代码：最坏也只是代码按已经不准的次数来排布。profile 里每个函数带着它的分支和循环结构的哈希，结构变了的函数编译时不用它的 profile。只改结构里面的东西（一个常量、一个表达式）时 profile 照用。`make gen` 会打印 profile 对不上的函数有多少（`profile engine/pgo/profile.txt: 3393 functions, 0 it no longer fits ...`），超过 30 个，或者其中有执行每条语句的那个循环 `sqlite3VdbeExec` 时，会给出警告。

这些时候要重新训练（`engine/pgo/train.sh`，再给两个目标各跑一次 `make gen`）：每次升级 SQLite，改了 `engine/sqlite.c` 的编译选项或 `engine/os.c`，以及 `make gen` 给出上面的警告时。改 `engine/libc` 只影响改到的那个函数。升级之后训练的检查没过，见 [engine/pgo/README.md](../engine/pgo/README.md#the-check)。

## 新增 libc 函数

升级之后，SQLite 可能会调用一个新的 libc 函数。从同一个提交的 musl 里取它：加进 `engine/libc/vendor.sh` 的列表，用那个提交的 musl 检出运行脚本，再在 C 标准规定的那个头文件里声明它（比如 `memchr` 放在 `string.h`）。

## 升级 Go

`internal/csqlite` 的 Go 汇编是用 Go 1.27.2 生成的，用 Go 1.26 和 1.27 验证过。`go.mod` 要求 1.26.6 或更新。更新的 Go 会被 `internal/goversion` 的报错拦下构建（它的 `newer.go` 的构建行是 `go1.28 && !sqlite_anygo`），因为汇编从没在那个版本上验证过。加 `-tags sqlite_anygo` 可以照样构建。汇编依赖 Go 里不在 Go 1 兼容承诺之内的东西：

- 每个非 NOSPLIT 函数开头的栈检查：栈指针和 `g.stackguard0`（`g` 的偏移 16，`g` 在 amd64 的 R14、arm64 的 X28）比较，再调用 `runtime.morestack_noctxt.abi0`（`internal/goasm/gen/verify_insn.go`、`arm64_verify.go`）。
- Go 汇编器自己会把哪些叶子函数标成 NOSPLIT：不调用任何函数、栈帧小于 128 字节的函数。生成器把这份名单写下来，汇编器标了别的函数时，链接后检查会失败（`internal/goasm/gen/nosplit.go`）。

每个会调用别人的函数都有栈检查。没有栈检查的只有叶子函数：用寄存器接参数的 `memcpy`、`memmove`、`memset`，以及汇编器自己标的那些。这种叶子函数里出错时，用的是调用方的栈检查留下的空间，和任何 Go 程序的叶子函数出错时一样，所以生成器不再依赖运行时的某个数字。

升级到更新的 Go 时，用新版本：

1. 关掉门禁跑测试，再跑 `make check-gen`（它的链接后检查用 `sqlite_anygo` 构建，在新 Go 链接出的二进制里逐个核对生成函数是上面的样子）和 `make test-gen`。这里不能用 `make check`，因为命令行上的 `-tags` 会取代 `GOFLAGS` 里的。

   ```sh
   go vet -asmdecl=false -tags sqlite_anygo ./...
   go test -count=1 -tags sqlite_anygo ./...
   go test -race -count=1 -tags sqlite_anygo . ./internal/vfs ./internal/csqlite
   make check-gen test-gen
   ```

2. 有失败就去读新版本源码里的栈检查和汇编器的 NOSPLIT 规则，生成器只按读到的改。
3. 全部通过后，把 `internal/goversion/newer.go` 的构建行改到下一个版本，把里面的名字改成验证过的版本，再在这里、README（两种语言）、AGENTS.md 和 `.github/workflows/test.yml` 的 `go-version` 里写上新的版本范围。`TestNewerGoStops` 会检查构建行和名字是否一致。

最低是 Go 1.26.6，即 `go.mod` 的 `go` 行。1.26.6 之前，goroutine 的栈被复制时，被 Go 注入了调用的函数（比如出错的生成函数被注入 `sigpanic`），它的栈帧会留着指向旧栈的帧指针，执行跟踪器和 block、mutex 两种 profile 可能顺着它读（`runtime/stack.go` 的 `adjustframe`）。Go 1.26 的 `database/sql` 没有 `RowsColumnScanner`，所以 `Scan` 通过 `Next` 读一行，分配的内存更多（`scan_go127.go` 只在 Go 1.27 上编译）。结果一样。

`scripts/go-versions.sh` 用最低版本，以及到 `internal/goversion` 拦下的那个版本为止每个小版本的最新补丁版，各跑一遍测试。每周的 `go-latest` 工作流在每个平台上跑它，还会用最新的稳定版 Go 加 `sqlite_anygo` 跑测试，提前发现会弄坏汇编、并且测试看得出来的 Go 版本。
