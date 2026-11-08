---
summary: 'Every rule of group commit: which statements merge, how a batch runs, who gets what when each step fails, how a merged statement differs from one run alone, and what a change to this code must keep'
read_when:
  - Changing the write path of writer.go or stmt.go, or goClassify in engine/os.c
  - Changing how Go functions, aggregates, window functions, collations or virtual tables are registered or called
  - Changing the committed callback of replication, or how the write lock is handed on
  - Asked why concurrent writes can get SQLITE_ABORT_ROLLBACK, or what group commit guarantees
---

# Group commit

English · [简体中文](group-commit.zh-CN.md)

When many goroutines each write a row in autocommit mode, every row pays for a commit of its own, and SQLite has one writer at a time, so they wait in line. The driver puts these writes in one transaction and commits once.

The driver decides this for its callers, who do not know they were merged. The rules below keep two promises: a statement that ran never runs a second time, and a merged statement gives what it would give run alone, except for the differences in section 5. Read this whole page before changing this code.

## 1. Which statements merge

1. **Only autocommit INSERT, UPDATE and DELETE (REPLACE, UPSERT and writes that start with WITH too), and only on the main database.** Only `Exec` merges, never `Query`. `EXPLAIN` does not merge, as it writes nothing. Why: other statements (DDL, PRAGMA, ATTACH) change the connection or the schema, and would give another result on another connection.
2. **A statement of a user transaction never merges.** In `exec` of `stmt.go`, a statement joins the queue only when its connection is out of any transaction, does not hold the write lock, has not changed itself, and the statement is mergeable. Why: the caller sets the bounds of its transaction. The driver must not put statements of others in it, nor take its statements out of it.
3. **A statement must not touch what belongs to its own connection.** The authorizer `goClassify` in `engine/os.c` decides as the statement is prepared:
   - no temp database and no attached database
   - no writes through a trigger (a trigger may be a temporary one). A trigger that only reads or calls functions does not count
   - no reads or writes of a Go virtual table (SQLite cannot undo the state of Go code, and each connection may have another instance of the table)
   - no `last_insert_rowid()`, `changes()` or `total_changes()` (they read counts of the connection)

   SQLite does not call the authorizer for the columns a `USING` or `NATURAL JOIN` compares, so a table named only there is not seen. A Go table is reported by the `xBestIndex` of the Go module, which SQLite calls for every virtual table it plans, also as it prepares a statement again. The temp database and attached ones are missed there, but a connection that uses them has changed itself (rule 6) and never merges.

   The statements SQLite prepares while it prepares this one (for example those of the shadow tables R*Tree prepares when a connection first uses it) pass through the authorizer too. They may make the outer statement unmergeable, but never make it a write of the main database.
4. **After a change of the schema, a statement may stop being mergeable.** SQLite prepares it again at the start of `sqlite3_step`, before it has done anything. When that happens on a connection that runs a batch for others and the statement is no longer mergeable, `goClassify` makes that prepare fail, and the statement runs on its caller's connection, sorted again. A refusal that falls on a statement prepared inside that prepare (one a virtual table prepares as it connects) is put on the Parse of the outer statement, so that its prepare fails. Why: a virtual table may swallow the error of its own statement and go on (R*Tree with auxiliary columns does), and the refused statement would then really run.

## 2. Which connections run statements for each other

5. **Only connections of the same file and the same merge key.** The key is made of the normalized file name and URI parameters, the `_pragma` settings in order, and the set of Go functions and virtual table modules (`NewConnector` in `sqlite.go`). Two connectors with the same DSN, made with the same `Register` calls, share a key. A connector made with options of its own, `NewConnector(dsn, opts...)`, has a key of its own. Registration is read when the connector is made: a later `Register` does not change it. Why: the connection that runs the statement must have all the caller's connection has, for the statement to give the same result.
6. **A connection that leaves group commit for good** (it gives no statement to the queue and runs none for others):
   - it prepared a statement that changes the connection: a PRAGMA that sets something, ATTACH, DETACH, creating or dropping a temporary object. This counts from the prepare, whether the statement ran or its prepare succeeded
   - it was opened read-only
   - its DSN turns `defer_foreign_keys` on (SQLite turns it off at the end of each transaction, so connections of the same DSN differ)
7. **A connection that cannot merge for a while**, until that state ends: one in a transaction (a read-only one too, and the read snapshot of a query not yet read to its end). A connection that is closed, failed or broken runs nothing for others.

## 3. How a batch runs

8. **One file has one write lock in the process** (found by what the file is, not by its name). An in-memory database has no such lock and does not merge. Other processes only meet SQLite's own file locks.
9. **Scheduling:**
   - When the lock is free, a statement takes it and runs at once, without waiting for a batch to fill. One writer pays nothing.
   - When the lock is held, the statement joins the queue with its place in line.
   - Before the connection that holds the lock gives it back, it takes the statements of its key from the queue (up to 256) and runs them on itself: `BEGIN IMMEDIATE`, each statement between a `SAVEPOINT` and a `RELEASE` of its own, then `COMMIT`. The call that gave the lock back (a write, or the `Commit` of a transaction) returns only once this batch is over, though its own work committed before. Why: this is the price of batching, and handing the batch to a request of the queue instead was measured slower (one more wake up and switch of connection per batch).
   - Then it calls the committed callback of replication (whatever became of the batch: the holder's own transaction before may not have been reported yet), and hands the lock to whichever came first of the writers that wait for it and the first statement of the queue.
   - A statement of the queue handed the lock leads a batch on its own connection: it comes first, followed by statements of its key. As it gives the lock back it may run one more batch before the lock goes on.

   The place in line orders the queue and the hand over of the lock. It does not make all SQL run in the order it came: a batch takes statements of its key that came after a writer still waiting for the lock. Why: taking by key is what makes merging fast, and handing the lock on in order means no one waits forever.
10. **A statement that called a Go function not marked `Innocuous` is the last of its batch.** A scalar function marks the connection when it is called, an aggregate or window function when it makes the state of a group (`markEffects`). Once that statement has run (it succeeded or failed alone), the batch commits, and the statements after it that have not run go back to the queue in their place. A call from a trigger, a CHECK, a generated column or a DEFAULT counts too, a function that is never called does not. Why: such a function may act outside the database, which no rollback undoes, so no later statement must be able to roll its statement back. The smaller the batch, the fewer statements a failure takes with it. The cost: writes that call such functions get little from merging. Collations get no mark: they must have no side effects.
11. **Once a virtual table takes part in the transaction (FTS5, for example), each statement is followed by a `SAVEPOINT` and `RELEASE` of its own, inside its own `SAVEPOINT`.** Why: FTS5 writes its index only at the next `SAVEPOINT` or at `COMMIT`. Without this, a failure to write the index would fall on the next statement or on the whole batch.

## 4. Who gets what when each step fails

**Nothing runs again.** The driver does not know what a statement did beside writing the database (side effects of Go functions, the state of a virtual table outside it), and running it again could do those twice. A whole batch failing together is the price of merging.

Each request taken into a batch ends one way only, once: it gets its result, or it goes back to the queue. Below, a request "ran" when its `sqlite3_step` was called.

| Step | Result |
| --- | --- |
| Still in the queue, its context ends or it waits past the busy timeout | Taken out of the queue, gets the error of its context or `SQLITE_BUSY`. It never ran and touches no one else. A request that goes back to the queue after its caller stopped waiting there is not taken out again: it waits for its answer from the next batch or the lock |
| Taken into the batch, its context is done when its turn comes | Does not run. Gets the error of its context alone, and is no part of the batch |
| Taken into the batch, its prepare or its bind fails | Did not run. Gets its own error alone, and is no part of the batch |
| Refused as it is prepared again (rule 4) | Once its `SAVEPOINT` is undone, it goes back to the queue, and in its turn runs on its caller's connection. It did nothing, so this is not running it again. If the undo fails, as below for a transaction that ended |
| `BEGIN IMMEDIATE` fails | No statement ran. The whole batch gets the error of BEGIN |
| A statement fails and the transaction goes on (a constraint, for example) | Its own error and the rows SQLite kept of it (`OR FAIL` keeps the rows before the failure) are kept. What it finally gets depends on how the batch ends |
| A statement leaves a deferred foreign key unresolved | It is undone with `ROLLBACK TO`, and keeps the foreign key error its own commit would have given. What it finally gets depends on how the batch ends |
| Writing a virtual table's index fails and the transaction goes on | The statement is undone and keeps its error. What it finally gets depends on how the batch ends |
| A statement, or a `SAVEPOINT`, `ROLLBACK TO`, `RELEASE` or index write of the driver, ends the whole transaction (an interrupt, an IO error, a full disk, out of memory, `OR ROLLBACK`, `RAISE(ROLLBACK)`, a panic in a collation) | The request running then gets the error that ended the transaction. Every request that ran before it, whether it succeeded or failed alone, gets `SQLITE_ABORT_ROLLBACK` (516) instead, whose `Unwrap()` is the error that ended the transaction. The requests after it have not run: they go back to the queue in their place |
| The driver's `ROLLBACK` fails too, and the transaction is still open | Results as in the row above. The connection is marked bad and keeps the write lock while its transaction is open. When `database/sql` drops it, it is closed, which rolls the transaction back and gives the lock back. A request handed the lock that led this batch and did not run gets `driver.ErrBadConn`, so that `database/sql` runs it on another connection |
| Rule 10 ends the batch | What ran commits as usual. What did not run goes back to the queue |
| `COMMIT` succeeds | Each request that ran gets what it kept: `RowsAffected` and `LastInsertId` when it succeeded, its own error when it failed alone |
| `COMMIT` fails | Every request of the batch that ran gets the error of COMMIT, those that failed alone too. Nothing runs again |

Notes:

- **Whether the transaction ended is read from the connection (`AutoCommit()`), never from the error code.** The same code sometimes rolls back one statement and sometimes the whole transaction, as SQLite's state at the time decides.
- **"Has not run" is exact.** The requests after the one that failed were only moved from the queue to a Go slice: the connection of the batch never prepared, bound, stepped or put a `SAVEPOINT` around them, and none of their Go functions was called. A request refused as it was prepared again is different: it was prepared and stepped, but its step failed before it did anything.
- **When the context ends while a statement runs, the driver tries to interrupt it.** When SQLite returns the interrupt and the transaction ended, it is a transaction that ended, as above. A statement that finished before the interrupt could stop it ran as usual. The context does not stop `BEGIN`, `SAVEPOINT` or `COMMIT`, and does not undo a write that committed.
- **The leading request does not run again either.** When a request handed the lock leads a batch and it ran, it gets its result by the table above, and never runs again on its own connection. Only a request refused as it is prepared again goes back to it.
- **Once taken into a batch, a call waits for how the batch ends.** Neither `busy_timeout` nor the context bounds the call from then on.
- **last_insert_rowid:** each request keeps the value its statement left, which is written back to the caller's connection. A request that did not run, went back to the queue or was refused keeps the caller's value as it was. For a request that got an error, the value on the caller's connection is what it would be alone, by what SQLite did (SQLite does not restore it when it rolls back).

## 5. How a merged statement differs from one run alone

12. **A statement that would have succeeded can get `SQLITE_ABORT_ROLLBACK` because another statement of its batch failed.** SQLite gives this code to the statements of a transaction it rolled back. The driver gives it to each request a failed batch took with it. An autocommit statement run alone never gets it, so seeing it means another statement of the batch failed.
    - It means this write to the database was rolled back. It does not mean nothing happened outside the database (Go functions of the batch may have been called), nor that the statement will succeed if tried again. Whether to try again is the caller's choice.
    - `Unwrap()` is the cause as SQLite reported it. When the context of another caller ended and interrupted the batch, the cause is `SQLITE_INTERRUPT`, not `context.Canceled`: `errors.Is(err, context.Canceled)` holds only when your own context ended.
13. **A statement that failed alone can still end with 516 or the error of COMMIT** instead of its own error, since what it left was rolled back with the batch. The statement that ended the transaction may also have failed over a write of the batch before it, which was rolled back with it: a UNIQUE error over a row that in the end is not in the database.
14. **`changes()` and `total_changes()` belong to the connection that ran the statement.** When another connection runs it, the caller's counts leave it out, and the counts of the running connection include the statements of others. `RowsAffected()`, `LastInsertId()` and `last_insert_rowid()` on the caller's connection are kept for each request and are right.
15. **The writes of a batch become visible together at `COMMIT`.** A read that starts after the commit sees an early statement of the batch only once the whole batch committed. A read transaction already open keeps its older snapshot, as with a statement run alone.
16. **Replication gives no restore point inside a batch.** A batch is one SQLite transaction. One TXID of replication is one staging, which may hold several batches or other transactions (see [replication.md](replication.md)).

## 6. Reading the errors

The driver exports its error type `sqlite.Error`: `Code()` is the primary result code, `ExtendedCode()` the extended one, and `Unwrap()` the cause. Each code has a constant of the same name (`sqlite.BUSY`, `sqlite.CONSTRAINT`, `sqlite.ABORT_ROLLBACK` and the others).

```go
var e *sqlite.Error
if errors.As(err, &e) && e.ExtendedCode() == sqlite.ABORT_ROLLBACK {
	// Another statement of the batch failed: this write was rolled back.
}
```

The driver never turns these errors into `driver.ErrBadConn`: `database/sql` would try the statement again by itself, which is running it again. The one `driver.ErrBadConn` is for a request that never ran, on a connection that went bad (section 4).

## 7. What a change to this code must keep

- **A statement that ran never runs again.** This binds `runBatch`, `lead` and its fall back to the caller's connection, and the path of a bad connection. Any new way to fail must say what each request that ran gets.
- **Only these requests go back to the queue:** those after the point of failure or of the cut that the batch never touched, those refused as they were prepared again whose undo succeeded, and those whose statement the connection of the batch already knows it cannot merge (no `SAVEPOINT`, no step). All keep their place in line, context, arguments and last_insert_rowid, and their `done` stays open.
- **Each request ends once**, and its `done` is closed once. The path of a leading request and the path back to the queue both make sure the lock is handed on in the end, and the committed callback of replication is called before the lock is handed on.
- **Before another kind of statement merges**, show in `goClassify` that it touches nothing of its connection, then add tests. When unsure, do not merge: it is only slower, never wrong.
- **A new kind of Go callback** (a function, a method of a virtual table) must say what happens when it fails inside a batch, has side effects or is called more than once, and whether it marks the connection as in rule 10.
- **Each rule has tests.** They are in `merge_test.go`, `fault_test.go`, `functions_test.go`, `builtin_test.go`, `vtab_test.go`, `group_test.go`, `replhook_test.go`, `readtx_test.go` and `internal/csqlite`. A test checks what each request gets, by the type of its error, and the rows at the end, not only the count of rows that succeeded.
- **When a rule changes, change with it:** both languages of this page, the hard constraints in [AGENTS.md](../AGENTS.md), the summary in [architecture.md](architecture.md), the public docs such as `functions.go`, and the tests it touches.
