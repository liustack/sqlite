---
summary: 'SQLite 为什么会报 database is locked，驱动为此做了什么，事务怎么写才不会碰上它，什么情况下还会碰上，碰上了怎么办'
read_when:
  - 碰到 database is locked 或 SQLITE_BUSY
  - 想决定要不要用 _txlock=immediate
  - 要写一个先读后写的事务
  - 被问到 SQLite 能不能承受很多写者同时写
translatedFrom: 5c435cc87251
---

# database is locked

[English](database-is-locked.md) · 简体中文

用这个驱动，你程序里的写者会排队等，不会报 `database is locked`。要做到这一点，打开数据库时用 WAL 模式并加上 `_txlock=immediate`，只读的事务标明只读，写事务尽量短。下面讲为什么，以及还剩哪些情况。

## SQLite 为什么会报这个错

SQLite 同一时刻只让一个连接写数据库。别的连接想写时，如果已经有人在写，就会拿到 `SQLITE_BUSY`，错误信息就是 `database is locked`。设了 `busy_timeout` 的话，它会先睡一会儿再试，最多试这么久。别的驱动里，你程序的每个连接都这样抢锁，负载一高，一直抢不到的那个会等满整个超时然后失败，而别的连接只等了几毫秒。

还有一种情况，等多久都没用。WAL 模式下，事务读的是数据库的一个快照。如果事务先读了，然后要写，而在它读之后别的连接提交过，它的快照就过时了。SQLite 会马上拒绝这次写（`SQLITE_BUSY_SNAPSHOT`），因为照着旧数据写下去，可能会把别人的提交覆盖掉。再等也改变不了这一点。

## 驱动做了什么

同一个进程里的写者在 Go 里排队，按到达的先后，一个写完马上把写锁交给下一个。一次写只等排在它前面的写，最多等 `busy_timeout`（默认 5 秒），不会因为运气不好多等。事务外同时到达的写会一起提交（[合并提交](group-commit.zh-CN.md)），所以队伍走得比一次写一次提交快。

## 你的代码怎么写

打开数据库时用 WAL 模式，并加上 `_txlock=immediate`：

```go
db, err := sql.Open("sqlite", "file:app.db?_pragma=journal_mode(WAL)&_txlock=immediate")
```

**单独一句写**（用 `db.Exec` 执行 `INSERT`、`UPDATE` 或 `DELETE`），不用再做什么。

**先读后写的事务**，就是 `_txlock=immediate` 要管的。它的 `BEGIN` 先排队拿到写锁，读和写之间别人提交不了，所以不会写到一半失败：

```go
func buy(ctx context.Context, db *sql.DB, item, qty int) error {
	tx, err := db.BeginTx(ctx, nil) // 在这里排队拿写锁
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

不加 `_txlock=immediate` 的话，SQLite 会把这个事务当成读者开始。到 `UPDATE` 时，只要别的连接正拿着写锁，或者在 `SELECT` 之后提交过，它就会马上失败。64 个 goroutine 同时调用时，大多数调用都会失败，哪个驱动都一样（见 [benchmarks.zh-CN.md](benchmarks.zh-CN.md#很多写者同时写)）。第一句就是写的事务不需要它，因为它在读任何东西之前就拿到了锁。

**只读的事务**要标明只读：

```go
tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
```

它不拿写锁，也不用等任何写者。加了 `_txlock=immediate` 以后，普通的 `db.Begin()` 在 `BEGIN` 时就会拿写锁，即使后面只读，所有写者也都要等它。

**写事务尽量短。** 事务从 `BEGIN` 到 `COMMIT` 一直拿着写锁，别的写者都要等它。调用别的服务、做慢的事，放在 `BEGIN` 之前或 `COMMIT` 之后。

**需要的话给等待设个截止时间。** 调用的 context 结束时，等写锁的那次等待也会结束，返回 context 的错误。

## 什么情况下还会碰上

照上面做了以后，`database is locked`（`sqlite.BUSY`）只会是下面几种情况之一：

- **写入快过磁盘能提交的速度**，而且持续得比 `busy_timeout` 还久。这是真的过载了：给请求回一个「稍后再试」的错误，或者少放一些写进来。把 `busy_timeout` 调长（`_pragma=busy_timeout(10000)`），也只是对着同一块磁盘等得更久。
- **别的进程也在写同一个文件**，比如定时任务或 `sqlite3` 命令行。排队只在一个进程里面，进程之间走的是 SQLite 自己的锁和忙等，各用各的 `busy_timeout`。给那个进程也设上 `busy_timeout`（命令行里是 `.timeout 5000`）。
- **有先读后写的事务没加 `_txlock=immediate` 就开始了。** 要从 `BEGIN` 起把整个事务重跑，不能只重跑失败的那一句。更好的办法是用 `_txlock=immediate` 开它。
- **`busy_timeout` 是 0。** 这时只要别人拿着锁，写就会马上失败。

判断是不是这个错误：

```go
var e *sqlite.Error
if errors.As(err, &e) && e.Code() == sqlite.BUSY {
	// 过载了，或者是别的进程
}
```

## 不用 WAL 的时候

SQLite 默认的日志模式（回滚日志）下，有别的连接在读时，写者提交不了，有写者在提交时，读者也读不了，所以读也会报 `database is locked`。`_pragma=journal_mode(WAL)` 让一个连接写的时候，读照样能进行。这个设置会存在数据库文件里。

## 差别有多大

[benchmarks.zh-CN.md](benchmarks.zh-CN.md#很多写者同时写) 里，每个驱动都让 64 和 256 个 goroutine 同时写 30 秒，给出最慢的一次写、多少次报了 `database is locked`，linux 和 Windows 上都有。
