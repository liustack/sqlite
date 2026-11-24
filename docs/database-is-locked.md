---
summary: 'Why SQLite answers database is locked, what the driver does about it, how to write your transactions so that they do not get it, when it can still come and what to do then'
read_when:
  - Seeing database is locked or SQLITE_BUSY
  - Deciding whether to use _txlock=immediate
  - Writing a transaction that reads and then writes
  - Asked whether SQLite can take many writers at once
audience: users
---

# database is locked

English · [简体中文](database-is-locked.zh-CN.md)

With this driver, the writers of your program wait their turn instead of failing with `database is locked`. For that, open the database in WAL mode with `_txlock=immediate`, start transactions that only read as read-only, and keep write transactions short. This page says why, and what is left.

## Why SQLite says it

SQLite lets one connection write a database at a time. A connection that wants to write while another does gets `SQLITE_BUSY`, which reads `database is locked`. With `busy_timeout` set, it sleeps and tries again instead, up to that long. With other drivers every connection of your program races for the lock this way, so under load one that keeps losing waits the whole timeout and fails, while the others wait a few milliseconds.

There is a second case that no timeout helps. In WAL mode a transaction reads from a snapshot of the database. When it has read and then wants to write, and another connection committed since it read, its snapshot is old: SQLite refuses the write at once (`SQLITE_BUSY_SNAPSHOT`), since a write made from old data could undo that commit. Waiting longer does not change that.

## What the driver does

Inside one process the writers queue in Go, in the order they came, and the write lock goes to the next one as soon as a writer is done. A write waits for the writes ahead of it, up to `busy_timeout` (5 s by default), and not longer because it was unlucky. Writes outside a transaction that come at the same time are committed together ([group commit](group-commit.md)), so the queue moves faster than one commit per write.

## What your code does

Open the database in WAL mode, with `_txlock=immediate`:

```go
db, err := sql.Open("sqlite", "file:app.db?_pragma=journal_mode(WAL)&_txlock=immediate")
```

**A write on its own** (`db.Exec` of an `INSERT`, `UPDATE` or `DELETE`) needs nothing more.

**A transaction that reads and then writes** is what `_txlock=immediate` is for. Its `BEGIN` waits its turn for the write lock, so nothing can commit between its read and its write, and it never fails halfway:

```go
func buy(ctx context.Context, db *sql.DB, item, qty int) error {
	tx, err := db.BeginTx(ctx, nil) // waits here for the write lock
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stock int
	if err := tx.QueryRowContext(ctx, `SELECT stock FROM items WHERE id = ?`, item).Scan(&stock); err != nil {
		return err
	}
	if stock < qty {
		return errSoldOut
	}
	if _, err := tx.ExecContext(ctx, `UPDATE items SET stock = stock - ? WHERE id = ?`, qty, item); err != nil {
		return err
	}
	return tx.Commit()
}
```

Without `_txlock=immediate`, SQLite starts this transaction as a reader, and its `UPDATE` fails at once when another connection holds the write lock or has committed since the `SELECT`. With 64 goroutines that call it at once, most calls fail, with every driver (see [benchmarks.md](benchmarks.md#many-writers-at-once)). A transaction whose first statement writes does not need it, as it takes the lock before it reads anything.

**A transaction that only reads** says so:

```go
tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
```

It takes no write lock and waits for no writer. Under `_txlock=immediate` a plain `db.Begin()` takes the write lock at `BEGIN`, even if it then only reads, and every writer waits for it.

**Keep write transactions short.** A transaction holds the write lock from `BEGIN` to `COMMIT`, and every other writer waits for it. Call other services, and do slow work, before `BEGIN` or after `COMMIT`.

**Give the wait a deadline when you need one.** The wait for the write lock ends when the context of the call is done, with the context's error.

## When it can still come

With the above, `database is locked` (`sqlite.BUSY`) means one of these:

- **Writes came faster than the disk can commit them**, for longer than `busy_timeout`. That is real overload: answer the request with an error that says to try again later, or let fewer writes in. A longer `busy_timeout` (`_pragma=busy_timeout(10000)`) only waits longer for the same disk.
- **Another process writes the same file**, as a cron job or the `sqlite3` shell. The queue is inside one process: between processes SQLite's own lock and busy handler apply, with the `busy_timeout` of each. Give the other process a `busy_timeout` too (`.timeout 5000` in the shell).
- **A transaction that read before it wrote was started without `_txlock=immediate`.** Run the whole transaction again from `BEGIN`, not only the statement that failed, or better, start it with `_txlock=immediate`.
- **`busy_timeout` is 0.** A write then fails at once whenever another holds the lock.

Check for it:

```go
var e *sqlite.Error
if errors.As(err, &e) && e.Code() == sqlite.BUSY {
	// overload, or another process
}
```

## Without WAL

In SQLite's default journal mode (a rollback journal), a writer cannot commit while another connection reads, and a reader cannot read while a writer commits, so reads can get `database is locked` too. `_pragma=journal_mode(WAL)` lets reads go on while one connection writes. The setting stays in the database file.

## How much it changes

[benchmarks.md](benchmarks.md#many-writers-at-once) has 64 and 256 goroutines write at once for 30 s with each driver: the slowest write, how many got `database is locked`, on linux and on Windows.
