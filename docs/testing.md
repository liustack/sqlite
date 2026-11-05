---
summary: 'The checks every commit passes, the check of the generated engine, the tests of the S3 client and the replica, the tests of the capture of changes for replication, the tests of the generator that run LLVM, and the ones that only run on linux/amd64'
read_when:
  - Getting ready to commit
  - Changing the generator, its LLVM pass, engine/ or engine/flags.sh
  - A test of internal/goasm/gen fails
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

## The S3 client and the replica

The tests of `replica` and `replica/internal/s3` that need a store run against MinIO when `MINIO` names a minio binary, and are skipped otherwise. MinIO no longer publishes binaries, so build the release CI uses:

```sh
GOBIN=$PWD/.build go install github.com/minio/minio@RELEASE.2025-09-07T16-13-09Z
MINIO=$PWD/.build/minio go test ./replica/...
```

The tests start it on a free port and stop it. `S3_TEST_ENDPOINT` (with `S3_TEST_REGION`, `S3_TEST_BUCKET`, `S3_TEST_ACCESS_KEY`, `S3_TEST_SECRET_KEY`) runs them against a store that runs already, such as R2. The bucket must exist. With the MinIO image of that release already pulled, run it in Docker:

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

The tests of `replica` rebuild the database from the files in the store, checking every checksum. `TestTwoReplicators` has two replicators with the same history write to one replica: the second must stop with `ErrConflict` at its first file, and the replica must hold the first one's database. `TestDiverged` checks that a replica written from another database is refused, and taken over with `FreshStart`. The tests of `Restore` restore every TXID and the time after each, refuse a replica with a missing, damaged or wrong file (also one whose checksums all agree but that `integrity_check` fails), and go on replicating from a restored database. `TestRestoreLevels` restores from a snapshot and compacted files, each TXID a file ends at and each time. `TestRetention` moves a clock over thirteen hours with a retention of three: after each pass of compaction, snapshots and retention, the replica must restore the database as it was a retention ago and every kept point from the oldest kept snapshot on, and hold no file that no restore or compaction needs. `TestIdle` counts the requests through a proxy: at most about one upload of level 0 each `SyncInterval` while the database is busy, and none once it is idle and the due work is done. `TestKill` kills a process that writes and replicates, ten times: after each kill the replica must restore a whole database with every transaction entirely there or not at all, and the commits made a second before the kill. `-short` skips it. `TestOutage` takes the store down for three seconds under writes, answering 503 and then dropping connections: the writes take no longer, the WAL and the staging folder stay bounded, staging stops at `StagingLimit`, the requests back off, and once the store is back a Sync uploads all that is staged and the replica restores to the database. `TestSyncStopped` checks that Sync goes on from a stopped staging at once, without waiting for the uploads in the background. `TestNoSecrets` collects every error and status a program sees from a store that puts the headers of the request in its errors, and finds neither the secret nor the session token. In `replica/internal/s3`, `TestErrorScrubsRotatedToken` changes the credentials while a request waits for its answer: its error must not have the old token. `TestSoak` runs for the time `REPLICA_SOAK` gives (such as `10m`) and is skipped without it: writers, a store that goes down at random, a small staging limit and compaction, snapshots and retention every few seconds, then every point the replica still has must restore. `BenchmarkGroupCommit` runs group commit inserts with replication off and on. For a fair figure, run the store on other cores than the benchmark, as a store on another machine would be. With `LITESTREAM` naming a litestream binary (v0.5.18, `go install github.com/benbjohnson/litestream/cmd/litestream@v0.5.18`), `TestLitestreamRestore` has it restore the replica at every TXID and time, and checks that it gives the same bytes as `Restore`. CI sets it.

## Capture

The tests of `replica/internal/capture` check that the staged LTX files rebuild the database. `TestKill` kills a process that writes from six goroutines and stages, twelve times: after each kill the staged files must rebuild a whole database with every transaction entirely there or not at all, nothing of a transaction rolled back after it spilled to the WAL, and the commits made 300 ms before the kill, and the next process must go on from them without a full copy. With `SQLITE3` set, `TestOtherProcess` has the sqlite3 shell write and checkpoint the database: what it appends is staged as usual, and a checkpoint it runs must lead to a full copy. `TestCheckpointLost` does the same from a connection of the test that is not the driver's, so it runs without the shell. `TestStagingLimit` uploads nothing until staging stops at `Options.Limit`: the WAL must stay bounded meanwhile, and once all is uploaded a full copy must follow and rebuild the database. `TestStagingLimitLost` has another process checkpoint after each change, so that each step stages a full copy: those count, so staging stops all the same. `TestStagingLimitLarge` opens a database larger than the limit twice, nothing uploaded: its full copy must not stop staging, also after the restart. `TestStagingLimitSyncDir` stops with a staged file whose folder did not sync, and `TestStagingLimitStep` passes the limit at the read of a step: that step must stage no more. `TestSyncDirIdle` has the folder fail to sync and then nothing committed: the next step must make the staged file durable. `TestCheckpointAtCommit` writes with no step: the writes must keep the WAL at about `CheckpointFrames` frames by staging and checkpointing as they commit, and while the staging folder fails to sync they must checkpoint nothing. `TestStagingLimitAtCommitSyncDir` passes the limit at a write while the folder of the last staged file has not synced: the WAL must not start over before it does. `TestCheckpointAtCommitOldReader` keeps an old snapshot open across those checkpoints, and `TestCloseDuringCommitted` closes while a write is inside the capturer. `TestCloseReportsLater` closes with the error of a write not yet reported: Close must give it to `OnError` with no lock held, as `OnError` here calls the capturer back. `TestReportUnlocked` has `OnError` call the capturer when a step stages a full copy: the step must not wait for it. `-short` skips `TestKill`.

## The generated engine

`internal/csqlite` holds the engine generated from `engine/`. After a change to `engine/`, the generator or `scripts/gen-engine.sh`, run `make gen ARCH=amd64` and `make gen ARCH=arm64` and commit the files they write with the change. Then:

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

Two tests only pass on a linux/amd64 machine, since they assemble x86 ELF code with the clang of the host: `TestEmitLeafFrame` and `TestNegativeBuild`. On darwin/arm64 they fail the same way in slowjs.
