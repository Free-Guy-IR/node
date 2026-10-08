package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	testsWorkflow = "./.github/workflows/run-tests.yml"
	driftWorkflow = "./.github/workflows/upstream-drift.yml"
	selfRepo      = "free-guy-ir/node"
)

var retiredPaths = map[string]string{
	"docker-build.yml":     "image-manual.yml",
	"docker-build-dev.yml": "image-branch.yml",
	"release.yml":          "publish-release.yml",
}

var gateWorkflows = []string{"run-tests.yml", "upstream-drift.yml"}

var writeScopes = map[string]map[string][]string{
	"image-branch.yml":    {"image": {"packages", "id-token"}},
	"image-manual.yml":    {"image": {"packages", "id-token"}},
	"image.yml":           {"build": {"packages", "id-token"}},
	"publish-release.yml": {"image": {"packages", "id-token"}, "publish": {"contents", "packages"}, "latest": {"packages"}},
	"release-guard.yml":   {"refuse-hand-published": {"contents"}},
}

var ungatedWriters = map[string]map[string]string{
	"release-guard.yml": {"refuse-hand-published": "it only turns a hand-published release back into a draft"},
}

type condition struct {
	file, job, step, expr string
}

var allowedConditions = []condition{
	{"upstream-drift.yml", "drift", "Explain why this blocks image builds and releases", "failure() && steps.drift.outcome == 'failure'"},
}

var allowedShells = map[string]bool{"bash": true, "sh": true}

var swallowed = []*regexp.Regexp{
	regexp.MustCompile(`\|\|\s*(true|:)(\s|$|;|\)|&)`),
	regexp.MustCompile(`\|\|\s*exit\s+0\b`),
	regexp.MustCompile(`\bset\s+\+[A-Za-z]*e`),
	regexp.MustCompile(`\bset\s+\+o\s+errexit\b`),
}

var (
	expressionRe = regexp.MustCompile(`(?s)\$\{\{(.*?)\}\}`)
	secretsRe    = regexp.MustCompile(`(?i)\bsecrets\b`)
	jobTokenRe   = regexp.MustCompile(`(?i)^\s*(\.\s*github_token\b|\[\s*['"]github_token['"]\s*\])`)
)

type checker struct {
	findings []string
}

func (c *checker) fail(format string, args ...any) {
	c.findings = append(c.findings, fmt.Sprintf(format, args...))
}

func check(workflows []*workflow, lock []byte) []string {
	c := &checker{}
	names := map[string]*workflow{}
	for _, w := range workflows {
		names[w.name] = w
	}
	for _, w := range workflows {
		c.checkWorkflow(w, names)
	}
	c.checkFingerprints(workflows, lock)
	return c.findings
}

func (c *checker) checkWorkflow(w *workflow, names map[string]*workflow) {
	if replacement, ok := retiredPaths[w.name]; ok {
		c.fail("%s: a retired workflow path; old tags and branches carry an ungated workflow here, so its workflow id is disabled in the repository settings and a file at this path never runs; use %s", w.name, replacement)
	}
	c.checkTopPermissions(w)
	c.checkDefaults(w.name, w.doc["defaults"])
	c.checkSecrets(w)
	jobs := w.jobs()
	if jobs == nil {
		c.fail("%s: no jobs mapping", w.name)
		return
	}
	for _, name := range w.jobOrder {
		job, ok := asMap(jobs[name])
		if !ok {
			c.fail("%s: job %s is not a mapping", w.name, name)
			continue
		}
		c.checkJob(w, name, job, names)
	}
	c.checkGateTopology(w)
}

func (c *checker) checkTopPermissions(w *workflow) {
	value, ok := w.doc["permissions"]
	if !ok {
		c.fail("%s: no top-level permissions block", w.name)
		return
	}
	scopes, ok := asMap(value)
	if !ok {
		c.fail("%s: top-level permissions must be a mapping of scopes, not %q", w.name, scalar(value))
		return
	}
	for _, scope := range sortedKeys(scopes) {
		level := scalar(scopes[scope])
		if level != "read" && level != "none" {
			c.fail("%s: top-level permissions grant %s: %s; grant write access per job, inside the allowlist of workflowgate", w.name, scope, level)
		}
	}
}

func (c *checker) checkDefaults(where string, value any) {
	defaults, ok := asMap(value)
	if !ok {
		return
	}
	runDefaults, ok := asMap(defaults["run"])
	if !ok {
		return
	}
	if shell, ok := runDefaults["shell"]; ok && !allowedShells[scalar(shell)] {
		c.fail("%s: defaults.run.shell %q; only bash and sh are allowed, a custom shell command can skip the script", where, scalar(shell))
	}
}

func (c *checker) checkSecrets(w *workflow) {
	for _, m := range expressionRe.FindAllStringSubmatch(string(w.raw), -1) {
		expr := m[1]
		for _, loc := range secretsRe.FindAllStringIndex(expr, -1) {
			if !jobTokenRe.MatchString(expr[loc[1]:]) {
				c.fail("%s: the expression ${{%s}} reads a stored secret; only secrets.GITHUB_TOKEN, the job's own token, is allowed", w.name, expr)
				break
			}
		}
	}
}

func (c *checker) checkJob(w *workflow, name string, job map[string]any, names map[string]*workflow) {
	where := w.name + ": job " + name
	if value, ok := job["permissions"]; ok {
		scopes, ok := asMap(value)
		if !ok {
			c.fail("%s: permissions must be a mapping of scopes, not %q", where, scalar(value))
		} else {
			for _, scope := range sortedKeys(scopes) {
				switch level := scalar(scopes[scope]); level {
				case "read", "none":
				case "write":
					if !writeAllowed(w.name, name, scope) {
						c.fail("%s: %s: write is not in the allowlist of workflowgate", where, scope)
					}
				default:
					c.fail("%s: %s: %q is not read, write or none", where, scope, level)
				}
			}
		}
	}
	if _, ok := job["continue-on-error"]; ok {
		c.fail("%s: continue-on-error lets a failing job count as passed", where)
	}
	if value, ok := job["if"]; ok && !conditionAllowed(w.name, name, "", scalar(value)) {
		c.fail("%s: if: %s; a condition on a job can skip it or run it after a failed gate, and only the conditions listed in workflowgate are allowed", where, scalar(value))
	}
	if _, ok := job["secrets"]; ok {
		c.fail("%s: secrets passed to a reusable workflow; only the job's own token is allowed", where)
	}
	c.checkDefaults(where, job["defaults"])
	if value, ok := job["uses"]; ok {
		uses, isString := value.(string)
		if !isString {
			c.fail("%s: uses is not a string", where)
		} else if strings.ContainsAny(uses, " \t\r\n") {
			c.fail("%s: uses %q contains whitespace", where, uses)
		} else if strings.HasPrefix(uses, "./") {
			target := strings.TrimPrefix(uses, "./.github/workflows/")
			if target == uses || strings.Contains(target, "/") || names[target] == nil {
				c.fail("%s: uses %s; a local path must name a reusable workflow in .github/workflows", where, uses)
			}
		}
	}
	for i, step := range steps(job) {
		stepWhere := fmt.Sprintf("%s step %d", where, i+1)
		if step == nil {
			c.fail("%s: not a mapping", stepWhere)
			continue
		}
		stepName := scalar(step["name"])
		if stepName != "" {
			stepWhere = fmt.Sprintf("%s step %q", where, stepName)
		}
		if _, ok := step["continue-on-error"]; ok {
			c.fail("%s: continue-on-error lets a failing step count as passed", stepWhere)
		}
		if value, ok := step["if"]; ok && !conditionAllowed(w.name, name, stepName, scalar(value)) {
			c.fail("%s: if: %s; only the conditions listed in workflowgate are allowed", stepWhere, scalar(value))
		}
		if shell, ok := step["shell"]; ok && !allowedShells[scalar(shell)] {
			c.fail("%s: shell %q; only bash and sh are allowed, a custom shell command can skip the script", stepWhere, scalar(shell))
		}
		if value, ok := step["uses"]; ok {
			uses, isString := value.(string)
			switch {
			case !isString:
				c.fail("%s: uses is not a string", stepWhere)
			case strings.ContainsAny(uses, " \t\r\n"):
				c.fail("%s: uses %q contains whitespace", stepWhere, uses)
			case strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "../"):
				c.fail("%s: uses %s; local actions are not allowed, because the policy cannot vouch for their steps", stepWhere, uses)
			case strings.HasPrefix(strings.ToLower(uses), selfRepo+"/") || strings.HasPrefix(strings.ToLower(uses), selfRepo+"@"):
				c.fail("%s: uses %s; actions from this repository are not allowed, because the policy cannot vouch for their steps", stepWhere, uses)
			}
		}
		if script, ok := step["run"].(string); ok {
			for _, re := range swallowed {
				if loc := re.FindString(script); loc != "" {
					c.fail("%s: run contains %q, which swallows a failure", stepWhere, strings.TrimSpace(loc))
				}
			}
		}
	}
}

func (c *checker) checkGateTopology(w *workflow) {
	if onlyWorkflowCall(w.doc["on"]) {
		return
	}
	jobs := w.jobs()
	for _, name := range w.jobOrder {
		job, ok := asMap(jobs[name])
		if !ok {
			continue
		}
		scopes := writeScopesOf(job)
		if len(scopes) == 0 {
			continue
		}
		if _, exempt := ungatedWriters[w.name][name]; exempt {
			continue
		}
		closure := needsClosure(w, name)
		var hasTests, hasDrift bool
		for _, dep := range closure {
			switch scalar(w.job(dep)["uses"]) {
			case testsWorkflow:
				hasTests = true
			case driftWorkflow:
				hasDrift = true
			}
		}
		if !hasTests {
			c.fail("%s: job %s can write (%s) but does not need, directly or through its needs, a job that calls %s", w.name, name, strings.Join(scopes, ", "), testsWorkflow)
		}
		if !hasDrift {
			c.fail("%s: job %s can write (%s) but does not need, directly or through its needs, a job that calls %s", w.name, name, strings.Join(scopes, ", "), driftWorkflow)
		}
	}
}

func needsClosure(w *workflow, name string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(job string) {
		for _, dep := range needs(w.job(job)) {
			if !seen[dep] {
				seen[dep] = true
				walk(dep)
			}
		}
	}
	walk(name)
	return sortedKeys(seen)
}

func onlyWorkflowCall(on any) bool {
	switch v := on.(type) {
	case string:
		return v == "workflow_call"
	case []any:
		return len(v) == 1 && scalar(v[0]) == "workflow_call"
	case map[string]any:
		_, ok := v["workflow_call"]
		return ok && len(v) == 1
	}
	return false
}

func writeScopesOf(job map[string]any) []string {
	scopes, ok := asMap(job["permissions"])
	if !ok {
		if value, present := job["permissions"]; present && scalar(value) == "write-all" {
			return []string{"write-all"}
		}
		return nil
	}
	var out []string
	for _, scope := range sortedKeys(scopes) {
		if scalar(scopes[scope]) == "write" {
			out = append(out, scope)
		}
	}
	return out
}

func writeAllowed(file, job, scope string) bool {
	for _, allowed := range writeScopes[file][job] {
		if allowed == scope {
			return true
		}
	}
	return false
}

func conditionAllowed(file, job, step, expr string) bool {
	for _, allowed := range allowedConditions {
		if allowed.file == file && allowed.job == job && allowed.step == step && allowed.expr == strings.TrimSpace(expr) {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
