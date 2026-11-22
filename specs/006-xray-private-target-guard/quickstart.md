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

## CI

Push the branch and read every run for the exact commit with `python3 -I S/pgupd/ci_status.py Free-Guy-IR/node@<sha>`.
