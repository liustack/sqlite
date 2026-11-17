---
summary: 'The checks every commit passes, the check of the generated engine, the tests that play a power loss, the tests of the S3 client and the replica, the tests of the capture of changes for replication, the tests of the generator that run LLVM, and the ones that only run on linux/amd64'
read_when:
  - Getting ready to commit
  - Changing the generator, its LLVM pass, engine/ or engine/flags.sh
  - A test of internal/goasm/gen fails
  - Changing how internal/vfs writes, syncs, cuts or deletes a file, or a test of power loss fails
  - Changing replica or replica/internal/s3, or running their tests against MinIO
  - Changing replica/internal/capture or internal/replhook
---

# Testing

## Before committing

```sh
make check
```

It runs, in order: `engine/vendor.sh --verify` and `engine/libc/vendor.sh --verify` (the vendored C is the pinned release), `go build ./...`, `gofmt -l -s` over the Go files git knows or would add (prints nothing, and leaves out `.build/`), `go vet -asmdecl=false ./...`, `scripts/docs-list.sh --check`, `go test -count=1 ./...`, and the tests of the driver, the VFS, the C API and the replicator again under the race detector. With `SQLITE3` set to a sqlite3 shell, `TestInteropShell` has it write the same database as the driver at the same time. CI sets it on every target. The `-asmdecl=false` leaves out the check of the declarations of generated assembly, which the generator's own checks cover.

CI runs `make check`'s tests on the five targets (darwin/amd64 on an arm64 Mac, under Rosetta) with the lowest supported Go and with Go 1.27. `scripts/go-versions.sh` runs them with each Go release the module supports, and the weekly workflow `go-latest` runs it on every target (see [upstream.md](upstream.md#moving-to-a-new-go)).

## Power loss

`TestCrash` kills a process, which loses nothing the process wrote. `TestPowerLoss` and `TestPowerLossGroupCommit` (`powerloss_test.go`, with the disk of `internal/powerloss`) check that each sync comes where SQLite needs it. They keep a log of everything the VFS does to the files of a database while a program writes (`vfs.SetTrace`): each file made or deleted, each write, cut and sync. Then, at many moments of the log, they build the files a power loss at that moment could leave and open them. What was synced is there. Of what was not, nothing arrived, or all of it, or what was done before some moment, or any part: of each write all its sectors, none, the first ones or any of them, a file longer than the data that arrived with anything in the rest, and a name that changed since its directory was synced with its old file, its new one or none.

Each database must pass `integrity_check`. In `TestPowerLoss` it must hold the transactions up to some number, each whole with the bytes it wrote, and that number is at least the last commit that returned, less what the case may lose. In `TestPowerLossGroupCommit` each row must have the bytes it wrote, no row of a goroutine may be there without the ones that goroutine inserted before it, and where commits are durable every row whose merged insert returned must be there. The cases are WAL with `synchronous` FULL and NORMAL, WAL started over by `_wal_limit`, and the rollback journal as DELETE (EXTRA, FULL, NORMAL), TRUNCATE and PERSIST. Three of them hold less, as SQLite documents:

- DELETE with FULL deletes the journal and does not sync its directory, so a power loss can bring back the database of before the last commit, and no older one. EXTRA syncs it.
- WAL with NORMAL syncs the WAL at a checkpoint, so the commits since the last one can be lost.
- DELETE with NORMAL writes the number of records into the header of the journal before it syncs them, and the checksum of a record looks at one byte in 200. A power loss that keeps the header and a part of a record can damage the database. The test found this within 50,000 power losses, so in this case the writes arrive in the order they were made, each whole.

On Windows there is no directory to sync, so the tests take a file made or deleted as on disk at once: they check the syncs of the files there, not what the file system does with names.

`POWERLOSS_POINTS` sets the number of moments (300, 60 with `-short`, and 30 on Windows, where writing and opening the files of each power loss is slow), and `POWERLOSS_KEEP` names a directory that gets the files of a power loss that fails, as they were before they were opened. A change to how the VFS writes or syncs must keep these tests red for a sync left out: taking out the sync of the WAL, of the database, of the journal, of the directory of a new journal, or of the directory after a delete each fails them with 60 moments. The sync of the header of a WAL that starts over is needed only for some orders of arrival: 300 or 500 moments found it missing in 14 runs of 16.

### A machine that replicates

`TestPowerLoss` of `replica` (`replica/powerloss_test.go`, which needs a store, see [below](#the-s3-client-and-the-replica)) plays a power loss on a machine that replicates its database. Its log also holds what the replicator does to its staged files (`capture.SetTrace`: each file made, written, synced, renamed and removed, and each sync of the folder), and each upload and delete the store got and answered, which a proxy writes down. A power loss leaves the files of the database and the staged files as above. The store has what it answered for, and each upload and delete under way arrived or did not. Then:

- The replica alone must restore a database that is sound and at a commit, with every commit a `Sync` returned after.
- `New` must go on from what is there with no error. The database must hold every commit that returned (with `synchronous=FULL`), one more commit and a `Sync` must succeed, and the replica must then restore the database as it is.
- With `synchronous=FULL` no full copy may follow: the database is never behind the staged files. With NORMAL the commits since the last checkpoint can be lost, and a full copy brings the replica back to the database.

The cases are `synchronous` FULL and NORMAL, and FULL over two seconds in which the replicator also compacts into levels 1 and 2, takes snapshots and deletes what retention lets go. `POWERLOSS_POINTS` is 15 here (5 with `-short`), each played four ways, and `POWERLOSS_KEEP` works as above. A change to how the replicator stages, uploads or removes a file must keep the test red for a step left out: without the sync of a staged file, without the sync of the folder after a file gets its name, or when the last staged file is removed once it is uploaded, the test fails within its first moments.

## Out of memory and failed writes

The tests of `oom_test.go` take memory away from SQLite while the driver writes: `csqlite.SetAllocFault` fails the allocations a test picks. Whatever allocation fails, a caller told that its statement committed finds what it wrote, a caller told that it failed finds nothing of it, no statement runs twice, and the file passes `integrity_check`.

- `TestBatchOutOfMemory` runs one batch of group commit with one of its allocations failing, and with every allocation from that one on failing. `TestExecOutOfMemory` runs a statement alone with one of its allocations failing. `TestQueryOutOfMemory` reads rows that way, from a UTF-8 and a UTF-16 database: a row that comes without an error is the row of the database.
- `TestMergeOutOfMemory` has 16 goroutines insert through `database/sql` while one allocation in every few hundred fails.
- `TestFaultThenFTS5` (`spent_test.go`) has two connections take turns inserting into an FTS5 table while one allocation fails, one write fails, one write finds the disk full or an interrupt arrives, and checks that the index still matches the table. `TestSpentConnection`, `TestSpentInTransaction` and `TestSpentBetweenStatements` check the rule that keeps it so, and `TestCheckpointFailureKeepsConnection` what the rule leaves out (see [architecture.md](architecture.md#after-out-of-memory-an-io-error-or-a-full-disk)).

SQLite serves most small allocations of a connection from a pool of its own, which the hook does not see. So these tests also run on a connection that holds 60 prepared statements, which fills the pool: a batch then makes about 400 allocations that can fail instead of 160.

A point is one allocation or one write that fails. Each test first counts the points of its work, then tries `FAULT_POINTS` of them, spread evenly from a first one picked at random: 40 when it is not set, 10 with `-short` or on Windows (where a point takes several times longer), and every point with `FAULT_POINTS=0`, which takes about four minutes. A point that fails does so again with `FAULT_POINTS=0`. A change to `runBatch`, to when a connection is dropped or to how a row is read must keep them red with `FAULT_POINTS=0` for each of the faults they found: going on with a batch whose prepare ended the transaction, preparing the empty text after the last statement of an `Exec`, using a connection again after `SQLITE_NOMEM`, `SQLITE_IOERR` or `SQLITE_FULL`, answering `driver.ErrBadConn` for a later statement of an `Exec`, and giving an empty text for one SQLite had no memory to convert.

## The S3 client and the replica

The tests of `replica` and `replica/internal/s3` that need a store run against MinIO when `MINIO` names a minio binary, and are skipped otherwise. MinIO no longer publishes binaries, so build the release CI uses:

```sh
GOBIN=$PWD/.build go install github.com/minio/minio@RELEASE.2025-09-07T16-13-09Z
MINIO=$PWD/.build/minio go test ./replica/...
```

The tests start it on a free port and stop it. `S3_TEST_ENDPOINT` (with `S3_TEST_REGION`, `S3_TEST_BUCKET`, `S3_TEST_ACCESS_KEY`, `S3_TEST_SECRET_KEY`) runs them against a store that runs already, such as R2. The bucket must exist. The tests that count or break requests go through a proxy on this machine, which signs nothing again: they are skipped against a store that refuses a request signed for another host, as R2 and AWS do. On R2 a test that restores many times takes minutes, so give `go test` a longer `-timeout`. With the MinIO image of that release already pulled, run it in Docker:

```sh
docker run -d --rm --name sqlite-minio -p 127.0.0.1:19000:9000 \
  -e MINIO_ROOT_USER=testaccess -e MINIO_ROOT_PASSWORD=testsecret-0123456789 \
  minio/minio:RELEASE.2025-09-07T16-13-09Z server /data
docker exec sqlite-minio sh -c 'mc alias set local http://127.0.0.1:9000 testaccess testsecret-0123456789 && mc mb local/test'
S3_TEST_ENDPOINT=http://127.0.0.1:19000 S3_TEST_REGION=us-east-1 S3_TEST_BUCKET=test \
  S3_TEST_ACCESS_KEY=testaccess S3_TEST_SECRET_KEY=testsecret-0123456789 go test ./replica/...
docker stop sqlite-minio
```

`TestCreateBucket` only runs against a MinIO the tests start, so it never makes buckets in another store. CI runs them in the job `s3`, where `S3_TEST_REQUIRED` makes a missing store fail instead of skip. `replica/internal/s3test` starts the store for the tests of other packages.

The tests of `replica` rebuild the database from the files in the store, checking every checksum. `TestTwoReplicators` has two replicators with the same history write to one replica: the second must stop with `ErrConflict` at its first file, and the replica must hold the first one's database. `TestStaleReplicator` cuts the second off from the store while the first uploads, compacts and deletes the level 0 files the second would write: once back, the second must stop with `ErrConflict` and upload nothing. `TestRecheckLostAnswer` loses the answer of an upload the store took, then moves the clock past the time of that check: the replicator must see the file is its own and go on. `TestCloseHoldsStaging` starts a second replicator of the database while `Close` uploads what it staged: it must fail until that upload is done. `TestCloseEndsSync` closes while the upload of a Sync hangs: when Close returns, the Sync must have ended, and a Sync after Close must fail. `TestDiverged` checks that a replica written from another database is refused, and taken over with `FreshStart`. The tests of `Restore` restore every TXID and the time after each, refuse a replica with a missing, damaged or wrong file (also one cut where ltx's decoder would panic, and one whose checksums all agree but that `integrity_check` fails), and go on replicating from a restored database. `TestRestoreLevels` restores from a snapshot and compacted files, each TXID a file ends at and each time. `TestCompactLeftovers` leaves the files of a killed compaction, in a folder whose name has glob characters: the next New must remove them. `TestCompactDirMode` checks that only the user can read the folder of a compaction. `TestMergeCanceled` merges with an ended context: the merge must stop with its error, as ltx's Compactor does not look at it. `TestCompactLeftoversLink` names the database through a link to its file: the leftovers must go from the folder of the file. `TestRestoreBadTime` puts a later time in the header of the last file, its checksum left as it was: a restore to a time after every state must fail, not give an older one. `TestRestoreBadTimeLevels` has a level 0 file made after that time and a level 1 file whose time was damaged: the restore must check both and fail. `TestRestoreFileGone` deletes a file the restore planned to read just before it reads it: the restore must plan again and give the same state from the files left. `TestRetention` moves a clock over thirteen hours with a retention of three: after each pass of compaction, snapshots and retention, the replica must restore the database as it was a retention ago and every kept point from the oldest kept snapshot on, and hold no file that no restore or compaction needs. `TestIdle` counts the requests through a proxy: at most about one upload of level 0 each `SyncInterval` while the database is busy, and none once it is idle and the due work is done. `TestKill` kills a process that writes and replicates, ten times: after each kill, 2.5 to 4 seconds after the writes start, the replica must restore a whole database with every transaction entirely there or not at all, and the commits made 2 seconds before the kill (not checked against a store set with `S3_TEST_ENDPOINT`, which may be far away). `-short` skips it. `TestOutage` takes the store down for three seconds under writes, answering 503 and then dropping connections: the writes take no longer, the WAL and the staging folder stay bounded, staging stops at `StagingLimit`, the requests back off, and once the store is back a Sync uploads all that is staged and the replica restores to the database. `TestSyncStopped` checks that Sync goes on from a stopped staging at once, without waiting for the uploads in the background. `TestCloseStagingStopped` closes while staging is stopped at `StagingLimit`, the store back up: the replica must restore the database with what was committed while staging was stopped. `TestCloseStagingStoppedFails` has the first upload of that Close fail: Close must give `ErrStagingFull`, and the next replicator must upload the rest. `TestNoSecrets` collects every error and status a program sees from a store that puts the headers of the request in its errors, and finds neither the secret nor the session token. In `replica/internal/s3`, `TestErrorScrubsRotatedToken` changes the credentials while a request waits for its answer: its error must not have the old token. `TestErrorScrubsEveryField` has the store copy the token into the code, message and request ID of its error, the request ID header, a Content-Range, an upload ID (also in the URL of a request whose connection it drops, escaped) and the size of a listed object: no error may have it. `TestStalled` has the store stop in the middle of an answer, of an error and of an upload it does not read: each request must end with an error once nothing moved for a while. `TestListRepeatedKey` has a list give the same object twice in a row, as MinIO does at times while objects are written: it must come once, and `TestListChecksPages` checks that a key given again for another object is an error, and so are pages that give only repeats on new tokens. `TestSoak` runs for the time `REPLICA_SOAK` gives (such as `10m`) and is skipped without it: writers, a store that goes down at random, a small staging limit and compaction, snapshots and retention every few seconds, then every point the replica still has must restore. `BenchmarkGroupCommit` runs group commit inserts with replication off and on. For a fair figure, run the store on other cores than the benchmark, as a store on another machine would be. With `LITESTREAM` naming a litestream binary (v0.5.18, `go install github.com/benbjohnson/litestream/cmd/litestream@v0.5.18`), `TestLitestreamRestore` has it restore the replica at every TXID and time, and checks that it gives the same bytes as `Restore`. CI sets it.

## Capture

The tests of `replica/internal/capture` check that the staged LTX files rebuild the database. `TestKill` kills a process that writes from six goroutines and stages, twelve times: after each kill the staged files must rebuild a whole database with every transaction entirely there or not at all, nothing of a transaction rolled back after it spilled to the WAL, and the commits made 300 ms before the kill, and the next process must go on from them without a full copy. With `SQLITE3` set, `TestOtherProcess` has the sqlite3 shell write and checkpoint the database: what it appends is staged as usual, and a checkpoint it runs must lead to a full copy. `TestCheckpointLost` does the same from a connection of the test that is not the driver's, so it runs without the shell. `TestStagingLimit` uploads nothing until staging stops at `Options.Limit`: the WAL must stay bounded meanwhile, and once all is uploaded a full copy must follow and rebuild the database. `TestStagingLimitLost` has another process checkpoint after each change, so that each step stages a full copy: those count, so staging stops all the same. `TestStagingLimitReopen` closes and opens five times with nothing uploaded, the WAL started over each time: Open must not stage a full copy past the limit, and once all is uploaded one must follow. `TestStagingLimitLarge` opens a database larger than the limit twice, nothing uploaded: its full copy must not stop staging, also after the restart. `TestStagingLimitSyncDir` stops with a staged file whose folder did not sync, and `TestStagingLimitStep` passes the limit at the read of a step: that step must stage no more. `TestSyncDirIdle` has the folder fail to sync and then nothing committed: the next step must make the staged file durable. `TestCheckpointAtCommit` writes with no step: the writes must keep the WAL at about `CheckpointFrames` frames by staging and checkpointing as they commit, and while the staging folder fails to sync they must checkpoint nothing. `TestStagingLimitAtCommitSyncDir` passes the limit at a write while the folder of the last staged file has not synced: the WAL must not start over before it does. `TestCheckpointAtCommitOldReader` keeps an old snapshot open across those checkpoints, and `TestCloseDuringCommitted` closes while a write is inside the capturer. `TestCloseReportsLater` closes with the error of a write not yet reported: Close must give it to `OnError` with no lock held, as `OnError` here calls the capturer back. `TestReportUnlocked` has `OnError` call the capturer when a step stages a full copy: the step must not wait for it. `TestVerifyTruncated` cuts a staged file at every length: `Verify` must give an error each time, not a panic. `TestOpenSyncsFolders` checks that the first Open syncs each folder it made for staging, up to the database's. `TestOpenStagedElsewhere` holds the lock of the staging as another process would: Open must fail and leave the staging as it is. `TestCloseStagingStopped` closes with staging stopped at the limit, also while a write transaction keeps it from checkpointing: Close must give `ErrStagingFull`. `TestCloseHolds` closes with `Hold`: the staging must stay locked until `Unlock`. `TestCloseDuringSync` closes while a Sync waits for a write transaction: Close must not wait for it. `TestSyncWaitsBounded` has a Sync wait behind that one: it must give up at its deadline. `-short` skips `TestKill`.

## The generated engine

`internal/csqlite` holds the engine generated from `engine/`. After a change to `engine/` (the profile `engine/pgo/profile.txt` is part of it), the generator or `scripts/gen-engine.sh`, run `make gen ARCH=amd64` and `make gen ARCH=arm64` and commit the files they write with the change. Then:

```sh
make check-gen
```

generates both again, fails if `internal/csqlite` differs from what is checked in, and runs the post-link check on a linux binary of the package for each target: every generated function and `engine_rodata` held to llc's object.

## The generator

Without LLVM, `go test ./internal/goasm/...` runs only the rule tests of the generator. With LLVM 22.1.8 installed:

```sh
make test-gen
```

builds the pass into `.build/shadow.so` and runs the tests again with `GEN_LLVM` (the LLVM bin folder, from `scripts/llvm-bin.sh`) and `GEN_SHADOW_PASS` (the pass) set. `GEN_LLD` (an `ld.lld`) adds the tests that link and run samples natively. On macOS, LLVM comes from Homebrew (`brew install llvm@22`).

The tests that link and run samples (`TestSampleRuns`, `TestVarargsNative`, `TestNarrowArgs`) need `GEN_LLD` and a linux/amd64 machine, and are skipped anywhere else. CI has no LLVM and runs none of the tests of this section. So after a change to the generator or its pass, run `make test-gen` with `GEN_LLD` set on a linux/amd64 machine as well as on the Mac.
