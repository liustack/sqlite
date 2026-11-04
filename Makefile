# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.

.PHONY: bench bench-docs check check-gen gen pass test-gen vendor-check

# The checks every commit passes (docs/testing.md).
check: vendor-check
	go build ./...
	test -z "$$(git ls-files --cached --others --exclude-standard '*.go' | xargs gofmt -l -s)"
	go vet -asmdecl=false ./...
	scripts/docs-list.sh --check >/dev/null
	go test -count=1 ./...
	go test -race -count=1 . ./internal/vfs ./internal/csqlite
	cd bench && go vet -tags liustack ./... && go run ./cmd/benchrun docs -check

# The C of engine/ as Go assembly, on a machine with LLVM 22.1.8: make gen,
# make gen ARCH=amd64. ARCH defaults to the machine's.
gen:
	scripts/gen-engine.sh $(ARCH)

# The checked-in engine is what engine/ generates, and passes the post-link
# check on both targets (docs/testing.md).
check-gen:
	scripts/check-gen.sh

# The LLVM pass alone, into .build/shadow.so.
pass:
	scripts/gen-engine.sh pass

# The tests of the generator that run LLVM (docs/testing.md).
test-gen: pass
	GEN_LLVM=$$(scripts/llvm-bin.sh) GEN_SHADOW_PASS=$(CURDIR)/.build/shadow.so \
		go test -count=1 -timeout 30m ./internal/goasm/...

# The driver benchmarks (bench/README.md), for example
# make bench ARGS="-count 2 -drivers liustack,mattn".
bench:
	cd bench && go run ./cmd/benchrun run $(ARGS)

# The tables of docs/benchmarks*.md and the READMEs, from bench/published.
bench-docs:
	cd bench && go run ./cmd/benchrun docs

# The vendored C is what its pinned release has.
vendor-check:
	engine/vendor.sh --verify
	engine/libc/vendor.sh --verify
