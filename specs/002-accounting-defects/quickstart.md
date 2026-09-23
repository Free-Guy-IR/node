# Quickstart: validating the accounting fixes

## Local unit runs (macOS or Linux)

```
export PATH=$HOME/singbox-build/go/bin:$PATH GOTOOLCHAIN=local
go test -count=1 ./pkg/stats/ ./backend/mtproto/ ./backend/wireguard/
```

`backend/l2tp` builds only on Linux and `backend/openvpn` management tests need a short socket path;
run those as Linux test binaries:

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/l2tp.test ./backend/l2tp
```

then run the binary in a throwaway `golang:1.26.6-alpine` container on the shared test host
(`--memory 512m --init`, check `free -m` first).

## RED/GREEN evidence

For each fix: check out `eab2601` plus only the new test files, run the new tests and keep the failing
output; then run the same tests on the fix and keep the passing output.

| Fix | Tests (by name pattern) | RED on eab2601 shows |
|-----|-------------------------|----------------------|
| MTProto | `TestNew*Authorises*`, middle-proxy variant | the user's secret is missing after `New` |
| L2TP | `TestCounted*`, `TestRestart*` | the restarted instance reports the full counter / the final twice |
| WireGuard | `TestQuiet*`, `TestFlush*`, `TestHandover*`, `TestRestartCounters*` | stale-handshake growth missing, tail lost |
| Direction | `TestDirection*` | uplink carries the server-sent bytes |

## Full gate (shared test host, one heavy container at a time)

```
make build_xray && make test_xray_patches
TEST_INTEGRATION=true go test ./... -p 1
go test -race -count=1 ./pkg/stats/... ./backend/l2tp/... ./backend/wireguard/... ./backend/mtproto/... ./backend/openvpn/...
go vet ./... && gofmt -l .
```

Expected: all green; gofmt lists only the three files that were already unformatted on the base.
