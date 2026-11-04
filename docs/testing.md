---
summary: 'The checks every commit passes, the check of the generated engine, the tests of the generator that run LLVM, and the ones that only run on linux/amd64'
read_when:
  - Getting ready to commit
  - Changing the generator, its LLVM pass, engine/ or engine/flags.sh
  - A test of internal/goasm/gen fails
---

# Testing

## Before committing

```sh
make check
```

It runs, in order: `engine/vendor.sh --verify` and `engine/libc/vendor.sh --verify` (the vendored C is the pinned release), `go build ./...`, `gofmt -l -s` over the Go files git knows or would add (prints nothing, and leaves out `.build/`), `go vet -asmdecl=false ./...`, `scripts/docs-list.sh --check`, `go test -count=1 ./...`, and the tests of the driver, the VFS and the C API again under the race detector. With `SQLITE3` set to a sqlite3 shell, `TestInteropShell` has it write the same database as the driver at the same time. CI sets it on every target. `scripts/go-versions.sh` runs the tests again with each Go release the module supports (see [upstream.md](upstream.md#moving-to-a-new-go)). The `-asmdecl=false` leaves out the check of the declarations of generated assembly, which the generator's own checks cover.

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
