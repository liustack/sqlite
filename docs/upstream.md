---
summary: 'Where the code comes from (SQLite, musl, the generator of slowjs), at which versions, and how to move to a new SQLite release'
read_when:
  - Moving to a new SQLite release
  - Adding a function to engine/libc
  - Asked which version of SQLite the driver has
---

# Upstream

English · [简体中文](upstream.zh-CN.md)

| Code | From | Version | Kept by |
| --- | --- | --- | --- |
| `engine/sqlite3.c`, `engine/sqlite3.h` | The SQLite amalgamation, https://sqlite.org/download.html | 3.54.1 | `engine/vendor.sh` and `engine/SQLITE.sha256` |
| `engine/libc/string/*.c` except `memcpy.c`, `engine/libc/COPYRIGHT` | musl, https://git.musl-libc.org/cgit/musl | commit `7ada6dd` (2024-03-02) | `engine/libc/vendor.sh` and `engine/libc/MUSL.sha256` |
| `internal/goasm/gen/`, the headers of `engine/libc/`, `engine/libc/string/memcpy.c`, `scripts/docs-list.sh` | github.com/liustack/slowjs | commit `2445c180` | Copied once, then changed here |

## A new SQLite release

1. Take the `PRODUCT` line of the amalgamation zip from https://sqlite.org/download.html. It gives the version, the path (with the year) and the SHA3-256.
2. Put them in `version`, `year` and `zip_sha3` at the top of `engine/vendor.sh`, and run it. It downloads the zip, checks it, copies `sqlite3.c` and `sqlite3.h` and writes `engine/SQLITE.sha256`.
3. Read the release notes for changed compile options, and check `engine/sqlite.c`.
4. Run `make gen` and the tests, and commit the C, the manifest and the generated files together.

## A new libc function

SQLite may call a new libc function after an upgrade. Take it from musl at the same commit: add it to the list in `engine/libc/vendor.sh`, run the script with a musl checkout at that commit, and declare the function in the header where C has it.
