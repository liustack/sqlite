#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# Copy the SQLite amalgamation this repository pins, or check the copies.
#
#   engine/vendor.sh            download the pinned amalgamation, check its
#                               SHA3-256, copy sqlite3.c and sqlite3.h here and
#                               write SQLITE.sha256
#   engine/vendor.sh --verify   check sqlite3.c and sqlite3.h against SQLITE.sha256
#
# To move to a new SQLite, change version, year and zip_sha3 below (the
# PRODUCT line of https://sqlite.org/download.html has all three) and run the
# script. See docs/upstream.md.
set -eu

version=3.54.1
year=2026
zip_sha3=92281c86e2957f9b9305890f9ac04204caa7b677cc868dc326d942689df28e30

here=$(cd "$(dirname "$0")" && pwd)
manifest=$here/SQLITE.sha256

if command -v sha256sum >/dev/null 2>&1; then sum() { sha256sum "$1" | cut -d' ' -f1; }
else sum() { shasum -a 256 "$1" | cut -d' ' -f1; }; fi

# 3.54.1 is 3540100 in the file name: major, two digits of minor, two of
# patch, two zeros.
name() {
	echo "$1" | awk -F. '{ printf "sqlite-amalgamation-%d%02d%02d00", $1, $2, $3 }'
}

verify() {
	head -n 1 "$manifest" | grep -qx "version $version" || {
		echo "vendor.sh: $manifest is not for SQLite $version" >&2
		exit 1
	}
	bad=$(tail -n +2 "$manifest" | while read -r want file; do
		[ "$(sum "$here/$file")" = "$want" ] || echo "$file"
	done)
	if [ -n "$bad" ]; then
		echo "vendor.sh: $(echo $bad) differ from SQLite $version" >&2
		exit 1
	fi
	echo "sqlite3.c and sqlite3.h are SQLite $version"
}

case "${1:-}" in
--verify) verify; exit 0 ;;
"") ;;
*) echo "usage: $0 [--verify]" >&2; exit 2 ;;
esac

n=$(name "$version")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -sSfL -o "$tmp/$n.zip" "https://sqlite.org/$year/$n.zip"
got=$(openssl dgst -sha3-256 "$tmp/$n.zip" | awk '{ print $NF }')
if [ "$got" != "$zip_sha3" ]; then
	echo "vendor.sh: $n.zip has SHA3-256 $got, want $zip_sha3" >&2
	exit 1
fi
(cd "$tmp" && unzip -q "$n.zip")
cp "$tmp/$n/sqlite3.c" "$tmp/$n/sqlite3.h" "$here/"
{
	echo "version $version"
	for f in sqlite3.c sqlite3.h; do echo "$(sum "$here/$f") $f"; done
} >"$manifest"
verify
