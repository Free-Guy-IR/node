# Quickstart: validating release 0.6.21

Run everything in the release worktree unless a step names a host. Hosts are referred to by role;
their addresses and access are in the KG entity "Node fleet map 2026-09-23".

## 1. Unit and integration suite

```sh
make build_xray
make test_xray_patches
go vet ./...
go test ./... -p 1
go test -race -count=1 ./backend/singbox/... ./backend/openvpn/...
```

Expected: all green. Install the patched xray exactly as `.github/workflows/run-tests.yml` does before
`go test`, and keep the stock binary as `XRAY_STOCK_EXECUTABLE_PATH`.

## 2. Fail-before proof (F3 and version pin)

```sh
git worktree add ../node-v0621-red 4af18c2
git -C ../node-v0621-red checkout <release-sha> -- controller/base_info_test.go backend/singbox/stats_ledger_test.go backend/openvpn/stats_trackers_test.go
cd ../node-v0621-red && go test -count=1 -run 'TestNodeVersionIsPinned|Ledger|Trackers' ./controller/... ./backend/singbox/... ./backend/openvpn/...
```

Expected: the version pin fails with `0.6.20`; the sing-box "second read gets the full traffic" cases
fail with 0 bytes; the OpenVPN inbound-after-outbound case fails. The tests use only API present at
`4af18c2`, so a compile failure is itself a defect in the tests. Remove the scratch worktree afterwards.

## 3. Session-kill regression (User Story 1)

```sh
go test -race -count=1 -run 'TestRemovedUserEstablishedSessionsAreClosed|TestSyncUser|TestUpdateUsers|TestRemovingUsersTheCoreDoesNotHoldIsSilent|TestRemovedUserStreamsOnAConnectionAuthenticatedBeforeAResyncAreRefused' ./backend/xray/...
```

Expected: 8/8 transport cases pass; the stock-core fallback test passes against the stock binary.

## 4. Stopwatch cutoff test on node 56

1. Create a throwaway test user on node 56's inbounds for vless-xhttp, trojan-xhttp, vless-tcp and
   vmess-tcp, plus a bystander user.
2. Start one sustained download per transport for each user from a client machine.
3. Limit the victim in the panel and start the stopwatch at the panel action.
4. Record per transport: time until the victim's stream stops, bytes after the action, bytes on a new
   connection, bystander throughput for the next 30 s.
5. Pass: SC-001. Delete the test users.

## 5. F3 check on node 63 (after 1 h on 0.6.21)

Read-only SELECTs on the panel database for node 63, over the first full hour after the upgrade:
`sum(node_inbound_usages) / sum(node_usages)` must be within 0.95..1.05 (SC-002) and
`sum(node_user_usages) / sum(node_usages)` within 0.02 of the pre-upgrade hour (SC-003). Repeat the
first ratio for node 33 (OpenVPN) over the same window.

## 6. Fleet completion

The panel lists all 21 previously connected nodes as `connected` with `node_version == "0.6.21"`; the 6
disabled nodes are unchanged (SC-004).
