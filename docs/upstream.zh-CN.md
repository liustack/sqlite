# 上游

[English](upstream.md) · 简体中文

| 代码 | 来源 | 版本 | 由谁维护 |
| --- | --- | --- | --- |
| `engine/sqlite3.c`、`engine/sqlite3.h` | SQLite amalgamation，https://sqlite.org/download.html | 3.54.1 | `engine/vendor.sh` 和 `engine/SQLITE.sha256` |
| `engine/libc/string/*.c`（`memcpy.c` 除外）、`engine/libc/COPYRIGHT` | musl，https://git.musl-libc.org/cgit/musl | 提交 `7ada6dd`（2024-03-02） | `engine/libc/vendor.sh` 和 `engine/libc/MUSL.sha256` |
| `internal/goasm/gen/`、`engine/libc/` 的头文件、`engine/libc/string/memcpy.c`、`scripts/docs-list.sh` | github.com/liustack/slowjs | 提交 `2445c180` | 复制过来一次，之后在本仓库里改 |

## 升级 SQLite

1. 从 https://sqlite.org/download.html 取 amalgamation zip 的 `PRODUCT` 那一行，里面有版本号、路径（带年份）和 SHA3-256。
2. 把它们填进 `engine/vendor.sh` 开头的 `version`、`year` 和 `zip_sha3`，然后运行它。脚本会下载 zip、校验、复制 `sqlite3.c` 和 `sqlite3.h`，并写出 `engine/SQLITE.sha256`。
3. 看发布说明里有没有编译选项的变化，对照检查 `engine/sqlite.c`。
4. 运行 `make gen` 和测试，把 C 文件、清单和生成的文件放进同一个提交。

## 新增 libc 函数

升级之后，SQLite 可能会调用一个新的 libc 函数。从同一个提交的 musl 里取它：加进 `engine/libc/vendor.sh` 的列表，用那个提交的 musl 检出运行脚本，再在 C 标准规定的那个头文件里声明它（比如 `memchr` 放在 `string.h`）。
