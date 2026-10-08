#!/usr/bin/env bash
set -u

root=$(git rev-parse --show-toplevel) || exit 2
cd "$root" || exit 2

work=$(mktemp -d "${TMPDIR:-/tmp}/workflow-policy-test.XXXXXX")
trap 'rm -rf "$work"' EXIT

failures=0

WF_BRANCH=image-branch.yml
WF_MANUAL=image-manual.yml
WF_RELEASE=publish-release.yml
RETIRED="docker-build.yml docker-build-dev.yml release.yml"

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

gate="$work/workflowgate"
(cd scripts/workflowgate && go build -o "$gate" .) || exit 2

run_policy() {
  WORKFLOWGATE="$gate" WORKFLOW_DIR="$1" WORKFLOW_POLICY_REMOTE=0 bash scripts/workflow_policy.sh >"$1.log" 2>&1
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

must_refuse_path() {
  local name=$1 path=$2 dir="$work/$1"
  copy_workflows "$dir"
  cat >"$dir/$path" <<'YML'
name: Retired path
on:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  noop:
    runs-on: ubuntu-latest
    steps:
      - name: Do nothing
        run: echo noop
YML
  if run_policy "$dir"; then
    printf 'FAIL %s: workflow_policy.sh accepts it\n' "$name"
    failures=$((failures + 1))
  else
    printf 'ok   %s is refused\n' "$name"
  fi
}

must_refuse_cut() {
  local name=$1 file=$2 marker=$3 dir="$work/$1"
  copy_workflows "$dir"
  if ! python3 - "$dir/$file" "$marker" <<'PY2'
import sys
path, marker = sys.argv[1:3]
text = open(path).read()
if text.count(marker) != 1:
    sys.exit(f"{path}: expected one match for {marker!r}, found {text.count(marker)}")
open(path, "w").write(text[: text.index(marker)])
PY2
  then
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

for retired in $RETIRED; do
  must_refuse_path "workflow at the retired path $retired" "$retired"
done

for doc in README.md docs/upstream-parity.md .github/workflows/*.yml; do
  while IFS= read -r named; do
    case " $RETIRED " in
      *" $named "*)
        printf 'FAIL %s tells people to run %s, a retired workflow path\n' "$doc" "$named"
        failures=$((failures + 1))
        ;;
      *)
        if [ -f ".github/workflows/$named" ]; then
          printf 'ok   %s names the existing workflow %s\n' "$doc" "$named"
        else
          printf 'FAIL %s tells people to run %s, which is not a workflow here\n' "$doc" "$named"
          failures=$((failures + 1))
        fi
        ;;
    esac
  done < <(grep -oE 'gh workflow run [A-Za-z0-9_.-]+' "$doc" | awk '{ print $4 }' | sort -u)
done

must_refuse "branch image built without tests and drift" "$WF_BRANCH" \
  $'  image:\n    needs: [tests, drift]\n' $'  image:\n'
must_refuse "release published after a failed gate" "$WF_RELEASE" \
  $'  publish:\n    needs: [assets, image]\n' $'  publish:\n    needs: [assets, image]\n    if: always()\n'
must_refuse "release image built without tests" "$WF_RELEASE" \
  $'  image:\n    needs: [tests, drift]\n' $'  image:\n    needs: [resolve]\n'
must_refuse "manual image built without tests" "$WF_MANUAL" \
  $'    needs: [tags, tests, drift]\n' $'    needs: [tags]\n'
must_refuse "MTProto fronting leg allowed to fail" run-tests.yml \
  $'  mtproto-fronting:\n    runs-on: ubuntu-latest\n' $'  mtproto-fronting:\n    runs-on: ubuntu-latest\n    continue-on-error: true\n'
must_refuse "drift check allowed to fail" upstream-drift.yml \
  $'        id: drift\n' $'        id: drift\n        continue-on-error: true\n'
must_refuse "workflow policy job switched off by a condition" run-tests.yml \
  $'  workflow-policy:\n    runs-on: ubuntu-latest\n' $'  workflow-policy:\n    if: github.run_attempt == 0\n    runs-on: ubuntu-latest\n'
must_refuse "test suite step switched off by a condition" run-tests.yml \
  $'      - name: Run regular test suite\n' $'      - name: Run regular test suite\n        if: github.run_attempt == 0\n'
must_refuse "write-all at workflow level" "$WF_BRANCH" \
  $'permissions:\n  contents: read\n' $'permissions: write-all\n'
must_refuse "write-all on a gate job" run-tests.yml \
  $'  govulncheck:\n    runs-on: ubuntu-latest\n' $'  govulncheck:\n    permissions: write-all\n    runs-on: ubuntu-latest\n'
must_refuse "flow-style step on a branch-floating action" upstream-drift.yml \
  $'      - name: Checkout codebase\n        uses: actions/checkout@v7\n        with:\n          fetch-depth: 0\n          persist-credentials: false\n' \
  $'      - {name: Checkout codebase, uses: actions/checkout@main, with: {fetch-depth: 0, persist-credentials: false}}\n'
must_refuse "quoted uses key on a branch-floating action" upstream-drift.yml \
  $'        uses: actions/checkout@v7\n' $'        "uses": actions/checkout@main\n'

must_refuse "gate command allowed to fail with || true" run-tests.yml \
  $'        run: bash scripts/workflow_policy.sh\n' $'        run: bash scripts/workflow_policy.sh || true\n'
must_refuse "drift step running under set +e" upstream-drift.yml \
  $'          set -o pipefail\n          log="$RUNNER_TEMP/upstream-drift.log"\n' $'          set +e\n          set -o pipefail\n          log="$RUNNER_TEMP/upstream-drift.log"\n'
must_refuse "test suite step allowed to fail" run-tests.yml \
  $'      - name: Run regular test suite\n' $'      - name: Run regular test suite\n        continue-on-error: true\n'
must_refuse "test suite narrowed to one package tree" run-tests.yml \
  $'          TEST_INTEGRATION=true go test -v -p 1 "${packages[@]}"\n' $'          TEST_INTEGRATION=true go test -v -p 1 ./pkg/...\n'
must_refuse "regular test suite replaced by a no-op" run-tests.yml \
  $'          TEST_INTEGRATION=true go test -v -p 1 "${packages[@]}"\n' $'          echo skipped\n'
must_refuse "race detector step removed" run-tests.yml \
  $'      - name: Run the controller and backend tests under the race detector\n        run: TEST_INTEGRATION=true CGO_ENABLED=1 go test -race -timeout 20m -v -p 1 ./controller/... ./backend/...\n' ''
must_refuse "race detector dropped from the controller and backend run" run-tests.yml \
  $'go test -race -timeout 20m -v -p 1 ./controller/... ./backend/...\n' $'go test -timeout 20m -v -p 1 ./controller/... ./backend/...\n'
must_refuse "backend dropped from the race run" run-tests.yml \
  $' ./controller/... ./backend/...\n' $' ./controller/...\n'
must_refuse "Xray patch tests run without the race detector" run-tests.yml \
  $'          CGO_ENABLED=1 GOFLAGS=-race make test_xray_patches\n' $'          make test_xray_patches\n'
must_refuse "Xray patch guard replay removed" run-tests.yml \
  $'      - name: Replay the image build\'s Xray patch guards\n        run: make test_xray_guards XRAY_GUARD_SRC="$RUNNER_TEMP/xray-guard-src"\n' ''
must_refuse_cut "workflow policy job removed" run-tests.yml $'  workflow-policy:\n'
must_refuse "branch image job forced to run by an expression" "$WF_BRANCH" \
  $'  image:\n    needs: [tests, drift]\n' $'  image:\n    needs: [tests, drift]\n    if: ${{ always() }}\n'
must_refuse "tests job that does not call the test workflow" "$WF_BRANCH" \
  $'  tests:\n    uses: ./.github/workflows/run-tests.yml\n' $'  tests:\n    runs-on: ubuntu-latest\n    steps:\n      - name: Pretend\n        run: echo ok\n'
must_refuse "test workflow called from a floating remote ref" "$WF_BRANCH" \
  $'  tests:\n    uses: ./.github/workflows/run-tests.yml\n' $'  tests:\n    uses: Free-Guy-IR/node/.github/workflows/run-tests.yml@main\n'
must_refuse "write scope outside the allowlist" "$WF_BRANCH" \
  $'      contents: read\n      packages: write\n' $'      contents: write\n      packages: write\n'
must_refuse "write permission at workflow level" run-tests.yml \
  $'permissions:\n  contents: read\n' $'permissions:\n  contents: write\n'
must_refuse "new publishing job without gates" "$WF_MANUAL" \
  $'      branch: ${{ needs.tags.outputs.branch }}\n' $'      branch: ${{ needs.tags.outputs.branch }}\n\n  sneak:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      packages: write\n    steps:\n      - name: Push\n        run: echo push\n'
must_refuse "stored secret instead of the job token" image.yml \
  $'          password: ${{ secrets.GITHUB_TOKEN }}\n' $'          password: ${{ secrets.GHCR_PAT }}\n'
must_refuse "every secret handed to the image workflow" "$WF_BRANCH" \
  $'    uses: ./.github/workflows/image.yml\n' $'    uses: ./.github/workflows/image.yml\n    secrets: inherit\n'
must_refuse "branch-floating action in a folded scalar" upstream-drift.yml \
  $'        uses: actions/checkout@v7\n' $'        uses: >-\n          actions/checkout@main\n'
must_refuse "local composite action" upstream-drift.yml \
  $'        uses: actions/checkout@v7\n' $'        uses: ./.github/actions/checkout\n'

for owned in /.github/workflows/ /.github/actions/ /.github/CODEOWNERS /.github/upstream-waivers.txt \
  /scripts/workflow_policy.sh /scripts/workflowgate/ /scripts/workflow_gates.lock /scripts/image_tag_policy.sh \
  /scripts/upstream_drift.sh /scripts/test_workflow_policy.sh /scripts/test_image_tag_policy.sh \
  /scripts/test_workflow_steps.sh /scripts/release_tag_commit.sh; do
  if awk -v path="$owned" '$1 == path && NF > 1 { found = 1 } END { exit !found }' .github/CODEOWNERS; then
    printf 'ok   CODEOWNERS covers %s\n' "$owned"
  else
    printf 'FAIL CODEOWNERS has no owner for %s\n' "$owned"
    failures=$((failures + 1))
  fi
done

if [ "$failures" -gt 0 ]; then
  printf 'workflow policy test: %d failures\n' "$failures"
  exit 1
fi
printf 'workflow policy test: every gate-disabling edit is refused\n'
