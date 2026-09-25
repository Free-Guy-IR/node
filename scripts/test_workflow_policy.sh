#!/usr/bin/env bash
set -u

root=$(git rev-parse --show-toplevel) || exit 2
cd "$root" || exit 2

work=$(mktemp -d "${TMPDIR:-/tmp}/workflow-policy-test.XXXXXX")
trap 'rm -rf "$work"' EXIT

failures=0

copy_workflows() {
  mkdir -p "$1"
  cp .github/workflows/*.yml "$1/"
}

replace_once() {
  python3 - "$1" "$2" "$3" <<'PY'
import sys
path, old, new = sys.argv[1:4]
text = open(path).read()
if text.count(old) != 1:
    sys.exit(f"{path}: expected one match for {old!r}, found {text.count(old)}")
open(path, "w").write(text.replace(old, new))
PY
}

run_policy() {
  WORKFLOW_DIR="$1" WORKFLOW_POLICY_REMOTE=0 bash scripts/workflow_policy.sh >"$1.log" 2>&1
}

must_refuse() {
  local name=$1 file=$2 old=$3 new=$4 dir="$work/$1"
  copy_workflows "$dir"
  if ! replace_once "$dir/$file" "$old" "$new"; then
    printf 'FAIL %s: the mutation no longer applies\n' "$name"
    failures=$((failures + 1))
    return
  fi
  if run_policy "$dir"; then
    printf 'FAIL %s: workflow_policy.sh accepts it\n' "$name"
    failures=$((failures + 1))
  else
    printf 'ok   %s is refused\n' "$name"
  fi
}

copy_workflows "$work/baseline"
if run_policy "$work/baseline"; then
  printf 'ok   the unchanged workflows pass\n'
else
  printf 'FAIL the unchanged workflows do not pass; see %s\n' "$work/baseline.log"
  failures=$((failures + 1))
fi

must_refuse "branch image built without tests and drift" docker-build-dev.yml \
  $'  image:\n    needs: [tests, drift]\n' $'  image:\n'
must_refuse "release published after a failed gate" release.yml \
  $'  publish:\n    needs: [assets, image]\n' $'  publish:\n    needs: [assets, image]\n    if: always()\n'
must_refuse "release image built without tests" release.yml \
  $'  image:\n    needs: [tests, drift]\n' $'  image:\n    needs: [resolve]\n'
must_refuse "manual image built without tests" docker-build.yml \
  $'    needs: [tags, tests, drift]\n' $'    needs: [tags]\n'
must_refuse "MTProto fronting leg allowed to fail" run-tests.yml \
  $'  mtproto-fronting:\n    runs-on: ubuntu-latest\n' $'  mtproto-fronting:\n    runs-on: ubuntu-latest\n    continue-on-error: true\n'
must_refuse "drift check allowed to fail" upstream-drift.yml \
  $'        id: drift\n' $'        id: drift\n        continue-on-error: true\n'
must_refuse "workflow policy job switched off by a condition" run-tests.yml \
  $'  workflow-policy:\n    runs-on: ubuntu-latest\n' $'  workflow-policy:\n    if: github.run_attempt == 0\n    runs-on: ubuntu-latest\n'
must_refuse "test suite step switched off by a condition" run-tests.yml \
  $'      - name: Run regular test suite\n' $'      - name: Run regular test suite\n        if: github.run_attempt == 0\n'
must_refuse "write-all at workflow level" docker-build-dev.yml \
  $'permissions:\n  contents: read\n' $'permissions: write-all\n'
must_refuse "write-all on a gate job" run-tests.yml \
  $'  govulncheck:\n    runs-on: ubuntu-latest\n' $'  govulncheck:\n    permissions: write-all\n    runs-on: ubuntu-latest\n'
must_refuse "flow-style step on a branch-floating action" upstream-drift.yml \
  $'      - name: Checkout codebase\n        uses: actions/checkout@v7\n        with:\n          fetch-depth: 0\n          persist-credentials: false\n' \
  $'      - {name: Checkout codebase, uses: actions/checkout@main, with: {fetch-depth: 0, persist-credentials: false}}\n'
must_refuse "quoted uses key on a branch-floating action" upstream-drift.yml \
  $'        uses: actions/checkout@v7\n' $'        "uses": actions/checkout@main\n'

if [ "$failures" -gt 0 ]; then
  printf 'workflow policy test: %d failures\n' "$failures"
  exit 1
fi
printf 'workflow policy test: every gate-disabling edit is refused\n'
