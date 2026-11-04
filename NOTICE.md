# Notices

This repository holds code from several sources, each under its own license. This file says which license covers which path. It is not legal advice.

| Code | Written by | License | License file |
| --- | --- | --- | --- |
| Everything not named below | The liustack/sqlite Authors ([AUTHORS](AUTHORS)) | MIT | [LICENSE](LICENSE) |
| `engine/sqlite3.c`, `engine/sqlite3.h`: the SQLite amalgamation, unchanged | D. Richard Hipp and the SQLite developers | Public domain | The notice at the top of each file, and https://sqlite.org/copyright.html |
| `engine/libc/string/*.c` except `memcpy.c`, `engine/libc/math/*`, `engine/libc/internal/libm.h` and `engine/libc/COPYRIGHT`: musl at commit `7ada6dd` (2024-03-02), unchanged except `libm.h` (`engine/libc/patches/libm.h.patch`) | Rich Felker and others (musl) | MIT | [engine/libc/COPYRIGHT](engine/libc/COPYRIGHT) |
