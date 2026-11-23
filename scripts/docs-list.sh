#!/usr/bin/env bash
# Copyright (c) 2026 The liustack/sqlite Authors.
# SPDX-License-Identifier: MIT
# Use of this source code is governed by the MIT license in LICENSE at the
# root of this repository.
#
# List docs/ with the summary and read_when from each file's front matter.
#
#   scripts/docs-list.sh           print the index
#   scripts/docs-list.sh --check   also fail when a doc has no summary or no
#                                  audience, or a Chinese version is missing
#                                  or behind its English (see below)
#
# Front matter format:
#
#   ---
#   summary: 'One line saying what the doc answers'
#   read_when:
#     - Situation in which an agent should read it
#   audience: users      (or developers)
#   ---
#
# read_when also accepts the inline form: read_when: [a, b]
#
# Chinese versions (*.zh-CN.md) are not listed in the index.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
docs="$root/docs"
check=false
case "${1:-}" in
  "") ;;
  --check) check=true ;;
  *) echo "usage: $0 [--check]" >&2; exit 2 ;;
esac

if [ ! -d "$docs" ]; then
  echo "no docs/ directory at $root" >&2
  exit 1
fi

# Drafts and history are not part of the index. A *.zh-CN.md file is the
# Chinese version of the English doc next to it, so it is not listed again.
files="$(cd "$root" && find docs -type f -name '*.md' -not -name '*.zh-CN.md' \
  -not -path 'docs/archive/*' -not -path 'docs/research/*' | LC_ALL=C sort)"

missing=0
listed=0
while IFS= read -r file; do
  [ -n "$file" ] || continue
  out="$(awk -v path="$file" '
    function unquote(s) {
      sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s)
      if (s ~ /^'\''.*'\''$/ || s ~ /^".*"$/) s = substr(s, 2, length(s) - 2)
      return s
    }
    NR == 1 { if ($0 != "---") exit; inside = 1; next }
    inside && $0 == "---" { closed = 1; exit }
    inside && /^summary:/ { sub(/^summary:/, ""); summary = unquote($0); list = 0; next }
    inside && /^read_when:/ {
      rest = $0; sub(/^read_when:/, "", rest); rest = unquote(rest)
      if (rest ~ /^\[.*\]$/) {
        rest = substr(rest, 2, length(rest) - 2)
        n = split(rest, items, ",")
        for (i = 1; i <= n; i++) { item = unquote(items[i]); if (item != "") when = when (when == "" ? "" : " | ") item }
        list = 0
      } else list = 1
      next
    }
    inside && list && /^[ \t]+-[ \t]+/ {
      item = $0; sub(/^[ \t]+-[ \t]+/, "", item); item = unquote(item)
      when = when (when == "" ? "" : " | ") item
      next
    }
    inside && /^[^ \t]/ { list = 0 }
    END {
      if (!closed || summary == "") exit 1
      print path " - " summary
      if (when != "") print "  Read when: " when
    }
  ' "$root/$file")" || {
    missing=$((missing + 1))
    echo "warning: $file has no front matter summary" >&2
    continue
  }
  echo "$out"
  listed=$((listed + 1))
done <<EOF
$files
EOF

if [ "$listed" -eq 0 ]; then
  echo "No docs with front matter found in docs/"
fi
echo
echo 'Reminder: before coding, check the "Read when" hints above and read the docs that apply.'

if $check && [ "$missing" -gt 0 ]; then
  exit 1
fi

# --check also holds each doc for the users of the driver to its Chinese
# version: README.md and the docs whose front matter says audience: users
# have a *.zh-CN.md beside them, whose translatedFrom is the first 12 hex
# digits of the SHA-256 of the English file. So a change to the English
# fails the check until the Chinese is changed to match, in the same
# commit. The docs about working on the driver say audience: developers
# and are in English only. A Chinese file has translatedFrom in its front
# matter, or, with none, as its first line: <!-- translatedFrom: <hex> -->

# fm key file: the value of key in the front matter of file, empty if none.
fm() {
  awk -v key="$1" '
    NR == 1 { if ($0 != "---") exit; next }
    $0 == "---" { exit }
    index($0, key ":") == 1 {
      v = substr($0, length(key) + 2)
      sub(/^[ \t]+/, "", v); sub(/[ \t]+$/, "", v)
      if (v ~ /^'\''.*'\''$/ || v ~ /^".*"$/) v = substr(v, 2, length(v) - 2)
      print v; exit
    }
  ' "$2"
}

translated() {
  local v
  v="$(fm translatedFrom "$1")"
  if [ -z "$v" ]; then
    v="$(head -n 1 "$1" | sed -n 's/^<!-- translatedFrom: \([0-9a-f]*\) -->$/\1/p')"
  fi
  echo "$v"
}

hash12() {
  if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -c1-12
}

# pair english: the Chinese version of english is there and up to date.
pair() {
  local zh="${1%.md}.zh-CN.md" want got
  if [ ! -f "$root/$zh" ]; then
    echo "error: $1 is for the users of the driver and has no Chinese version $zh" >&2
    return 1
  fi
  want="$(hash12 "$root/$1")"
  got="$(translated "$root/$zh")"
  if [ "$got" != "$want" ]; then
    echo "error: $zh is translated from ${got:-no version} of $1, which is now $want: change it to match, and set translatedFrom" >&2
    return 1
  fi
}

if $check; then
  bad=0
  pair README.md || bad=1
  while IFS= read -r file; do
    [ -n "$file" ] || continue
    case "$(fm audience "$root/$file")" in
      users) pair "$file" || bad=1 ;;
      developers)
        if [ -f "$root/${file%.md}.zh-CN.md" ]; then
          echo "error: $file is for developers, in English only, but ${file%.md}.zh-CN.md is there" >&2
          bad=1
        fi ;;
      *) echo "error: $file has no audience: users or developers in its front matter" >&2; bad=1 ;;
    esac
  done <<EOF2
$files
EOF2
  for zh in $(cd "$root" && find docs -type f -name '*.zh-CN.md' | LC_ALL=C sort); do
    en="${zh%.zh-CN.md}.md"
    if [ ! -f "$root/$en" ] || [ "$(fm audience "$root/$en")" != users ]; then
      echo "error: $zh is not the Chinese version of a doc for the users of the driver" >&2
      bad=1
    fi
  done
  [ "$bad" -eq 0 ] || exit 1
fi
