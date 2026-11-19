#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# Get SQL Logic Test, SQLite's suite of SQL scripts with their results, at the
# check-in pinned below, for TestSQLLogic (internal/csqlite).
#
#   scripts/slt.sh [dir]   clone https://www.sqlite.org/sqllogictest into
#                          dir/slt.fossil (.build/slt by default) and check
#                          the check-in out in dir/src
#
# Then: SLT=$PWD/.build/slt/src/test go test -run TestSQLLogic ./internal/csqlite
#
# It needs fossil (FOSSIL names it, fossil by default): the suite has no
# release files, and its web download is closed to programs. Fossil names a
# check-in by the hash of its files, so the pin is a pin of their content.
set -eu

checkin=ab599cbdd0976a63c5cfc7755588f716a7621f27

fossil=${FOSSIL:-fossil}
here=$(cd "$(dirname "$0")/.." && pwd)
dir=${1:-$here/.build/slt}
mkdir -p "$dir"
cd "$dir"
if [ ! -f slt.fossil ]; then
	"$fossil" clone https://www.sqlite.org/sqllogictest slt.fossil
fi
if [ ! -f src/.fslckout ] && [ ! -f src/_FOSSIL_ ]; then
	rm -rf src
	mkdir src
	(cd src && "$fossil" open ../slt.fossil "$checkin")
else
	# A clone made before the pin moved: fetch the newer check-ins.
	(cd src && { "$fossil" update "$checkin" || { "$fossil" pull && "$fossil" update "$checkin"; }; })
fi
got=$(cd src && "$fossil" info | awk '$1 == "checkout:" { print $2 }')
if [ "$got" != "$checkin" ]; then
	echo "slt.sh: checked out $got, want $checkin" >&2
	exit 1
fi
# A file changed or added by hand is not the pinned suite.
changed=$(cd src && "$fossil" changes --hash --differ)
if [ -n "$changed" ]; then
	echo "slt.sh: files differ from $checkin:" >&2
	echo "$changed" >&2
	exit 1
fi
echo "SQL Logic Test $checkin in $dir/src/test"
