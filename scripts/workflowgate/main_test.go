package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gatedRelease = `name: Publish Release
on:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  tests:
    uses: ./.github/workflows/run-tests.yml
  drift:
    uses: ./.github/workflows/upstream-drift.yml
  publish:
    needs: [tests, drift]
    runs-on: ubuntu-latest
    permissions:
      contents: write
      packages: write
    steps:
      - name: Publish
        run: echo publish
`

const runTests = `name: Test Modules
on:
  workflow_call:
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout codebase
        uses: actions/checkout@v7
      - name: Run regular test suite
        run: go test ./...
`

const upstreamDrift = `name: Upstream Drift
on:
  workflow_call:
permissions:
  contents: read
jobs:
  drift:
    runs-on: ubuntu-latest
    steps:
      - name: Compare
        run: bash scripts/upstream_drift.sh
`

func load(t *testing.T, files map[string]string) []*workflow {
	t.Helper()
	dir := t.TempDir()
	base := map[string]string{
		"publish-release.yml": gatedRelease,
		"run-tests.yml":       runTests,
		"upstream-drift.yml":  upstreamDrift,
	}
	for name, body := range files {
		base[name] = body
	}
	for name, body := range base {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	workflows, err := loadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return workflows
}

func lockFor(t *testing.T, workflows []*workflow) []byte {
	t.Helper()
	lines, err := fingerprints(workflows)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func findingsOf(t *testing.T, files map[string]string) []string {
	t.Helper()
	baseline := load(t, nil)
	return check(load(t, files), lockFor(t, baseline))
}

func expectFinding(t *testing.T, findings []string, want string) {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f, want) {
			return
		}
	}
	t.Fatalf("no finding contains %q; findings: %q", want, findings)
}

func TestBaselinePasses(t *testing.T) {
	if findings := findingsOf(t, nil); len(findings) != 0 {
		t.Fatalf("unexpected findings: %q", findings)
	}
}

func TestUsesIsReadFromParsedYAML(t *testing.T) {
	body := strings.Replace(runTests, "      - name: Checkout codebase\n        uses: actions/checkout@v7\n",
		"      - {name: Checkout codebase, \"uses\": 'actions/checkout@main'}\n", 1)
	got := allUses(load(t, map[string]string{"run-tests.yml": body}))
	var found bool
	for _, u := range got {
		if u.value == "actions/checkout@main" {
			found = true
		}
	}
	if !found {
		t.Fatalf("flow-style quoted uses not listed: %+v", got)
	}
}

func TestUsesThroughAnAliasIsListed(t *testing.T) {
	body := strings.Replace(runTests, "jobs:\n", "x-checkout: &checkout actions/checkout@main\njobs:\n", 1)
	body = strings.Replace(body, "uses: actions/checkout@v7", "uses: *checkout", 1)
	var found bool
	for _, u := range allUses(load(t, map[string]string{"run-tests.yml": body})) {
		if u.value == "actions/checkout@main" {
			found = true
		}
	}
	if !found {
		t.Fatal("aliased uses not listed")
	}
}

func TestDuplicateKeysAreRefused(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(gatedRelease, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    runs-on: self-hosted\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "publish-release.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDir(dir); err == nil {
		t.Fatal("a duplicate key was accepted")
	}
}

func TestSecondDocumentIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yml"), []byte(gatedRelease+"---\nname: other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDir(dir); err == nil {
		t.Fatal("a second YAML document was accepted")
	}
}

func TestWriterNeedsBothGatesTransitively(t *testing.T) {
	body := strings.Replace(gatedRelease, "    needs: [tests, drift]\n", "    needs: [tests]\n", 1)
	expectFinding(t, findingsOf(t, map[string]string{"publish-release.yml": body}), "upstream-drift.yml")
	chained := strings.Replace(gatedRelease, "  publish:\n    needs: [tests, drift]\n",
		"  middle:\n    needs: [tests, drift]\n    runs-on: ubuntu-latest\n    steps:\n      - name: Middle\n        run: echo middle\n  publish:\n    needs: middle\n", 1)
	if findings := findingsOf(t, map[string]string{"publish-release.yml": chained}); len(findings) != 0 {
		t.Fatalf("a transitive gate was not followed: %q", findings)
	}
}

func TestConditionsAndContinueOnError(t *testing.T) {
	body := strings.Replace(gatedRelease, "    needs: [tests, drift]\n", "    needs: [tests, drift]\n    if: ${{ !cancelled() }}\n", 1)
	expectFinding(t, findingsOf(t, map[string]string{"publish-release.yml": body}), "if:")
	body = strings.Replace(runTests, "      - name: Run regular test suite\n", "      - name: Run regular test suite\n        continue-on-error: ${{ true }}\n", 1)
	expectFinding(t, findingsOf(t, map[string]string{"run-tests.yml": body}), "continue-on-error")
}

func TestPermissions(t *testing.T) {
	body := strings.Replace(gatedRelease, "permissions:\n  contents: read\n", "permissions: read-all\n", 1)
	expectFinding(t, findingsOf(t, map[string]string{"publish-release.yml": body}), "must be a mapping")
	body = strings.Replace(gatedRelease, "      packages: write\n", "      packages: write\n      actions: write\n", 1)
	expectFinding(t, findingsOf(t, map[string]string{"publish-release.yml": body}), "actions: write is not in the allowlist")
}

func TestStoredSecretsAndShells(t *testing.T) {
	body := strings.Replace(gatedRelease, "run: echo publish", "run: echo \"${{ secrets['GHCR'] }}\"", 1)
	expectFinding(t, findingsOf(t, map[string]string{"publish-release.yml": body}), "stored secret")
	body = strings.Replace(gatedRelease, "run: echo publish", "run: echo \"${{ toJSON(secrets) }}\"", 1)
	expectFinding(t, findingsOf(t, map[string]string{"publish-release.yml": body}), "stored secret")
	body = strings.Replace(gatedRelease, "run: echo publish", "run: echo \"${{ secrets.github_token }}\"", 1)
	if findings := findingsOf(t, map[string]string{"publish-release.yml": body}); len(findings) != 0 {
		t.Fatalf("the job token was refused: %q", findings)
	}
	body = strings.Replace(runTests, "        run: go test ./...\n", "        shell: bash -c 'exit 0' {0}\n        run: go test ./...\n", 1)
	expectFinding(t, findingsOf(t, map[string]string{"run-tests.yml": body}), "shell")
}

func TestSwallowedFailures(t *testing.T) {
	for _, tail := range []string{" || true", " || :", " || exit 0", "\nset +e"} {
		body := strings.Replace(runTests, "run: go test ./...", "run: |\n          go test ./..."+strings.ReplaceAll(tail, "\n", "\n          "), 1)
		expectFinding(t, findingsOf(t, map[string]string{"run-tests.yml": body}), "swallows a failure")
	}
}

func TestLocalActionsAndRetiredPaths(t *testing.T) {
	body := strings.Replace(runTests, "uses: actions/checkout@v7", "uses: ./.github/actions/checkout", 1)
	expectFinding(t, findingsOf(t, map[string]string{"run-tests.yml": body}), "local actions are not allowed")
	body = strings.Replace(runTests, "uses: actions/checkout@v7", "uses: Free-Guy-IR/node/.github/actions/x@0123456789012345678901234567890123456789", 1)
	expectFinding(t, findingsOf(t, map[string]string{"run-tests.yml": body}), "actions from this repository")
	expectFinding(t, findingsOf(t, map[string]string{"release.yml": gatedRelease}), "retired workflow path")
}

func TestFingerprints(t *testing.T) {
	baseline := load(t, nil)
	lock := lockFor(t, baseline)
	bumped := strings.Replace(runTests, "actions/checkout@v7", "actions/checkout@v8", 1)
	if findings := check(load(t, map[string]string{"run-tests.yml": bumped}), lock); len(findings) != 0 {
		t.Fatalf("an action version bump changed a fingerprint: %q", findings)
	}
	flow := strings.Replace(runTests, "      - name: Run regular test suite\n        run: go test ./...\n", "      - {name: Run regular test suite, run: go test ./...}\n", 1)
	if findings := check(load(t, map[string]string{"run-tests.yml": flow}), lock); len(findings) != 0 {
		t.Fatalf("a style-only change changed a fingerprint: %q", findings)
	}
	narrowed := strings.Replace(runTests, "go test ./...", "go test ./pkg/...", 1)
	expectFinding(t, check(load(t, map[string]string{"run-tests.yml": narrowed}), lock), "gate job test changed")
	env := strings.Replace(runTests, "jobs:\n", "env:\n  GOFLAGS: -run=^$\njobs:\n", 1)
	expectFinding(t, check(load(t, map[string]string{"run-tests.yml": env}), lock), "workflow-level env")
	removed := strings.Replace(upstreamDrift, "  drift:\n", "  other:\n", 1)
	findings := check(load(t, map[string]string{"upstream-drift.yml": removed}), lock)
	expectFinding(t, findings, "gate job drift is gone")
	expectFinding(t, findings, "gate job other has no fingerprint")
}

func TestStepsExport(t *testing.T) {
	out := t.TempDir()
	body := strings.Replace(gatedRelease, "      - name: Publish\n", "      - name: Publish\n        env:\n          TAG: ${{ github.ref_name }}\n          COUNT: 0\n          ON: true\n", 1)
	if err := exportSteps(load(t, map[string]string{"publish-release.yml": body}), out); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(out, "publish-release.yml", "publish", "0.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(env) != "COUNT=0\x00ON=true\x00" {
		t.Fatalf("env = %q", env)
	}
	jobs, err := os.ReadFile(filepath.Join(out, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jobs), "publish-release.yml\tpublish\ttests,drift\t-\t-\n") {
		t.Fatalf("jobs = %q", jobs)
	}
}
