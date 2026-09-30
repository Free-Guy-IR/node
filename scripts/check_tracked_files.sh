#!/usr/bin/env bash
set -eu
set -o pipefail

max_bytes=${TRACKED_FILES_MAX_BYTES:-5242880}

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
work=$(mktemp -d "${TMPDIR:-/tmp}/tracked-files.XXXXXX")
trap 'rm -rf "$work"' EXIT

git ls-files -s -z >"$work/index"
checked=0
problems=0
refuse() {
  printf 'check-tracked-files: %s\n' "$1" >&2
  problems=$((problems + 1))
}

while IFS= read -r -d '' entry; do
  meta=${entry%%$'\t'*}
  path=${entry#*$'\t'}
  mode=${meta%% *}
  rest=${meta#* }
  sha=${rest%% *}
  [ "$mode" != 160000 ] || continue
  checked=$((checked + 1))
  size=$(git cat-file -s "$sha")
  if [ "$size" -gt "$max_bytes" ]; then
    refuse "$path is $size bytes, more than the $max_bytes byte limit"
    continue
  fi
  git cat-file blob "$sha" >"$work/blob"
  magic=$(od -An -tx1 -N64 -v "$work/blob" | tr -d ' \n')
  case "$magic" in
    7f454c46*)
      refuse "$path is an ELF executable or object"
      ;;
    feedface* | feedfacf* | cefaedfe* | cffaedfe* | cafebabe* | bebafeca*)
      refuse "$path is a Mach-O binary"
      ;;
    4d5a*)
      [ "${#magic}" -ge 128 ] || continue
      header_offset=$((16#${magic:126:2}${magic:124:2}${magic:122:2}${magic:120:2}))
      [ $((header_offset + 4)) -le "$size" ] || continue
      signature=$(od -An -tx1 -j "$header_offset" -N4 -v "$work/blob" | tr -d ' \n')
      if [ "$signature" = 50450000 ]; then
        refuse "$path is a PE executable"
      else
        refuse "$path is an MZ executable"
      fi
      ;;
  esac
done <"$work/index"

if [ "$problems" -gt 0 ]; then
  printf 'check-tracked-files: %d of %d tracked files refused; build outputs belong in .gitignore, large assets are fetched and pinned by checksum at build time\n' "$problems" "$checked" >&2
  exit 1
fi
printf 'check-tracked-files: %d tracked files checked, none is a binary executable or over %d bytes\n' "$checked" "$max_bytes"
