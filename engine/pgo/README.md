# engine/pgo

What `make gen` needs to compile the engine with a profile: the profile itself, and the program and the SQL that train it. What the profile does to the generated code, what it gains and where it stops is in [docs/architecture.md](../../docs/architecture.md#the-profile).

## What is here

| Path | What |
| --- | --- |
| `profile.txt` | The profile, in the text form of `llvm-profdata`. One file for amd64 and arm64: the hash of a function depends on the C only. It is checked in, and `make gen` never trains |
| `train.sh` | Trains the profile. Builds the engine as the generator compiles it with counters in, runs `train.sql`, checks it and writes `profile.txt` |
| `train.sql` | What the training runs: the work of a program that uses the driver, each kind of it at least a little. Its first lines say how it is written |
| `train.c` | Runs `train.sql` the way the driver uses the C API: statements prepared once and run many times with bound values, each column read by its type, writes merged as group commit merges them |
| `host.c` | The callbacks of `engine/host.h` in plain C, in the place of the Go host: files, the locks of SQLite between the connections of the process, shared memory for WAL on the heap. `engine/diff` uses it too, with the time fixed (`HOST_NOW_MS`) |
| `cover.awk` | The check of the training that `train.sh` runs (below) |

## Train again

On linux or macOS with LLVM 22.1.8 (`scripts/llvm-bin.sh` finds it, or `LLVM_BIN` names its bin folder):

```sh
engine/pgo/train.sh   # about a minute
make gen ARCH=amd64 && make gen ARCH=arm64
git add engine/pgo/profile.txt internal/csqlite
```

`train.sh` works in `.build/pgo-train` (`TRAIN_OUT` changes it) and needs nothing outside the repository. The profile and the generated files go in one commit, as `make check-gen` wants. When to train again is in [docs/upstream.md](../../docs/upstream.md#the-profile).

`make gen` prints how well the profile fits the C: `profile engine/pgo/profile.txt: 3393 functions, 0 it no longer fits ...`. A function whose branches and loops changed since the training is compiled as without a profile and only loses the gain.

## What the training runs

A count of 0 tells the compiler that a path never runs. It then moves the path out of the way, and a program that does run it pays for that. So the training has to run every kind of work a program may do, and how much of each matters far less. `train.sql` has ten sections:

| Section | The work |
| --- | --- |
| `app` | A WAL database with foreign keys and triggers: inserts that each commit, writes merged as group commit merges them, lookups by key and by index, joins, upserts, `RETURNING`, constraints that refuse a write, a reader beside the writer, checkpoints, transactions and savepoints rolled back, new connections |
| `journal` | A database with a rollback journal in each of its modes: writes, a transaction larger than the cache, `ALTER TABLE`, `ANALYZE`, `REINDEX`, `VACUUM`, incremental vacuum, an attached database, a temp table, `integrity_check` |
| `query` | Queries over 150000 rows: grouping, sorting, window functions, recursive CTEs, subqueries, compound queries, every kind of join, row values, percentiles, generated columns, indexes on expressions, views with `INSTEAD OF` triggers |
| `text` | Text and blob functions, `LIKE` and `GLOB`, `printf`, casts between text and numbers, dates and times |
| `json` | JSON as text and as JSONB: reading, changing, `json_each` and `json_tree`, aggregates, an index on a member |
| `search` | FTS5 with three tokenizers, an external content table kept by triggers, queries of every kind, ranking, `highlight` and `snippet`, updates and deletes, `optimize` |
| `space` | R-tree tables with float and integer coordinates: inserts, range queries, a join, updates and deletes |
| `number` | The math functions, integer and float arithmetic, numbers to text and back |
| `large` | Rows of up to 30 KB in a database larger than its cache: overflow pages, a transaction that changes many pages |
| `schema` | A schema of ten tables with indexes, views and triggers: statements prepared again and again, the schema PRAGMAs, connections opened again and again, changes of the schema |

Nothing in it is taken from the benchmarks of `bench/`. They measure what the profile does to work it was not trained on, so none of their SQL may go into `train.sql`, not even in part.

## The check

`train.sh` checks the training (`cover.awk`) and stops when the check fails:

- It runs the ten workloads of `bench/native/engine.sql`, for the check only: their counts never go into the profile. Of the paths they run at least 100 times, the training must run at least 95% (`COVER_FLOOR`). It prints `engine/pgo/train.sql: runs 3156 of the 3156 paths ... (100.0%, at least 95%)`, and the functions with the most paths the training misses.
- Each kind of work is there for functions listed in `need` in `train.sh`, and the training stops when it no longer runs one of them, or when one no longer exists.

When the C changes, the check can fail. Then add the kind of work the missed paths belong to, as a program would do it, and train again. Lower the floor only with the reason in the commit.

`train.c` prints a line for each section: its name, the statements run, the rows read, the statements a constraint refused, and a sum of every value read. The sums are the same on linux and macOS, so a training that did other work than the last one shows. The profiles of the two differ in a few counts (32 lines of 53000 when it was measured), and so can two trainings on one machine: the reason the profile is checked in.
