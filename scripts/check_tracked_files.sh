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

max_fat_arches=30
elf_ident=$'\x7fELF[\x01\x02][\x01\x02]\x01'

classify() {
  local magic=$1 size=$2 blob=$3 fat_arches header_offset signature
  case "$magic" in
    7f454c46*)
      echo "is an ELF executable or object"
      ;;
    cafebabe*)
      fat_arches=0
      [ "${#magic}" -lt 16 ] || fat_arches=$((16#${magic:8:8}))
      if [ "${#magic}" -lt 16 ] || { [ "$fat_arches" -ge 1 ] && [ "$fat_arches" -le "$max_fat_arches" ]; }; then
        echo "is a Mach-O binary"
      else
        echo "is a compiled Java class"
      fi
      ;;
    feedface* | feedfacf* | cefaedfe* | cffaedfe* | bebafeca* | cafebabf* | bfbafeca*)
      echo "is a Mach-O binary"
      ;;
    0061736d*)
      echo "is a WebAssembly module"
      ;;
    4d5a*)
      [ "${#magic}" -ge 128 ] || return 0
      header_offset=$((16#${magic:126:2}${magic:124:2}${magic:122:2}${magic:120:2}))
      [ $((header_offset + 4)) -le "$size" ] || return 0
      signature=$(od -An -tx1 -j "$header_offset" -N4 -v "$blob" | tr -d ' \n')
      if [ "$signature" = 50450000 ]; then
        echo "is a PE executable"
      else
        echo "is an MZ executable"
      fi
      ;;
  esac
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
  verdict=$(classify "$magic" "$size" "$work/blob")
  if [ -z "$verdict" ] && LC_ALL=C grep -aq -e "$elf_ident" "$work/blob"; then
    verdict="carries an embedded ELF executable"
  fi
  [ -z "$verdict" ] || refuse "$path $verdict"
done <"$work/index"

if [ "$problems" -gt 0 ]; then
  printf 'check-tracked-files: %d of %d tracked files refused; build outputs belong in .gitignore, large assets are fetched and pinned by checksum at build time\n' "$problems" "$checked" >&2
  exit 1
fi
printf 'check-tracked-files: %d tracked files checked, none is a binary executable or over %d bytes\n' "$checked" "$max_bytes"
