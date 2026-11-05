---
summary: 'How to keep a copy of a database in S3-compatible storage with the replica package, restore it, and what replication changes for the program, its limits included'
read_when:
  - Setting up replication of a database, or restoring one
  - Asked what the replicator guarantees, or what happens while the store is down
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
- **The database is in WAL mode,** and the replicator runs its checkpoints. While it runs, `PRAGMA wal_checkpoint`, setting `wal_autocheckpoint`, a `journal_mode` other than WAL and `locking_mode=EXCLUSIVE` give an error. The replicator checkpoints once the WAL holds 4000 frames, about 16 MB with 4 KiB pages. As with any checkpoint, frames a read transaction still uses stay, so a read that never ends lets the WAL grow.
- **Only this driver, in this process, is followed.** Open the database with this driver after `New`. If another process (the sqlite3 shell, another program) writes and checkpoints the file, the replicator notices that the WAL went on in a way it did not see, stages a full copy of the database, and reports it to `OnError`. Nothing is lost, but a full copy costs an upload of the whole file.
- **One process replicates a database file at a time.** A second `New` on the same file fails while the first one runs.
- **Disk:** the staging folder holds what is not uploaded yet and the last full copy of the database. Staging stops once what waits passes `StagingLimit`, so the last copy into it (a large transaction) can take it further, and snapshots and merges use the folder for a while too. A snapshot is one copy of the database. A merge downloads all its input files, up to 256, before it writes its output beside them, so a database that rewrites most of its pages every minute may need many copies of itself for the hourly merge. Leave room for `StagingLimit`, the largest transaction, a snapshot and the largest merge.
- **CPU:** copying each commit costs a little on the writer's side. [Benchmarks](benchmarks.md#replication) has the numbers.

## How far behind the copy is

With the store up and nothing waiting, the copy is usually `SyncInterval` plus one upload behind the last commit. Files go up one at a time, and wait while a merge or a snapshot runs, so after an outage, or on a store far away, the copy can fall further behind. When a write must be in the store before you go on, call `r.Sync(ctx)`: once it returns nil, what was committed before the call is in the store.

`r.Close()` copies what is left, checkpoints, and tries for 10 seconds to upload it. What it could not upload stays staged, and the next `New` uploads it. While staging is stopped at `StagingLimit`, Close does not copy what was committed since, and a later `New` uploads it as a full copy: call `Sync` before `Close` to have it in the store.

`r.Status()` says where replication is: the last TXID staged, the last TXID in the store, the bytes waiting, and the error of the last upload. A TXID numbers the files of the replica, as Litestream counts them: each copy into the staging folder is one TXID, and holds every commit since the copy before. A restore point is the end of a file, not each commit.

## While the store is down

Writes go on, and changes pile up in the staging folder. Uploads are tried again, longer after each failure, up to a minute apart. Past `StagingLimit`, staging stops and `OnError` gets an error under `replica.ErrStagingFull`. Checkpoints go on all the same, so the WAL does not grow because the store is down. Once the store answers and what was staged is uploaded, a full copy of the database follows, and staging goes on from it. The changes made while staging was stopped have no point of their own to restore to. If the machine is lost while the store is down, what was not uploaded is lost with it: no design keeps it while the store cannot be reached.

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

A database that is idle makes no request once this work is done. A busy one uploads a file each `SyncInterval`, one each time the WAL reaches 4000 frames, and one for each `Sync`, plus the merges.

`litestream restore` v0.5.18 rebuilds the same database from it at every point the replica can still be restored to, by TXID, and by any time both accept. The tests check this ([testing.md](testing.md#the-s3-client-and-the-replica)).

## Stores

The replicator is tested against MinIO. AWS S3 and Cloudflare R2 support the conditional writes it needs. Any store that speaks the S3 API, signs with SigV4 and supports `If-None-Match: *` on PUT and on the completion of a multipart upload works the same. For a store without it, set `SingleWriter` and make sure yourself that one replicator writes.
