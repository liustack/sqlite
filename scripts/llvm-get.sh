#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# llvm-get.sh [DIR]: download LLVM 22.1.8 as the LLVM project releases it for
# linux (amd64 or arm64), check its SHA-256, unpack it into DIR (default
# .build/llvm) and print its bin folder, for LLVM_BIN. CI uses it for the
# checks that need LLVM (.github/workflows/weekly.yml). The static libraries
# are left out: nothing here links against LLVM.
set -eu
case $(uname -s)/$(uname -m) in
Linux/x86_64) name=LLVM-22.1.8-Linux-X64 sum=df0e1ecf16caf3489a272a5eea4eec9b0d82878f6477fa309504f918a0006384 ;;
Linux/aarch64) name=LLVM-22.1.8-Linux-ARM64 sum=805efad2bb91cb4967fa569e0881d10c0f69c04461cf671cccbae19f547acc34 ;;
*) echo "llvm-get.sh: no release for $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac
dir=${1:-.build/llvm}
mkdir -p "$dir"
tarball=$dir/$name.tar.xz
curl -fsSL --retry 3 -o "$tarball" "https://github.com/llvm/llvm-project/releases/download/llvmorg-22.1.8/$name.tar.xz"
echo "$sum  $tarball" | sha256sum -c --quiet
tar -xJf "$tarball" -C "$dir" --exclude='*.a'
rm "$tarball"
echo "$(cd "$dir/$name/bin" && pwd)"
