---
summary: 'The checks every commit passes, the check of the generated engine, the tests of the S3 client, the tests of the capture of changes for replication, the tests of the generator that run LLVM, and the ones that only run on linux/amd64'
read_when:
  - Getting ready to commit
  - Changing the generator, its LLVM pass, engine/ or engine/flags.sh
  - A test of internal/goasm/gen fails
  - Changing replica/internal/s3, or running its tests against MinIO
  - Changing replica/internal/capture or internal/replhook
---

# Testing

## Before committing

```sh
make check
```

It runs, in order: `engine/vendor.sh --verify` and `engine/libc/vendor.sh --verify` (the vendored C is the pinned release), `go build ./...`, `gofmt -l -s` over the Go files git knows or would add (prints nothing, and leaves out `.build/`), `go vet -asmdecl=false ./...`, `scripts/docs-list.sh --check`, `go test -count=1 ./...`, and the tests of the driver, the VFS, the C API and the replicator again under the race detector. With `SQLITE3` set to a sqlite3 shell, `TestInteropShell` has it write the same database as the driver at the same time. CI sets it on every target. The `-asmdecl=false` leaves out the check of the declarations of generated assembly, which the generator's own checks cover.

CI runs `make check`'s tests on the five targets (darwin/amd64 on an arm64 Mac, under Rosetta) with the lowest supported Go and with Go 1.27. `scripts/go-versions.sh` runs them with each Go release the module supports, and the weekly workflow `go-latest` runs it on every target (see [upstream.md](upstream.md#moving-to-a-new-go)).

## The S3 client

The tests of `replica/internal/s3` that need a store run against MinIO when `MINIO` names a minio binary, and are skipped otherwise. MinIO no longer publishes binaries, so build the release CI uses:

```sh
GOBIN=$PWD/.build go install github.com/minio/minio@RELEASE.2025-09-07T16-13-09Z
MINIO=$PWD/.build/minio go test ./replica/...
```

The tests start it on a free port and stop it. `S3_TEST_ENDPOINT` (with `S3_TEST_REGION`, `S3_TEST_BUCKET`, `S3_TEST_ACCESS_KEY`, `S3_TEST_SECRET_KEY`) runs them against a store that runs already, such as R2. CI runs them in the job `s3`, where `S3_TEST_REQUIRED` makes a missing store fail instead of skip.

## Capture

The tests of `replica/internal/capture` check that the staged LTX files rebuild the database. `TestKill` kills a process that writes from six goroutines and stages, twelve times: after each kill the staged files must rebuild a whole database with every transaction entirely there or not at all, nothing of a transaction rolled back after it spilled to the WAL, and the commits made 300 ms before the kill, and the next process must go on from them without a full copy. With `SQLITE3` set, `TestOtherProcess` has the sqlite3 shell write and checkpoint the database: what it appends is staged as usual, and a checkpoint it runs must lead to a full copy. `TestCheckpointLost` does the same from a connection of the test that is not the driver's, so it runs without the shell. `-short` skips `TestKill`.

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
