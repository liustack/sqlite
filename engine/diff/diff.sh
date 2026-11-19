#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# diff.sh: build engine/diff/diff.c, the C side of TestDiffC, into
# .build/diff/diff (DIFF_OUT changes the folder). The engine and its libc are
# compiled as the generator compiles them (engine/native.sh), with LLVM
# 22.1.8, and the host callbacks are engine/pgo/host.c with the time fixed.
# Then, with the fuzz cases of go run scripts/fuzzdata.go:
#
#   DIFFC=$PWD/.build/diff/diff FUZZDATA=$PWD/.build/fuzzdata \
#     go test -run TestDiffC ./internal/csqlite
set -eu

cd "$(dirname "$0")/../.."
. engine/native.sh
native_setup diff.sh
out=${DIFF_OUT:-.build/diff}
rm -rf "$out"
native_engine "$out"
# 2026-01-01 00:00:00 UTC, which diff prints for the Go side to use.
now=-DHOST_NOW_MS=1767225600000LL
$cc -O2 -Wall $now -I engine -c -o "$out/host.o" engine/pgo/host.c
$cc -O2 -Wall $now -I engine -c -o "$out/diff.o" engine/diff/diff.c
$cc -o "$out/diff" "$out/diff.o" "$out/host.o" "$out"/obj/*.o -lpthread
echo "$out/diff"
