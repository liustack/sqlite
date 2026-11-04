#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# check-gen.sh [ARCH ...]: generate the engine again for each ARCH (default
# amd64 and arm64), fail if internal/csqlite is not what is checked in, and
# hold every generated function and engine_rodata to llc's object in a
# linux binary of the package (the post-link check). The binary is built with
# sqlite_anygo, since this is the check that clears a newer Go
# (docs/upstream.md). Needs LLVM 22.1.8, like gen-engine.sh.
set -eu
cd "$(dirname "$0")/.."
archs=${*:-amd64 arm64}
llvm=$(scripts/llvm-bin.sh)
for arch in $archs; do
	GEN_JOBS=${GEN_JOBS:-4} scripts/gen-engine.sh "$arch"
	bin=.build/csqlite-$arch.test
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch go test -c -tags goasmkeep,sqlite_anygo -o "$bin" ./internal/csqlite
	.build/engine-gen -release -engine engine -llvm "$llvm" -pass .build/shadow.so -arch "$arch" -mode pn \
		-varargs-block -shadow-size-log2=25 -pkg csqlite -work ".build/w-$arch" -out internal/csqlite \
		-j "${GEN_JOBS:-4}" -verify-bin "$bin" -verify-pkg github.com/liustack/sqlite/internal/csqlite -verify-all
done
if ! git diff --quiet -- internal/csqlite || [ -n "$(git ls-files --others --exclude-standard internal/csqlite)" ]; then
	echo "check-gen.sh: internal/csqlite is not what engine/ generates: commit the generated files with the change" >&2
	git status --short internal/csqlite >&2
	exit 1
fi
