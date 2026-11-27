---
summary: 'Why the WAL file of a database can grow large, what the driver does about it, when it still grows, and how to keep it small'
read_when:
  - The -wal file beside a database is large, or keeps growing
  - A program holds reads open for long (reports, exports, rows not closed)
  - Asked whether the driver keeps the WAL small
  - Choosing journal_size_limit or _wal_limit
audience: users
---

# A WAL that grows

English · [简体中文](wal-growth.zh-CN.md)

In WAL mode SQLite writes each change to the `-wal` file beside the database first, and copies it into the database later (a checkpoint). The WAL can start over from its beginning only when every change in it is copied and no reader still reads it. The driver keeps the WAL small while your program writes. It does not keep it small when reads never stop: a read that stays open lets it grow without end, with this driver as with any SQLite. This page says what the driver does, what it cannot stop, and what your program can do.

## What the driver does

- **Writes alone do not make it grow.** The driver checkpoints while it holds the write lock, each time the WAL has grown by `wal_autocheckpoint` pages (1000 by default), so with no reader on the WAL it starts over at that length however many goroutines write. A single transaction larger than that still writes all its pages to the WAL first. With 64 goroutines inserting for 30 s on linux/amd64 it stayed at 4 MiB, where the other drivers' grew to 3.40 to 5.49 GiB ([benchmarks](benchmarks.md#many-writers-at-once)).
- **Past 64 MiB it waits for readers.** Once the WAL is longer than `_wal_limit` (64 MiB by default), the connection that gives the write lock back waits up to 100 ms for the readers of the WAL to end, and the next writer starts it over. When they do not end in time, it tries again once the WAL has grown by another quarter of the limit. The details are in [architecture.md](architecture.md#a-limit-on-the-length-of-the-wal).

## When it still grows

- **A read that does not end.** Rows not read to the end and not closed keep a read open, and so does a transaction that has read and is never committed or rolled back. On an Apple M4, one query left open while 100,000 rows were inserted let the WAL grow to 595 MiB. No limit stops this, in this driver or in SQLite.
- **Many short reads that overlap without a gap.** SQLite has four slots for the readers of the WAL. When short read transactions keep all four taken, the 100 ms wait does not find them free. On an Apple M4, 64 goroutines that each read first and then write in a deferred transaction held off 1 to 5 of 13 to 22 tries in 10 s, and the WAL reached 80 to 96 MiB. In the benchmarks the same kind of load left a WAL of 144 MiB on linux/amd64 and 199 to 272 MiB on Windows, where modernc's reached 89 to 132 MiB ([benchmarks](benchmarks.md#on-windows)).
- **The file keeps its size.** When the WAL starts over, SQLite writes it again from the beginning but does not make the file smaller. Without `journal_size_limit`, a WAL that once reached 595 MiB stays 595 MiB on disk until a `TRUNCATE` checkpoint, or the last connection closes.

## How to keep it small

1. **Close what you read.** Read rows to the end or call `rows.Close()` (with `defer` right after the query), and end every transaction with `Commit` or `Rollback`.
2. **Keep reads short.** Read a report or an export in pages, each page a query of its own (`WHERE id > ? ORDER BY id LIMIT 1000`), rather than one query or one transaction that runs for minutes. Each page then sees the database as it is when that page runs. When the export must be one consistent snapshot, it has to be one read, and the WAL grows while it lasts.
3. **Read with single statements where you can.** A read transaction holds its reader slot for every statement in it. On an Apple M4, sixty goroutines reading with single statements beside four writers held off none of 8 tries to start the WAL over.
4. **Open with `_txlock=immediate`.** A transaction that will write then waits for the write lock before it reads, so 64 such transactions do not hold reader slots while they wait. On linux/amd64 the WAL stayed at 4 MiB with `_txlock=immediate` and reached 144 MiB with deferred transactions.
5. **Give the disk space back with `journal_size_limit`.** With `_pragma=journal_size_limit(67108864)` in the DSN, the first commit after the WAL starts over cuts the file down to 64 MiB, or to what that commit needs when it needs more. In the run with the query left open and a limit of 4 MiB, the file was 4 MiB once the query was closed and writes went on, against 598 MiB without the limit.
6. **A smaller `_wal_limit` tries sooner.** During a try the writers wait for it: up to 100 ms for the readers, then the copy into the database and its sync. Keep `busy_timeout` well above that (the driver's default is 5 s). `_wal_limit=0` turns the tries off.
7. **Watch the size of the `-wal` file.** Look at whether it keeps getting larger while the program writes, not at whether it gets smaller: without `journal_size_limit` it never does. One that keeps growing past `_wal_limit` usually means a read that does not end.

A database that is replicated has its checkpoints run by its replicator, each 4000 frames by default, with the same wait for readers past `_wal_limit`. It refuses `PRAGMA wal_checkpoint` from your program ([replication.md](replication.md)).
