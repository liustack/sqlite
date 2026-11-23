---
summary: 'How to keep a copy of a database in S3-compatible storage with the replica package, restore it, what happens when the machine is lost and what the store costs, what replication changes for the program, and its limits'
read_when:
  - Setting up replication of a database, or restoring one
  - Asked what the replicator guarantees, or what happens while the store is down
  - Asked what a lost machine means for the data, or what replication costs on R2
  - Moving a replicated database to another machine
  - Changing the user-facing behavior of replica
---

# Replication

English · [简体中文](replication.zh-CN.md)

The `replica` package keeps a copy of a database in S3-compatible storage (AWS S3, Cloudflare R2, MinIO and the like), a second or so behind the live file, so that a lost machine costs seconds of writes, not the database. It runs inside your program: there is no other process to deploy. The copy is laid out as Litestream lays it out, so `litestream restore` can rebuild it too.

## Start

Start the replicator before the program opens the database, and close it after:

```go
r, err := replica.New(ctx, "app.db", replica.Config{
	URL: "s3://my-bucket/app.db?endpoint=https://<account>.r2.cloudflarestorage.com&region=auto",
})
if err != nil {
	return err
}
defer r.Close()

db, err := sql.Open("sqlite", "app.db")
```

To replicate several databases, call `New` once for each file, each with a URL of its own, and open each file with `sql.Open` after its `New`. Each replicator has its own goroutines, staging folder and connection to the store.

`URL` is `s3://bucket/path`. The query takes `endpoint` (default `https://s3.<region>.amazonaws.com`) and `region` (default `AWS_REGION`, else `us-east-1`). R2 takes `region=auto`. A plain `http://` endpoint is refused unless `AllowHTTP` is set, for a local MinIO.

Credentials come from `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` unless `Config.Credentials` gives them: `replica.StaticCredentials` for fixed keys, or your own `CredentialsProvider`, such as the AWS SDK's provider of an instance role, wrapped. No error, status or log line of the replicator shows the secret or the session token.

The settings you are most likely to change:

| Setting | Default | What it does |
| --- | --- | --- |
| `SyncInterval` | 1s | How often what was committed is copied into the staging folder |
| `Retention` | 24h | How far back a restore can go at least. A snapshot of the database is uploaded every `Retention` or every day, the more often |
| `StagingLimit` | 1 GiB | Staging stops once the changes staged and not uploaded yet pass it, such as while the store is down |
| `OnError` | none | Gets the errors of the work done in the background. Log them |

## What changes for your program

- **Writes never wait for S3.** A commit is copied into a staging folder beside the database, `app.db-replica/`, and uploaded from there in the background. A store that is slow or down never slows a write or makes it fail.
- **The database is in WAL mode,** and the replicator runs its checkpoints. While it runs, `PRAGMA wal_checkpoint`, setting `wal_autocheckpoint`, a `journal_mode` other than WAL and `locking_mode=EXCLUSIVE` give an error. The replicator checkpoints each time the WAL has grown by 4000 frames, about 16 MB with 4 KiB pages. Once the WAL is past `_wal_limit` (64 MiB by default), its checkpoint waits up to 100 ms for the readers so that the WAL can start over, as the driver does for a database that is not replicated (see [architecture.md](architecture.md#a-limit-on-the-length-of-the-wal)). Frames a read transaction still uses stay, so a read that never ends lets the WAL grow. The replicator reads the whole database for a snapshot and for a full copy, and while it does the WAL grows by what is written, as with any other reader: on a large database or a slow disk, by more than 4000 frames.
- **Only this driver, in this process, is followed.** Open the database with this driver after `New`. If another process (the sqlite3 shell, another program) writes and checkpoints the file, the replicator notices that the WAL went on in a way it did not see, stages a full copy of the database, and reports it to `OnError`. Nothing is lost, but a full copy costs an upload of the whole file.
- **A failed sync of the WAL costs a full copy.** The writes cut from the WAL may have been uploaded already, so once the WAL works again the replicator stages a full copy of the database and reports it to `OnError`. See [architecture.md](architecture.md#when-a-sync-of-the-wal-fails).
- **One process replicates a database file at a time.** A second `New` on the same file fails while the first one runs.
- **A replicated file cannot be attached.** `ATTACH` of it fails, from a connection of another database or of the file itself: writes through it would go around the replicator. The other way round, `New` fails while a connection has the file attached, as it does while one has it open.
- **Disk:** the staging folder holds what is not uploaded yet and the last full copy of the database. Staging stops once what waits passes `StagingLimit`, so the last copy into it (a large transaction) can take it further, and snapshots and merges use the folder for a while too. A snapshot is one copy of the database. A merge downloads all its input files, up to 256, before it writes its output beside them, so a database that rewrites most of its pages every minute may need many copies of itself for the hourly merge. Leave room for `StagingLimit`, the largest transaction, a snapshot and the largest merge.
- **CPU:** copying each commit costs a little on the writer's side. [Benchmarks](benchmarks.md#replication) has the numbers.

## How far behind the copy is

With the store up and nothing waiting, the copy is usually `SyncInterval` plus one upload behind the last commit. Files go up one at a time, and wait while a merge or a snapshot runs, so after an outage, or on a store far away, the copy can fall further behind. When a write must be in the store before you go on, call `r.Sync(ctx)`: once it returns nil, what was committed before the call is in the store.

`r.Close()` copies what is left, checkpoints, and tries for 10 seconds to upload it. What it could not upload stays staged, and the next `New` uploads it. While staging is stopped at `StagingLimit`, Close first uploads what was staged, then stages a full copy with what was committed since, and uploads it in the same 10 seconds. If the store is still down, Close gives an error under `replica.ErrStagingFull`, and the next `New` stages and uploads that full copy.

`r.Status()` says where replication is: the last TXID staged, the last TXID in the store, the bytes waiting, and the error of the last upload. A TXID numbers the files of the replica, as Litestream counts them: each copy into the staging folder is one TXID, and holds every commit since the copy before. A restore point is the end of a file, not each commit.

## When the machine is lost

A failed disk is covered by the RAID or the replicated volume under your VPS. The replica is for the rarer case, the whole machine gone with its disks: a new machine restores the database from it, with every commit up to about the last second. The commits of that last second, if there were any, are the ones not uploaded yet.

Measured with `TestLostOnKill` (see [bench/README.md](../bench/README.md)): a process replicates to Cloudflare R2 with the default settings while goroutines insert, and is killed with SIGKILL, which stops its uploads at the moment a lost machine would. The replica is then restored on the same machine and compared with what the process had acknowledged. Apple M4, a home connection to R2, 10 s of writes before each kill:

| Writes a second | Kills | Acknowledged writes missing | Newest write in the replica, before the kill | Restore |
| --: | --: | --: | --: | --: |
| 100 | 5 | 84 to 94, and 290 once | 0.86 to 0.95 s, and 2.9 s once | 4.0 to 5.6 s |
| 1,000 | 5 | 867 to 951 | 0.87 to 0.95 s | 6.7 to 7.3 s |

The one longer loss came from one upload that took longer than the others. The window holds while uploads keep up with the writes: at 1,000 small rows a second the WAL grows by about 0.1 MB/s. Writing as fast as 64 goroutines can (250,000 to 330,000 rows a second, 30 to 40 MB/s), the home connection fell behind, 300 to 390 MB waited at the kill, and nearly all of the 10 s was lost. A VPS has more bandwidth to its store than a home connection, but the store must still take what the database writes.

For comparison, managed Postgres in its default setup:

- **Amazon RDS in one Availability Zone** uploads its transaction log every 5 minutes, so losing the instance typically costs up to 5 minutes, and the restore is started by hand and takes from about 10 minutes to hours ([AWS](https://aws.amazon.com/blogs/database/amazon-rds-under-the-hood-single-az-instance-recovery)).
- **Supabase** backs up once a day unless you pay for point-in-time recovery, whose worst case is 2 minutes ([Supabase](https://supabase.com/docs/guides/platform/backups)).
- **A synchronous standby** (RDS Multi-AZ) costs a second instance and slower commits, and still loses commits when the replication falls back to asynchronous or a wait for it is cut short ([MySQL](https://dev.mysql.com/doc/refman/8.4/en/replication-semisync.html), [PostgreSQL](https://postgresql.org/message-id/FDE157D7-3F35-450D-B927-7EC2F82DB1D6%40amazon.com)).

When a write must not be lost even then, call `r.Sync(ctx)` before you answer the request: once it returns nil, the write is in the store.

### What it costs on R2

A database that is written to uploads about one file a second while writes go on, and nothing while it is idle, plus a merge each minute and each hour. R2 counts each upload as a Class A operation, with 1 million a month free and $4.50 a million after that, and does not charge for downloads ([R2 pricing](https://developers.cloudflare.com/r2/pricing)). A database written to in every second of a 30-day month makes about 2.6 million uploads, about $7. One written to in fewer than about 11 days' worth of seconds stays in the free tier, and so does any database with `SyncInterval` set to 3 s, which makes the window about 3 s.

## While the store is down

Writes go on, and changes pile up in the staging folder. Each request that fails with an error that may pass (no answer, a 5xx, 429) is sent again up to twice, within a second or two, so a store that drops a connection now and then or answers 503 for a moment does not fail the work. Past that, the work is tried again, longer after each failure, up to a minute apart. Past `StagingLimit`, staging stops and `OnError` gets an error under `replica.ErrStagingFull`. Checkpoints go on all the same, so the WAL does not grow because the store is down. Once the store answers and what was staged is uploaded, a full copy of the database follows, and staging goes on from it. The changes made while staging was stopped have no point of their own to restore to. If the machine is lost while the store is down, what was not uploaded is lost with it: no design keeps it while the store cannot be reached.

## Restore

```go
txid, err := replica.Restore(ctx, "app.db", replica.Config{URL: url}, replica.RestoreOptions{})
```

`RestoreOptions` picks the state: the latest (the default), the one at a `TXID`, or the one before a time (`At`). Restore downloads the newest snapshot that fits and the files after it, checks every file it uses (its own checksum, the checksum of the database after it, and of the database before it when it starts where the file before ended), runs `PRAGMA integrity_check` on the result, and only then puts the database in place. It never writes over a database that is there, nor beside a WAL, a journal or a staging folder of another database.

Restore leaves the last file of the replica in the staging folder, so that `New` goes on from the replica. After a restore to the latest state, `New` goes on with no full copy. After a restore to an earlier state, the database and the replica went apart, and `New` fails with `replica.ErrDiverged` unless `Config.FreshStart` is set, which uploads a full copy as the replica's next TXID.

## Limits

### One replicator per replica

A replica must have one replicator writing to it.

- **For one database file on one machine this is enforced.** A second `New` on the same file gets an error. It does not stop two different files, such as a copy of the database, from replicating to the same URL.
- **Across machines it is your program's job.** S3 has no way to let only the holder of a lease write, so no replicator can enforce it there, Litestream included.
- **What the replicator does find.** Every file is uploaded only if its key is free, so a second replicator from the same history is refused at its first upload, and stops with `replica.ErrConflict`. Before an upload, when it has not checked for 5 minutes, the replicator also checks that the replica still ends where it left it. Together these catch a second replicator in normal running and after one of them could not reach the store.
- **What it can miss.** Some races, such as an old machine that still runs while a new one takes over, and whose files the new one compacted and deleted at the wrong moment. Two histories can then end up in the replica. A restore checks every checksum, so it never builds a mix of the two, but it may fail, or bring back the old machine's history.
- **So, when a database moves to another machine:** stop the old replicator before the new one starts, with a lock or lease of your own, and set `Config.SingleWriter`.

`New` checks that the store refuses a second write of a key (`If-None-Match`). A store that does not is refused with `replica.ErrNoConditionalWrites`, unless `SingleWriter` says your program makes sure there is one writer.

### Restoring to a time

`At` picks files by the time in their headers, which comes from the clock of the machine that staged them. Restore checks the first file it leaves out of each level and trusts the times of the files after it, as `litestream restore` does. If that clock went back, a restore to a time may give a state a little before or after the one asked for. A restore to a TXID or to the latest state does not use the times.

### What is left out

- No client-side encryption: it would make the replica unreadable by `litestream restore`. Data goes over HTTPS, and the bucket's own encryption at rest applies.
- No read replicas, no other kind of storage, no command line tool.
- Replication is asynchronous: a commit returns before it reaches the store.

## What the store holds

The replica of `s3://bucket/path` is LTX files under `path/`: level 0 holds one file per copy into staging, level 1 merges the new level 0 files each minute, level 2 merges level 1 each hour, and level 9 holds the snapshots. Level 0 files go 30 minutes after a level 1 file holds them. From then on, a TXID inside that level 1 file can no longer be restored, and a restore to a time inside it gives the state at the end of the last file made before that time: usually up to a minute earlier, more after an outage, when one merge takes all that was staged meanwhile. The replica keeps the newest snapshot made before `Retention` ago and every one after it, and deletes the older ones with the files only they needed.

A database that is idle makes no request once this work is done. A busy one uploads a file each `SyncInterval`, one each time the WAL has grown by 4000 frames, and one for each `Sync`, plus the merges.

`litestream restore` v0.5.18 rebuilds the same database from it at every point the replica can still be restored to, by TXID, and by any time both accept. The tests check this ([testing.md](testing.md#the-s3-client-and-the-replica)).

## Stores

The replicator is tested against MinIO. AWS S3 and Cloudflare R2 support the conditional writes it needs. Any store that speaks the S3 API, signs with SigV4 and supports `If-None-Match: *` on PUT and on the completion of a multipart upload works the same. For a store without it, set `SingleWriter` and make sure yourself that one replicator writes.
