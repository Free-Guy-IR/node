package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const regenerate = "(cd scripts/workflowgate && go run . fingerprint -dir ../../.github/workflows) > scripts/workflow_gates.lock"

func isGateWorkflow(name string) bool {
	for _, gate := range gateWorkflows {
		if gate == name {
			return true
		}
	}
	return false
}

func fingerprintMap(workflows []*workflow) (map[string]string, error) {
	out := map[string]string{}
	for _, w := range workflows {
		if !isGateWorkflow(w.name) {
			continue
		}
		sum, err := digest(map[string]any{"env": w.doc["env"], "defaults": w.doc["defaults"]})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", w.name, err)
		}
		out[w.name+" *"] = sum
		for _, name := range w.jobOrder {
			sum, err := digest(w.jobs()[name])
			if err != nil {
				return nil, fmt.Errorf("%s: job %s: %w", w.name, name, err)
			}
			out[w.name+" "+name] = sum
		}
	}
	return out, nil
}

func fingerprints(workflows []*workflow) ([]string, error) {
	m, err := fingerprintMap(workflows)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, key := range sortedKeys(m) {
		lines = append(lines, key+" "+m[key])
	}
	return lines, nil
}

func digest(v any) (string, error) {
	data, err := json.Marshal(normalize(v))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, value := range t {
			if s, ok := value.(string); ok && k == "uses" {
				if at := strings.Index(s, "@"); at >= 0 {
					s = s[:at]
				}
				out[k] = s
				continue
			}
			out[k] = normalize(value)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, value := range t {
			out[i] = normalize(value)
		}
		return out
	}
	return v
}

func (c *checker) checkFingerprints(workflows []*workflow, lock []byte) {
	loaded := map[string]bool{}
	for _, w := range workflows {
		loaded[w.name] = true
	}
	for _, gate := range gateWorkflows {
		if !loaded[gate] {
			c.fail("%s: the gate workflow is missing", gate)
		}
	}
	got, err := fingerprintMap(workflows)
	if err != nil {
		c.fail("cannot fingerprint the gate jobs: %v", err)
		return
	}
	want := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(lock))
	line := 0
	for scanner.Scan() {
		line++
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 || !isGateWorkflow(fields[0]) {
			c.fail("workflow_gates.lock line %d is not '<gate workflow> <job or *> <sha256>'", line)
			continue
		}
		want[fields[0]+" "+fields[1]] = fields[2]
	}
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	for k := range want {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, key := range sorted {
		file, job, _ := strings.Cut(key, " ")
		label := "job " + job
		if job == "*" {
			label = "the workflow-level env and defaults"
		}
		g, inWorkflow := got[key]
		l, inLock := want[key]
		switch {
		case !inLock:
			c.fail("%s: gate %s has no fingerprint in workflow_gates.lock; review it, then regenerate the lock with %s", file, label, regenerate)
		case !inWorkflow:
			c.fail("%s: gate %s is gone but workflow_gates.lock still lists it; a removed gate needs a reviewed lock change", file, label)
		case g != l:
			c.fail("%s: gate %s changed (fingerprint %s, lock %s); a gate change must be reviewed, then regenerate the lock with %s", file, label, g, l, regenerate)
		}
	}
}
