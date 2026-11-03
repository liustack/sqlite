#!/bin/sh
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# Copy the files this library takes from musl, or check the copies. They are
# copied unchanged. string/memcpy.c is not one of them: it is written here, and
# the script leaves it alone.
#
#   engine/libc/vendor.sh MUSL_DIR            copy the files out of a musl checkout at
#                                             the pinned commit and write MUSL.sha256
#   engine/libc/vendor.sh --verify            check the files here against MUSL.sha256
#   engine/libc/vendor.sh --verify MUSL_DIR   the same, and compare them with the checkout
#
# MUSL.sha256 has the commit on its first line, then one line per file: its
# SHA-256, its path in musl and its path here.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
commit=7ada6dde6f9dc6a2836c3d92c2f762d35fd229e0
manifest=$here/MUSL.sha256

if command -v sha256sum >/dev/null 2>&1; then sum() { sha256sum "$1" | cut -d' ' -f1; }
else sum() { shasum -a 256 "$1" | cut -d' ' -f1; }; fi

# musl path and path here.
files() {
	echo "COPYRIGHT COPYRIGHT"
	for f in bcmp bzero memchr memcmp memmove memrchr memset strchr strchrnul strcmp \
		strcspn strlen strncmp strrchr strspn; do
		echo "src/string/$f.c string/$f.c"
	done
}

if [ "${1:-}" = --verify ]; then
	musl=${2:-}
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	status=0
	bad() { echo "vendor.sh: $*" >&2; status=1; }
	[ "$(sed -n 1p "$manifest")" = "# musl $commit" ] || bad "$manifest names another commit"
	files | awk '{print $2}' | sort >"$tmp/script"
	grep -v '^#' "$manifest" | awk '{print $3}' | sort >"$tmp/listed"
	(cd "$here" && find string -type f | grep -v '^string/memcpy.c$'; echo COPYRIGHT) | sort >"$tmp/present"
	cmp -s "$tmp/script" "$tmp/listed" || bad "MUSL.sha256 does not list the files this script copies"
	cmp -s "$tmp/listed" "$tmp/present" || bad "the files here are not the ones MUSL.sha256 lists: $(diff "$tmp/listed" "$tmp/present" | tr '\n' ' ')"
	if [ -n "$musl" ] && [ "$(git -C "$musl" rev-parse HEAD)" != "$commit" ]; then
		bad "$musl is not at $commit"
	fi
	grep -v '^#' "$manifest" >"$tmp/lines"
	while read -r want mpath path; do
		[ "$(sum "$here/$path" 2>/dev/null)" = "$want" ] || bad "$path differs from MUSL.sha256"
		if [ -n "$musl" ]; then
			[ "$(sum "$musl/$mpath")" = "$want" ] || bad "$mpath in musl is not the recorded file"
		fi
	done <"$tmp/lines"
	[ $status = 0 ] && echo "vendor.sh: $(wc -l <"$tmp/lines" | tr -d ' ') files are the ones in MUSL.sha256${musl:+, the same in $musl}"
	exit $status
fi

musl=${1:?usage: vendor.sh MUSL_DIR | --verify [MUSL_DIR]}
[ "$(git -C "$musl" rev-parse HEAD)" = "$commit" ] || { echo "vendor.sh: $musl is not at $commit" >&2; exit 1; }
git -C "$musl" diff --quiet HEAD || { echo "vendor.sh: $musl has changes" >&2; exit 1; }

{
	echo "# musl $commit"
	files | while read -r mpath path; do
		mkdir -p "$(dirname "$here/$path")"
		cp "$musl/$mpath" "$here/$path"
		echo "$(sum "$here/$path") $mpath $path"
	done
} >"$manifest"
echo "vendor.sh: wrote $manifest"
