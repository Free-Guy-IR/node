# Feature Specification: Release pipeline hardening (CI findings F-CI-F1 to F-CI-F10)

**Feature Branch**: `release/v0.6.21-acct-ci4` (feature directory `specs/003-release-pipeline-hardening`)

**Created**: 2026-10-07

**Status**: Draft

**Input**: "Close the r2-C-ci findings F-CI-F1 to F-CI-F10 against the release pipeline of the node fork. Each
in-repository fix ships with a test that fails on its parent and passes on the fix. Fixes that only a repository
setting can make are prepared with evidence for the owner and are not applied."

**Base**: `e1fc852` on `release/v0.6.21-acct-ci4` (32bb663 plus the failing policy tests of 050ed73).

## Clarifications

### Session 2026-10-07

Answered by the implementer without the owner (standing rule: never ask him), choosing the safest option.

- Q: Old tags and branches keep their ungated workflow files forever. Can the repository fix that? -> A: No. A
  workflow runs from the file in the commit of its event, and old commits cannot change. The repository moves every
  publishing workflow to a new file path, and the policy refuses the old paths, so the owner can disable the old
  workflow ids (`docker-build.yml`, `docker-build-dev.yml`, `release.yml`) without disabling the new workflows.
- Q: How is the gate topology checked when the policy itself lives in the same commit as the workflows? -> A: The
  policy parses the workflows with a YAML parser and refuses every edit that lets a publishing job run without the
  tests and the drift check, every `if:` and `continue-on-error` outside a named allowlist, every write permission
  outside a named allowlist, and every change to a gate job that does not also update the reviewed fingerprint
  file. It catches mistakes and makes deliberate gate edits visible in review; only branch and tag rulesets make it
  binding, and those are settings.
- Q: Which release does `:latest` follow? -> A: GitHub's latest release. A job that only one run at a time may
  enter points `:latest` at the image of whatever release GitHub reports as latest when the job runs, so any order
  of overlapping runs and any re-run converges, and nothing moves it backwards.
- Q: What happens when `:<release tag>` already exists with another digest? -> A: Publish Release refuses to
  repoint it and fails. Re-running the failed jobs of the run that built it reuses its own digest and goes through.

## User Scenarios & Testing

### User Story 1 - Old refs cannot publish (F-CI-F1, F-CI-F2, F-CI-F3, priority P1)

The publishing workflows live at new paths: `image-branch.yml`, `image-manual.yml` and `publish-release.yml`.
The policy refuses a workflow at `docker-build.yml`, `docker-build-dev.yml` or `release.yml`, because those
workflow ids get disabled. The README states what Release Guard covers and what only the settings cover.

**Acceptance**: the policy test refuses each retired path; the step test reads the Publish Release name from the
Release Guard message and the README.

### User Story 2 - The policy refuses gate-disabling edits (F-CI-F4, P1)

**Acceptance**: every edit in `scripts/test_workflow_policy.sh` is refused: a dropped or narrowed `needs`, `if:
always()`, a `run_attempt` condition on a job or a step, `continue-on-error` on a job or a step, `|| true` after a
gate command, `permissions: write-all` at workflow or job level, a write scope outside the allowlist, a stored
secret, `secrets: inherit`, a branch-floating action in flow style, under a quoted key or in a folded scalar, a
local composite action, a removed gate job or step. CODEOWNERS lists every file the policy depends on.

### User Story 3 - `:latest` never runs ahead of the release or backwards (F-CI-F5, P2)

**Acceptance**: the release is published before `:latest` moves; the `:latest` job has a concurrency group that
does not depend on the ref; it points `:latest` at the image of GitHub's latest release, so a run for an older
release, or a re-run of one, moves `:latest` to the newest release instead of to its own image.

### User Story 4 - Reserved and owned image tags (F-CI-F6, P3)

**Acceptance**: `image_tag_policy.sh` refuses `sha256-*` tags, refuses `edge` unless the build is of `main`,
refuses `main` and any existing branch name unless it is the branch being built.

### User Story 5 - The released commit is a merged commit and stays the tested one (F-CI-F7, F-CI-F8, P2)

**Acceptance**: Publish Release refuses a tag whose commit is on neither `main` nor a `release/**` branch; it
re-reads the tag right before and right after publishing, refuses to publish when the tag moved, and turns the
release back into a draft when the tag moved during the publish.

### User Story 6 - A human publish during a run is reported correctly (F-CI-F9, P3)

**Acceptance**: Publish Release refuses to publish a release that stopped being a draft during the run, and says
why; Release Guard no longer claims that the release had no tested assets, and names the Publish Release run for
that tag when there is one.

### User Story 7 - Release image tags are never repointed (F-CI-F10, P2)

**Acceptance**: when `:<tag>` exists with another digest, Publish Release fails without moving it; when it exists
with the same digest, it goes on; a registry error fails closed.

### Session 2026-10-27 (VD-1)

- Q: How does CI notice a change that breaks the drift checker itself? -> A: The `repo-hygiene` job of
  `run-tests.yml` runs `make test-drift`, which runs both synthetic suites of the checker,
  `scripts/upstream_drift_test.sh` and `scripts/upstream_drift_bypass_test.sh`. The target is phony, so a file
  named `test-drift` in the checkout cannot turn it into a no-op.

- Q: How long may the job that runs the drift suites run, and on which runner? -> A: The two suites add about 15 to
  17 minutes, so `repo-hygiene` is bounded by `timeout-minutes: 30` instead of the 360-minute default. It runs on
  `ubuntu-24.04`, pinned, because `ubuntu-latest` moves to Ubuntu 26.04 in November 2026, whose rust-coreutils the
  drift checker and its suites have never been tested on; moving the pin is a reviewed change.

## Requirements

- **FR-001**: No workflow file may exist at a retired path. The retired paths are listed in the policy.
- **FR-002**: Every job with a write permission, except the allowlisted Release Guard job, needs (transitively) a
  job that calls `run-tests.yml` and a job that calls `upstream-drift.yml`.
- **FR-003**: No job or step carries `if:` outside the allowlist, and none carries `continue-on-error`.
- **FR-004**: Top-level permissions are a mapping of `read` or `none`; job permissions are a mapping, and each
  write scope is allowlisted for that job.
- **FR-005**: Only `secrets.GITHUB_TOKEN` is referenced; `secrets: inherit` is refused.
- **FR-006**: `uses:` is read from the parsed YAML of every job and step; `./` is allowed only for a reusable
  workflow in the same directory; every other value follows the existing pin rule.
- **FR-007**: Each job of `run-tests.yml` and `upstream-drift.yml` matches its fingerprint in
  `scripts/workflow_gates.lock`; action versions are left out of the fingerprint.
- **FR-008**: Run blocks contain no `|| true`, `|| :` or `set +e`.
- **FR-009**: The policy, its tests and the step tests run in the `workflow-policy` job.
- **FR-010** to **FR-016**: the behaviours of User Stories 3 to 7.
- **FR-017**: A step of `run-tests.yml` runs `make test-drift`; the step fails when either drift suite fails,
  passes only when both pass, and still runs both suites when a file named `test-drift` exists in its working
  directory.
- **FR-018**: The `repo-hygiene` job of `run-tests.yml` runs on `ubuntu-24.04` and sets a numeric
  `timeout-minutes` from 1 to 30.

## Out of repository: settings for the owner

Prepared with read-only evidence and not applied: disable the three old workflow ids; a tag ruleset for `v*`
(creation by admins only; no update, no deletion); an environment `release` with the owner as required reviewer and
a `v*` tag policy; read-only default workflow permissions; optionally a `main` ruleset with the gate checks
required. The exact calls are in the hand-back of NODE-CI and in the ledger.

## Success Criteria

- **SC-001**: each test added here fails on its parent commit and passes on the fix commit, recorded in the
  evidence directory `acct-lead-state/evidence/node-ci`.
- **SC-002**: `actionlint` and `shellcheck` stay clean; the policy passes on the final tree.
