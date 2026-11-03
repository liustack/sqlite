#!/usr/bin/env bash
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# Usage: bench/run.sh [driver tag ...]    (default: liustack mattn modernc ncruces)
# Env: COUNT (default 6), BENCHTIME (default 1s), BENCH (default .)
set -euo pipefail
cd "$(dirname "$0")"

tags=("$@")
[ ${#tags[@]} -gt 0 ] || tags=(liustack mattn modernc ncruces)
goos=$(go env GOOS)
goarch=$(go env GOARCH)
out=results/$goos-$goarch
mkdir -p "$out"

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
	echo ">> $tag (CGO_ENABLED=$cgo)" >&2
	CGO_ENABLED=$cgo go test -tags "$tag" -count=1 -run '^TestPragmas$' . >&2
	CGO_ENABLED=$cgo go test -tags "$tag" -run '^$' -bench "${BENCH:-.}" -benchmem \
		-count "${COUNT:-6}" -benchtime "${BENCHTIME:-1s}" . | tee "$out/$tag.txt" >&2
done

# One file per column. benchstat labels each column with its file name.
files=()  # relative to $out so column titles are short
for tag in "${tags[@]}"; do files+=("$tag.txt"); done
(cd "$out" && go run golang.org/x/perf/cmd/benchstat@latest "${files[@]}")
