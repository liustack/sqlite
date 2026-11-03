# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# The compile flags of the C tree, in one place: the generator
# (internal/goasm/gen, which runs this file in sh and adds only flags of its
# own) reads them, and so will any script that builds the C natively, so that
# what is built and what is checked cannot differ. Source this file, and keep
# it to the assignment and the function below: internal/goasm/gen/flags_test.go
# reads engine_flags by its text. The options of SQLite itself are in
# engine/sqlite.c.
#
# - no system headers or libc, so nothing outside engine/libc can creep in.
#   -ffreestanding also means -fno-builtin
# - -fno-math-errno, so that no floating point operation sets errno
# - no fused multiply-add, so that every target gives the same results
engine_flags="-ffreestanding -nostdinc -fno-math-errno -ffp-contract=off -U__FP_FAST_FMA -U__FP_FAST_FMAF -fno-stack-protector"

# The flags a target needs on top of those: engine_target_flags native, or a
# clang target triple. On arm64 the compiler must not turn an atomic into a
# call of the runtime's outline atomics (__aarch64_*), a symbol nobody defines.
engine_target_flags() {
	t=$1
	[ "$t" = native ] && t=$(uname -m)
	case $t in
	aarch64* | arm64*) echo "-mno-outline-atomics" ;;
	esac
}
