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
  ran=$((ran + 1))
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

xray_final='FROM scratch
COPY --from=xray-builder /out/xray /usr/local/bin/xray'

xray_tail="RUN CGO_ENABLED=0 go build -trimpath -o /out/xray ./main

$xray_final"

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
    printf 'FROM scratch AS xray-builder\nCOPY patches/%s /patches/%s\nRUN git apply --verbose /patches/%s\n%s\n' "$2" "$2" "$2" "$xray_tail" >>"$f/Dockerfile"
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

xray_rewrite() {
  f=$tmp/$1/fork
  perl -pi -e "$2" "$f/Dockerfile"
  git -C "$f" add -A
  commit "$f" -m "rewrite the Dockerfile"
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

xray_fixture xray-patch-cd-before-apply "$xray_copy
RUN cd /tmp && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-cd-before-apply 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-workdir-after-copy "$xray_copy
WORKDIR /tmp
RUN git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-workdir-after-copy 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-rm-after-apply "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch && rm /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-rm-after-apply 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-exec-form-revert "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch
RUN [\"git\", \"apply\", \"-R\", \"/patches/xray-fix.patch\"]" "$xray_makefile" X1
check xray-patch-exec-form-revert 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-binary-from-other-stage "$xray_copy
RUN git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
printf 'FROM scratch\nCOPY --from=builder /out/xray /usr/local/bin/xray\n' >>"$tmp/xray-patch-binary-from-other-stage/fork/Dockerfile"
git -C "$tmp/xray-patch-binary-from-other-stage/fork" add -A
commit "$tmp/xray-patch-binary-from-other-stage/fork" -m "ship the xray binary from a different stage"
check xray-patch-binary-from-other-stage 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-checkout-after-apply "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && git checkout -- ." "$xray_makefile" X1
check xray-patch-checkout-after-apply 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-failure-ignored-later "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && true || true" "$xray_makefile" X1
check xray-patch-failure-ignored-later 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-built-before-apply "$xray_copy
RUN CGO_ENABLED=0 go build -o /out/xray ./main
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_final"
check xray-patch-built-before-apply 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-source-copied-over "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch
COPY orig/core.go /xray-src/core.go" "$xray_makefile" X1
check xray-patch-source-copied-over 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-prebuilt-binary "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -o /out/xray ./main
COPY prebuilt/xray /out/xray

$xray_final"
check xray-patch-prebuilt-binary 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-final-stage-replaces-binary "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
RUN curl -fsSL -o /usr/local/bin/xray https://example.com/xray"
check xray-patch-final-stage-replaces-binary 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-final-stage-copies-nothing "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -o /out/xray ./main

FROM scratch
RUN true"
check xray-patch-final-stage-copies-nothing 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-env-in-builder "$xray_copy
ENV GOFLAGS=-overlay=/overlay.json
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-env-in-builder 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-arg-in-builder "$xray_copy
ARG GOFLAGS=-overlay=/overlay.json
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-arg-in-builder 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-reverted-in-a-substitution "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && test -z \"\$(git checkout -- .)\"" "$xray_makefile" X1
check xray-patch-reverted-in-a-substitution 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-build-with-a-mount "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN --mount=type=cache,target=/xray-src CGO_ENABLED=0 go build -o /out/xray ./main

$xray_final"
check xray-patch-build-with-a-mount 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-build-with-an-overlay "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -overlay=/overlay.json -o /out/xray ./main

$xray_final"
check xray-patch-build-with-an-overlay 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-executable-path-overridden "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
ENV XRAY_EXECUTABLE_PATH=/opt/xray-stock"
check xray-patch-executable-path-overridden 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-replaced-through-a-glob "$xray_copy
RUN git init -q . && cp /tmp/evil /patches/xray-fix.pat?h && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-replaced-through-a-glob 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-applied-twice "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-applied-twice 1 DRIFT_COMPONENTS="node xray"

xray_real_body=$(cat <<'BODY'
ARG TARGETOS
ARG TARGETARCH
RUN apk update && apk add --no-cache git
WORKDIR /xray-src
COPY patches/xray-fix.patch /patches/xray-fix.patch
RUN git init -q . \
 && git remote add origin https://github.com/XTLS/Xray-core.git \
 && git fetch -q --depth 1 origin ${XRAY_COMMIT} \
 && git checkout -q FETCH_HEAD \
 && test "$(git rev-parse HEAD)" = "${XRAY_COMMIT}" \
 && git apply --verbose /patches/xray-fix.patch \
 && grep -qF "if dest.Port == 0 && port != \"\" {" proxy/dokodemo/dokodemo.go \
 && ! grep -rqE "(^|[^.[:alnum:]_])stats[.]GetOrRegister(Counter|OnlineMap)[(]" app proxy \
 && test "$(grep -c 'revocable.Wrap(w.dispatcher)' app/proxyman/inbound/worker.go)" = "2" \
 && grep -B1 -F "close(bind.closedCh)" proxy/wireguard/bind.go | grep -qF "default:" \
 && grep -q '"xray.trusted.forwarder.cidrs"' common/protocol/http/trusted_forwarder.go \
 && grep -q "google.golang.org/grpc v1.83.2$" go.mod
BODY
)
xray_real_tail=$(cat <<'TAIL'
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -buildid=" \
    -o /out/xray ./main

FROM scratch
COPY --from=xray-builder /out/xray /usr/local/bin/xray
TAIL
)
xray_fixture xray-patch-real-build-shape "$xray_real_body" "$xray_makefile" X1 "$xray_real_tail"
check xray-patch-real-build-shape 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-hash-inside-words "$xray_copy
RUN git init -q . && test a#b = a#b && test \"c #d\" = \"c #d\" && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-hash-inside-words 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-workdir-before-copy "WORKDIR /xray-src
$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-workdir-before-copy 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-after-a-heredoc-elsewhere "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-patch-after-a-heredoc-elsewhere 'FROM scratch AS notes
COPY <<EOF /notes.txt
hello
EOF'
check xray-patch-after-a-heredoc-elsewhere 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-heredoc-fakes-a-stage "$xray_copy
RUN true" "$xray_makefile" X1
xray_prepend xray-patch-heredoc-fakes-a-stage 'FROM scratch AS notes
COPY <<EOF /notes.txt
FROM scratch AS xray-builder
COPY patches/xray-fix.patch /patches/xray-fix.patch
RUN git init -q . && git apply --verbose /patches/xray-fix.patch
RUN CGO_ENABLED=0 go build -o /out/xray ./main
EOF'
check xray-patch-heredoc-fakes-a-stage 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-builder-on-a-poisoned-stage "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-patch-builder-on-a-poisoned-stage 'FROM scratch AS poison
RUN ln -sf /bin/true /usr/local/bin/git'
perl -pi -e 's/^FROM scratch AS xray-builder$/FROM poison AS xray-builder/' "$tmp/xray-patch-builder-on-a-poisoned-stage/fork/Dockerfile"
git -C "$tmp/xray-patch-builder-on-a-poisoned-stage/fork" add -A
commit "$tmp/xray-patch-builder-on-a-poisoned-stage/fork" -m "build xray on the poisoned stage"
check xray-patch-builder-on-a-poisoned-stage 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-syntax-directive "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-patch-syntax-directive '# syntax=example.com/another-frontend'
check xray-patch-syntax-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-uppercase-escape-directive "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-patch-uppercase-escape-directive '# ESCAPE=`'
check xray-patch-uppercase-escape-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-final-run-replaces-through-a-glob "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
RUN cp /opt/xray-stock /usr/local/bin/xr?y"
check xray-patch-final-run-replaces-through-a-glob 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-final-copy-into-a-directory "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
COPY --from=builder /out/xray /usr/local/bin/"
check xray-patch-final-copy-into-a-directory 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-two-xray-builder-stages "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -trimpath -o /out/xray ./main

FROM scratch AS xray-builder
RUN true

$xray_final"
check xray-patch-two-xray-builder-stages 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-odd-volume "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch
VOLUME /xray-src" "$xray_makefile" X1
check xray-patch-odd-volume 1 DRIFT_COMPONENTS="node xray"
if ! grep -q 'is not one the check can follow: xray-builder has a VOLUME instruction' "$tmp/xray-patch-odd-volume/out"; then
  printf 'FAIL     %-28s no note names the instruction the check refused\n' xray-patch-odd-volume
  failed=$((failed + 1))
fi
if grep -q 'is not one the check can follow' "$tmp/xray-patch-real-build-shape/out"; then
  printf 'FAIL     %-28s the real build shape was refused\n' xray-patch-real-build-shape
  failed=$((failed + 1))
fi

xray_fixture xray-patch-odd-go-generate "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && go generate ./..." "$xray_makefile" X1
check xray-patch-odd-go-generate 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-odd-sh-c "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && sh -c 'git checkout -- .'" "$xray_makefile" X1
check xray-patch-odd-sh-c 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-odd-label "$xray_copy
LABEL stage=xray
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-odd-label 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-odd-make "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN make build_xray

$xray_final"
check xray-patch-odd-make 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-odd-eval "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && eval true" "$xray_makefile" X1
check xray-patch-odd-eval 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-file-is-a-symlink "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
rm "$tmp/xray-patch-file-is-a-symlink/fork/patches/xray-fix.patch"
ln -s "$(printf 'xray-real.patch\nUpstream: %s' "$(sha_of "$tmp/xray-patch-file-is-a-symlink/xray-up" X1)")" \
  "$tmp/xray-patch-file-is-a-symlink/fork/patches/xray-fix.patch"
git -C "$tmp/xray-patch-file-is-a-symlink/fork" add -A
commit "$tmp/xray-patch-file-is-a-symlink/fork" -m "turn the patch into a symlink"
check xray-patch-file-is-a-symlink 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-pin-declared-twice "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-pin-declared-twice "ARG XRAY_COMMIT=$(sha_of "$tmp/xray-pin-declared-twice/xray-up" X1)"
check xray-pin-declared-twice 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-pin-declared-twice-lowercase "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-pin-declared-twice-lowercase "arg XRAY_COMMIT=$(sha_of "$tmp/xray-pin-declared-twice-lowercase/xray-up" X1)"
check xray-pin-declared-twice-lowercase 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-pin-declared-twice-in-the-builder-stage "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-pin-declared-twice-in-the-builder-stage "s/^(ARG XRAY_COMMIT=.*)\$/\$1\nARG XRAY_COMMIT=$(sha_of "$tmp/xray-pin-declared-twice-in-the-builder-stage/xray-up" X1)/"
check xray-pin-declared-twice-in-the-builder-stage 2 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-fetches-another-ref "$xray_copy
RUN git init -q . && git fetch -q --depth 1 origin main && git checkout -q FETCH_HEAD && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-fetches-another-ref 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-followed-with-nothing-to-port "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
perl -pi -e "s/^ARG XRAY_COMMIT=.*/ARG XRAY_COMMIT=$(sha_of "$tmp/xray-build-followed-with-nothing-to-port/xray-up" X1)/" \
  "$tmp/xray-build-followed-with-nothing-to-port/fork/Dockerfile"
git -C "$tmp/xray-build-followed-with-nothing-to-port/fork" add -A
commit "$tmp/xray-build-followed-with-nothing-to-port/fork" -m "pin the newest commit"
check xray-build-followed-with-nothing-to-port 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-refused-with-nothing-to-port "$xray_copy
LABEL stage=xray
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
perl -pi -e "s/^ARG XRAY_COMMIT=.*/ARG XRAY_COMMIT=$(sha_of "$tmp/xray-build-refused-with-nothing-to-port/xray-up" X1)/" \
  "$tmp/xray-build-refused-with-nothing-to-port/fork/Dockerfile"
git -C "$tmp/xray-build-refused-with-nothing-to-port/fork" add -A
commit "$tmp/xray-build-refused-with-nothing-to-port/fork" -m "pin the newest commit"
check xray-build-refused-with-nothing-to-port 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-overwritten-by-a-redirect "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && grep -c x go.mod > core.go" "$xray_makefile" X1
check xray-patch-overwritten-by-a-redirect 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-apply-piped-into-a-check "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch | grep -q x" "$xray_makefile" X1
check xray-patch-apply-piped-into-a-check 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-applied-after-the-build "$xray_copy
COPY patches/xray-other.patch /patches/xray-other.patch
RUN git init -q . && git apply --verbose /patches/xray-other.patch
RUN CGO_ENABLED=0 go build -o /out/xray ./main
RUN git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_final"
check xray-patch-applied-after-the-build 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-source-from-patches "$xray_copy
RUN git init -q . && git fetch -q /patches/xray-source HEAD && git checkout -q FETCH_HEAD && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-source-from-patches 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-reverted-in-backquotes "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && test -z \"\`git checkout -- .\`\"" "$xray_makefile" X1
check xray-patch-reverted-in-backquotes 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-setup-after-apply "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch && git checkout -q FETCH_HEAD" "$xray_makefile" X1
check xray-build-setup-after-apply 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-goflags-assignment "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN GOFLAGS=-overlay=/overlay.json go build -o /out/xray ./main

$xray_final"
check xray-build-goflags-assignment 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-fetches-no-ref "$xray_copy
RUN git init -q . && git fetch -q --depth 1 origin && git checkout -q FETCH_HEAD && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-fetches-no-ref 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-checks-out-another-commit "$xray_copy
RUN git init -q . && git fetch -q --depth 1 origin \${XRAY_COMMIT} && git checkout -q main && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-checks-out-another-commit 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-init-in-the-parent "$xray_copy
RUN git init -q .. && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-init-in-the-parent 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-remote-to-a-local-repo "$xray_copy
RUN git init -q . && git remote add origin file:///opt/xray && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-remote-to-a-local-repo 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-apk-untrusted-package "$xray_copy
RUN apk add --allow-untrusted /tmp/git.apk
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-apk-untrusted-package 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-fetch-by-the-pin "$xray_copy
RUN git init -q . && git remote add origin https://github.com/XTLS/Xray-core.git && git fetch -q --depth 1 origin \${XRAY_COMMIT} && git checkout -q FETCH_HEAD && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-fetch-by-the-pin 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-comment-lines-in-the-stage "# the xray source
$xray_copy
RUN git init -q . \\
# the fork patches
 && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-comment-lines-in-the-stage 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-applied-without-copy "RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-applied-without-copy 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-copied-from-another-stage "COPY --from=builder patches/xray-fix.patch /patches/xray-fix.patch
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-copied-from-another-stage 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-directory-copied-onto-patches "$xray_copy
COPY other-patches/ /patches/
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-directory-copied-onto-patches 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-directory-copied-onto-patches-no-slash "$xray_copy
COPY other-patches/ /patches
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-directory-copied-onto-patches-no-slash 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-trailing-comment-on-the-apply "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch # take the fork fix" "$xray_makefile" X1
check xray-build-trailing-comment-on-the-apply 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-heredoc-body-is-not-read "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-build-heredoc-body-is-not-read 'FROM scratch AS notes
COPY <<EOF /notes.txt
RUN cp /opt/xray-stock /usr/local/bin/xray
EOF'
check xray-build-heredoc-body-is-not-read 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-never-built "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_final"
check xray-build-never-built 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-fetch-with-an-upload-pack "$xray_copy
RUN git init -q . && git fetch -q --upload-pack=true origin \${XRAY_COMMIT} && git checkout -q FETCH_HEAD && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-build-fetch-with-an-upload-pack 1 DRIFT_COMPONENTS="node xray"

xray_branch_scope() {
  xray_fixture "$1" "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
  s=$tmp/$1
  git -C "$s/xray-up" checkout -q -b side
  printf 'package core\n\nvar raced = true\n' >"$s/xray-up/race.go"
  git -C "$s/xray-up" add -A
  commit "$s/xray-up" -m "XB fix a race on the side branch"
  git -C "$s/xray-up" checkout -q main
  printf '%s superseded: pre-squash commit on upstream branch side, folded into a later main commit branch:side\n' \
    "$(sha_of "$s/xray-up" XB)" >>"$s/waivers"
  if [ "$2" = merged ]; then
    git -C "$s/xray-up" merge -q --no-ff --no-edit side
    git -C "$s/xray-up" branch -q -D side
  fi
  git --git-dir="$s/cache/xray.git" fetch -q --prune "$s/xray-up" '+refs/heads/*:refs/remotes/up/*' '+refs/tags/*:refs/tags/*'
}

xray_branch_scope xray-branch-scope-on-branch open
check xray-branch-scope-on-branch 0 DRIFT_COMPONENTS="node xray"

xray_branch_scope xray-branch-scope-after-merge merged
check xray-branch-scope-after-merge 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-crlf-hides-the-builder-stage "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-build-crlf-hides-the-builder-stage 's/^ARG SINGBOX_VERSION=v1\.0\.0\n/ARG SINGBOX_VERSION=v1.0.0 \\\r\n/'
check xray-build-crlf-hides-the-builder-stage 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-copied-over-through-a-double-slash "$xray_copy
COPY other/x.patch /patches//xray-fix.patch
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-copied-over-through-a-double-slash 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-patch-copied-over-through-a-dot-segment "$xray_copy
COPY other/x.patch /patches/./xray-fix.patch
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
check xray-patch-copied-over-through-a-dot-segment 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-directory-copied-over-usr-local "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
COPY --from=builder /tree /usr/local"
check xray-build-directory-copied-over-usr-local 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-exec-path-in-the-entrypoint "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
ENTRYPOINT [\"env\", \"XRAY_EXECUTABLE_PATH=/opt/xray-stock\", \"./main\"]"
check xray-build-exec-path-in-the-entrypoint 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-env-file-copied-into-the-image "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
COPY deploy/node.env /app/.env"
check xray-build-env-file-copied-into-the-image 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-label-heredoc-hides-a-replace "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
LABEL note=\"see <<EOF\"
RUN cp /opt/xray-stock /usr/local/bin/xray"
check xray-build-label-heredoc-hides-a-replace 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-slash-slash-directive "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-build-slash-slash-directive '// syntax=example.com/another-frontend'
check xray-build-slash-slash-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-bom-before-a-directive "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-build-bom-before-a-directive '# syntax=example.com/another-frontend'
xray_rewrite xray-build-bom-before-a-directive 's/^/\xef\xbb\xbf/ if $. == 1'
check xray-build-bom-before-a-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-vertical-tab-before-a-directive "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_prepend xray-build-vertical-tab-before-a-directive '# syntax=example.com/another-frontend'
xray_rewrite xray-build-vertical-tab-before-a-directive 's/^/\x0b/ if $. == 1'
check xray-build-vertical-tab-before-a-directive 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-copies-a-stock-binary-from-the-builder "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -trimpath -o /out/xray ./main

FROM scratch
COPY --from=xray-builder /usr/bin/xray-stock /usr/local/bin/xray"
check xray-build-copies-a-stock-binary-from-the-builder 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-unpinned-builder-image "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-build-unpinned-builder-image 's/^FROM scratch AS xray-builder$/FROM golang:latest AS xray-builder/'
check xray-build-unpinned-builder-image 1 DRIFT_COMPONENTS="node xray"

noint_awk=$tmp/noint-awk
mkdir -p "$noint_awk"
cat >"$noint_awk/awk" <<EOF
#!/bin/sh
for a do
  case \$a in
    *[{][0-9]*) echo "awk: this awk has no interval expressions" >&2; exit 2 ;;
  esac
done
exec $(command -v awk) "\$@"
EOF
chmod +x "$noint_awk/awk"

builder_digest() {
  xray_fixture "$1" "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
  xray_rewrite "$1" "s/^FROM scratch AS xray-builder\$/FROM golang:1.26\@sha256:$2 AS xray-builder/"
}

builder_digest xray-build-digest-pinned-builder-on-an-awk-without-intervals "$(printf '%064d' 0)"
check xray-build-digest-pinned-builder-on-an-awk-without-intervals 0 DRIFT_COMPONENTS="node xray" PATH="$noint_awk:$PATH"

builder_digest xray-build-builder-digest-one-digit-short "$(printf '%063d' 0)"
check xray-build-builder-digest-one-digit-short 1 DRIFT_COMPONENTS="node xray" PATH="$noint_awk:$PATH"

builder_digest xray-build-builder-digest-not-hex "$(printf '%063dg' 0)"
check xray-build-builder-digest-not-hex 1 DRIFT_COMPONENTS="node xray" PATH="$noint_awk:$PATH"

xray_fixture xray-build-builder-tag-shaped-like-a-digest "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-build-builder-tag-shaped-like-a-digest "s/^FROM scratch AS xray-builder\$/FROM golang:$(printf '%064d' 0) AS xray-builder/"
check xray-build-builder-tag-shaped-like-a-digest 1 DRIFT_COMPONENTS="node xray" PATH="$noint_awk:$PATH"

xray_fixture xray-pin-declared-on-a-continuation-line "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-pin-declared-on-a-continuation-line 's/^ARG XRAY_COMMIT=/ARG TARGETOS \\\n XRAY_COMMIT=/'
xray_prepend xray-pin-declared-on-a-continuation-line "ARG XRAY_COMMIT=$(sha_of "$tmp/xray-pin-declared-on-a-continuation-line/xray-up" X1)"
check xray-pin-declared-on-a-continuation-line 1 DRIFT_COMPONENTS="node xray"

xray_stock_image='docker.io/example/xray-stock@sha256:1111111111111111111111111111111111111111111111111111111111111111'

xray_fixture xray-build-crlf-valid-build "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-build-crlf-valid-build 's/\n/\r\n/'
check xray-build-crlf-valid-build 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-heredoc-fakes-the-install "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -trimpath -o /out/xray ./main

FROM $xray_stock_image
RUN true <<LABEL
COPY --from=xray-builder /out/xray /usr/local/bin/xray
LABEL"
check xray-build-heredoc-fakes-the-install 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-directory-copied-over-usr-local-through-a-double-slash "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
COPY --from=builder /tree /usr//local"
check xray-build-directory-copied-over-usr-local-through-a-double-slash 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-directory-copied-over-usr-local-through-a-relative-path "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "$xray_tail
COPY --from=builder /tree usr/local"
check xray-build-directory-copied-over-usr-local-through-a-relative-path 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-installed-off-the-executable-path "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -trimpath -o /out/xray ./main

FROM $xray_stock_image
COPY --from=xray-builder /out/xray /opt/xray"
check xray-build-installed-off-the-executable-path 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-build-to-a-relative-output-path "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1 "RUN CGO_ENABLED=0 go build -trimpath -o xray ./main

FROM scratch
COPY --from=xray-builder xray /usr/local/bin/xray"
check xray-build-to-a-relative-output-path 1 DRIFT_COMPONENTS="node xray"

node_branch_only() {
  base "$1"
  s=$tmp/$1
  git -C "$s/fork" fetch -q "$s/upw" "$(sha_of "$s/upw" U1)"
  git -C "$s/fork" cherry-pick -x "$(sha_of "$s/upw" U1)" >/dev/null
  git -C "$s/upw" checkout -q -b "$2"
  printf 'package main\n\nfunc extra() int { return 7 }\n' >"$s/upw/extra.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "B1 work on $2"
  git -C "$s/upw" checkout -q main
  printf '%s rejected: upstream work on %s still awaits a decision upstream\n' "$(sha_of "$s/upw" B1)" "$2" >>"$s/waivers"
}

node_branch_only branch-scope-required-on-a-branch-only-commit pr-9
check branch-scope-required-on-a-branch-only-commit 1
if grep -q 'waiver for this commit is no longer needed' "$tmp/branch-scope-required-on-a-branch-only-commit/out"; then
  printf 'FAIL     %-28s calls a waiver that only lacks its scope no longer needed\n' branch-scope-required-on-a-branch-only-commit
  failed=$((failed + 1))
fi

node_branch_only branch-scope-not-required-on-dev dev
check branch-scope-not-required-on-dev 0

xray_branch_only() {
  xray_fixture "$1" "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
  s=$tmp/$1
  git -C "$s/xray-up" checkout -q -b "$2"
  printf 'package core\n\nvar raced = true\n' >"$s/xray-up/race.go"
  git -C "$s/xray-up" add -A
  commit "$s/xray-up" -m "XB fix a race on $2"
  [ -z "$4" ] || git -C "$s/xray-up" branch -q "$4"
  git -C "$s/xray-up" checkout -q main
  printf '%s superseded: pre-squash commit on upstream branch %s, folded into a later main commit%s\n' \
    "$(sha_of "$s/xray-up" XB)" "$2" "$3" >>"$s/waivers"
  git --git-dir="$s/cache/xray.git" fetch -q --prune "$s/xray-up" '+refs/heads/*:refs/remotes/up/*' '+refs/tags/*:refs/tags/*'
}

xray_branch_only xray-branch-scope-required-on-a-branch-only-commit side "" ""
check xray-branch-scope-required-on-a-branch-only-commit 1 DRIFT_COMPONENTS="node xray"

xray_branch_only xray-branch-scope-not-required-on-unstable unstable "" ""
check xray-branch-scope-not-required-on-unstable 0 DRIFT_COMPONENTS="node xray"

xray_branch_only xray-bot-branch-does-not-reopen-a-scoped-waiver side " branch:side" renovate/deps
check xray-bot-branch-does-not-reopen-a-scoped-waiver 0 DRIFT_COMPONENTS="node xray"

xray_fixture stale-claim-on-a-merged-xray-commit "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
s=$tmp/stale-claim-on-a-merged-xray-commit
git -C "$s/xray-up" checkout -q -b side
printf 'package core\n\nvar raced = true\n' >"$s/xray-up/race.go"
git -C "$s/xray-up" add -A
commit "$s/xray-up" -m "XB fix a race on side"
git -C "$s/xray-up" checkout -q main
printf '%s rejected: the upstream pull request is still open and does not touch server code\n' "$(sha_of "$s/xray-up" XB)" >>"$s/waivers"
git -C "$s/xray-up" merge -q --no-ff --no-edit side
git -C "$s/xray-up" branch -q -D side
git --git-dir="$s/cache/xray.git" fetch -q --prune "$s/xray-up" '+refs/heads/*:refs/remotes/up/*' '+refs/tags/*:refs/tags/*'
check stale-claim-on-a-merged-xray-commit 1 DRIFT_COMPONENTS="node xray"

xray_fixture xray-pin-declared-in-another-stage "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-pin-declared-in-another-stage "s/^ARG SINGBOX_VERSION=v1\.0\.0\$/ARG SINGBOX_VERSION=v1.0.0\nARG XRAY_COMMIT=$(sha_of "$tmp/xray-pin-declared-in-another-stage/xray-up" X1)/"
check xray-pin-declared-in-another-stage 0 DRIFT_COMPONENTS="node xray"

xray_fixture xray-pin-only-global-without-a-stage-declaration "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-pin-only-global-without-a-stage-declaration 's/^ARG XRAY_COMMIT=.*\n//'
xray_prepend xray-pin-only-global-without-a-stage-declaration "ARG XRAY_COMMIT=$(sha_of "$tmp/xray-pin-only-global-without-a-stage-declaration/xray-up" X0)"
check xray-pin-only-global-without-a-stage-declaration 2 DRIFT_COMPONENTS="node xray"

xray_fixture xray-pin-two-global-declarations "$xray_copy
RUN git init -q . && git apply --verbose /patches/xray-fix.patch" "$xray_makefile" X1
xray_rewrite xray-pin-two-global-declarations 's/^ARG XRAY_COMMIT=.*$/ARG XRAY_COMMIT/'
xray_prepend xray-pin-two-global-declarations "ARG XRAY_COMMIT=$(sha_of "$tmp/xray-pin-two-global-declarations/xray-up" X1)
ARG XRAY_COMMIT=$(sha_of "$tmp/xray-pin-two-global-declarations/xray-up" X0)"
check xray-pin-two-global-declarations 2 DRIFT_COMPONENTS="node xray"

pending pending-not-a-calendar-day 'taken with the next release until:2026-09-31'
check pending-not-a-calendar-day 2

pending pending-day-zero 'taken with the next release until:2026-10-00'
check pending-day-zero 2

pending pending-malformed-issue-with-date 'taken with the next release until:2026-10-05 issue:none'
check pending-malformed-issue-with-date 2

pending pending-issue-without-number 'taken with the next release until:2026-10-05 issue:x/y#z'
check pending-issue-without-number 2

branch_scope() {
  base "$1"
  s=$tmp/$1
  git -C "$s/fork" fetch -q "$s/upw" "$(sha_of "$s/upw" U1)"
  git -C "$s/fork" cherry-pick -x "$(sha_of "$s/upw" U1)" >/dev/null
  git -C "$s/upw" checkout -q -b pr-61
  printf 'package main\n\nfunc telemt() int { return 2 }\n' >"$s/upw/telemt.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "P1 telemt backend"
  p1=$(sha_of "$s/upw" P1)
  git -C "$s/upw" checkout -q main
  printf '%s rejected: upstream PR #61 (branch pr-61, open, not merged) collides with the fork mtproto backend; revisit if upstream merges it branch:pr-61\n' "$p1" >>"$s/waivers"
  if [ "$2" = merged ]; then
    git -C "$s/upw" merge -q --no-ff --no-edit pr-61
    git -C "$s/upw" branch -q -D pr-61
    git -C "$s/upw" tag v9.9.9
  fi
}

branch_scope branch-scope-on-branch open
check branch-scope-on-branch 0

branch_scope branch-scope-after-merge merged
check branch-scope-after-merge 1

base branch-scope-lint-unmerged
printf '%s rejected: upstream PR, open, not merged; revisit if upstream merges it\n' \
  "$(sha_of "$tmp/branch-scope-lint-unmerged/upw" U1)" >>"$tmp/branch-scope-lint-unmerged/waivers"
check branch-scope-lint-unmerged 2

base branch-scope-lint-not-merged-only
printf '%s rejected: upstream PR 7 is open and not merged into a release\n' \
  "$(sha_of "$tmp/branch-scope-lint-not-merged-only/upw" U1)" >>"$tmp/branch-scope-lint-not-merged-only/waivers"
check branch-scope-lint-not-merged-only 2

base branch-scope-lint-never-merged-only
printf '%s not-applicable: diagnostic logging on a debug branch, never merged into a release\n' \
  "$(sha_of "$tmp/branch-scope-lint-never-merged-only/upw" U1)" >>"$tmp/branch-scope-lint-never-merged-only/waivers"
check branch-scope-lint-never-merged-only 2

base branch-scope-lint-revisit-only
printf '%s rejected: collides with the fork mtproto backend; revisit if upstream merges it\n' \
  "$(sha_of "$tmp/branch-scope-lint-revisit-only/upw" U1)" >>"$tmp/branch-scope-lint-revisit-only/waivers"
check branch-scope-lint-revisit-only 2

base branch-scope-malformed-token
printf '%s rejected: upstream PR #61 branch, open, not merged; revisit if upstream merges it branch:\n' \
  "$(sha_of "$tmp/branch-scope-malformed-token/upw" U1)" >>"$tmp/branch-scope-malformed-token/waivers"
check branch-scope-malformed-token 2

branch_scope branch-scope-tagged-on-the-branch open
git -C "$tmp/branch-scope-tagged-on-the-branch/upw" tag v9.9.9 pr-61
check branch-scope-tagged-on-the-branch 1

branch_scope_merge() {
  base "$1"
  s=$tmp/$1
  git -C "$s/fork" fetch -q "$s/upw" "$(sha_of "$s/upw" U1)"
  git -C "$s/fork" cherry-pick -x "$(sha_of "$s/upw" U1)" >/dev/null
  git -C "$s/upw" checkout -q -b side
  printf 'package main\n\nfunc c() int { return 3 }\n' >"$s/upw/c.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "S1 add c"
  git -C "$s/fork" fetch -q "$s/upw" "$(sha_of "$s/upw" S1)"
  git -C "$s/fork" cherry-pick -x "$(sha_of "$s/upw" S1)" >/dev/null
  git -C "$s/upw" checkout -q main
  git -C "$s/upw" checkout -q -b pr-61
  git -C "$s/upw" merge -q --no-ff --no-commit side >/dev/null 2>&1
  printf 'package main\n\nfunc telemt() int { return 2 }\n' >"$s/upw/telemt.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "M1 merge side with a telemt backend"
  git -C "$s/upw" checkout -q main
  git -C "$s/upw" branch -q -D side
  printf '%s rejected: upstream merge on the open PR #61 branch pr-61 (open, not merged) collides with the fork mtproto backend; revisit if upstream merges it branch:pr-61\n' \
    "$(sha_of "$s/upw" M1)" >>"$s/waivers"
  if [ "$2" = merged ]; then
    git -C "$s/upw" merge -q --no-ff --no-edit pr-61
    git -C "$s/upw" branch -q -D pr-61
    git -C "$s/upw" tag v9.9.9
  fi
}

branch_scope_merge branch-scope-merge-on-branch open
check branch-scope-merge-on-branch 0

branch_scope_merge branch-scope-merge-after-merge merged
check branch-scope-merge-after-merge 1

merge_branch_only() {
  base "$1"
  s=$tmp/$1
  git -C "$s/fork" fetch -q "$s/upw" "$(sha_of "$s/upw" U1)"
  git -C "$s/fork" cherry-pick -x "$(sha_of "$s/upw" U1)" >/dev/null
  git -C "$s/upw" checkout -q -b side
  printf 'package main\n\nfunc c() int { return 3 }\n' >"$s/upw/c.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "S1 add c"
  git -C "$s/fork" fetch -q "$s/upw" "$(sha_of "$s/upw" S1)"
  git -C "$s/fork" cherry-pick -x "$(sha_of "$s/upw" S1)" >/dev/null
  git -C "$s/upw" checkout -q main
  git -C "$s/upw" checkout -q -b pr-61
  git -C "$s/upw" merge -q --no-ff --no-commit side >/dev/null 2>&1
  printf 'package main\n\nfunc telemt() int { return 2 }\n' >"$s/upw/telemt.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "M1 merge side with a telemt backend"
  git -C "$s/upw" checkout -q main
  git -C "$s/upw" branch -q -D side
  printf '%s rejected: %s\n' "$(sha_of "$s/upw" M1)" "$2" >>"$s/waivers"
  if [ "$3" = merged ]; then
    git -C "$s/upw" merge -q --no-ff --no-edit pr-61
    git -C "$s/upw" branch -q -D pr-61
  fi
}

merge_branch_only branch-scope-required-on-a-branch-only-merge 'collides with the fork mtproto backend' open
check branch-scope-required-on-a-branch-only-merge 1

merge_branch_only stale-claim-on-a-merged-merge 'the pull request is still open and collides with the fork mtproto backend' merged
check stale-claim-on-a-merged-merge 1

branch_scope branch-scope-bot-branch-does-not-reopen-a-node-scope open
git -C "$tmp/branch-scope-bot-branch-does-not-reopen-a-node-scope/upw" branch -q dependabot/go pr-61
check branch-scope-bot-branch-does-not-reopen-a-node-scope 0

d10_plain() {
  base "$1"
  s=$tmp/$1
  git -C "$s/fork" fetch -q "$s/upw" "$(sha_of "$s/upw" U1)"
  git -C "$s/fork" cherry-pick -x "$(sha_of "$s/upw" U1)" >/dev/null
  git -C "$s/upw" checkout -q -b "$2"
  printf 'package main\n\nfunc telemt() int { return 2 }\n' >"$s/upw/telemt.go"
  git -C "$s/upw" add -A
  commit "$s/upw" -m "P1 telemt backend"
  git -C "$s/upw" checkout -q main
  printf '%s rejected: %s\n' "$(sha_of "$s/upw" P1)" "$3" >>"$s/waivers"
  case $4 in
    merged)
      git -C "$s/upw" merge -q --no-ff --no-edit "$2"
      git -C "$s/upw" branch -q -D "$2"
      ;;
    tagged) git -C "$s/upw" tag v9.9.9 "$2" ;;
  esac
}

d10_plain branch-scope-token-in-parentheses-plain-wording pr-61 'collides with the fork mtproto backend (branch:pr-61), see the fork backend' merged
check branch-scope-token-in-parentheses-plain-wording 1

d10_plain stale-claim-unmerged-alone pr-61 'upstream PR #61 is unmerged; it collides with the fork mtproto backend' merged
check stale-claim-unmerged-alone 1

d10_plain branch-scope-not-required-on-a-tagged-commit pr-9 'collides with the fork mtproto backend' tagged
check branch-scope-not-required-on-a-tagged-commit 0

d10_plain branch-scope-required-on-a-bot-only-commit dependabot/go 'collides with the fork mtproto backend' open
check branch-scope-required-on-a-bot-only-commit 1

d10_plain branch-scope-naming-a-bot-branch dependabot/go 'collides with the fork mtproto backend branch:dependabot/go' open
check branch-scope-naming-a-bot-branch 0

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

finished=1
printf 'upstream-drift-test: %d of %d cases ran, %d passed, %d failed\n' "$ran" "$declared" "$pass" "$failed"
if [ "$ran" != "$declared" ]; then
  printf 'upstream-drift-test: INCOMPLETE, %d cases ran but the suite declares %d\n' "$ran" "$declared"
  exit 2
fi
[ "$failed" = 0 ]
