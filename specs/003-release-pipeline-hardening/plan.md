# Implementation Plan: Release pipeline hardening

**Branch**: `release/v0.6.21-acct-ci4` | **Spec**: `specs/003-release-pipeline-hardening/spec.md`

## Summary

Move the publishing workflows to new paths so the owner can disable the old workflow ids; replace the grep-based
parts of the workflow policy with a YAML-parsing gate checker; make Publish Release publish before `:latest`
moves, converge `:latest` under one concurrency group, refuse unmerged tags, re-check the tag around the publish,
refuse to repoint a release image tag and to publish a release a human published mid-run; tighten the image tag
policy; correct the Release Guard message and the README.

## Technical Context

- Workflows: GitHub Actions, checked by actionlint 1.7.12 with shellcheck 0.11.0.
- Gate checker: Go 1.26.6, `gopkg.in/yaml.v3` v3.0.1 (the parser actionlint uses), in its own module
  `scripts/workflowgate`, so the node module, its `go.mod`, its tests and its govulncheck run stay unchanged.
- Tests: bash. `scripts/test_workflow_policy.sh` (mutations the policy must refuse),
  `scripts/test_image_tag_policy.sh`, `scripts/test_workflow_steps.sh` (runs each `run:` step with stubbed `gh`,
  `git`, `curl`, `docker`, `make`), `go test` in `scripts/workflowgate`.

## Constitution Check

- II Fail Before, Pass After: every fix commit has a test commit before it that fails on its parent.
- III No Explanatory Comments: none in the new Go, bash or YAML.
- VI Identity and Secrecy: Free-Guy-IR, no trailer, no IP, no token.
- VII Fork Seam: only fork-owned CI files change; the upstream README badge line stays.

## Source Code

```
.github/CODEOWNERS
.github/workflows/image-branch.yml        (was docker-build-dev.yml)
.github/workflows/image-manual.yml        (was docker-build.yml)
.github/workflows/publish-release.yml     (was release.yml)
.github/workflows/image.yml
.github/workflows/release-guard.yml
.github/workflows/run-tests.yml
.github/workflows/upstream-drift.yml
scripts/workflow_policy.sh
scripts/workflow_gates.lock
scripts/workflowgate/{go.mod,go.sum,main.go,check.go,steps.go,main_test.go}
scripts/image_tag_policy.sh
scripts/test_workflow_policy.sh
scripts/test_image_tag_policy.sh
scripts/test_workflow_steps.sh
README.md, docs/upstream-parity.md
```

## Ordering

1. Step harness for the current workflows (green, pins current behaviour).
2. F1/F2/F3: RED (retired paths, message names), then the renames and the retired-path rule.
3. F4: RED (more mutations, CODEOWNERS), then the gate checker, the lock, CI wiring.
4. F6: RED, then the image tag policy.
5. F5, F8, F9, F10: RED step cases, then the Publish Release and Release Guard changes.
6. F7: RED step cases, then the reachability check and the `release` environment.
7. README and parity doc.
