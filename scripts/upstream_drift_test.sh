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

stub_curl() {
  mkdir -p "$tmp/stub"
  cat >"$tmp/stub/curl" <<'STUB'
#!/usr/bin/env bash
out="" hdr="" url=""
while [ $# -gt 0 ]; do
  case $1 in
    --output) out=$2; shift 2 ;;
    --dump-header) hdr=$2; shift 2 ;;
    --config | --retry | --write-out) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
cat >/dev/null
printf '%s\n' "$url" >>"$STUB_LOG"
calls=$(wc -l <"$STUB_LOG" | tr -d ' ')
full='['
for i in $(seq 1 100); do
  [ "$i" = 1 ] || full="$full,"
  full="$full{\"ghsa_id\":\"GHSA-stub-$i\",\"withdrawn_at\":\"2020-01-01T00:00:00Z\"}"
done
full="$full]"
next=""
case $STUB_MODE:$url in
  endless:*) body=$full; next="https://api.github.com/advisories?after=cursor$calls&per_page=100" ;;
  two-pages:*after=*) body='[{"ghsa_id":"GHSA-stub-last","withdrawn_at":"2020-01-01T00:00:00Z"}]' ;;
  two-pages:*) body=$full; next="${url%%\?*}?after=cursor1&per_page=100" ;;
  foreign-next:*) body=$full; next="https://api.github.com.attacker.example/advisories?after=cursor1&per_page=100" ;;
esac
printf '%s\n' "$body" >"$out"
if [ -n "$hdr" ]; then
  printf 'HTTP/2 200\r\ncontent-type: application/json\r\n' >"$hdr"
  [ -z "$next" ] || printf 'link: <%s>; rel="next"\r\n' "$next" >>"$hdr"
  printf '\r\n' >>"$hdr"
fi
printf '200'
STUB
  chmod +x "$tmp/stub/curl"
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

xray_patch() {
  f=$tmp/$1/fork
  mkdir -p "$f/patches"
  {
    printf 'Subject: %s\n\nUpstream: %s\n---\n' "$2" "$3"
    printf 'diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,%d @@\n' "$(($4 + 3))"
    awk -v n="$4" 'BEGIN { for (i = 0; i < n; i++) printf "+var line%08d = 0\n", i }'
    printf ' package main\n \n-func a() int { return 0 }\n+func a() int { return 1 }\n'
  } >"$f/patches/$2"
  printf 'build_xray:\n\tfor patch in $(CURDIR)/patches/xray-*.patch; do git -C "$(XRAY_SRC)" apply --verbose "$$patch" || exit 1; done\n' >"$f/Makefile"
  if [ "$5" = applied ]; then
    printf 'FROM scratch AS xray-builder\nCOPY patches/%s /patches/%s\nRUN git apply --verbose /patches/%s\n' "$2" "$2" "$2" >>"$f/Dockerfile"
  fi
  git -C "$f" add -A
  commit "$f" -m "carry $2"
}

base big-patch
git -C "$tmp/big-patch/fork" fetch -q "$tmp/big-patch/upw" "$(sha_of "$tmp/big-patch/upw" U1)"
git -C "$tmp/big-patch/fork" cherry-pick -x "$(sha_of "$tmp/big-patch/upw" U1)" >/dev/null
xray_patch big-patch xray-big.patch "$(printf 'a%.0s' $(seq 1 40))" 20000 applied
check big-patch 0

base patch-not-applied
xray_patch patch-not-applied xray-unused.patch "$(sha_of "$tmp/patch-not-applied/upw" U1)" 3 not-applied
check patch-not-applied 1

base patch-other-component
xray_patch patch-other-component xray-fix.patch "$(sha_of "$tmp/patch-other-component/upw" U1)" 3 applied
check patch-other-component 1

base ours-merge-upstream-reachable
git -C "$tmp/ours-merge-upstream-reachable/fork" fetch -q "$tmp/ours-merge-upstream-reachable/upw" main
git -C "$tmp/ours-merge-upstream-reachable/fork" merge -q -s ours --no-edit FETCH_HEAD
git -C "$tmp/ours-merge-upstream-reachable/upw" fetch -q "$tmp/ours-merge-upstream-reachable/fork" main:refs/heads/fork-contribution
check ours-merge-upstream-reachable 1

pending pending-issue-only 'taken with the next release issue:x/y#1'
check pending-issue-only 2

pending pending-issue-and-until 'taken with the next release issue:x/y#1 until:2026-10-05'
check pending-issue-and-until 0

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
  } >"$f/Dockerfile"
  git -C "$f" add -A
  commit "$f" -m "carry xray-fix.patch"
}

xray_makefile_commented='# build_xray:
#	for patch in $(CURDIR)/patches/xray-*.patch; do git -C "$(XRAY_SRC)" apply --verbose "$$patch" || exit 1; done'

xray_copy='COPY patches/xray-fix.patch /patches/xray-fix.patch'

xray_fixture xray-patch-applied "$xray_copy
RUN git init -q . \\
 && git apply --verbose /patches/xray-fix.patch \\
 && true" "$xray_makefile" X1
check xray-patch-applied 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-not-applied "$xray_copy
RUN true" "$xray_makefile" X1
check xray-patch-not-applied 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-commented-continuation "$xray_copy
RUN git init -q . \\
#  && git apply --verbose /patches/xray-fix.patch \\
 && true" "$xray_makefile" X1
check xray-patch-commented-continuation 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-commented-run "$xray_copy
# RUN git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-commented-run 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-check-only "$xray_copy
RUN git apply --check /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-check-only 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-stat-only "$xray_copy
RUN git apply --stat /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-stat-only 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-reversed "$xray_copy
RUN git apply -R /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-reversed 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-failure-ignored "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch || true" "$xray_makefile" X1
check xray-patch-failure-ignored 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-other-stage "RUN true" "$xray_makefile" X1
printf 'FROM scratch AS builder\n%s\nRUN git apply --verbose /patches/xray-fix.patch\n' "$xray_copy" >>"$tmp/xray-patch-other-stage/fork/Dockerfile.head"
{ cat "$tmp/xray-patch-other-stage/fork/Dockerfile.head"; cat "$tmp/xray-patch-other-stage/fork/Dockerfile"; } >"$tmp/xray-patch-other-stage/fork/Dockerfile.new"
mv "$tmp/xray-patch-other-stage/fork/Dockerfile.new" "$tmp/xray-patch-other-stage/fork/Dockerfile"
rm "$tmp/xray-patch-other-stage/fork/Dockerfile.head"
git -C "$tmp/xray-patch-other-stage/fork" add -A
commit "$tmp/xray-patch-other-stage/fork" -m "apply the patch in another stage"
check xray-patch-other-stage 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-copied-from-elsewhere "COPY patches/other.patch /patches/xray-fix.patch
RUN git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-copied-from-elsewhere 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-makefile-commented "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch" "$xray_makefile_commented" X1
check xray-patch-makefile-commented 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-makefile-check-only "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch" 'build_xray:
	for patch in $(CURDIR)/patches/xray-*.patch; do git -C "$(XRAY_SRC)" apply --check "$$patch" || exit 1; done' X1
check xray-patch-makefile-check-only 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-makefile-failure-ignored "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch" 'build_xray:
	for patch in $(CURDIR)/patches/xray-*.patch; do git -C "$(XRAY_SRC)" apply --verbose "$$patch" || true; done' X1
check xray-patch-makefile-failure-ignored 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-names-another-component "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch" "$xray_makefile" B1
check xray-patch-names-another-component 1 DRIFT_COMPONENTS="node sing-box"

xray_fixture xray-patch-shell-comment "$xray_copy
RUN true # && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-shell-comment 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-in-a-copy-heredoc "$xray_copy
COPY <<NOTES /notes.txt
RUN git apply --verbose /patches/xray-fix.patch
NOTES" "$xray_makefile" X1
check xray-patch-in-a-copy-heredoc 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-under-a-no-op-shell "$xray_copy
SHELL [\"/bin/true\"]
RUN git apply --verbose /patches/xray-fix.patch
SHELL [\"/bin/sh\", \"-c\"]" "$xray_makefile" X1
check xray-patch-under-a-no-op-shell 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-reverted-later "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch
RUN git apply -R /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-reverted-later 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-replaced-before-apply "$xray_copy
RUN curl -fsSL -o /patches/xray-fix.patch https://example.com/other.patch && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-replaced-before-apply 1 DRIFT_COMPONENTS="node xray"

pending pending-not-a-calendar-day 'taken with the next release until:2026-09-31'
check pending-not-a-calendar-day 2

pending pending-day-zero 'taken with the next release until:2026-10-00'
check pending-day-zero 2

pending pending-malformed-issue-with-date 'taken with the next release until:2026-10-05 issue:none'
check pending-malformed-issue-with-date 2

pending pending-issue-without-number 'taken with the next release until:2026-10-05 issue:x/y#z'
check pending-issue-without-number 2

stub_curl
base advisories-endless
: >"$tmp/advisories-endless/stub.log"
git -C "$tmp/advisories-endless/fork" fetch -q "$tmp/advisories-endless/upw" "$(sha_of "$tmp/advisories-endless/upw" U1)"
git -C "$tmp/advisories-endless/fork" cherry-pick -x "$(sha_of "$tmp/advisories-endless/upw" U1)" >/dev/null
check advisories-endless 2 UPSTREAM_FETCH=1 PATH="$tmp/stub:$PATH" STUB_MODE=endless STUB_LOG="$tmp/advisories-endless/stub.log"

base advisories-two-pages
: >"$tmp/advisories-two-pages/stub.log"
git -C "$tmp/advisories-two-pages/fork" fetch -q "$tmp/advisories-two-pages/upw" "$(sha_of "$tmp/advisories-two-pages/upw" U1)"
git -C "$tmp/advisories-two-pages/fork" cherry-pick -x "$(sha_of "$tmp/advisories-two-pages/upw" U1)" >/dev/null
check advisories-two-pages 0 UPSTREAM_FETCH=1 PATH="$tmp/stub:$PATH" STUB_MODE=two-pages STUB_LOG="$tmp/advisories-two-pages/stub.log"
if [ "$(grep -c 'after=cursor1' "$tmp/advisories-two-pages/stub.log")" != 2 ]; then
  printf 'FAIL     %-28s the second page was not requested through the Link header of each endpoint\n' advisories-two-pages
  sed 's/^/         | /' "$tmp/advisories-two-pages/stub.log" | tail -6
  failed=$((failed + 1))
fi

base advisories-foreign-next
: >"$tmp/advisories-foreign-next/stub.log"
git -C "$tmp/advisories-foreign-next/fork" fetch -q "$tmp/advisories-foreign-next/upw" "$(sha_of "$tmp/advisories-foreign-next/upw" U1)"
git -C "$tmp/advisories-foreign-next/fork" cherry-pick -x "$(sha_of "$tmp/advisories-foreign-next/upw" U1)" >/dev/null
check advisories-foreign-next 2 UPSTREAM_FETCH=1 GITHUB_TOKEN=fixture-token PATH="$tmp/stub:$PATH" STUB_MODE=foreign-next STUB_LOG="$tmp/advisories-foreign-next/stub.log"
if grep -q 'attacker[.]example' "$tmp/advisories-foreign-next/stub.log"; then
  printf 'FAIL     %-28s the API token was sent to a next link outside the GitHub API\n' advisories-foreign-next
  failed=$((failed + 1))
fi

printf 'upstream-drift-test: %d passed, %d failed\n' "$pass" "$failed"
[ "$failed" = 0 ]
