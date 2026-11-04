package xray

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const masqueProbeTag = "MASQUE LOOPBACK PROBE"

func masqueSiblingProbeConfig(t *testing.T) *Config {
	t.Helper()

	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{
				"tag": masqueProbeTag, "listen": "0.0.0.0", "port": 443,
				"protocol": "masque",
				"settings": map[string]any{"users": []any{}, "address": []any{"10.0.0.0/24"}, "mtu": 1400},
				"streamSettings": map[string]any{
					"network": "masque", "security": "tls",
					"masqueSettings": map[string]any{"path": "/.well-known/masque/ip/*/*/"},
				},
			},
			map[string]any{
				"tag": "PANEL TUNNEL", "listen": "0.0.0.0", "port": 12346,
				"protocol": "dokodemo-door",
				"settings": map[string]any{"address": "127.0.0.1", "port": 39000, "network": "tcp"},
			},
		},
		"outbounds": []any{
			map[string]any{"tag": "DIRECT", "protocol": "freedom"},
			map[string]any{"tag": "BLOCK", "protocol": "blackhole"},
		},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "DIRECT"},
		}},
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := NewConfig(string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestSiblingGuardBlocksMasqueUserFromLoopback(t *testing.T) {
	const (
		apiPort     = 48001
		metricPort  = 48002
		siblingPort = 39000
		nodeAPIPort = 62050
	)

	cfg := masqueSiblingProbeConfig(t)
	if tags := cfg.userInboundTags(); !slices.Contains(tags, masqueProbeTag) {
		t.Fatalf("userInboundTags() = %q, want it to contain the MASQUE inbound %q", tags, masqueProbeTag)
	}
	if err := cfg.ApplyAPI(apiPort, metricPort); err != nil {
		t.Fatal(err)
	}

	guarded := false
	for _, rawRule := range cfg.RouterConfig.RuleList {
		var r resolvedRule
		if err := json.Unmarshal(rawRule, &r); err != nil {
			t.Fatal(err)
		}
		if r.RuleTag == siblingGuardRuleTag && slices.Contains(r.InboundTag, masqueProbeTag) {
			guarded = true
		}
	}
	if !guarded {
		t.Fatalf("no %s rule names the MASQUE inbound %q", siblingGuardRuleTag, masqueProbeTag)
	}

	for _, q := range []destQuery{
		{inbound: masqueProbeTag, isIP: true, value: "127.0.0.1", port: siblingPort},
		{inbound: masqueProbeTag, isIP: true, value: "127.0.0.1", port: nodeAPIPort},
		{inbound: masqueProbeTag, isIP: true, value: "0.0.0.0", port: siblingPort},
		{inbound: masqueProbeTag, isIP: true, value: "169.254.1.2", port: siblingPort},
		{inbound: masqueProbeTag, isIP: false, value: "localhost", port: nodeAPIPort},
	} {
		if tag, protocol := resolveOutbound(t, cfg, q); !destinationIsBlocked(protocol) {
			t.Errorf("a MASQUE user reached loopback %q:%d via %q (%s); want blackholed", q.value, q.port, tag, protocol)
		}
	}

	if tag, protocol := resolveOutbound(t, cfg, destQuery{inbound: "PANEL TUNNEL", isIP: true, value: "127.0.0.1", port: siblingPort}); protocol != "freedom" {
		t.Errorf("the panel tunnel to loopback now routes to %q (%s), want freedom", tag, protocol)
	}
	if tag, protocol := resolveOutbound(t, cfg, destQuery{inbound: masqueProbeTag, isIP: false, value: "example.com", port: 443}); protocol != "freedom" {
		t.Errorf("a MASQUE user to example.com:443 now routes to %q (%s), want freedom", tag, protocol)
	}
}

func packageProtocolConstants(t *testing.T, files []*ast.File) map[string]string {
	t.Helper()

	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			values := map[string]string{}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != len(vs.Names) {
					continue
				}
				for idx, name := range vs.Names {
					lit, ok := vs.Values[idx].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					values[name.Name] = value
				}
			}
			if _, ok := values["Vmess"]; ok {
				return values
			}
		}
	}
	t.Fatal("no const block declares Vmess")
	return nil
}

func protocolSwitchCases(files []*ast.File, constants map[string]string) map[string]int {
	cases := map[string]int{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			sw, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			sel, ok := sw.Tag.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Protocol" {
				return true
			}
			for _, stmt := range sw.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					var value string
					switch e := expr.(type) {
					case *ast.Ident:
						value, ok = constants[e.Name]
						if !ok {
							value = "<unresolved " + e.Name + ">"
						}
					case *ast.BasicLit:
						value, _ = strconv.Unquote(e.Value)
					default:
						value = "<unresolved expression>"
					}
					cases[value]++
				}
			}
			return true
		})
	}
	return cases
}

func TestSiblingGuardCoversEveryUserProtocolOfTheConfig(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}

	constants := packageProtocolConstants(t, files)
	cases := protocolSwitchCases(files, constants)
	if len(cases) == 0 {
		t.Fatal("found no switch over an inbound Protocol field")
	}

	var missing []string
	for name, value := range constants {
		if _, ok := siblingGuardUserProtocols[value]; !ok {
			missing = append(missing, "const "+name+" = "+strconv.Quote(value))
		}
	}
	for value := range cases {
		if _, ok := siblingGuardUserProtocols[value]; !ok {
			missing = append(missing, "switch case "+strconv.Quote(value))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("protocols the config manages users for but the sibling loopback guard does not cover: %s", strings.Join(missing, "; "))
	}

	for _, value := range constants {
		cfg := &Config{InboundConfigs: []*Inbound{{Tag: "probe-" + value, Protocol: value}}}
		if tags := cfg.userInboundTags(); !slices.Contains(tags, "probe-"+value) {
			t.Errorf("an inbound with protocol %q is not in userInboundTags() = %q", value, tags)
		}
	}
}
