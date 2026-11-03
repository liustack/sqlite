#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# llvm-bin.sh: print the bin folder of LLVM 22.1.8, LLVM_BIN when it is set,
# else where the package manager of the system puts it.
if [ -n "${LLVM_BIN:-}" ]; then
	echo "$LLVM_BIN"
elif [ "$(uname -s)" = Darwin ]; then
	echo /opt/homebrew/opt/llvm@22/bin
else
	echo /usr/lib/llvm-22/bin
fi
