# Tasks: proxy users cannot reach the xray node's own control ports

- [X] T001 Spec with the scope clarification (own ports), the residual and the alignment note
- [ ] T002 Config-level RED: a route resolver over the rules `ApplyAPI` writes shows a VLESS user's
  `127.0.0.1`, `127.0.0.53` and `localhost` destinations on the api and metric ports reach the freedom
  outbound with no panel rules, with the panel's private-address rule and with a panel catch-all to the
  freedom outbound; the guard is absent after Clone and ToBytes; ApplyAPI run three times leaves no single
  guard pair and no node blackhole; other destinations and API_INBOUND keep their routes
- [ ] T003 Live RED: a real VLESS user tunnels to the api port as `127.0.0.1`, `localhost` and numeric host
  forms and runs `QueryStats(reset=true)`; the user's counter is zeroed
- [ ] T004 Interleaving RED: the attacker reaches the API while `SyncUser`, `UpdateUsers`, `SyncUsers`,
  `Restart`, failed restarts that restore the previous config and stats polls run concurrently
- [ ] T005 Fix: `backend/xray/api_guard.go`, the calls in `ApplyAPI`, the strip in `filterRules`
- [ ] T006 GREEN of T002-T004 on the fix, full `./backend/xray/...` under `-race`, `go vet`
- [ ] T007 Mutants: guard rule dropped, address rule only, name rule only, guard appended after the panel's
  rules, node outbound missing, guard stripped from the config `Restart` replays, guard stripped from
  clones; each killed
- [ ] T008 Production core 1 read-only shape check
