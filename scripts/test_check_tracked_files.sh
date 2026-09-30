#!/usr/bin/env bash
set -eu
set -o pipefail

repo_root=$(git rev-parse --show-toplevel)
guard=${TRACKED_FILES_GUARD:-$repo_root/scripts/check_tracked_files.sh}
[ -f "$guard" ] || { printf 'test-check-tracked-files: %s is missing\n' "$guard" >&2; exit 2; }
guard=$(cd "$(dirname "$guard")" && pwd)/$(basename "$guard")

tmp=$(mktemp -d "${TMPDIR:-/tmp}/tracked-files-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=$tmp/gitconfig
export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.com
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.com

pass=0
failed=0

fixture() {
  r=$tmp/$1
  git init -q "$r"
  printf 'package main\n' >"$r/main.go"
  printf '# readme\n' >"$r/README.md"
}

check() {
  name=$1 want=$2
  r=$tmp/$name
  git -C "$r" add -A
  set +e
  (cd "$r" && bash "$guard") >"$tmp/$name.out" 2>&1
  got=$?
  set -e
  if [ "$got" = "$want" ]; then
    printf 'ok       %-26s exit %s\n' "$name" "$got"
    pass=$((pass + 1))
  else
    printf 'FAIL     %-26s exit %s, want %s\n' "$name" "$got" "$want"
    sed 's/^/         | /' "$tmp/$name.out" | tail -5
    failed=$((failed + 1))
  fi
}

fixture clean
check clean 0

fixture elf
printf '\177ELF\002\001\001\000' >"$tmp/elf/main"
check elf 1

fixture elf-hidden-name
printf '\177ELF\002\001\001\000' >"$tmp/elf-hidden-name/notes.txt"
check elf-hidden-name 1

fixture macho-64
printf '\317\372\355\376\007\000\000\001' >"$tmp/macho-64/node"
check macho-64 1

fixture macho-32
printf '\316\372\355\376\007\000\000\000' >"$tmp/macho-32/node"
check macho-32 1

fixture macho-fat
printf '\312\376\272\276\000\000\000\002' >"$tmp/macho-fat/node"
check macho-fat 1

fixture pe
{ printf 'MZ'; head -c 58 /dev/zero; printf '\100\000\000\000PE\000\000'; } >"$tmp/pe/node.exe.bin"
check pe 1

fixture large
head -c $((5 * 1024 * 1024 + 1)) /dev/zero | tr '\0' 'a' >"$tmp/large/big.txt"
check large 1

fixture at-limit
head -c $((5 * 1024 * 1024)) /dev/zero | tr '\0' 'a' >"$tmp/at-limit/limit.txt"
check at-limit 0

fixture text-starting-mz
printf 'MZ is a text line, not a PE header\n' >"$tmp/text-starting-mz/notes.md"
check text-starting-mz 0

fixture long-text-starting-mz
printf 'MZ opens this note, and the note runs on well past the sixty-four bytes a PE header needs before its offset\n' >"$tmp/long-text-starting-mz/notes.md"
check long-text-starting-mz 0

fixture spaced-name
printf '\177ELF\002\001\001\000' >"$tmp/spaced-name/a binary with spaces"
check spaced-name 1

fixture untracked-binary
printf '\177ELF\002\001\001\000' >"$tmp/untracked-binary/main"
printf 'main\n' >"$tmp/untracked-binary/.gitignore"
check untracked-binary 0

fixture deleted-from-index
printf '\177ELF\002\001\001\000' >"$tmp/deleted-from-index/main"
git -C "$tmp/deleted-from-index" add -A
git -C "$tmp/deleted-from-index" commit -q -m "carry a binary"
git -C "$tmp/deleted-from-index" rm -q --cached main
printf 'main\n' >"$tmp/deleted-from-index/.gitignore"
check deleted-from-index 0

printf 'test-check-tracked-files: %d passed, %d failed\n' "$pass" "$failed"
[ "$failed" = 0 ]
