#!/usr/bin/env bash
set -u

root=$(git rev-parse --show-toplevel) || exit 2
cd "$root" || exit 2

WF_BRANCH=image-branch.yml
WF_MANUAL=image-manual.yml
WF_RELEASE=publish-release.yml
WF_IMAGE=image.yml
WF_GUARD=release-guard.yml
WF_TESTS=run-tests.yml
WF_DRIFT=upstream-drift.yml

H=$(mktemp -d "${TMPDIR:-/tmp}/workflow-steps-test.XXXXXX")
trap 'rm -rf "$H"' EXIT
mkdir -p "$H/bin" "$H/cases" "$H/fixtures"

(cd "$root/scripts/workflowgate" && go build -o "$H/workflowgate" .) || exit 2
"$H/workflowgate" steps -dir "$root/.github/workflows" -out "$H/steps" || exit 2

cat >"$H/bin/sudo" <<'SH'
#!/usr/bin/env bash
while [ "${1#-}" != "$1" ]; do shift; done
exec "$@"
SH
cat >"$H/bin/sleep" <<'SH'
#!/usr/bin/env bash
echo "sleep $*" >>"$STUB_LOG"
SH
cat >"$H/bin/make" <<'SH'
#!/usr/bin/env bash
echo "make $*" >>"$STUB_LOG"
if [ -n "${MTP_PLAN:-}" ]; then
  n=$(( $(cat "$STUB_DIR/mtp_count" 2>/dev/null || echo 0) + 1 ))
  echo "$n" >"$STUB_DIR/mtp_count"
  outcome=$(echo "$MTP_PLAN" | cut -d' ' -f"$n")
  case "$outcome" in
    pass) echo "=== RUN   TestLiveSmoke_MTProtoStartsAndDomainFronts"; echo "--- PASS: TestLiveSmoke_MTProtoStartsAndDomainFronts (3.10s)"; echo PASS; exit 0 ;;
    none) echo "testing: warning: no tests to run"; echo PASS; exit 0 ;;
    *) echo "--- FAIL: TestLiveSmoke_MTProtoStartsAndDomainFronts (30.00s)"; echo FAIL; exit 1 ;;
  esac
fi
cat "$MAKE_OUT"
exit "${MAKE_RC:-0}"
SH
cat >"$H/bin/git" <<'SH'
#!/usr/bin/env bash
echo "git $*" >>"$STUB_LOG"
case "$1" in
  ls-remote)
    [ "${LS_RC:-0}" = 0 ] || exit "$LS_RC"
    printf '%b' "${LS_OUT:-}"
    exit 0
    ;;
  for-each-ref)
    [ "${GIT_REFS_RC:-0}" = 0 ] || exit "$GIT_REFS_RC"
    printf '%b' "${GIT_REFS:-}"
    exit 0
    ;;
  merge-base)
    [ "$2" = --is-ancestor ] || exit 99
    for ref in ${GIT_CONTAINS:-}; do
      [ "$ref" = "$4" ] && exit 0
    done
    exit 1
    ;;
esac
exit 99
SH
cat >"$H/bin/docker" <<'SH'
#!/usr/bin/env bash
echo "docker $*" >>"$STUB_LOG"
if [ "$1 $2 $3" = "buildx imagetools inspect" ]; then
  ref=$4
  if [ -n "${DOCKER_INSPECT_ERROR:-}" ]; then
    echo "ERROR: ${DOCKER_INSPECT_ERROR}" >&2
    exit 1
  fi
  if [ -n "${DOCKER_TAGS:-}" ]; then
    while read -r name digest; do
      if [ "$name" = "$ref" ]; then
        printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","digest":"%s","size":1}\n' "$digest"
        exit 0
      fi
    done <"$DOCKER_TAGS"
  fi
  echo "ERROR: ${ref}: not found" >&2
  exit 1
fi
if [ "$1 $2 $3" = "buildx imagetools create" ]; then
  exit "${DOCKER_CREATE_RC:-0}"
fi
exit 0
SH
cat >"$H/bin/curl" <<'SH'
#!/usr/bin/env bash
echo "curl $*" >>"$STUB_LOG"
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    -H|-w) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
status=${CURL_STATUS:-200}
body=${CURL_BODY:-'{}'}
if [ -n "${CURL_MAP:-}" ]; then
  status=404
  body='{}'
  while read -r pat st bd; do
    [ -n "$pat" ] || continue
    case "$url" in *"$pat") status=$st; body=${bd:-'{}'}; break ;; esac
  done <"$CURL_MAP"
fi
[ -z "$out" ] || printf '%s' "$body" >"$out"
printf '%s' "$status"
SH
cat >"$H/bin/gh" <<'SH'
#!/usr/bin/env bash
echo "gh $*" >>"$STUB_LOG"
case "$1 ${2:-}" in
  "release edit") exit "${GH_EDIT_RC:-${GH_RC:-0}}" ;;
  "release upload") exit "${GH_UPLOAD_RC:-${GH_RC:-0}}" ;;
  "release view")
    [ "${GH_VIEW_RC:-0}" = 0 ] || exit "$GH_VIEW_RC"
    printf '%s\n' "${GH_VIEW:-0}"
    exit 0
    ;;
  "run list")
    [ "${GH_RUNS_RC:-0}" = 0 ] || exit "$GH_RUNS_RC"
    printf '%s\n' "${GH_RUNS:-[]}"
    exit 0
    ;;
esac
if [ "$1" != api ]; then
  exit "${GH_RC:-0}"
fi
path=""
for arg in "$@"; do
  case "$arg" in
    api|-*) ;;
    *) path=$arg ;;
  esac
done
if [ -z "${GH_MAP:-}" ]; then
  case "$path" in
    *releases\?per_page=100) cat "$GH_RELEASES" ;;
    */git/ref/tags/*) printf '%s' "$GH_REF" ;;
    */git/tags/*) printf '%s' "$GH_TAGOBJ" ;;
    *) exit 98 ;;
  esac
  exit 0
fi
entries=()
while read -r suffix rc file; do
  [ -n "$suffix" ] || continue
  case "$path" in *"$suffix") entries+=("$rc $file") ;; esac
done <"$GH_MAP"
[ "${#entries[@]}" -gt 0 ] || exit 98
key=$(printf '%s' "$path" | cksum | cut -d' ' -f1)
count=$(cat "$STUB_DIR/gh.$key" 2>/dev/null || echo 0)
echo $((count + 1)) >"$STUB_DIR/gh.$key"
pick=$count
[ "$pick" -lt "${#entries[@]}" ] || pick=$((${#entries[@]} - 1))
entry=${entries[$pick]}
rc=${entry%% *}
file=${entry#* }
[ ! -f "$file" ] || cat "$file"
exit "$rc"
SH
chmod +x "$H"/bin/*

pass=0
failn=0

ok() {
  pass=$((pass + 1))
}

bad() {
  failn=$((failn + 1))
  printf 'FAIL %s\n' "$*"
}

step_index() {
  awk -F'\t' -v wf="$1" -v job="$2" -v name="$3" '$1 == wf && $2 == job && $5 == name { print $3; exit }' "$H/steps/index"
}

new_case() {
  local d="$H/cases/$1"
  rm -rf "$d"
  mkdir -p "$d/tmp"
  : >"$d/out"
  : >"$d/summary"
  : >"$d/stub"
  : >"$d/env"
  : >"$d/log"
  : >"$d/steps"
}

exec_step() {
  local d=$1 wf=$2 job=$3 idx=$4
  shift 4
  local base="$H/steps/$wf/$job/$idx" wd kv
  local lit=() added=()
  wd=${CASE_WD:-$d}
  if [ -s "$base.wd" ]; then
    wd="$wd/$(cat "$base.wd")"
  fi
  while IFS= read -r -d '' kv; do
    lit+=("$kv")
  done <"$base.env"
  while IFS= read -r kv; do
    case "$kv" in *=*) added+=("$kv") ;; esac
  done <"$d/env"
  (
    cd "$wd" && env -i HOME="$HOME" PATH="$H/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
      GITHUB_OUTPUT="$d/out" GITHUB_STEP_SUMMARY="$d/summary" GITHUB_ENV="$d/env" RUNNER_TEMP="$d/tmp" \
      STUB_LOG="$d/stub" STUB_DIR="$d" ${lit[@]+"${lit[@]}"} ${added[@]+"${added[@]}"} "$@" bash -e "$base.sh"
  ) </dev/null >>"$d/log" 2>&1
}

judge() {
  local name=$1 rc=$2 want=$3
  if [ "$rc" = "$want" ] || { [ "$want" = nz ] && [ "$rc" != 0 ]; }; then
    ok
    printf 'ok   %-34s rc=%s\n' "$name" "$rc"
  else
    bad "$(printf '%-34s rc=%s want=%s' "$name" "$rc" "$want")"
    sed 's/^/     | /' "$H/cases/$name/log" | tail -6
  fi
}

run_step() {
  local name=$1 wf=$2 job=$3 step=$4 want=$5
  shift 5
  local idx
  new_case "$name"
  idx=$(step_index "$wf" "$job" "$step")
  if [ -z "$idx" ]; then
    bad "$name: $wf job $job has no step named '$step'"
    return
  fi
  exec_step "$H/cases/$name" "$wf" "$job" "$idx" "$@"
  judge "$name" "$?" "$want"
}

run_job() {
  local name=$1 wf=$2 job=$3 want=$4
  shift 4
  local d="$H/cases/$name" rc=0 w j idx kind step found=0
  new_case "$name"
  while IFS=$'\t' read -r w j idx kind step; do
    [ "$w" = "$wf" ] && [ "$j" = "$job" ] || continue
    found=1
    if [ "$kind" != run ]; then
      printf 'skip %s\n' "$step" >>"$d/steps"
      continue
    fi
    printf 'run  %s\n' "$step" >>"$d/steps"
    exec_step "$d" "$wf" "$job" "$idx" "$@" || {
      rc=$?
      printf 'fail %s\n' "$step" >>"$d/steps"
      break
    }
  done <"$H/steps/index"
  if [ "$found" = 0 ]; then
    bad "$name: $wf has no job $job"
    return
  fi
  judge "$name" "$rc" "$want"
}

expect() {
  local name=$1 file=$2 pattern=$3
  if grep -Eq -- "$pattern" "$H/cases/$name/$file"; then
    ok
  else
    bad "$name: $file lacks /$pattern/"
    sed 's/^/     | /' "$H/cases/$name/$file" | head -8
  fi
}

reject() {
  local name=$1 file=$2 pattern=$3
  if grep -Eq -- "$pattern" "$H/cases/$name/$file"; then
    bad "$name: $file has /$pattern/"
    grep -E -- "$pattern" "$H/cases/$name/$file" | head -4 | sed 's/^/     | /'
  else
    ok
  fi
}

SHA=1111111111111111111111111111111111111111
OTHER=2222222222222222222222222222222222222222
TAGOBJ=3333333333333333333333333333333333333333
DIG=sha256:$(printf 'a%.0s' $(seq 64))
DIG2=sha256:$(printf 'c%.0s' $(seq 64))
IMG=ghcr.io/free-guy-ir/node
API=(GITHUB_API_URL=https://api.example GITHUB_REPOSITORY=Free-Guy-IR/node GH_TOKEN=t)
base=("${API[@]}" "GITHUB_SHA=$SHA")
F=$H/fixtures
: >"$F/map_none"
printf 'releases/tags/stable 200 {}\n' >"$F/map_release"
printf 'git/ref/tags/nightly 200 {}\n' >"$F/map_gittag"
printf 'releases/tags/nightly 502 {}\n' >"$F/map_err"
printf 'git/ref/heads/dev 200 {}\n' >"$F/map_branch_dev"
printf 'git/ref/heads/dev 500 {}\n' >"$F/map_branch_err"

WG_STEP="Run the real-kernel WireGuard integration tests"
wg_log() { printf '%b' "$1" >"$F/wg_$2.txt"; }
wg_log '=== RUN   TestIntegrationMTUWithoutKeyKeepsTheKernelDefaultAndAHostSetValue\n--- PASS: TestIntegrationMTUWithoutKeyKeepsTheKernelDefaultAndAHostSetValue (1.20s)\n--- PASS: TestIntegrationMTUFromConfigIsAppliedAndReapplied (0.90s)\n--- PASS: TestIntegrationE2ESyncUsers (12.00s)\n    --- PASS: TestIntegrationE2ESyncUsers/add (0.10s)\nPASS\nok  \tgithub.com/pasarguard/node/backend/wireguard\t30.1s\n' allpass
wg_log '--- SKIP: TestIntegrationMTUWithoutKeyKeepsTheKernelDefaultAndAHostSetValue (0.00s)\n    mtu_integration_test.go:10: no kernel\n--- PASS: TestIntegrationMTUFromConfigIsAppliedAndReapplied (0.90s)\nPASS\n' reqskip
wg_log '--- PASS: TestIntegrationE2ESyncUsers (12.00s)\nPASS\n' reqmissing
wg_log 'WireGuard E2E integration tests must run as root/sudo\nFAIL\tgithub.com/pasarguard/node/backend/wireguard\t0.01s\n' nomain
wg_log '--- PASS: TestIntegrationMTUWithoutKeyKeepsTheKernelDefaultAndAHostSetValue (1.20s)\n--- PASS: TestIntegrationMTUFromConfigIsAppliedAndReapplied (0.90s)\n--- FAIL: TestIntegrationE2ESyncUsers (12.00s)\nFAIL\n' e2efail
wg_log '--- PASS: TestIntegrationMTUWithoutKeyKeepsTheKernelDefaultAndAHostSetValue (1.20s)\n--- PASS: TestIntegrationMTUFromConfigIsAppliedAndReapplied (0.90s)\n--- SKIP: TestIntegrationE2EUpdateUsers (0.00s)\nPASS\n' otherskip
run_step wg-allpass "$WF_TESTS" wireguard-integration "$WG_STEP" 0 MAKE_OUT="$F/wg_allpass.txt" MAKE_RC=0
expect wg-allpass summary '\| TestIntegrationE2ESyncUsers/add \| PASS \|'
expect wg-allpass summary '^4 passed, 0 failed, 0 skipped'
expect wg-allpass stub 'make test-integration-wireguard'
run_step wg-reqskip "$WF_TESTS" wireguard-integration "$WG_STEP" 1 MAKE_OUT="$F/wg_reqskip.txt" MAKE_RC=0
expect wg-reqskip log 'Required WireGuard test did not pass::TestIntegrationMTUWithoutKeyKeepsTheKernelDefaultAndAHostSetValue: SKIP'
expect wg-reqskip log 'warning title=WireGuard integration test skipped'
run_step wg-reqmissing "$WF_TESTS" wireguard-integration "$WG_STEP" 1 MAKE_OUT="$F/wg_reqmissing.txt" MAKE_RC=0
expect wg-reqmissing summary 'TestIntegrationMTUFromConfigIsAppliedAndReapplied: did not run'
run_step wg-nomain "$WF_TESTS" wireguard-integration "$WG_STEP" 1 MAKE_OUT="$F/wg_nomain.txt" MAKE_RC=1
expect wg-nomain log 'No WireGuard integration test ran'
expect wg-nomain summary 'exited 1'
run_step wg-e2efail "$WF_TESTS" wireguard-integration "$WG_STEP" 1 MAKE_OUT="$F/wg_e2efail.txt" MAKE_RC=1
expect wg-e2efail summary '\| TestIntegrationE2ESyncUsers \| FAIL \|'
run_step wg-otherskip "$WF_TESTS" wireguard-integration "$WG_STEP" 0 MAKE_OUT="$F/wg_otherskip.txt" MAKE_RC=0
expect wg-otherskip log 'warning title=WireGuard integration test skipped::TestIntegrationE2EUpdateUsers'
expect wg-otherskip summary '2 passed, 0 failed, 1 skipped'
run_step wg-required-list "$WF_TESTS" wireguard-integration "$WG_STEP" 1 MAKE_OUT="$F/wg_reqmissing.txt" MAKE_RC=0
expect wg-required-list log 'TestIntegrationMTUWithoutKeyKeepsTheKernelDefaultAndAHostSetValue: did not run'

MT_STEP="Run the MTProto domain-fronting leg"
run_step mt-pass1 "$WF_TESTS" mtproto-fronting "$MT_STEP" 0 MTP_PLAN="pass"
expect mt-pass1 summary '^\| 1 \| pass \|'
expect mt-pass1 summary 'Passed on attempt 1 of 3'
reject mt-pass1 stub 'sleep'
run_step mt-ffp "$WF_TESTS" mtproto-fronting "$MT_STEP" 0 MTP_PLAN="fail fail pass"
expect mt-ffp summary '^\| 2 \| fail \(exit 1\) \|'
expect mt-ffp summary 'Passed on attempt 3 of 3'
expect mt-ffp stub '^sleep 15$'
expect mt-ffp stub '^sleep 45$'
run_step mt-fff "$WF_TESTS" mtproto-fronting "$MT_STEP" 1 MTP_PLAN="fail fail fail"
expect mt-fff summary 'Failed all 3 attempts'
expect mt-fff log 'MTProto fronting leg failed'
reject mt-fff stub 'sleep 135'
run_step mt-none "$WF_TESTS" mtproto-fronting "$MT_STEP" 1 MTP_PLAN="none"
expect mt-none log 'ran no test'
reject mt-none stub 'sleep'

DS="Compare with every upstream branch, tag, release and advisory"
for kind in ok unported crash die; do
  mkdir -p "$F/wd-$kind/scripts"
  case $kind in
    ok) printf 'echo "upstream-drift: 0 unported items across [node]"\nexit 0\n' ;;
    unported) printf 'echo "upstream-drift: 3 unported items across [node xray]"\nexit 1\n' ;;
    crash) printf 'echo partial\nfalse\n' ;;
    die) printf 'echo "upstream-drift: GitHub API request x returned HTTP 403" >&2\nexit 2\n' ;;
  esac >"$F/wd-$kind/scripts/upstream_drift.sh"
done
CASE_WD=$F/wd-ok run_step drift-ok "$WF_DRIFT" drift "$DS" 0
expect drift-ok out '^kind=clean$'
CASE_WD=$F/wd-unported run_step drift-unported "$WF_DRIFT" drift "$DS" 1
expect drift-unported out '^kind=unported$'
CASE_WD=$F/wd-crash run_step drift-crash "$WF_DRIFT" drift "$DS" 1
expect drift-crash out '^kind=error$'
CASE_WD=$F/wd-die run_step drift-die "$WF_DRIFT" drift "$DS" 2
expect drift-die out '^status=2$'
EX="Explain why this blocks image builds and releases"
run_step explain-unported "$WF_DRIFT" drift "$EX" 0 DRIFT_STATUS=1 DRIFT_KIND=unported CALLER="Publish Release"
expect explain-unported summary '^## Unported upstream items block this build'
run_step explain-error "$WF_DRIFT" drift "$EX" 0 DRIFT_STATUS=2 DRIFT_KIND=error CALLER="Publish Release"
expect explain-error summary '^## Upstream drift check could not complete'

CT="Check the requested tags"
IB=("${API[@]}" "CURL_MAP=$F/map_none")
export CASE_WD=$root
run_step it-main "$WF_IMAGE" build "$CT" 0 "${IB[@]}" TAGS="main" BRANCH="main"
run_step it-two "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS="v0.6.21 latest" BRANCH=""
run_step it-two-ok "$WF_IMAGE" build "$CT" 0 "${IB[@]}" TAGS="nightly edge" BRANCH="main"
run_step it-two-edge-nobranch "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS="nightly edge" BRANCH=""
run_step it-empty "$WF_IMAGE" build "$CT" 0 "${IB[@]}" TAGS="" BRANCH=""
reject it-empty stub curl
run_step it-bang "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS="bad!" BRANCH=""
run_step it-comma "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS="a,ghcr.io/x:latest" BRANCH=""
run_step it-newline "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS="$(printf 'a\nb')" BRANCH=""
run_step it-lead-dot "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS=".a" BRANCH=""
run_step it-long "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS="$(printf 'a%.0s' $(seq 129))" BRANCH=""
run_step it-br-slash "$WF_IMAGE" build "$CT" 0 "${IB[@]}" TAGS="x" BRANCH="feat/x"
run_step it-br-dash "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS="x" BRANCH="-x"
run_step it-br-space "$WF_IMAGE" build "$CT" 1 "${IB[@]}" TAGS="x" BRANCH="a b"
run_step i3-latest "$WF_IMAGE" build "$CT" 1 "${base[@]}" TAGS="v0.6.21 latest" BRANCH="" CURL_MAP="$F/map_none"
run_step i3-v "$WF_IMAGE" build "$CT" 1 "${base[@]}" TAGS="v2" BRANCH="v2" CURL_MAP="$F/map_none"
run_step i3-gittag "$WF_IMAGE" build "$CT" 1 "${base[@]}" TAGS="nightly" BRANCH="" CURL_MAP="$F/map_gittag"
SIG="sha256-$(printf 'b%.0s' $(seq 64)).sig"
run_step f6-edge-branch "$WF_IMAGE" build "$CT" 1 "${base[@]}" TAGS="edge" BRANCH="edge" CURL_MAP="$F/map_none"
expect f6-edge-branch log 'edge'
run_step f6-edge-main "$WF_IMAGE" build "$CT" 0 "${base[@]}" TAGS="nightly edge" BRANCH="main" CURL_MAP="$F/map_none"
run_step f6-sig-branch "$WF_IMAGE" build "$CT" 1 "${base[@]}" TAGS="$SIG" BRANCH="$SIG" CURL_MAP="$F/map_none"
run_step f6-main-elsewhere "$WF_IMAGE" build "$CT" 1 "${base[@]}" TAGS="main" BRANCH="" CURL_MAP="$F/map_none"
run_step f6-dev-branch "$WF_IMAGE" build "$CT" 0 "${base[@]}" TAGS="dev" BRANCH="dev" CURL_MAP="$F/map_branch_dev"
unset CASE_WD

PT="Point the tags at the image while the commit is still current"
run_step pt-none "$WF_IMAGE" build "$PT" 0 TAGS="" BRANCH="" DIGEST="$DIG" GITHUB_SHA=$SHA
reject pt-none stub 'docker'
expect pt-none summary 'pushed by digest only'
run_step pt-head "$WF_IMAGE" build "$PT" 0 TAGS="main" BRANCH="main" DIGEST="$DIG" GITHUB_SHA=$SHA LS_OUT="$SHA\trefs/heads/main\n"
expect pt-head stub "^git ls-remote origin refs/heads/main$"
expect pt-head stub "^docker buildx imagetools create --tag $IMG:main $IMG@$DIG$"
run_step pt-moved "$WF_IMAGE" build "$PT" 0 TAGS="main edge" BRANCH="main" DIGEST="$DIG" GITHUB_SHA=$SHA LS_OUT="$OTHER\trefs/heads/main\n"
reject pt-moved stub 'docker'
expect pt-moved log 'notice title=Image tags not moved::main is now at 2222'
run_step pt-gone "$WF_IMAGE" build "$PT" 0 TAGS="dev" BRANCH="dev" DIGEST="$DIG" GITHUB_SHA=$SHA LS_OUT=""
reject pt-gone stub 'docker'
expect pt-gone log 'nothing, the branch is gone'
run_step pt-tailmatch "$WF_IMAGE" build "$PT" 0 TAGS="main" BRANCH="main" DIGEST="$DIG" GITHUB_SHA=$SHA LS_OUT="$SHA\trefs/heads/x/refs/heads/main\n$OTHER\trefs/heads/main\n"
reject pt-tailmatch stub 'docker'
run_step pt-lsfail "$WF_IMAGE" build "$PT" nz TAGS="main" BRANCH="main" DIGEST="$DIG" GITHUB_SHA=$SHA LS_RC=128
reject pt-lsfail stub 'docker'
run_step pt-nobranch "$WF_IMAGE" build "$PT" 0 TAGS="nightly edge" BRANCH="" DIGEST="$DIG" GITHUB_SHA=$SHA
expect pt-nobranch stub "^docker buildx imagetools create --tag $IMG:nightly --tag $IMG:edge $IMG@$DIG$"
reject pt-nobranch stub 'ls-remote'

TS="Choose the image tags for this ref"
export CASE_WD=$root
run_step c3-main "$WF_MANUAL" tags "$TS" 0 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="" CURL_MAP="$F/map_none"
expect c3-main out '^tags=edge$'
expect c3-main out '^branch=main$'
reject c3-main out 'latest'
run_step c3-main-custom "$WF_MANUAL" tags "$TS" 0 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="nightly2" CURL_MAP="$F/map_none"
expect c3-main-custom out '^tags=nightly2 edge$'
reject c3-main-custom out 'latest'
run_step c3-main-latest "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="latest" CURL_MAP="$F/map_none"
run_step c3-main-v "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="v0.6.22" CURL_MAP="$F/map_none"
expect c3-main-v log 'release'
run_step c3-main-v9x "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="v9-test" CURL_MAP="$F/map_none"
run_step c3-main-vword "$WF_MANUAL" tags "$TS" 0 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="very-new" CURL_MAP="$F/map_none"
expect c3-main-vword out '^tags=very-new edge$'
run_step c3-main-release "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="stable" CURL_MAP="$F/map_release"
expect c3-main-release stub 'releases/tags/stable'
run_step c3-main-gittag "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="nightly" CURL_MAP="$F/map_gittag"
expect c3-main-gittag stub 'git/ref/tags/nightly'
run_step c3-main-apierr "$WF_MANUAL" tags "$TS" 2 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="nightly" CURL_MAP="$F/map_err"
run_step c3-main-custom-dev "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="dev" CURL_MAP="$F/map_branch_dev"
expect c3-main-custom-dev log 'branch'
reject c3-main-custom-dev out '^tags='
run_step c3-reltag-custom-dev "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_NAME=v0.6.21 INPUT_TAG="dev" CURL_MAP="$F/map_branch_dev"
reject c3-reltag-custom-dev out '^tags='
run_step c3-reltag-custom-lookup "$WF_MANUAL" tags "$TS" 2 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_NAME=v0.6.21 INPUT_TAG="dev" CURL_MAP="$F/map_branch_err"
run_step c3-dev-own "$WF_MANUAL" tags "$TS" 0 "${base[@]}" GITHUB_REF=refs/heads/dev GITHUB_REF_NAME=dev INPUT_TAG="" CURL_MAP="$F/map_branch_dev"
expect c3-dev-own out '^tags=dev$'
run_step c3-main-edge "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="edge" CURL_MAP="$F/map_none"
run_step c3-main-main "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_NAME=main INPUT_TAG="main" CURL_MAP="$F/map_none"
run_step c3-dev "$WF_MANUAL" tags "$TS" 0 "${base[@]}" GITHUB_REF=refs/heads/dev GITHUB_REF_NAME=dev INPUT_TAG="" CURL_MAP="$F/map_none"
expect c3-dev out '^tags=dev$'
expect c3-dev out '^branch=dev$'
run_step c3-dev-custom "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/dev GITHUB_REF_NAME=dev INPUT_TAG="other" CURL_MAP="$F/map_none"
run_step c3-vbranch "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/heads/v2 GITHUB_REF_NAME=v2 INPUT_TAG="" CURL_MAP="$F/map_none"
run_step c3-reltag "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_NAME=v0.6.21 INPUT_TAG="" CURL_MAP="$F/map_none"
expect c3-reltag log 'Publish Release'
expect c3-reltag log "gh workflow run $WF_RELEASE --ref v0.6.21"
reject c3-reltag out 'latest'
run_step c3-reltag-custom "$WF_MANUAL" tags "$TS" 0 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_NAME=v0.6.21 INPUT_TAG="rebuild-0.6.21" CURL_MAP="$F/map_none"
expect c3-reltag-custom out '^tags=rebuild-0.6.21$'
expect c3-reltag-custom out '^branch=$'
run_step c3-reltag-vcustom "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_NAME=v0.6.21 INPUT_TAG="v0.6.21-rebuild" CURL_MAP="$F/map_none"
run_step c3-reltag-edge "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_NAME=v0.6.21 INPUT_TAG="edge" CURL_MAP="$F/map_none"
run_step c3-pull "$WF_MANUAL" tags "$TS" 1 "${base[@]}" GITHUB_REF=refs/pull/3/merge GITHUB_REF_NAME=3/merge INPUT_TAG="" CURL_MAP="$F/map_none"
unset CASE_WD

RS="Require a release tag that is not published yet"
run_step rs-branch "$WF_RELEASE" resolve "$RS" 1 "${base[@]}" GITHUB_REF=refs/heads/main GITHUB_REF_TYPE=branch GITHUB_REF_NAME=main
expect rs-branch log 'Run this from the release tag'
reject rs-branch stub curl
run_step rs-ok "$WF_RELEASE" resolve "$RS" 0 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v0.6.21 CURL_STATUS=404
expect rs-ok summary "Releasing v0.6.21 at $SHA"
run_step rs-published "$WF_RELEASE" resolve "$RS" 1 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v0.6.21 CURL_STATUS=200
expect rs-published log 'Release already published'
run_step rs-apierr "$WF_RELEASE" resolve "$RS" 1 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v0.6.21 CURL_STATUS=500
run_step rs-badname "$WF_RELEASE" resolve "$RS" 1 "${base[@]}" GITHUB_REF=refs/tags/v1+x GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v1+x CURL_STATUS=404
reject rs-badname stub curl

RJ_REFS='refs/remotes/origin/main\nrefs/remotes/origin/release/v0.6.21\n'
RJ_ENV=("${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v0.6.21 CURL_STATUS=404 GIT_REFS="$RJ_REFS")
run_job rj-unmerged "$WF_RELEASE" resolve 1 "${RJ_ENV[@]}" GIT_CONTAINS=""
expect rj-unmerged log 'neither main nor a release'
run_job rj-main "$WF_RELEASE" resolve 0 "${RJ_ENV[@]}" GIT_CONTAINS="refs/remotes/origin/main"
expect rj-main summary 'is on main'
run_job rj-release-branch "$WF_RELEASE" resolve 0 "${RJ_ENV[@]}" GIT_CONTAINS="refs/remotes/origin/release/v0.6.21"
expect rj-release-branch summary 'is on release/v0.6.21'
run_job rj-no-branches "$WF_RELEASE" resolve 1 "${base[@]}" GITHUB_REF=refs/tags/v0.6.21 GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v0.6.21 CURL_STATUS=404 GIT_REFS="" GIT_CONTAINS=""
run_job rj-refs-fail "$WF_RELEASE" resolve nz "${RJ_ENV[@]}" GIT_REFS_RC=1 GIT_CONTAINS="refs/remotes/origin/main"
reject rj-refs-fail stub 'merge-base'
if awk -F'\t' -v wf="$WF_RELEASE" '$1 == wf && $2 == "publish" && $5 == "release" { found = 1 } END { exit !found }' "$H/steps/jobs"; then
  ok
else
  bad "$WF_RELEASE job publish does not run in the environment release"
fi

FD="Find the draft release for this tag"
printf '[{"id":120,"tag_name":"v0.6.20","draft":false,"prerelease":false}]\n[{"id":123,"tag_name":"v0.6.21","draft":true,"prerelease":false}]\n' >"$F/rel_pages.json"
printf '[{"id":123,"tag_name":"v0.6.21","draft":true,"prerelease":true}]' >"$F/rel_pre.json"
printf '[{"tag_name":"v0.6.20","draft":false,"prerelease":false}]' >"$F/rel_none.json"
printf '[{"tag_name":"v0.6.21","draft":false,"prerelease":false}]' >"$F/rel_pub.json"
printf '[{"tag_name":"v0.6.21","draft":true,"prerelease":false},{"tag_name":"v0.6.21","draft":true,"prerelease":false}]' >"$F/rel_two.json"
run_step fd-pages "$WF_RELEASE" publish "$FD" 0 "${base[@]}" TAG=v0.6.21 GH_RELEASES="$F/rel_pages.json"
expect fd-pages out '^prerelease=false$'
expect fd-pages out '^id=123$'
expect fd-pages stub 'gh api --paginate repos/Free-Guy-IR/node/releases\?per_page=100'
run_step fd-pre "$WF_RELEASE" publish "$FD" 0 "${base[@]}" TAG=v0.6.21 GH_RELEASES="$F/rel_pre.json"
expect fd-pre out '^prerelease=true$'
run_step fd-none "$WF_RELEASE" publish "$FD" 1 "${base[@]}" TAG=v0.6.21 GH_RELEASES="$F/rel_none.json"
expect fd-none log 'found 0 releases for v0.6.21'
run_step fd-pub "$WF_RELEASE" publish "$FD" 1 "${base[@]}" TAG=v0.6.21 GH_RELEASES="$F/rel_pub.json"
expect fd-pub log 'not a draft any more'
run_step fd-two "$WF_RELEASE" publish "$FD" 1 "${base[@]}" TAG=v0.6.21 GH_RELEASES="$F/rel_two.json"

TC="Check that the tag still points at the tested commit"
export CASE_WD=$root
run_step tc-light "$WF_RELEASE" publish "$TC" 0 "${base[@]}" TAG=v0.6.21 DIGEST="$DIG" GH_REF="{\"object\":{\"type\":\"commit\",\"sha\":\"$SHA\"}}"
run_step tc-annot "$WF_RELEASE" publish "$TC" 0 "${base[@]}" TAG=v0.6.21 DIGEST="$DIG" GH_REF="{\"object\":{\"type\":\"tag\",\"sha\":\"$TAGOBJ\"}}" GH_TAGOBJ="{\"object\":{\"type\":\"commit\",\"sha\":\"$SHA\"}}"
expect tc-annot stub "git/tags/$TAGOBJ"
run_step tc-moved "$WF_RELEASE" publish "$TC" 1 "${base[@]}" TAG=v0.6.21 DIGEST="$DIG" GH_REF="{\"object\":{\"type\":\"commit\",\"sha\":\"$OTHER\"}}"
expect tc-moved log 'Tag moved::v0.6.21 now points at commit 2222'
run_step tc-nodigest "$WF_RELEASE" publish "$TC" 1 "${base[@]}" TAG=v0.6.21 DIGEST="" GH_REF="{\"object\":{\"type\":\"commit\",\"sha\":\"$SHA\"}}"
expect tc-nodigest log 'No image digest'
unset CASE_WD

VC="Verify the asset checksums"
mkdir -p "$F/vc-ok/dist" "$F/vc-bad/dist" "$F/vc-empty/dist"
(
  cd "$F/vc-ok/dist" &&
    printf a >pasarguard-node-linux-amd64.zip &&
    printf b >pasarguard-node-linux-arm64.zip &&
    sha256sum pasarguard-node-linux-amd64.zip >pasarguard-node-linux-amd64.zip.sha256 &&
    sha256sum pasarguard-node-linux-arm64.zip >pasarguard-node-linux-arm64.zip.sha256
)
cp "$F/vc-ok/dist/"* "$F/vc-bad/dist/"
printf tampered >"$F/vc-bad/dist/pasarguard-node-linux-arm64.zip"
CASE_WD=$F/vc-ok run_step vc-ok "$WF_RELEASE" publish "$VC" 0
CASE_WD=$F/vc-bad run_step vc-bad "$WF_RELEASE" publish "$VC" nz
CASE_WD=$F/vc-empty run_step vc-empty "$WF_RELEASE" publish "$VC" 1
expect vc-empty log 'the build jobs produced no zip'

run_step up "$WF_RELEASE" publish "Upload the assets to the draft release" 0 "${base[@]}" TAG=v0.6.21
expect up stub '^gh release upload v0.6.21 dist/\* --clobber$'

TI="Tag the tested image"
printf '%s %s\n' "$IMG:v0.6.21" "$DIG" >"$F/tags_same"
printf '%s %s\n' "$IMG:v0.6.21" "$DIG2" >"$F/tags_taken"
: >"$F/tags_none"
run_step ti-final "$WF_RELEASE" publish "$TI" 0 "${base[@]}" TAG=v0.6.21 DIGEST="$DIG" PRERELEASE=false DOCKER_TAGS="$F/tags_none"
expect ti-final stub "^docker buildx imagetools create --tag $IMG:v0.6.21 $IMG@$DIG$"
reject ti-final stub ":latest"
run_step ti-pre "$WF_RELEASE" publish "$TI" 0 "${base[@]}" TAG=v0.6.22-rc1 DIGEST="$DIG" PRERELEASE=true DOCKER_TAGS="$F/tags_none"
expect ti-pre stub "^docker buildx imagetools create --tag $IMG:v0.6.22-rc1 $IMG@$DIG$"
run_step ti-same "$WF_RELEASE" publish "$TI" 0 "${base[@]}" TAG=v0.6.21 DIGEST="$DIG" PRERELEASE=false DOCKER_TAGS="$F/tags_same"
reject ti-same stub 'imagetools create'
expect ti-same stub "imagetools inspect $IMG:v0.6.21"
run_step ti-taken "$WF_RELEASE" publish "$TI" 1 "${base[@]}" TAG=v0.6.21 DIGEST="$DIG" PRERELEASE=false DOCKER_TAGS="$F/tags_taken"
reject ti-taken stub 'imagetools create'
expect ti-taken log 'already points at'
expect ti-taken log 're-run'
run_step ti-registry "$WF_RELEASE" publish "$TI" 1 "${base[@]}" TAG=v0.6.21 DIGEST="$DIG" PRERELEASE=false DOCKER_INSPECT_ERROR="unauthorized: authentication required"
reject ti-registry stub 'imagetools create'

PB="Publish the release"
printf '{"object":{"type":"commit","sha":"%s"}}' "$SHA" >"$F/tag_sha.json"
printf '{"object":{"type":"commit","sha":"%s"}}' "$OTHER" >"$F/tag_other.json"
printf '{"id":123,"draft":true}' >"$F/rel_draft.json"
printf '{"id":123,"draft":false}' >"$F/rel_published.json"
printf 'git/ref/tags/v0.6.21 0 %s\nreleases/123 0 %s\n' "$F/tag_sha.json" "$F/rel_draft.json" >"$F/map_pb_ok"
printf 'git/ref/tags/v0.6.21 0 %s\nreleases/123 0 %s\n' "$F/tag_other.json" "$F/rel_draft.json" >"$F/map_pb_before"
printf 'git/ref/tags/v0.6.21 0 %s\ngit/ref/tags/v0.6.21 0 %s\nreleases/123 0 %s\n' "$F/tag_sha.json" "$F/tag_other.json" "$F/rel_draft.json" >"$F/map_pb_after"
printf 'git/ref/tags/v0.6.21 0 %s\nreleases/123 0 %s\n' "$F/tag_sha.json" "$F/rel_published.json" >"$F/map_pb_published"
printf 'git/ref/tags/v0.6.21 1 %s\nreleases/123 0 %s\n' "$F/missing.json" "$F/rel_draft.json" >"$F/map_pb_unreadable"
export CASE_WD=$root
run_step pb-ok "$WF_RELEASE" publish "$PB" 0 "${base[@]}" TAG=v0.6.21 RELEASE_ID=123 GH_MAP="$F/map_pb_ok"
expect pb-ok stub '^gh release edit v0.6.21 --draft=false$'
expect pb-ok stub 'git/ref/tags/v0.6.21'
reject pb-ok stub 'draft=true'
run_step pb-moved-before "$WF_RELEASE" publish "$PB" 1 "${base[@]}" TAG=v0.6.21 RELEASE_ID=123 GH_MAP="$F/map_pb_before"
reject pb-moved-before stub 'draft=false'
expect pb-moved-before log 'Tag moved'
run_step pb-moved-after "$WF_RELEASE" publish "$PB" 1 "${base[@]}" TAG=v0.6.21 RELEASE_ID=123 GH_MAP="$F/map_pb_after"
expect pb-moved-after stub '^gh release edit v0.6.21 --draft=false$'
expect pb-moved-after stub '^gh release edit v0.6.21 --draft=true$'
expect pb-moved-after log 'draft again'
run_step pb-published "$WF_RELEASE" publish "$PB" 1 "${base[@]}" TAG=v0.6.21 RELEASE_ID=123 GH_MAP="$F/map_pb_published"
reject pb-published stub 'draft=false'
expect pb-published log 'published while this run'
run_step pb-unreadable "$WF_RELEASE" publish "$PB" nz "${base[@]}" TAG=v0.6.21 RELEASE_ID=123 GH_MAP="$F/map_pb_unreadable"
reject pb-unreadable stub 'release edit'
unset CASE_WD

for needed in "$WF_BRANCH image tests,drift" "$WF_MANUAL image tags,tests,drift" "$WF_RELEASE image tests,drift" "$WF_RELEASE publish assets,image"; do
  read -r wf job deps <<<"$needed"
  if awk -F'\t' -v wf="$wf" -v job="$job" -v deps="$deps" '$1 == wf && $2 == job && $3 == deps { found = 1 } END { exit !found }' "$H/steps/jobs"; then
    ok
  else
    bad "$wf job $job does not need exactly $deps"
  fi
done
if grep -qxF "$(printf '%s\t%s\t%s\t%s\t%s' "$WF_RELEASE" latest publish publish-release-latest -)" "$H/steps/jobs"; then
  ok
else
  bad "$WF_RELEASE has no job latest that needs publish and runs in the concurrency group publish-release-latest"
  grep -F "$WF_RELEASE" "$H/steps/jobs" | sed 's/^/     | /'
fi
LT="Point :latest at the image of the latest release"
printf 'releases/latest 200 {"tag_name":"v0.6.22"}\n' >"$F/map_latest_b"
printf 'releases/latest 404 {}\n' >"$F/map_latest_none"
printf 'releases/latest 500 {}\n' >"$F/map_latest_err"
printf 'releases/latest 200 {"tag_name":"v0.6.23"}\n' >"$F/map_latest_noimage"
printf 'releases/latest 200 {"tag_name":"a;b"}\n' >"$F/map_latest_bad"
printf '%s %s\n%s %s\n%s %s\n' "$IMG:v0.6.21" "$DIG" "$IMG:v0.6.22" "$DIG2" "$IMG:latest" "$DIG" >"$F/tags_ab"
printf '%s %s\n%s %s\n%s %s\n' "$IMG:v0.6.21" "$DIG" "$IMG:v0.6.22" "$DIG2" "$IMG:latest" "$DIG2" >"$F/tags_ab_done"
run_step lt-older-run "$WF_RELEASE" latest "$LT" 0 "${base[@]}" TAG=v0.6.21 DIGEST="$DIG" CURL_MAP="$F/map_latest_b" DOCKER_TAGS="$F/tags_ab"
expect lt-older-run stub "^docker buildx imagetools create --tag $IMG:latest $IMG@$DIG2$"
reject lt-older-run stub "create --tag $IMG:latest $IMG@$DIG$"
run_step lt-done "$WF_RELEASE" latest "$LT" 0 "${base[@]}" TAG=v0.6.22 DIGEST="$DIG2" CURL_MAP="$F/map_latest_b" DOCKER_TAGS="$F/tags_ab_done"
reject lt-done stub 'imagetools create'
run_step lt-none "$WF_RELEASE" latest "$LT" 0 "${base[@]}" TAG=v0.6.22-rc1 DIGEST="$DIG2" CURL_MAP="$F/map_latest_none" DOCKER_TAGS="$F/tags_ab"
reject lt-none stub 'imagetools create'
run_step lt-apierr "$WF_RELEASE" latest "$LT" 1 "${base[@]}" TAG=v0.6.22 DIGEST="$DIG2" CURL_MAP="$F/map_latest_err" DOCKER_TAGS="$F/tags_ab"
reject lt-apierr stub 'imagetools create'
run_step lt-noimage "$WF_RELEASE" latest "$LT" 1 "${base[@]}" TAG=v0.6.23 DIGEST="$DIG2" CURL_MAP="$F/map_latest_noimage" DOCKER_TAGS="$F/tags_ab"
reject lt-noimage stub 'imagetools create'
run_step lt-badtag "$WF_RELEASE" latest "$LT" 1 "${base[@]}" TAG=v0.6.22 DIGEST="$DIG2" CURL_MAP="$F/map_latest_bad" DOCKER_TAGS="$F/tags_ab"
reject lt-badtag stub 'imagetools'
run_step lt-registry "$WF_RELEASE" latest "$LT" 1 "${base[@]}" TAG=v0.6.22 DIGEST="$DIG2" CURL_MAP="$F/map_latest_b" DOCKER_INSPECT_ERROR="unauthorized: authentication required"
reject lt-registry stub 'imagetools create'

GD="Turn a release published outside Publish Release back into a draft"
run_step guard "$WF_GUARD" refuse-hand-published "$GD" 1 GH_TOKEN=t GH_REPO=Free-Guy-IR/node TAG=v0.6.21 ACTOR=someone
expect guard stub '^gh release edit --draft=true -- v0.6.21$'
expect guard log 'Release turned back into a draft::someone published v0.6.21 outside'
expect guard log 'Publish Release'
expect guard log 'workflow_dispatch from the tag'
expect guard log "gh workflow run $WF_RELEASE --ref v0.6.21"
expect guard summary "gh workflow run $WF_RELEASE --ref v0.6.21"
reject guard log 'had no tested assets'
RUNS='[{"databaseId":77,"status":"in_progress","conclusion":"","url":"https://github.com/Free-Guy-IR/node/actions/runs/77"}]'
run_step guard-run "$WF_GUARD" refuse-hand-published "$GD" 1 GH_TOKEN=t GH_REPO=Free-Guy-IR/node TAG=v0.6.21 ACTOR=someone GH_RUNS="$RUNS"
expect guard-run stub '^gh release edit --draft=true -- v0.6.21$'
expect guard-run log 'actions/runs/77'
expect guard-run log 're-run'
run_step guard-runs-fail "$WF_GUARD" refuse-hand-published "$GD" 1 GH_TOKEN=t GH_REPO=Free-Guy-IR/node TAG=v0.6.21 ACTOR=someone GH_RUNS_RC=1
expect guard-runs-fail stub '^gh release edit --draft=true -- v0.6.21$'
expect guard-runs-fail log "gh workflow run $WF_RELEASE --ref v0.6.21"
run_step guard-dash "$WF_GUARD" refuse-hand-published "$GD" 1 GH_TOKEN=t GH_REPO=Free-Guy-IR/node TAG=-x ACTOR=someone
expect guard-dash stub '^gh release edit --draft=true -- -x$'
run_step guard-ghfail "$WF_GUARD" refuse-hand-published "$GD" nz GH_TOKEN=t GH_REPO=Free-Guy-IR/node TAG=v1 ACTOR=someone GH_RC=1
reject guard-ghfail log 'Release turned back into a draft'

if [ "$failn" -gt 0 ]; then
  printf 'workflow step test: %d checks passed, %d failed\n' "$pass" "$failn"
  exit 1
fi
printf 'workflow step test: %d checks passed, 0 failed\n' "$pass"
