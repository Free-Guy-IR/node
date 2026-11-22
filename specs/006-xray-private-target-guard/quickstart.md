# Quickstart: validating the private-target guard

## Build the patched Xray and run the patch tests

Go 1.27.4 is on `PATH`.

```sh
export GOTOOLCHAIN=local
make build_xray XRAY_SRC="$PWD/.xray-src" XRAY_OUTPUT="$PWD/.xray-src/xray"
CGO_ENABLED=1 GOFLAGS=-race make test_xray_patches XRAY_SRC="$PWD/.xray-src"
```

Expected: every `TestAUserInbound...Private...` test passes; on the tree without the new patch they fail with a
private target reached.

## Replay the Dockerfile guards

```sh
make test_xray_guards XRAY_GUARD_SRC="$PWD/.xray-guard-src"
```

Expected: every non-test file of every patch, dropped one at a time, is caught.

## Node tests against the built binary

```sh
XRAY_STOCK_EXECUTABLE_PATH="$PWD/.xray-src/xray" TEST_INTEGRATION=true CGO_ENABLED=1 \
  go test -race -count=1 -run 'PrivateTarget|AUserInboundReachesAPrivateTarget|CoreVersionDecides' ./backend/xray/
```

Expected locally: the live cases that need the CI test addresses skip with a reason; the rest pass. In CI the
`PG_NODE_PRIVATE_GUARD_REQUIRED=1` step turns those skips into failures, and the CGNAT, public and AsIs-by-name cases
run.

## The harness (CI only)

`scripts/private_target_harness.py` needs root on a throwaway Linux runner: it adds lo aliases and a dummy tunnel,
binds port 53 and points `/etc/resolv.conf` at its scripted DNS server until it exits, so it refuses to run outside
GitHub Actions. The `private-target-harness` job builds the patched core and the same core without this patch and
runs it:

```sh
sudo -n env GITHUB_ACTIONS="$GITHUB_ACTIONS" GITHUB_STEP_SUMMARY="$GITHUB_STEP_SUMMARY" \
  timeout --kill-after=30 1200 python3 -u -I scripts/private_target_harness.py \
  --guarded "$RUNNER_TEMP/xray-guarded" --base "$RUNNER_TEMP/xray-base" --out "$RUNNER_TEMP/private-target-harness" </dev/null
```

Expected: every security check refused on the guarded core and exposed on the base, the CGNAT allow and the public
name served on both, and the latency rows within the bounds of SC-006; the tables land in the job summary.

## CI

Push the branch and read every run for the exact commit with `python3 -I S/pgupd/ci_status.py Free-Guy-IR/node@<sha>`.
