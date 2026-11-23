---
summary: What the driver promises a program, the order it chooses in when two goals conflict, what it will not do even when it would be faster, and how a change is judged against that
read_when:
  - Deciding whether to run SQLite on a server with this driver
  - Proposing a change that makes the driver faster or smaller
  - Asked why the driver does not do something another driver does (yield to readers, wait to fill a batch, run C on its own stack)
audience: users
---

# Principles

English · [简体中文](principles.zh-CN.md)

## What it is for

SQLite has one writer, and that does not change. What changes is who waits: the writers of a program queue in Go, in the order they came, so a write waits for the writes ahead of it and never gets `database is locked` from its neighbours. In apps and on devices, where one user writes at a time, SQLite is everywhere. On a server many requests write at once, and that error is why many Go developers keep SQLite off their servers. Here concurrent autocommit writes also share one commit, so many writers are faster than one. A program can write from many goroutines at once, keep its data in one file next to one static Go binary, and stay plain Go.

## The order

When two goals conflict, the order is:

1. **Stable and safe.** No lost write, no wrong result, no weaker commit, no new way to get `database is locked` or `SQLITE_BUSY`.
2. **Fast.**
3. **Small in memory.**

A change that is faster but costs anything in the first line is not taken. A change that saves memory but costs speed or safety is not taken. Being conservative is the intended design, not a first version waiting to be loosened.

## What a program can count on

- **A call returns after its commit.** Group commit does not trade durability for speed: a merged write is as durable as one run alone, at the `synchronous` level the program chose.
- **A merged write gives what it would give alone.** Each one runs in its own SAVEPOINT, gets its own LastInsertId and RowsAffected, and one failure does not fail the others. The only exceptions are `changes()` and `total_changes()` ([group-commit.md](group-commit.md)).
- **A write that ran never runs again.** When a failure ends a whole batch, the writes that had run get `SQLITE_ABORT_ROLLBACK`, and only those that never reached SQLite go back to the queue.
- **Writers of one program do not get `database is locked` from each other** when a transaction says it will write (`_txlock=immediate`, or a first statement that writes). They wait in the order they came. The measured counts are in [benchmarks.md](benchmarks.md#many-writers-at-once).
- **Concurrent writes do not make the WAL grow.** The driver checkpoints while it holds the write lock, so the WAL starts over every 1000 pages however many goroutines write, and the database opens fast after a crash ([architecture.md](architecture.md#automatic-checkpoints)). When reads keep it from starting over, from 64 MiB on the driver waits up to 100 ms at a time for them to let go. A read that never ends still lets it grow, as in SQLite ([architecture.md](architecture.md#a-limit-on-the-length-of-the-wal)).
- **No C compiler, anywhere.** SQLite's own C, unchanged, is compiled to Go assembly that is checked in. `CGO_ENABLED=0` builds it for linux, darwin and windows, amd64 and arm64.

## What it cannot promise

- A deferred transaction that reads first and writes later can still get `database is locked` when another write committed in between. This is SQLite's rule for keeping the read consistent, and no driver can lift it. Use `_txlock=immediate` for transactions that write.
- Another process writing the same file is not in the driver's queue. The two meet at SQLite's lock, as with any driver.
- A disk that cannot keep up stays a disk that cannot keep up. Writes then wait longer, and a write that waits past `busy_timeout` fails: that is overload, not bad luck.

## What it will not do

Each of these was tried or weighed, and left out on purpose:

- **Share cores between readers and writers.** Who runs next is Go's scheduler's business. A yield or a quota that helps writes hurts reads in ways the driver cannot see (one experiment cut reads 180 times). The driver removes its own costs, a sleep on a lock, an extra system call, a false `SQLITE_BUSY`, and reports reads and writes together ([busy-cores.md](busy-cores.md)).
- **Wait to fill a batch.** The commit itself is the wait: writes that arrive during an fsync join the next batch.
- **Checkpoint outside the write lock.** It would let writers go on during a slow flush, but a program's own `PRAGMA wal_checkpoint` could then answer busy, and fast disks got slower.
- **Edit SQLite's C, or the generated assembly by hand.** Compile options carry their reason. Everything SQLite needs from outside comes from a small libc or the Go host.
- **Reach into Go's internals, or run C on a stack of its own.** Both break across Go releases, and a fault on a stack of its own ends the whole process instead of one call.
- **Take a gain smaller than the noise.** A speedup under 2 to 3% is code layout noise, and it is not taken however clean the change.
- **Ship a feature over a known bug.** A bug is fixed when found, however rare, unless every fix costs stability.

## How a change is judged

- List every new place a caller could get `database is locked` or `SQLITE_BUSY`. Each one counts against the change.
- Measure reads and writes together, on more than one machine. A gain on one side paid by the other is not a gain.
- For new behavior, a failing test first. For a bug, the root cause first, and no fallback value that hides it.
- The rules each part must keep are in [group-commit.md](group-commit.md), [query-cache.md](query-cache.md) and [busy-cores.md](busy-cores.md).
