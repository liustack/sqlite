#!/usr/bin/env bash
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# Usage: bench/run.sh [driver tag ...]    (default: liustack mattn modernc ncruces)
# Env: COUNT (default 6), BENCHTIME (default 1s), BENCH (default .)
#
# Each driver's test binary is built once. Then the drivers take turns, one
# run of every benchmark each, COUNT times, so that the load of the machine
# falls on all of them alike.
set -euo pipefail
cd "$(dirname "$0")"

tags=("$@")
[ ${#tags[@]} -gt 0 ] || tags=(liustack mattn modernc ncruces)
goos=$(go env GOOS)
goarch=$(go env GOARCH)
out=results/$goos-$goarch
bin=$out/bin
mkdir -p "$bin"

cpu=$(sysctl -n machdep.cpu.brand_string 2>/dev/null ||
	awk -F: '/model name/ {sub(/^ /, "", $2); print $2; exit}' /proc/cpuinfo 2>/dev/null || echo unknown)
{
	echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "go: $(go version)"
	echo "cpu: $cpu"
	echo "os: $(uname -srm)"
	echo "count: ${COUNT:-6}"
	echo "benchtime: ${BENCHTIME:-1s}"
	echo "modules:"
	go list -m all | grep -E 'mattn/go-sqlite3|modernc.org/sqlite|ncruces/go-sqlite3|tetratelabs/wazero' | sed 's/^/  /'
} >"$out/header.txt"

for tag in "${tags[@]}"; do
	cgo=0
	[ "$tag" = mattn ] && cgo=1
	echo ">> build $tag (CGO_ENABLED=$cgo)" >&2
	CGO_ENABLED=$cgo go test -c -tags "$tag" -o "$bin/$tag.test" .
	(cd "$bin" && "./$tag.test" -test.count=1 -test.run '^TestPragmas$' >&2)
	: >"$out/$tag.txt"
done

for ((i = 1; i <= ${COUNT:-6}; i++)); do
	for tag in "${tags[@]}"; do
		echo ">> run $i $tag" >&2
		(cd "$bin" && "./$tag.test" -test.run '^$' -test.bench "${BENCH:-.}" -test.benchmem \
			-test.count 1 -test.benchtime "${BENCHTIME:-1s}") >>"$out/$tag.txt"
	done
	uptime >&2
done

# One file per column. benchstat labels each column with its file name.
files=()  # relative to $out so column titles are short
for tag in "${tags[@]}"; do files+=("$tag.txt"); done
(cd "$out" && go run golang.org/x/perf/cmd/benchstat@latest "${files[@]}")
