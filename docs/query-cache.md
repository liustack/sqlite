---
summary: 'Every rule of the prepared statements the driver keeps for its callers: which it keeps, when it lets them go, why writes are not kept, why the statements of group commit can be, and what a change to this code must keep'
read_when:
  - Changing cache.go, prepare or QueryContext in conn.go, or Close of rows
  - Changing cached or execCached in writer.go
  - Changing goClassify in engine/os.c, or adding a kind of statement that changes the connection
  - Asked why Query is faster than in other drivers, or whether the cache can read something stale
---

# Statement cache

English · [简体中文](query-cache.zh-CN.md)

Preparing a statement (`sqlite3_prepare`) parses the SQL, looks up the schema and builds a program. For a simple query it often costs more than running it. When a caller uses `db.Query` and not a `*sql.Stmt`, `database/sql` has the driver prepare it again each time. The driver keeps the prepared statement on the connection, and the same SQL runs it again next time.

The driver decides this for its callers too. The rules below keep one promise: **a kept statement gives exactly what a statement prepared there and then would give.** A statement the driver is unsure of is not kept: that is only slower, never wrong.

## 1. The statements the driver keeps

| Cache | Where | What it keeps | Most on a connection |
| --- | --- | --- | --- |
| Query cache | `cache.go`, `conn.queries` | reads of `Query` and `QueryRow` | 64 |
| Batch cache | `cached` in `writer.go`, `conn.cache` | merged writes the connection runs for others | 64 |
| Control statements | `execCached` in `writer.go`, `conn.control` | the few fixed statements `BEGIN IMMEDIATE`, `SAVEPOINT merge`, `COMMIT` and the like | no limit (there are only those) |

The caller's own `*sql.Stmt` is not the driver's: the caller decides how long it lives, as with any driver.

## 2. What the query cache keeps

1. **Only statements that read, and read only the main database.** `sqlite3_stmt_readonly` is true, `goClassify` did not mark it `GO_PRIVATE` (no temp database, no attached database, no Go table, no PRAGMA), and it is not an `EXPLAIN`. SQLite does not call the authorizer for the columns a `USING` or `NATURAL JOIN` compares, so `goClassify` does not see a table named only there. A Go table is reported by the `xBestIndex` of the Go module, so its reads are still not kept. The temp database and attached ones are missed there, and keeping such a read does no harm: the ATTACH or the temporary object it needs, and the DETACH or the drop that ends it, empty the cache (rule 6).
2. **It is found by its whole SQL text.** Text that differs by one space is another statement.
3. **While rows of a statement are open, it is not shared.** A second query of the same SQL prepares a statement of its own, which is let go once used and never kept.
4. **When the cache is full, the statement used longest ago with no rows open is let go.** When all 64 have open rows, the new statement is not kept.
5. **A kept statement lets its arguments go as its rows close** (`sqlite3_clear_bindings`), so the driver does not hold memory of the caller's. SQLite copies arguments as they are bound, so this is about memory only.

## 3. When statements are let go

6. **The connection prepares a statement that changes it: all go.** A PRAGMA that sets something, ATTACH, DETACH, creating or dropping a temporary object. This counts from the prepare, whether the statement runs or its prepare succeeds (`JustChanged` in `conn.prepare`). Why: SQLite applies a PRAGMA that sets something as it prepares it, and the kept statements were prepared with the settings of before.
7. **SQLite prepared the statement again (the schema changed): it goes as its rows close.** Why: what it reads may have changed, a table may have become a Go table, and the next query sorts it again.
8. **It failed: it goes as its rows close.** The next query prepares from nothing, without the state of a failure.
9. **The query fails at its first step: it goes at once.**
10. **The connection closes: all go.** A statement whose rows are still open is let go by its rows as they close.

## 4. Why writes are not kept

11. **SQLite codes some settings of the connection into a write as it prepares it**, such as `foreign_keys` and `defer_foreign_keys`, and does not always prepare it again when they change: a PRAGMA prepared and never run has changed them already, and SQLite turns `defer_foreign_keys` off by itself at the end of each transaction. A kept write could then skip a foreign key check.
12. **A PRAGMA is not kept.** SQLite prepares one again each time it runs.
13. **An `EXPLAIN` is not kept, and is prepared again before each run** (`stmt.prepareAgain`), the caller's `*sql.Stmt` too. Why: SQLite runs an `EXPLAIN` without checking the schema, so an old program would list the plan of the old schema and the names of tables it freed.

## 5. Why the batch cache can keep writes

14. **Only connections that never changed themselves run batches for others** (see rule 6 of [group-commit.md](group-commit.md)). On such a connection the settings of rule 11 come only from the DSN, which is part of the merge key, so connections of one key have the same settings and they never change. A DSN that turns `defer_foreign_keys` on never merges at all. Once a connection changes itself it never runs a batch again, and its batch cache is not used again.
15. **When the batch cache is full, any statement in it is let go.** None runs at that moment: those that ran were reset, and the one about to run is not in it yet. Control statements are kept apart and never let go before the connection closes, so making room never lets go of the `SAVEPOINT` or `COMMIT` of the request.
16. **After a change of the schema SQLite prepares a batch statement again, and `goClassify` sorts it again.** One that can no longer merge goes back to its caller's connection, by rule 4 of group-commit.md.

## 6. How a kept statement differs from one prepared each time

17. **In what it gives, it does not.** SQLite prepares a kept statement again by itself after a change of the schema, and the names and declared types of its columns are read again after that (`stmt.columns`).
18. **Memory:** each connection may hold up to 64 more query statements and 64 batch statements. Once its rows close, a statement holds no read snapshot and no lock.

## 7. What a change to this code must keep

- **The caller's SQL is always prepared through `conn.prepare`**, so that a statement that changes the connection always empties the query cache. A new way to prepare either goes through it, or shows the SQL it prepares never changes the connection.
- **Only keep statements whose program depends on nothing but the SQL text and the schema.** A statement whose program also depends on a setting of the connection that can change without SQLite knowing must not be kept. Before keeping another kind of statement, show this, then add tests.
- **The authorizer misses some reads** (the columns of `USING` and `NATURAL JOIN`). Go tables are covered by `xBestIndex`, the temp database and attached ones by rule 6. A change to the authorizer, the Go module or rule 6 must keep all three in mind.
- **A kept statement SQLite prepared again is not run on its old sorting.** The query cache lets it go, the batch cache sorts it again.
- **Each rule has tests.** They are in `query_cache_test.go`, `query_cache_public_test.go`, `merge_test.go` and `internal/csqlite`.
- **When a rule changes, change with it:** both languages of this page, the summary in [architecture.md](architecture.md), and the tests it touches.
