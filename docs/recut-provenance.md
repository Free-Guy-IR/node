# Recut provenance

Node main from `0071e2c` onwards was delivered as linear commits re-cut from the fork's integration
branches, so the original fork commits and merges are not on main. Some delivered commit messages name
those originals as provenance: the parent a RED test was written on, the commit a replay came from.
This table maps every original that a delivered message names to the
delivered commit that carries its change. Upstream commits of PasarGuard/node, Xray-core, sing-box and
mtg are not listed: they keep their own SHAs.

| Original | Subject | Named in | Delivered as | Established by |
|---|---|---|---|---|
| `00c17db` | fix(l2tp): refuse user syncs once the instance is shut down | `8fa7caf` | `db096a7` | replay with an equal message |
| `0658b3e` | spec: bound repo-hygiene to 30 minutes and pin it to ubuntu-24.04 (VD-1) | `5fbb5ae` | `f2d6e2e` | equal patch-id |
| `24fd9c7` | drift: track the node v0.5.5 commits as released and pending their port | `f9b18ed` | `b11d772` | replay with an equal message |
| `2f00380` | drift: recognise the xray build by an allowlist of its stage, not by... | `9e42745` | `1813cba` | replay with an equal message |
| `3461764` | drift: read each pin from the stage that builds, the way docker resol... | `93769b6`, `aacd48b` | `38e923a` | replay with an equal message |
| `4124b00` | drift: count a patch header only for a patch the xray build really ap... | `54241c5`, `d36fec5` | `ac9a069` | replay with an equal message |
| `47434b9` | test(xray): every test_xray_patches line states its own race mode | `2b9e9cb` | `125a51e` | replay with an equal message |
| `5207e59` | test(l2tp): expect only the reachable bound when a counter cannot be... | `26a9de9`, `5db7227`, `8eee1fa` | `619015f` | replay with an equal message |
| `5918851` | drift: name NODE-SYNC-055 as the owner of the node v0.5.5 ports | `f9b18ed` | `17d7d63` | replay with an equal message |
| `595d4ca` | test(l2tp): the restart pins never touch the host's chap-secrets | `8fa7caf` | `5e86ff9` | replay with an equal message |
| `5981789` | drift: add a synthetic-repo test for the drift check, red on the merg... | `08ef206` | `ac29f67` | replay with an equal message |
| `5c948fb` | test(ci): RED, repo-hygiene is neither pinned to ubuntu-24.04 nor bou... | `4d7ec68` | `5fbb5ae` | equal patch-id |
| `5d15511` | drift: count a patch only when the build applies exactly it, fail clo... | `1813cba`, `7b5754a` | `66f800b` | replay with an equal message |
| `5ef5fa6` | refactor(l2tp): read interface counters through a replaceable function | `01416f8`, `a6b5728` | `2a64c5c` | replay with an equal message |
| `63ca286` | test(wireguard): a user who loses WireGuard access must lose its peer | `62d0058` | `bbc488e` | replay with an equal message |
| `65e016d` | docs(002): describe the listing and read holds and the unreadable-sta... | `619015f`, `7044833`, `9fd1aeb`, `bb7203f`, `cc05994` | `77fa55d` | replay with an equal message |
| `69829ad` | drift: count only regular patch files, and stop when a pin is declare... | `356bf4e`, `38e923a` | `f9eb559` | replay with an equal message |
| `6c3fc03` | fix(l2tp): a fallback window spanning reboots remembers the later boots | `01416f8` | `387d80c` | replay with an equal message |
| `72fa61c` | drift: RED, branch-only waivers that never turn UNPORTED after the merge | `ef9daab` | `69bc515` | equal patch-id |
| `7e69f9f` | test(l2tp): a boot whose id was never read stays after the fallback a... | `7a39c00` | `8eee1fa` | replay with an equal message |
| `85a3c11` | test(l2tp): pin the written fallback time, its grace, and a session f... | `7044833` | `5d015b5` | replay with an equal message |
| `89abb6e` | test(l2tp): pin three behaviours no test held | `1bed226` | `26a9de9` | replay with an equal message |
| `8d08f44` | fix(l2tp): keep every character of a record value in the scripts, and... | `0641456` | `9166cb5` | replay with an equal message |
| `921e5fb` | fix(l2tp): hold the watermarks while sessions or final records cannot... | `8c395f9`, `ea598a8` | `a076992` | replay with an equal message |
| `92c4957` | drift: RED, a branch-only waiver keeps waiving after upstream merges... | `b5e2af7` | `a398fc4` | replay with an equal message |
| `94f8db6` | Merge branch 'release/v0.6.21-acct-drift6cut' into release... | `2b9e9cb`, `41a905c`, `f9b18ed` | `f9b18ed` | merge; replay, then the same tree and subject after the message rewords |
| `9851e8b` | fix(l2tp): an unreadable boot id never makes a later boot look older... | `01416f8` | `e32fed4` | replay with an equal message |
| `a346bed` | test(l2tp): a user sync after Shutdown must not write the inbound bac... | `4f3a230`, `8fa7caf`, `db096a7` | `ac06334` | replay with an equal message |
| `a4fd641` | drift: allow the four fork-internal merges the rewritten-merge check... | `2b9e9cb` | `41a905c` | replay, then the same tree and subject after the message rewords |
| `a63b316` | Merge branch 'release/v0.6.21-acct-xhttprace' into release... | `41a905c` | `0c86c21` | merge; same tree |
| `a8e13b2` | test(l2tp): the unreadable-state fallback takes only what started bef... | `5d015b5` | `a713cda` | replay with an equal message |
| `acfe4d3` | fix(wireguard): remove the peer when a user loses WireGuard access | `62d0058` | `cbb63a0` | replay with an equal message |
| `ba4d9f1` | Merge branch 'release/v0.6.21-acct-singbridge' into releas... | `2b9e9cb`, `41a905c`, `f9b18ed` | `49681f5` | merge; same tree |
| `bbc524e` | test(ci): RED, no gating workflow step runs make test-drift | `854d444` | `fa53bc2` | equal patch-id |
| `bd0d52f` | refactor(l2tp): pass the load time to the unreadable-state fallback | `a713cda` | `69330fd` | replay with an equal message |
| `c9f34e7` | test(l2tp): a boot noted at the load must survive a reboot before the... | `ef5aebc` | `5db7227` | replay with an equal message |
| `cf0a5da` | test(l2tp): the fallback window must survive wall-clock steps and nev... | `976fefc` | `9fd1aeb` | replay with an equal message |
| `dafa411` | test(l2tp): the fake host's uptime never goes back when its wall cloc... | `619015f` | `c98bf5c` | replay with an equal message |
| `e1f2e1a` | drift: scope a branch-only waiver to its branch so a merge turns it U... | `4cbd5cf` | `b55f151` | replay with an equal message |
| `ea95958` | drift: give every remaining unported item a dated pending line with i... | `fa53bc2` | `4858d19` | replay, then the same tree and subject after the message rewords |
| `ef4c867` | drift: decide the third-party items the first 0.6.21 CI run reported | `83dc97f` | `ca9ff17` | replay with an equal message |
| `f73da61` | drift: pin the xray source setup to XRAY_COMMIT and fail the run on a... | `6d78f61`, `78b628f` | `43f7665` | replay with an equal message |
| `fad5ad5` | drift: renew the pending waivers that expired on 2026-10-24 | `f9b18ed` | `7b77d33` | replay with an equal message |
