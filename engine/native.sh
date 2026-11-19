# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# native.sh, sourced from the root of the repository by engine/pgo/train.sh
# and engine/diff/diff.sh: it compiles the engine and its libc for this
# machine as the generator compiles them (the flags of engine/flags.sh,
# -fno-builtin, engine/libc, SQLITE_OS_OTHER and os.c), with LLVM 22.1.8.
#
#   native_setup NAME     sets llvm (its bin folder) and cc, or stops with
#                         NAME in the message
#   native_engine OUT FLAG...
#                         compiles the engine to OUT/obj/sqlite.o and the
#                         libc to OUT/obj/libc-*.o, with the FLAGs added

. engine/flags.sh

native_setup() {
	llvm=$(scripts/llvm-bin.sh)
	if ! "$llvm/clang" --version 2>/dev/null | grep -q 'version 22\.1\.8'; then
		echo "$1: no clang 22.1.8 in $llvm (set LLVM_BIN)" >&2
		exit 1
	fi
	cc=$llvm/clang
	native_as_linux=
	if [ "$(uname -s)" = Darwin ]; then
		cc="$cc -isysroot $(xcrun --show-sdk-path)"
		# The generator compiles for linux. With the macros of the operating
		# system set as there, the C after the preprocessor differs only in
		# how 64-bit integer types and constants are spelled, which the hash
		# of a function does not see.
		native_as_linux="-U__APPLE__ -U__MACH__ -D__linux__=1 -D__linux=1 -D__unix__=1 -D__unix=1 -D__gnu_linux__=1 -D__ELF__=1"
	fi
}

native_engine() {
	native_out=$1
	shift
	mkdir -p "$native_out/obj"
	# shellcheck disable=SC2086
	native_eng="-O2 -fno-builtin $engine_flags $(engine_target_flags native) $*"
	# shellcheck disable=SC2086
	$cc $native_eng $native_as_linux -I engine/libc -I engine -c -o "$native_out/obj/sqlite.o" engine/sqlite.c
	# The libc keeps the macros of the system: on macOS features.h then
	# declares the second name of a function instead of an alias, which
	# Mach-O lacks. No control flow differs.
	for f in engine/libc/string/*.c engine/libc/math/*.c engine/libc/math.c; do
		# shellcheck disable=SC2086
		$cc $native_eng -I engine/libc -I engine/libc/internal -include engine/libc/internal/features.h \
			-c -o "$native_out/obj/libc-$(basename "$f" .c).o" "$f"
	done
}
