package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	name     string
	raw      []byte
	doc      map[string]any
	jobOrder []string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: workflowgate check|uses|steps|fingerprint [flags]")
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".github/workflows", "workflow directory")
	lock := fs.String("lock", "scripts/workflow_gates.lock", "gate fingerprint file")
	out := fs.String("out", "", "output directory for steps")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	workflows, err := loadDir(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "workflowgate: %v\n", err)
		return 2
	}
	switch args[0] {
	case "check":
		lockData, err := os.ReadFile(*lock)
		if err != nil {
			fmt.Fprintf(stderr, "workflowgate: %v\n", err)
			return 2
		}
		findings := check(workflows, lockData)
		for _, f := range findings {
			fmt.Fprintf(stdout, "FAIL     %s\n", f)
		}
		if len(findings) > 0 {
			return 1
		}
		fmt.Fprintf(stdout, "ok       %d workflow files pass the gate topology, permission, condition, secret and fingerprint rules\n", len(workflows))
		return 0
	case "uses":
		for _, u := range allUses(workflows) {
			fmt.Fprintf(stdout, "%s\t%s\n", u.where, u.value)
		}
		return 0
	case "fingerprint":
		lines, err := fingerprints(workflows)
		if err != nil {
			fmt.Fprintf(stderr, "workflowgate: %v\n", err)
			return 2
		}
		for _, l := range lines {
			fmt.Fprintln(stdout, l)
		}
		return 0
	case "steps":
		if *out == "" {
			fmt.Fprintln(stderr, "workflowgate: steps needs -out")
			return 2
		}
		if err := exportSteps(workflows, *out); err != nil {
			fmt.Fprintf(stderr, "workflowgate: %v\n", err)
			return 2
		}
		return 0
	}
	fmt.Fprintf(stderr, "workflowgate: unknown command %q\n", args[0])
	return 2
}

func loadDir(dir string) ([]*workflow, error) {
	var paths []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no workflow files in %s", dir)
	}
	var workflows []*workflow
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		w, err := parseWorkflow(filepath.Base(path), raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		workflows = append(workflows, w)
	}
	return workflows, nil
}

func parseWorkflow(name string, raw []byte) (*workflow, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one YAML document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the document is not a mapping")
	}
	var doc map[string]any
	if err := root.Content[0].Decode(&doc); err != nil {
		return nil, err
	}
	w := &workflow{name: name, raw: raw, doc: doc}
	if jobs := mappingValue(root.Content[0], "jobs"); jobs != nil && jobs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(jobs.Content); i += 2 {
			w.jobOrder = append(w.jobOrder, jobs.Content[i].Value)
		}
	}
	return w, nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			value := node.Content[i+1]
			for value.Kind == yaml.AliasNode && value.Alias != nil {
				value = value.Alias
			}
			return value
		}
	}
	return nil
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asList(v any) ([]any, bool) {
	l, ok := v.([]any)
	return l, ok
}

func scalar(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (w *workflow) jobs() map[string]any {
	m, _ := asMap(w.doc["jobs"])
	return m
}

func (w *workflow) job(name string) map[string]any {
	m, _ := asMap(w.jobs()[name])
	return m
}

func steps(job map[string]any) []map[string]any {
	list, _ := asList(job["steps"])
	var out []map[string]any
	for _, item := range list {
		m, ok := asMap(item)
		if !ok {
			m = nil
		}
		out = append(out, m)
	}
	return out
}

func needs(job map[string]any) []string {
	switch v := job["needs"].(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, scalar(item))
		}
		return out
	}
	return nil
}
