# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# cover.awk: the check train.sh makes of train.sql, over two profiles in the
# text form of llvm-profdata, in this order: the training, and the workloads
# of bench/native/engine.sql (run for the check only, never trained on).
#
#   awk -v hot=H -v floor=F -v need="NAME..." -f cover.awk train.txt bench.txt
#
# A path here is one counter of the profile. A path the training leaves at 0
# is, to the compiler, a path that never runs, and work that runs it pays for
# that. So of the paths the benchmarks run at least H times, the training
# must run at least F percent. It prints how many it runs and fails below
# that. Each NAME (a function, without the file of a static one) must run in
# the training: it names the work of one part of train.sql, and a part that
# no longer reaches its function fails the check by name.

FNR == 1 { file++ }
/^$/ { next }
/^#/ { vals = ($0 ~ /^# Counter Values:/); if (vals) i = 0; next }
/^[0-9]+$/ {
	if (vals) {
		i++
		c[file, fn, i] = $1
		if (file == 2) n[fn] = i
	}
	next
}
{ fn = $0; vals = 0; if (file == 1) trained[fn] = 1 }

END {
	for (f in n)
		for (k = 1; k <= n[f]; k++)
			if (c[2, f, k] + 0 >= hot) {
				total++
				if (c[1, f, k] + 0 > 0) got++
				else miss[f]++
			}
	bad = 0
	m = split(need, names, " ")
	for (j = 1; j <= m; j++) {
		found = 0
		for (f in trained) {
			b = f
			sub(/^.*:/, "", b)
			if (b == names[j]) { found = 1; if (c[1, f, 1] + 0 == 0) { printf "train.sh: train.sql no longer runs %s\n", names[j]; bad = 1 } }
		}
		if (!found) { printf "train.sh: no function %s in the profile\n", names[j]; bad = 1 }
	}
	if (total == 0) { print "train.sh: the benchmarks run no path " hot " times: the check is broken"; exit 1 }
	pct = 100 * got / total
	printf "engine/pgo/train.sql: runs %d of the %d paths the workloads of bench/native/engine.sql run (%.1f%%, at least %d%%)\n", got, total, pct, floor
	if (pct < floor) bad = 1
	if (got < total) {
		print "the functions with the most paths it misses:"
		for (f in miss) print miss[f], f | "sort -rn | head -10"
		close("sort -rn | head -10")
	}
	exit bad
}
