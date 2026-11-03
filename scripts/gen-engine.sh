#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# gen-engine.sh [ARCH]: generate the C of engine/ as Go assembly (make gen). Run
# it from anywhere inside the repository on a machine with LLVM 22.1.8. ARCH is
# arm64 or amd64 (default: the machine's). ARCH pass only builds the LLVM pass
# into .build/shadow.so, which the tests of internal/goasm/gen need with
# GEN_LLVM and GEN_SHADOW_PASS (see docs/testing.md).
#
# Environment:
#   LLVM_BIN     bin folder of LLVM 22.1.8 (default: scripts/llvm-bin.sh)
#   GEN_JOBS     parallel clang jobs (default 4)
#   GEN_OUT      write to this folder (default internal/csqlite, the package)
set -eu

case $(uname -m) in arm64 | aarch64) native=arm64 ;; *) native=amd64 ;; esac
arch=${1:-$native}
case $arch in amd64 | arm64 | pass) ;; *) echo "gen-engine.sh: ARCH $arch: want amd64, arm64 or pass" >&2; exit 2 ;; esac
llvm=$("$(dirname "$0")/llvm-bin.sh")
want=22.1.8

cd "$(dirname "$0")/.."
out=${GEN_OUT:-internal/csqlite}
got=$("$llvm/clang" --version 2>/dev/null | sed -n 's/.*clang version \([0-9][0-9.]*\).*/\1/p' | head -n 1)
if [ "$got" != "$want" ]; then
	echo "gen-engine.sh: clang in $llvm is '${got:-missing}': the engine is generated with LLVM $want only (set LLVM_BIN)" >&2
	exit 1
fi

export CGO_ENABLED=0
mkdir -p .build

# The pass is rebuilt when one of its sources is newer. On darwin, clang++ of
# Homebrew needs the SDK for the C++ library, and the plugin leaves LLVM's
# symbols to opt and llc, which load it.
pass=.build/shadow.so
src="internal/goasm/gen/pass/shadow.cpp internal/goasm/gen/pass/poll.cpp internal/goasm/gen/pass/icall.cpp internal/goasm/gen/pass/facts.cpp"
stale=0
[ -f $pass ] || stale=1
for f in $src; do [ "$f" -nt $pass ] && stale=1; done
if [ $stale = 1 ]; then
	host=
	[ "$(uname -s)" = Darwin ] && host="-isysroot $(xcrun --show-sdk-path) -undefined dynamic_lookup"
	# shellcheck disable=SC2046,SC2086
	"$llvm/clang++" $host -shared -fPIC -O1 $("$llvm/llvm-config" --cxxflags) $src -o $pass
else
	echo "the pass is up to date"
fi
[ "$arch" = pass ] && exit 0

go build -o .build/engine-gen ./internal/goasm/gen/cmd/engine
rm -rf ".build/w-$arch"
.build/engine-gen -release -engine engine -llvm "$llvm" -pass $pass -arch "$arch" -mode pn -varargs-block \
	-shadow-size-log2=25 -pkg csqlite -work ".build/w-$arch" -out "$out" -j "${GEN_JOBS:-4}"
