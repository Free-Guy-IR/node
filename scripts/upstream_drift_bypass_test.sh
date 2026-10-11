#!/usr/bin/env bash
set -eu
set -o pipefail

repo_root=$(git rev-parse --show-toplevel)
drift=${DRIFT_SCRIPT:-$repo_root/scripts/upstream_drift.sh}
[ -f "$drift" ] || { printf 'upstream-drift-test: %s is missing\n' "$drift" >&2; exit 2; }
drift=$(cd "$(dirname "$drift")" && pwd)/$(basename "$drift")

self=${BASH_SOURCE[0]}
declared=$(grep -c '^check ' "$self") || { printf 'upstream-drift-test: cannot count the cases declared in %s\n' "$self" >&2; exit 2; }
ran=0
finished=0

tmp=$(mktemp -d "${TMPDIR:-/tmp}/upstream-drift-test.XXXXXX")
finish() {
  rc=$?
  rm -rf "$tmp"
  if [ "$finished" != 1 ]; then
    printf 'upstream-drift-test: INCOMPLETE, the suite stopped after %d of %d cases with exit %s; this run proves nothing\n' "$ran" "$declared" "$rc"
    [ "$rc" != 0 ] || rc=2
  fi
  exit "$rc"
}
trap finish EXIT

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
  git -C "$1" log --all --format='%H %s' | awk -v s="$2" '$2 == s && !found { print $1; found = 1 }'
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
  (cd "$s/fork" && exec env UPSTREAM_FETCH=0 DRIFT_CACHE="$s/cache" DRIFT_COMPONENTS=node \
    UPSTREAM_WAIVERS="$s/waivers" UPSTREAM_URL="$s/up.git" DRIFT_TODAY=2026-09-25 "$@" \
    bash "$drift") >"$s/out" 2>&1 &
  child=$!
  waited=0
  while kill -0 "$child" 2>/dev/null && [ "$waited" -lt "${CASE_TIME_LIMIT:-60}" ]; do
    sleep 1
    waited=$((waited + 1))
  done
  if kill -0 "$child" 2>/dev/null; then
    kill "$child" 2>/dev/null
    wait "$child" 2>/dev/null
    got=timeout
  else
    wait "$child"
    got=$?
  fi
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
  ran=$((ran + 1))
}
xray_final='FROM scratch
COPY --from=xray-builder /out/xray /usr/local/bin/xray'

xray_tail="RUN CGO_ENABLED=0 go build -trimpath -o /out/xray ./main

$xray_final"
xray_makefile='build_xray:
	for patch in $(CURDIR)/patches/xray-*.patch; do git -C "$(XRAY_SRC)" apply --verbose "$$patch" || exit 1; done'
third_party() {
  s=$tmp/$1
  mkdir -p "$s/$2-up"
  git -C "$s/$2-up" init -q
  printf 'package core\n' >"$s/$2-up/core.go"
  git -C "$s/$2-up" add -A
  commit "$s/$2-up" -m "${3}0 pin"
  git -C "$s/$2-up" tag v1.0.0
  [ "$4" = main ] || git -C "$s/$2-up" branch -m "$4"
  printf 'package core\n\nvar guarded = true\n' >"$s/$2-up/core.go"
  git -C "$s/$2-up" add -A
  commit "$s/$2-up" -m "${3}1 fix a panic in the core"
  git init -q --bare "$s/cache/$2.git"
  git --git-dir="$s/cache/$2.git" fetch -q "$s/$2-up" '+refs/heads/*:refs/remotes/up/*' '+refs/tags/*:refs/tags/*'
  printf '[]\n' >"$s/cache/$2-repo-advisories.json"
  printf '[]\n' >"$s/cache/$2-global-advisories.json"
}

xray_fixture() {
  base "$1"
  s=$tmp/$1
  f=$s/fork
  git -C "$f" fetch -q "$s/upw" "$(sha_of "$s/upw" U1)"
  git -C "$f" cherry-pick -x "$(sha_of "$s/upw" U1)" >/dev/null
  third_party "$1" xray X main
  third_party "$1" sing-box B oldstable
  case $4 in
    X*) carried=$(sha_of "$s/xray-up" "$4") ;;
    *) carried=$(sha_of "$s/sing-box-up" "$4") ;;
  esac
  mkdir -p "$f/patches"
  {
    printf 'Subject: carried fix\n\nUpstream: %s\n---\n' "$carried"
    printf 'diff --git a/core.go b/core.go\n--- a/core.go\n+++ b/core.go\n@@ -1 +1,3 @@\n package core\n+\n+var guarded = true\n'
  } >"$f/patches/xray-fix.patch"
  printf '%s\n' "$3" >"$f/Makefile"
  {
    printf 'FROM scratch AS builder\nRUN true\n\n'
    printf 'FROM scratch AS singbox-builder\nARG SINGBOX_VERSION=v1.0.0\n\n'
    printf 'FROM scratch AS xray-builder\nARG XRAY_COMMIT=%s\n' "$(sha_of "$s/xray-up" X0)"
    printf '%s\n' "$2"
    printf '%s\n' "${5-$xray_tail}"
  } >"$f/Dockerfile"
  git -C "$f" add -A
  commit "$f" -m "carry xray-fix.patch"
}

xray_prepend() {
  f=$tmp/$1/fork
  { printf '%s\n' "$2"; cat "$f/Dockerfile"; } >"$f/Dockerfile.new"
  mv "$f/Dockerfile.new" "$f/Dockerfile"
  git -C "$f" add -A
  commit "$f" -m "add a stage in front"
}

xray_copy='COPY patches/xray-fix.patch /patches/xray-fix.patch'
xray_applied="$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch"
stock_image=docker.io/example/xray-stock:1.0.0
cr=$(printf '\r')
vt=$(printf '\013')
nbsp=$(printf '\302\240')

xray_fixture bypass-label-heredoc-hides-a-binary-swap "$xray_applied" "$xray_makefile" X1 "$xray_tail
LABEL note=\"<<EOF\"
COPY --from=$stock_image /usr/bin/xray /usr/local/bin/xray"
check bypass-label-heredoc-hides-a-binary-swap 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-env-heredoc-hides-the-executable-path "$xray_applied" "$xray_makefile" X1 "$xray_tail
ENV NOTE=<<EOF
ENV XRAY_EXECUTABLE_PATH=/opt/xray-stock"
check bypass-env-heredoc-hides-the-executable-path 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-crlf-continuation-swallows-the-apply "$xray_copy
RUN git init -q .
RUN ! test -n x \\$cr
RUN git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check bypass-crlf-continuation-swallows-the-apply 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-vtab-comment-line-drops-the-apply "$xray_copy
RUN git init -q . \\
 && test -n x \\
$vt# && git apply --verbose /patches/xray-fix.patch \\
 && true" "$xray_makefile" X1
check bypass-vtab-comment-line-drops-the-apply 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-nbsp-comment-line-drops-the-apply "$xray_copy
RUN git init -q . \\
 && test -n x \\
$nbsp# && git apply --verbose /patches/xray-fix.patch \\
 && true" "$xray_makefile" X1
check bypass-nbsp-comment-line-drops-the-apply 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-slash-slash-syntax-directive "$xray_applied" "$xray_makefile" X1
xray_prepend bypass-slash-slash-syntax-directive '// syntax=docker.io/example/frontend:1'
check bypass-slash-slash-syntax-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-bom-syntax-directive "$xray_applied" "$xray_makefile" X1
xray_prepend bypass-bom-syntax-directive "$(printf '\357\273\277')# syntax=docker.io/example/frontend:1"
check bypass-bom-syntax-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-vtab-syntax-directive "$xray_applied" "$xray_makefile" X1
xray_prepend bypass-vtab-syntax-directive "#${vt}syntax=docker.io/example/frontend:1"
check bypass-vtab-syntax-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-formfeed-syntax-directive "$xray_applied" "$xray_makefile" X1
xray_prepend bypass-formfeed-syntax-directive "# syntax$(printf '\014')=docker.io/example/frontend:1"
check bypass-formfeed-syntax-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-patch-overwritten-through-a-dot-path "$xray_copy
COPY other.patch /patches/./xray-fix.patch
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check bypass-patch-overwritten-through-a-dot-path 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-patch-overwritten-through-a-double-slash "$xray_copy
COPY other.patch /patches//xray-fix.patch
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check bypass-patch-overwritten-through-a-double-slash 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-stock-binary-shipped-through-patches "$xray_copy
COPY --from=$stock_image /usr/bin/xray /patches/stock
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -trimpath -o /out/xray ./main

FROM scratch
COPY --from=xray-builder /patches/stock /usr/local/bin/xray"
check bypass-stock-binary-shipped-through-patches 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-directory-copied-over-usr-local "$xray_applied" "$xray_makefile" X1 "$xray_tail
COPY --from=$stock_image /usr/local /usr/local"
check bypass-directory-copied-over-usr-local 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-entrypoint-selects-a-stock-binary "$xray_applied" "$xray_makefile" X1 "$xray_tail
COPY --from=$stock_image /usr/bin/xray /opt/stock/xray-core
ENTRYPOINT [\"env\", \"XRAY_EXECUTABLE_PATH=/opt/stock/xray-core\", \"./main\"]"
check bypass-entrypoint-selects-a-stock-binary 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-dotenv-selects-a-stock-binary "$xray_applied" "$xray_makefile" X1 "$xray_tail
COPY --from=$stock_image /usr/bin/xray /opt/stock/xray-core
COPY stock.env /app/.env"
check bypass-dotenv-selects-a-stock-binary 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-builder-on-an-unpinned-external-image "$xray_applied" "$xray_makefile" X1
perl -pi -e 's/^FROM scratch AS xray-builder$/FROM docker.io\/example\/golang-git-apply-is-true:1 AS xray-builder/' \
  "$tmp/bypass-builder-on-an-unpinned-external-image/fork/Dockerfile"
git -C "$tmp/bypass-builder-on-an-unpinned-external-image/fork" add -A
commit "$tmp/bypass-builder-on-an-unpinned-external-image/fork" -m "build xray on an external image"
check bypass-builder-on-an-unpinned-external-image 1 DRIFT_COMPONENTS="node xray"

xray_fixture bypass-pin-read-from-a-global-arg "$xray_applied" "$xray_makefile" B1
perl -pi -e 's/^ARG XRAY_COMMIT=(\w+)$/ARG XRAY_VERSION=v1.0.0 \\\n    XRAY_COMMIT=$1/' \
  "$tmp/bypass-pin-read-from-a-global-arg/fork/Dockerfile"
xray_prepend bypass-pin-read-from-a-global-arg "ARG XRAY_COMMIT=$(sha_of "$tmp/bypass-pin-read-from-a-global-arg/xray-up" X1)"
check bypass-pin-read-from-a-global-arg 1 DRIFT_COMPONENTS="node xray"

xray_fixture d10-branch-scope-ignored-for-xray "$xray_applied" "$xray_makefile" X1
s=$tmp/d10-branch-scope-ignored-for-xray
git -C "$s/xray-up" checkout -q -b feat
printf 'package core\n\nvar guarded = true\n\nvar bounded = true\n' >"$s/xray-up/core.go"
git -C "$s/xray-up" add -A
commit "$s/xray-up" -m "X2 fix a crash on a long name"
git -C "$s/xray-up" checkout -q main
printf '%s rejected: upstream PR on branch feat, open, not merged; revisit if upstream merges it branch:feat\n' \
  "$(sha_of "$s/xray-up" X2)" >>"$s/waivers"
git -C "$s/xray-up" merge -q --no-ff --no-edit feat
git -C "$s/xray-up" branch -q -D feat
git --git-dir="$s/cache/xray.git" fetch -q --prune "$s/xray-up" '+refs/heads/*:refs/remotes/up/*' '+refs/tags/*:refs/tags/*'
check d10-branch-scope-ignored-for-xray 1 DRIFT_COMPONENTS="node xray"

d10_node_merged() {
  base "$1"
  s=$tmp/$1
  git -C "$s/fork" fetch -q "$s/upw" "$(sha_of "$s/upw" U1)"
  git -C "$s/fork" cherry-pick -x "$(sha_of "$s/upw" U1)" >/dev/null
  git -C "$s/upw" checkout -q -b pr-61
  printf 'package main\n\nfunc telemt() int { return 2 }\n' >"$s/upw/telemt.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "P1 telemt backend"
  git -C "$s/upw" checkout -q main
  printf '%s rejected: %s\n' "$(sha_of "$s/upw" P1)" "$2" >>"$s/waivers"
  git -C "$s/upw" merge -q --no-ff --no-edit pr-61
  git -C "$s/upw" branch -q -D pr-61
}

d10_node_merged d10-scope-token-in-parentheses 'upstream PR #61 is still open and has not been merged yet (branch:pr-61), it collides with the fork mtproto backend'
check d10-scope-token-in-parentheses 1

d10_node_merged d10-unmerged-phrase-outside-the-lint 'upstream PR #61 is unmerged and still open, it collides with the fork mtproto backend'
check d10-unmerged-phrase-outside-the-lint 1

finished=1
printf 'upstream-drift-test: %d of %d cases ran, %d passed, %d failed\n' "$ran" "$declared" "$pass" "$failed"
if [ "$ran" != "$declared" ]; then
  printf 'upstream-drift-test: INCOMPLETE, %d cases ran but the suite declares %d\n' "$ran" "$declared"
  exit 2
fi
[ "$failed" = 0 ]
