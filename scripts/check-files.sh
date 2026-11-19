#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.

# check-files.sh fails when a file git tracks, or would add, is larger than
# 1 MiB or is binary, other than those listed here. A test binary, a build of
# benchrun or a database left in the tree would otherwise go into a commit
# unseen, and stay in the history of every clone.
#
# Allowed: engine/sqlite3.c, the amalgamation as released, and the images of
# assets/. Data that tests need and that is larger goes in a download pinned
# by its hash, as engine/vendor.sh does.
set -eu
cd "$(dirname "$0")/.."

big=1048576
bad=0
# The w/ column of --eol says how the file in the tree reads: -text is binary.
git ls-files -z --cached --others --exclude-standard --eol |
	tr '\0' '\n' |
	awk -F '\t' '{ split($1, f, " "); print f[2] "\t" $2 }' |
	sort -u >"${TMPDIR:-/tmp}/check-files.$$"
while IFS="$(printf '\t')" read -r eol path; do
	[ -f "$path" ] || continue # deleted in the tree, not yet in the index
	case $path in
	engine/sqlite3.c) continue ;;
	assets/*.png) continue ;;
	esac
	if [ "$eol" = "w/-text" ]; then
		echo "check-files.sh: $path is binary" >&2
		bad=1
	fi
	size=$(wc -c <"$path" | tr -d ' ')
	if [ "$size" -gt "$big" ]; then
		echo "check-files.sh: $path is $size bytes, more than $big" >&2
		bad=1
	fi
done <"${TMPDIR:-/tmp}/check-files.$$"
rm -f "${TMPDIR:-/tmp}/check-files.$$"
exit "$bad"
