---
summary: '驱动替调用方留下的预编译语句的全部规则：留哪些、什么时候扔、为什么写语句不留、合并提交的语句缓存为什么可以留写语句，以及改这部分代码时必须守住什么'
read_when:
  - 改 cache.go、conn.go 的 prepare 和 QueryContext、rows 的 Close
  - 改 writer.go 的 cached 或 execCached
  - 改 engine/os.c 的 goClassify，或者新加一种会改连接的语句
  - 被问到 Query 为什么比别的驱动快，或者缓存会不会读到旧的东西
---

# 语句缓存的规则

[English](query-cache.md) · 简体中文

准备一条语句（`sqlite3_prepare`）要解析 SQL、查 schema、生成程序，一条简单的查询里它常常比执行本身还贵。调用方用 `db.Query` 而不是 `*sql.Stmt` 时，`database/sql` 每次都要驱动重新准备。驱动在连接上留下准备好的语句，下次同样的 SQL 直接用。

这也是驱动替调用方做的决定。下面的规则守住一件事：**用留下的语句执行，结果和当场准备一条完全一样。** 拿不准的语句不留，只会慢，不会错。

## 一、驱动留下的三种语句

| 缓存 | 在哪 | 留什么 | 每个连接最多 |
| --- | --- | --- | --- |
| 查询缓存 | `cache.go`，`conn.queries` | `Query`、`QueryRow` 的读语句 | 64 条 |
| 批语句缓存 | `writer.go` 的 `cached`，`conn.cache` | 这个连接替别人跑的合并写语句 | 64 条 |
| 控制语句 | `writer.go` 的 `execCached`，`conn.control` | `BEGIN IMMEDIATE`、`SAVEPOINT merge`、`COMMIT` 这几条固定的 SQL | 不限（就那几条） |

调用方自己的 `*sql.Stmt` 不归驱动管，它留多久由调用方决定，和其他驱动一样。

## 二、查询缓存留哪些语句

1. **只留只读、只读 main 库的语句。** `sqlite3_stmt_readonly` 为真，`goClassify` 没标 `GO_PRIVATE`（不碰 temp 库和 ATTACH 进来的库，不读 Go 虚表，不是 PRAGMA），也不是 `EXPLAIN`。SQLite 不为 `USING` 和 `NATURAL JOIN` 比较的列调用授权回调，只在那里出现的表 `goClassify` 看不到：Go 虚表由 Go 模块的 `xBestIndex` 补报，照样不留。temp 库和 attach 进来的库在那里会漏，留下也无害，因为用到它们之前的 ATTACH 和建临时对象，以及之后的 DETACH 和删临时对象，都会清空缓存（第 6 条）。
2. **按完整的 SQL 文本查找。** 文本差一个空格就是另一条。
3. **同一条 SQL 的行还没关时，不共用。** 第二次查询自己准备一条，用完就扔，不进缓存。
4. **满了就扔最久没用、而且没有行开着的那条。** 64 条都在用时，新语句不留。
5. **留下的语句关行时清掉绑定的参数**（`sqlite3_clear_bindings`），不替调用方攥着参数的内存。SQLite 绑定时已经复制了参数，所以这条只关系到内存。

## 三、什么时候扔掉

6. **连接准备了会改自己的语句，扔掉全部。** 设值的 PRAGMA、ATTACH、DETACH、建或删临时对象，从准备的那一刻算，不管它执行没有、准备成没成功（`conn.prepare` 看 `JustChanged`）。原因：SQLite 准备设值的 PRAGMA 时就生效了，留下的语句是按之前的设置准备的。
7. **语句被 SQLite 重新准备过（schema 变了），关行时扔掉。** 原因：它读的东西可能变了，比如一张表变成了 Go 虚表，下次要重新分类。
8. **执行出错，关行时扔掉。** 下一次从头准备，不带着出过错的状态。
9. **查询启动就失败（第一次 step 出错），立刻扔掉。**
10. **连接关闭，扔掉全部。** 行还开着的语句由它的行在关闭时释放。

## 四、为什么写语句不留

11. **SQLite 准备写语句时，把连接的一些设置编进了程序**，比如 `foreign_keys`、`defer_foreign_keys`，而这些设置变了，SQLite 不一定让语句重新准备：准备了但没执行的 PRAGMA 已经改了设置，`defer_foreign_keys` 在每个事务结束时被 SQLite 自己关掉。留下的写语句就可能跳过外键检查。
12. **PRAGMA 不留**，SQLite 每次执行都会重新准备它。
13. **`EXPLAIN` 不留，而且每次执行前都重新准备**（`stmt.prepareAgain`），连调用方的 `*sql.Stmt` 也一样。原因：SQLite 执行 `EXPLAIN` 时不检查 schema，旧的程序会列出旧 schema 的计划和已经释放的表名。

## 五、批语句缓存为什么可以留写语句

14. **只有从没改过自己的连接才替别人跑批**（见 [group-commit.zh-CN.md](group-commit.zh-CN.md) 第 6 条）。这样的连接上，第 11 条说的设置只来自 DSN，而 DSN 是合并 key 的一部分，同一个 key 的连接设置完全一样，也不会变。打开了 `defer_foreign_keys` 的 DSN 本来就不合并。连接一旦改了自己，就永远不再跑批，它的批语句缓存也不再被用到。
15. **批语句缓存满了，随便扔一条腾地方。** 这时没有哪条批语句在执行：跑过的都已经 reset，要用的这条还没放进来。控制语句单独放、连接关闭前从不释放，所以腾地方不会释放这条请求的 `SAVEPOINT` 或 `COMMIT`。
16. **schema 变了，批语句由 SQLite 重新准备，`goClassify` 重新分类。** 不能再合并的，按 group-commit 第 4 条退回调用方的连接。

## 六、和每次准备的区别

17. **结果上没有区别。** 留下的语句在 schema 变化后由 SQLite 自己重新准备，列名和声明类型在重新准备后重新读（`stmt.columns`）。
18. **内存**：每个连接最多多占 64 条查询语句和 64 条批语句。语句关行后不持有读快照，也不持有锁。

## 七、改这部分代码时必须守住的

- **调用方的 SQL 一律经 `conn.prepare` 准备**，这样会改连接的语句一定会清空查询缓存。新加准备的路径，要么走它，要么证明那条 SQL 不会改连接。
- **只留「程序只取决于 SQL 文本和 schema」的语句。** 程序还取决于一个能在 SQLite 不知情时改变的连接设置，就不能留。新加一种可以留的语句之前，先证明这一点，再加测试。
- **授权回调有看不到的读**（`USING` 和 `NATURAL JOIN` 比较的列）。Go 虚表靠 `xBestIndex` 补报，temp 库和 attach 进来的库靠第 6 条兜住。改授权回调、Go 模块或第 6 条时，三处要一起想。
- **留下的语句在重新准备后不能直接复用分类结果。** 查询缓存扔掉它，批语句缓存重新分类。
- **每条规则都要有测试守着。** 相关测试在 `query_cache_test.go`、`query_cache_public_test.go`、`merge_test.go` 和 `internal/csqlite` 里。
- **改规则时同步改**：这份文档的中英两版、[architecture.zh-CN.md](architecture.zh-CN.md) 的摘要，以及受影响的测试。
