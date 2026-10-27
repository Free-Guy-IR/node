# Tasks: Release pipeline hardening

**Tests**: REQUIRED. Every fix task is preceded by a test task whose commit fails on its parent.

**Rules**: no explanatory comments; commits by Free-Guy-IR with no trailer; explicit paths only; no push, tag,
image or repository setting change.

- [ ] T001 Commit `scripts/test_workflow_steps.sh` green on e1fc852
- [ ] T002 [F1-F3] RED: retired workflow paths refused; Publish Release message and README name the new path
- [ ] T003 [F1-F3] Rename the three publishing workflows, retired-path rule, references, README scope of Release Guard
- [ ] T004 [F4] RED: more gate-disabling edits (`|| true`, local action, folded `uses`, stored secret, `secrets: inherit`, removed gate job and step, write scope outside the allowlist), CODEOWNERS coverage
- [ ] T005 [F4] `scripts/workflowgate` checker, `scripts/workflow_gates.lock`, policy wiring, CODEOWNERS, tests in the `workflow-policy` job, step harness on the Go extractor
- [ ] T006 [F6] RED: `sha256-*`, `edge` from a branch named `edge`, a custom tag naming a branch
- [ ] T007 [F6] `image_tag_policy.sh --branch`, callers in `image.yml` and `image-manual.yml`
- [ ] T008 [F5 F8 F9 F10] RED step cases for the publish and `:latest` jobs and Release Guard
- [ ] T009 [F5 F8 F9 F10] Publish Release and Release Guard changes
- [ ] T010 [F7] RED step cases for the reachability check
- [ ] T011 [F7] Reachability check in `resolve`, `environment: release` on `publish`
- [ ] T012 README and `docs/upstream-parity.md`
- [ ] T013 Record RED and GREEN runs in `acct-lead-state/evidence/node-ci`, the ledger and the KG
- [ ] T014 [VD-1] RED: the step test requires a `run-tests.yml` step that runs `make test-drift` and, with the real Makefile and both suites stubbed, fails when either suite fails, passes when both pass, and is not skipped by a file named `test-drift`
- [ ] T015 [VD-1] `make test-drift` step in `repo-hygiene`, `test-drift` declared phony, `scripts/workflow_gates.lock` regenerated with `scripts/workflowgate`
- [ ] T016 [VD-1] RED: `workflowgate steps` exports each job's `runs-on` and `timeout-minutes`, and the step test requires `repo-hygiene` to run on `ubuntu-24.04` with a numeric `timeout-minutes` from 1 to 30
- [ ] T017 [VD-1] `repo-hygiene` gets `runs-on: ubuntu-24.04` and `timeout-minutes: 30`, `scripts/workflow_gates.lock` regenerated
