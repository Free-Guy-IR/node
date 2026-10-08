package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type usesEntry struct {
	where, value string
}

func allUses(workflows []*workflow) []usesEntry {
	var out []usesEntry
	for _, w := range workflows {
		for _, name := range w.jobOrder {
			job := w.job(name)
			if job == nil {
				continue
			}
			if value, ok := job["uses"]; ok {
				out = append(out, usesEntry{fmt.Sprintf("%s:jobs.%s.uses", w.name, name), strings.TrimSpace(scalar(value))})
			}
			for i, step := range steps(job) {
				if step == nil {
					continue
				}
				if value, ok := step["uses"]; ok {
					out = append(out, usesEntry{fmt.Sprintf("%s:jobs.%s.steps[%d].uses", w.name, name, i), strings.TrimSpace(scalar(value))})
				}
			}
		}
	}
	return out
}

func literalEnv(layers ...any) ([]string, map[string]string) {
	env := map[string]string{}
	listed := map[string]bool{}
	var order []string
	for _, layer := range layers {
		m, ok := asMap(layer)
		if !ok {
			continue
		}
		for _, key := range sortedKeys(m) {
			value := scalar(m[key])
			if strings.Contains(value, "${{") {
				delete(env, key)
				continue
			}
			if !listed[key] {
				listed[key] = true
				order = append(order, key)
			}
			env[key] = value
		}
	}
	var keys []string
	for _, key := range order {
		if _, ok := env[key]; ok {
			keys = append(keys, key)
		}
	}
	return keys, env
}

func exportSteps(workflows []*workflow, out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	var index, jobsIndex strings.Builder
	for _, w := range workflows {
		for _, name := range w.jobOrder {
			job := w.job(name)
			if job == nil {
				continue
			}
			concurrency := "-"
			switch v := job["concurrency"].(type) {
			case string:
				concurrency = v
			case map[string]any:
				concurrency = scalar(v["group"])
			}
			environment := "-"
			switch v := job["environment"].(type) {
			case string:
				environment = v
			case map[string]any:
				environment = scalar(v["name"])
			}
			deps := strings.Join(needs(job), ",")
			if deps == "" {
				deps = "-"
			}
			fmt.Fprintf(&jobsIndex, "%s\t%s\t%s\t%s\t%s\n", w.name, name, deps, concurrency, environment)
			for i, step := range steps(job) {
				if step == nil {
					continue
				}
				base := filepath.Join(out, w.name, name, fmt.Sprint(i))
				if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
					return err
				}
				kind := "uses"
				if script, ok := step["run"]; ok {
					kind = "run"
					if err := os.WriteFile(base+".sh", []byte(scalar(script)), 0o644); err != nil {
						return err
					}
				}
				keys, env := literalEnv(w.doc["env"], job["env"], step["env"])
				var envData strings.Builder
				for _, key := range keys {
					envData.WriteString(key + "=" + env[key] + "\x00")
				}
				if err := os.WriteFile(base+".env", []byte(envData.String()), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(base+".wd", []byte(scalar(step["working-directory"])), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(&index, "%s\t%s\t%d\t%s\t%s\n", w.name, name, i, kind, scalar(step["name"]))
			}
		}
	}
	if err := os.WriteFile(filepath.Join(out, "index"), []byte(index.String()), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "jobs"), []byte(jobsIndex.String()), 0o644)
}
