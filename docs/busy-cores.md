---
summary: 'How long a read or a write can wait when the program has more goroutines that compute than cores, which part of that wait is Go and which the driver, what the driver does to stay out of it, what a program can do, how to measure it, and what a change to the driver must keep'
read_when:
  - A read or a write takes tens or hundreds of milliseconds now and then while the disk is idle
  - Running the driver on one core or two, or with more goroutines than cores
  - Writing or reading a benchmark where goroutines call the database in a loop with no pause
  - Changing a lock, a wait or a wake on the path of a transaction (internal/vfs/shm.go, writer.go, conn.go)
  - Thinking of calling runtime.Gosched in the driver
---

# When every core is busy

English · [简体中文](busy-cores.zh-CN.md)

A goroutine that is ready to run waits for a core. While the program has more goroutines that compute than cores, that wait is most of the time of a slow call, not the database. This page says how long the wait gets, what the driver does to stay out of it, what a program can do, how to measure it, and what a change to the driver must keep.

The numbers are from an Apple M4 (darwin/arm64, Go 1.27.2) with `GOMAXPROCS` set as each row says, unless a row says linux. A write is one autocommit `INSERT` with `synchronous=NORMAL` in WAL mode.

## 1. What a program sees

| Program | Cores | Longest write |
| --- | --- | --- |
| A `net/http` server, 64 keep-alive clients from another process, 4 reads and one write in 5 requests | 1 | 6 to 34 ms at the client, 2 to 16 ms in the handler |
| 32 goroutines that call the database in a loop with no pause, 32 connections | 1 | 0.25 to 0.46 s |
| 64 such goroutines, 32 connections | 1 | 42 to 228 ms. With the execution tracer of Go on, 0.7 to 2.4 s in 5 runs of 6 |
| The same with mattn/go-sqlite3 | 1 | 0.4 to 3.3 s |
| The same with modernc.org/sqlite | 1 | 0.7 to 1.0 s |

A program that answers requests from the network waits tens of milliseconds at most. The long waits belong to loops that never pause, which is what most benchmarks are.

## 2. Where the wait comes from

**The time slice of Go.** A goroutine that computes keeps its core for up to 10 ms before the runtime gives the core to the next one. With N goroutines that never block on one core, each waits up to N times 10 ms for its turn: 32 goroutines, about a third of a second. A call that leaves the core a few times (a write to the file, a sync, the end of its slice) waits that long each time. No database is needed to see it: 8 goroutines that only compute on one core make a step of 1 µs take up to 240 ms (161 ms on linux).

**A wake that takes the core.** A goroutine that wakes another and then blocks hands its core to the one it woke, ahead of everyone who was waiting. `database/sql` does this when a connection is released while goroutines wait for one: the connection goes to a waiter, and the goroutine that released it blocks for its next connection. On one core, with more goroutines than connections and no pause between calls, the core can go round a few goroutines this way for seconds while the others wait ([golang/go#73964](https://github.com/golang/go/issues/73964)). `database/sql` with a driver that only computes shows it alone: 64 goroutines, 32 connections, one core, and the longest call is 3.5 to 4 s of a 4 s run. With two cores it is 40 to 61 ms.

A server does not get there. Its goroutines block on the network between requests, and a goroutine that blocks on the network wakes no one.

## 3. What the driver does

A goroutine that waits for a Go mutex sleeps, and once it is woken it waits for a core like any other: milliseconds when every core is busy. So on the path of each transaction, the driver does not wait for a mutex another transaction can hold.

- A reader takes the read slot of the WAL index its process has locked by counting itself in, with one atomic step.
- The writer takes the write slot with a lock no reader takes.
- A connection that asks for a slot exclusive, to write or to move a read mark, does not wait for another connection of the process. When one holds the slot, takes it or gives it back just then, the ask looks at the slot again for about 20 µs, and is refused if the slot is still not free.

How it is built is in [Architecture](architecture.md#layers).

## 4. What the driver does not do

It does not give its core up (`runtime.Gosched`). A goroutine that gives up its core goes to the back of a queue that is served once in 61 turns, behind every goroutine that did not. A reader that gave the core up every 64 statements ran 2 300 reads a second beside one goroutine that computes, on one core. Without, it runs 420 000 to 460 000. No number of statements makes this safe: how long a statement takes is not known to the driver.

It does not share the cores between readers and writers. Which goroutine runs next is the choice of the scheduler, and a rule in the driver that favours one side takes from the other more than it gives, as the reader above shows. On one core of linux, a writer beside 8 readers that never pause writes 3 000 to 5 000 times a second, and beside 2 readers 14 000.

## 5. What a program can do

- **Give the runtime more than one core.** The wait of seconds needs one core: with `GOMAXPROCS=2` the longest call above falls from 3.5 s to 40 to 61 ms.
- **On a machine with one core, set `GOMAXPROCS` to 2 or more.** The kernel then shares the core between threads in shorter turns than Go's 10 ms. On linux the longest step of a goroutine that computes fell from 181 ms to 33 ms.
- **Keep long computations off the goroutines that answer requests,** or have them pause. Every goroutine that computes for 10 ms adds 10 ms to the round.
- **Measure with load from another process.** Goroutines that call the database in a loop with no pause measure the scheduler of Go, in any driver.

## 6. How to measure it

Two questions need two kinds of test.

- **How much work a second.** Goroutines that call the database in a loop with no pause. This says how much the driver gets done on the cores it has. It does not say how long a call takes: the longest call of such a run is the round of the scheduler (section 2).
- **How long a call takes.** Requests sent at a fixed rate from another process, each timed from the moment it was due, not from the moment it was sent. Pick rates below the most the program answers, such as 40% and 70% of it, run each a few times, and change the order of what you compare from round to round. `benchrun latency` does this for each driver ([bench/README.md](../bench/README.md#how-long-a-request-takes)).

Reads and writes are reported together. In WAL mode a reader that sees a new commit drops the pages it holds, because they may have changed, and reads the pages on its path again. So reads fall as writes rise, in any driver: 8 readers on 10 cores read 1 000 000 times a second with no writer, 750 000 beside 2 000 writes a second and 480 000 beside 16 000. Compare reads at the same rate of writes. A change that lets the writer commit more makes the readers read less, and that is not a slower read.

## 7. What a change to the driver must keep

- On the path of each transaction, no wait for a `sync.Mutex`, a channel or a condition that another transaction can hold up. Use an atomic step, or `TryLock` with an answer for when it fails. The tests of `internal/vfs/shm_nowait_test.go` hold each mutex and check that the call returns.
- `SQLITE_BUSY` is true: it is answered only for a slot that a connection or another process holds, takes or gives back. A checkpoint does not ask again, and "database is locked" with no reader and no writer is a bug.
- No `runtime.Gosched` on the path of a statement.
- A change to a lock or a wake is measured with readers that fill every core, at `GOMAXPROCS` 1, 2, 4 and all cores: 8 readers with one writer, 8 readers with 8 writers, 2 readers with one writer. Speed with idle cores says nothing about it.
- Reads and writes are measured together, and reads are compared at the same rate of writes (section 6).
- No rule that shares the cores between readers and writers (section 4).
