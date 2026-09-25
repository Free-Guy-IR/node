#!/usr/bin/env bash
set -eu
set -o pipefail

repo_root=$(git rev-parse --show-toplevel)
drift=${DRIFT_SCRIPT:-$repo_root/scripts/upstream_drift.sh}
[ -f "$drift" ] || { printf 'upstream-drift-test: %s is missing\n' "$drift" >&2; exit 2; }
drift=$(cd "$(dirname "$drift")" && pwd)/$(basename "$drift")

tmp=$(mktemp -d "${TMPDIR:-/tmp}/upstream-drift-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.com
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.com
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=$tmp/gitconfig
git config --global init.defaultBranch main
git config --global commit.gpgsign false
git config --global advice.detachedHead false

tick=1000000000
commit() {
  tick=$((tick + 60))
  GIT_AUTHOR_DATE="@$tick +0000" GIT_COMMITTER_DATE="@$tick +0000" git -C "$1" commit -q "${@:2}"
}

sha_of() {
  git -C "$1" log --all --format='%H %s' | awk -v s="$2" '$2 == s { print $1; exit }'
}

base() {
  s=$tmp/$1
  mkdir -p "$s/upw" "$s/cache"
  printf '[]\n' >"$s/cache/node-repo-advisories.json"
  printf '[]\n' >"$s/cache/node-global-advisories.json"
  git -C "$s/upw" init -q
  printf 'module github.com/pasarguard/node\n\ngo 1.22\n' >"$s/upw/go.mod"
  printf 'package main\n\nfunc a() int { return 0 }\n' >"$s/upw/a.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "U0 initial"
  git clone -q "$s/upw" "$s/fork"
  git -C "$s/fork" remote remove origin
  printf 'fork\n' >"$s/fork/fork.txt"
  git -C "$s/fork" add -A
  commit "$s/fork" -m "F1 fork start"
  printf 'package main\n\nfunc a() int { return 1 }\n' >"$s/upw/a.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "U1 fix a"
  printf '%s allowed-merge: fixture fork start on U0, the fork point\n' "$(sha_of "$s/fork" F1)" >"$s/waivers"
}

merge_fixture() {
  base "$1"
  s=$tmp/$1
  git -C "$s/upw" checkout -q -b side
  printf 'package main\n\nfunc c() int { return 3 }\n' >"$s/upw/c.go"
  printf 'package main\n\nfunc gone() int { return 9 }\n' >"$s/upw/gone.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "S1 add c"
  git -C "$s/upw" checkout -q main
  printf 'package main\n\nfunc d() int { return 4 }\n' >"$s/upw/d.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "U2 add d"
  git -C "$s/upw" merge -q --no-ff --no-commit side >/dev/null 2>&1
  case $2 in
    add) awk -v n="$3" 'BEGIN { print "package main"; for (i = 0; i < n / 40; i++) printf "var evilValue%08d = %08d\n", i, i }' >"$s/upw/evil.go" ;;
    drop) rm "$s/upw/gone.go" ;;
  esac
  git -C "$s/upw" add -A
  commit "$s/upw" -m "Merge side"
  for c in U1 S1 U2; do
    sha=$(sha_of "$s/upw" "$c")
    git -C "$s/fork" fetch -q "$s/upw" "$sha"
    git -C "$s/fork" cherry-pick -x "$sha" >/dev/null
  done
}

pending() {
  base "$1"
  s=$tmp/$1
  printf '%s pending: %s\n' "$(sha_of "$s/upw" U1)" "$2" >>"$s/waivers"
}

pass=0
failed=0
check() {
  name=$1 want=$2
  shift 2
  s=$tmp/$name
  git clone -q --bare "$s/upw" "$s/up.git"
  git -C "$s/fork" fetch -q --prune --no-tags "$s/up.git" \
    "+refs/heads/*:refs/upstream-drift/node/heads/*" "+refs/tags/*:refs/upstream-drift/node/tags/*"
  set +e
  (cd "$s/fork" && env UPSTREAM_FETCH=0 DRIFT_CACHE="$s/cache" DRIFT_COMPONENTS=node \
    UPSTREAM_WAIVERS="$s/waivers" UPSTREAM_URL="$s/up.git" DRIFT_TODAY=2026-09-25 "$@" \
    bash "$drift") >"$s/out" 2>&1
  got=$?
  set -e
  case "$want:$got" in
    "$got:$got" | nonzero:1 | nonzero:2)
      printf 'ok       %-28s exit %s\n' "$name" "$got"
      pass=$((pass + 1))
      ;;
    *)
      printf 'FAIL     %-28s exit %s, want %s\n' "$name" "$got" "$want"
      sed 's/^/         | /' "$s/out" | tail -8
      failed=$((failed + 1))
      ;;
  esac
}

base clean
git -C "$tmp/clean/fork" fetch -q "$tmp/clean/upw" "$(sha_of "$tmp/clean/upw" U1)"
git -C "$tmp/clean/fork" cherry-pick -x "$(sha_of "$tmp/clean/upw" U1)" >/dev/null
check clean 0

base missing
check missing 1

base ours-merge
git -C "$tmp/ours-merge/fork" fetch -q "$tmp/ours-merge/upw" main
git -C "$tmp/ours-merge/fork" merge -q -s ours --no-edit FETCH_HEAD
check ours-merge 1

merge_fixture clean-merge none 0
check clean-merge 0

merge_fixture evil-merge-small add 2000
check evil-merge-small 1

merge_fixture evil-merge-large add 300000
check evil-merge-large 1

merge_fixture merge-drops-side drop 0
check merge-drops-side 1

pending pending-valid 'taken with the next release until:2026-10-05'
check pending-valid 0

pending pending-expired 'taken with the next release until:2026-09-01'
check pending-expired 1

pending pending-undated 'taken with the next release, no date and no issue'
check pending-undated 2

pending pending-far 'taken with the next release until:2099-12-31'
check pending-far nonzero

pending pending-unpadded 'taken with the next release until:2026-9-1 issue:x/y#1'
check pending-unpadded nonzero

pending pending-spaced 'taken with the next release until: 2026-09-01 issue:x/y#1'
check pending-spaced nonzero

pending pending-two-dates 'taken with the next release until:2026-09-01 then until:2099-01-01'
check pending-two-dates nonzero

pending pending-bad-date 'taken with the next release until:2026-13-45'
check pending-bad-date nonzero

pending pending-bad-issue 'taken with the next release issue:none'
check pending-bad-issue nonzero

base big-patch
mkdir -p "$tmp/big-patch/fork/patches"
{
  printf 'Subject: big patch\n\nUpstream: %s\n---\n' "$(sha_of "$tmp/big-patch/upw" U1)"
  printf 'diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,20003 @@\n'
  awk 'BEGIN { for (i = 0; i < 20000; i++) printf "+var line%08d = 0\n", i }'
  printf ' package main\n \n-func a() int { return 0 }\n+func a() int { return 1 }\n'
} >"$tmp/big-patch/fork/patches/big.patch"
git -C "$tmp/big-patch/fork" add -A
commit "$tmp/big-patch/fork" -m "carry U1 as a big patch"
check big-patch 0

printf 'upstream-drift-test: %d passed, %d failed\n' "$pass" "$failed"
[ "$failed" = 0 ]
