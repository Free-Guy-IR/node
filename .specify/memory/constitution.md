# Free-Guy-IR PasarGuard Node Constitution

## Core Principles

### I. Committed Code Only

Every release image MUST be built from a clean export of a committed tree (`git archive <sha>`
or a clean worktree whose `git status --porcelain` is empty). A tree carrying uncommitted or
untracked work, including another agent's work in progress, MUST NOT be used as a build context.
Rationale: the running fleet must be reproducible from a sha.

### II. Fail Before, Pass After

Every behavioural fix MUST ship with a test that fails on the base commit and passes on the fix,
and the failing run MUST be recorded. Before a release, `go vet ./...` and `go test ./... -p 1`
MUST be green against the xray build the image ships (`make build_xray`), and concurrency-touching
changes MUST also pass under `-race`.

### III. No Explanatory Comments

Handwritten code MUST NOT contain explanatory comments or docstrings in any language or file type.
Exempt: protoc-generated files, shebangs, tooling pragmas and license headers. Existing comments
are neither added to nor removed unless the owner asks. Explanations live in commit messages,
PR bodies, specs and the knowledge graph.

### IV. Strict Patch Versioning

`controller.NodeVersion` moves `0.6.N` -> `0.6.N+1`; the image and git tag move
`v0.8.N-<slug>` -> `v0.8.N+1-<slug>` in lockstep with the same N. Numbers MUST NOT be skipped.
A minor bump requires a completed milestone; a major bump requires explicit owner approval.
The next version is derived from the last used tag and constant, never guessed.

### V. Safe Rollout

A release MUST reach one test node first and pass its acceptance test there before any production
node. The panel's `POST /api/node/{id}/update` MUST NOT be called (it downgrades fork nodes).
Every node update MUST handle both `docker-compose.yml` and `docker-compose.override.yml`,
including tag pins and bare digest pins, and MUST pin the new image by digest. Every step MUST
record a rollback (backup path plus previous digest) before it changes anything. Secrets MUST NOT
appear on a process argv; scripts read them from a `0600` file.

### VI. Identity and Secrecy

Commits MUST be authored by `Free-Guy-IR <free-guy-ir@users.noreply.github.com>` with no
Co-Authored-By trailer. No committed file may carry an AI tool or model name. No IP address,
password, token, private key, dump or `.env` file may be committed. Only explicit paths are staged.
Every pushed sha MUST have its GitHub Actions runs verified as `completed/success`.

### VII. Fork Seam

Fork changes MUST stay minimal and localized to the files that own the behaviour; upstream-shaped
code is not reshaped for style. New behaviour lives in new files where possible. Failures MUST be
loud: a patch that stops applying fails the build, a fallback logs once, a lost counter is a test
failure, never a silent zero.

## Release Constraints

- Go toolchain 1.26.3 with `GOTOOLCHAIN=local` for the xray build; xray pinned by commit in the
  Dockerfile and asserted after checkout; every `patches/xray-*.patch` the Dockerfile applies is
  asserted by a grep after `git apply`.
- Images are published only to `ghcr.io/free-guy-ir/node` for `linux/amd64` and `linux/arm64`.
- Per-user stats (billing) paths change only under a spec that names them explicitly.
- Release CI evidence comes from the release pull request's test workflow; a release image comes
  from the release workflow on a GitHub pre-release, so the floating `latest` tag moves only by an
  explicit owner decision.

## Development Workflow

Spec Kit governs what is built: constitution -> specify -> clarify -> plan -> tasks -> analyze ->
implement. An independent external review approves each logic-bearing diff, and a second,
whole-release review approves the release before build. Implementation, verification, release and rollout are separate owners; the
verifier re-derives every claim instead of relaying it. Progress is logged to the knowledge graph
as it happens.

## Governance

This constitution supersedes local habit in this repository. Amendments require owner approval,
a Sync Impact Report, and a semantic version bump of this file (MAJOR for removed or redefined
principles, MINOR for added or materially expanded ones, PATCH for wording). Every plan's
Constitution Check and every review MUST cite the principles it verified.

**Version**: 1.1.0 | **Ratified**: 2026-09-23 | **Last Amended**: 2026-09-23
