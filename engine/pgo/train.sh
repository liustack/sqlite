#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# train.sh: train the profile make gen compiles the engine with, into
# engine/pgo/profile.txt (see engine/pgo/README.md). Run it from anywhere
# inside the repository on linux or macOS with LLVM 22.1.8, then make gen and
# make gen ARCH=arm64.
#
# The engine and its libc are built as the generator compiles them (the flags
# of engine/flags.sh, -fno-builtin, engine/libc, SQLITE_OS_OTHER and os.c)
# with -fprofile-instr-generate, so each function has the hash the generator
# sees. The host callbacks are engine/pgo/host.c. The profile is what
# engine/pgo/train.c counts while it runs engine/pgo/train.sql.
#
# Then it checks the training (cover.awk): it runs the workloads of
# bench/native/engine.sql, for the check only, and stops when train.sql runs
# less than COVER_FLOOR percent of the paths they run, or when it no longer
# runs one of the functions listed in need below.
#
# Environment:
#   LLVM_BIN     bin folder of LLVM 22.1.8 (default: scripts/llvm-bin.sh)
#   TRAIN_OUT    work folder (default .build/pgo-train)
#   COVER_FLOOR  the least percent of those paths train.sql must run (default 95)
#   TRAIN_KEEP   set: leave engine/pgo/profile.txt alone, write TRAIN_OUT/profile.txt
set -eu

cd "$(dirname "$0")/../.."
llvm=$(scripts/llvm-bin.sh)
if ! "$llvm/clang" --version 2>/dev/null | grep -q 'version 22\.1\.8'; then
	echo "train.sh: no clang 22.1.8 in $llvm (set LLVM_BIN)" >&2
	exit 1
fi
out=${TRAIN_OUT:-.build/pgo-train}
rm -rf "$out"
mkdir -p "$out/obj" "$out/db"
. engine/flags.sh

cc=$llvm/clang
as_linux=
if [ "$(uname -s)" = Darwin ]; then
	cc="$cc -isysroot $(xcrun --show-sdk-path)"
	# The generator compiles for linux. With the macros of the operating
	# system set as there, the C after the preprocessor differs only in how
	# 64-bit integer types and constants are spelled, which the hash of a
	# function does not see.
	as_linux="-U__APPLE__ -U__MACH__ -D__linux__=1 -D__linux=1 -D__unix__=1 -D__unix=1 -D__gnu_linux__=1 -D__ELF__=1"
fi
# shellcheck disable=SC2086
eng="-O2 -fno-builtin $engine_flags $(engine_target_flags native) -fprofile-instr-generate"
# shellcheck disable=SC2086
$cc $eng $as_linux -I engine/libc -I engine -c -o "$out/obj/sqlite.o" engine/sqlite.c
# The libc keeps the macros of the system: on macOS features.h then declares
# the second name of a function instead of an alias, which Mach-O lacks. No
# control flow differs.
for f in engine/libc/string/*.c engine/libc/math/*.c engine/libc/math.c; do
	# shellcheck disable=SC2086
	$cc $eng -I engine/libc -I engine/libc/internal -include engine/libc/internal/features.h \
		-c -o "$out/obj/libc-$(basename "$f" .c).o" "$f"
done
$cc -O2 -Wall -I engine -c -o "$out/host.o" engine/pgo/host.c
$cc -O2 -Wall -I engine -c -o "$out/train.o" engine/pgo/train.c
$cc -O2 -I engine -c -o "$out/bench.o" bench/native/c/engine.c
$cc -fprofile-instr-generate -o "$out/train" "$out/train.o" "$out/host.o" "$out"/obj/*.o -lpthread
$cc -fprofile-instr-generate -o "$out/bench" "$out/bench.o" "$out/host.o" "$out"/obj/*.o -lpthread

# run NAME PROGRAM ARG...: the program, counted into NAME.profraw.
run() {
	name=$1
	shift
	LLVM_PROFILE_FILE="$out/$name.profraw" TMPDIR="$out/db" "$@" > "$out/$name.txt" || {
		echo "train.sh: $name failed ($out/$name.txt)" >&2
		exit 1
	}
	if [ ! -s "$out/$name.profraw" ]; then
		echo "train.sh: $name wrote no counters ($out/$name.profraw)" >&2
		exit 1
	fi
	"$llvm/llvm-profdata" merge -o "$out/$name.profdata" "$out/$name.profraw"
	"$llvm/llvm-profdata" merge --text -o "$out/$name.profile.txt" "$out/$name.profdata"
}
run train "$out/train" engine/pgo/train.sql "$out/db"
cat "$out/train.txt"
run bench "$out/bench" bench/native/engine.sql 1

# The function each kind of work of train.sql is there for: when the training
# no longer runs one of them, a part of train.sql no longer does its work.
need="sqlite3VdbeExec sqlite3BtreeInsert sqlite3BtreeTableMoveto sqlite3BtreeIndexMoveto sqlite3RunParser sqlite3Select \
sqlite3WhereBegin sqlite3WalFrames sqlite3WalCheckpoint sqlite3PagerCommitPhaseOne pager_playback sqlite3VdbeSorterWrite \
sqlite3FkCheck sqlite3CodeRowTrigger sqlite3WindowCodeStep sqlite3RunVacuum sqlite3AlterRenameTable sqlite3AnalysisLoad \
sqlite3InitOne sqlite3UpsertDoUpdate sqlite3VdbeMemGrow patternCompare sqlite3AtoF sqlite3_str_vappendf computeJD \
jsonConvertTextToBlob jsonLookupStep jsonEachFilter jsonBlobEdit fts5ExprNodeNext_AND fts5IndexFlush \
fts5StorageInsertCallback fts5PorterStep1A fts5TriTokenize fts5SetupPrefixIter fts5VocabNextMethod rtreeFilter \
rtreeInsertCell rtreeUpdate percentStep sin pow log memcmp strcspn"
awk -v hot=100 -v floor="${COVER_FLOOR:-95}" -v need="$need" -f engine/pgo/cover.awk \
	"$out/train.profile.txt" "$out/bench.profile.txt"

dst=engine/pgo/profile.txt
[ -n "${TRAIN_KEEP:-}" ] && dst=$out/profile.txt
cp "$out/train.profile.txt" "$dst"
echo "$dst: $(grep -c '^# Func Hash:' "$dst") functions"
