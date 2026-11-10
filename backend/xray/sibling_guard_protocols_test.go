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
		if (r.RuleTag == siblingGuardIPRuleTag || r.RuleTag == siblingGuardDomainRuleTag) && slices.Contains(r.InboundTag, masqueProbeTag) {
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

func packageStringConstants(t *testing.T, files []*ast.File) map[string]string {
	t.Helper()

	values := map[string]string{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			gen, ok := node.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				return true
			}
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
			return true
		})
	}
	return values
}

func isProtocolDerived(expr ast.Node, derived map[string]bool) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if n.Sel.Name == "Protocol" {
				found = true
			}
		case *ast.Ident:
			if derived[n.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

func protocolDerivedNames(body ast.Node) map[string]bool {
	derived := map[string]bool{}
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(node ast.Node) bool {
			var lhs, rhs []ast.Expr
			switch n := node.(type) {
			case *ast.AssignStmt:
				lhs, rhs = n.Lhs, n.Rhs
			case *ast.ValueSpec:
				for _, name := range n.Names {
					lhs = append(lhs, name)
				}
				rhs = n.Values
			default:
				return true
			}
			if len(lhs) != len(rhs) {
				return true
			}
			for idx := range lhs {
				id, ok := lhs[idx].(*ast.Ident)
				if !ok || id.Name == "_" || derived[id.Name] || !isProtocolDerived(rhs[idx], derived) {
					continue
				}
				derived[id.Name] = true
				changed = true
			}
			return true
		})
	}
	return derived
}

func protocolOperandValue(expr ast.Expr, constants map[string]string) (string, bool) {
	switch e := ast.Unparen(expr).(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(e.Value)
		return value, err == nil
	case *ast.Ident:
		value, ok := constants[e.Name]
		return value, ok
	}
	return "", false
}

func unresolvedOperand(expr ast.Expr) string {
	if id, ok := ast.Unparen(expr).(*ast.Ident); ok {
		return "<unresolved " + id.Name + ">"
	}
	return "<unresolved expression>"
}

func protocolValuesCompared(files []*ast.File, constants map[string]string) (map[string]map[string]bool, int) {
	uses := map[string]map[string]bool{}
	record := func(kind, value string) {
		if uses[value] == nil {
			uses[value] = map[string]bool{}
		}
		uses[value][kind] = true
	}
	switches := 0
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			derived := protocolDerivedNames(fn.Body)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.SwitchStmt:
					if n.Tag == nil || !isProtocolDerived(n.Tag, derived) {
						return true
					}
					switches++
					for _, stmt := range n.Body.List {
						clause, ok := stmt.(*ast.CaseClause)
						if !ok {
							continue
						}
						for _, expr := range clause.List {
							value, ok := protocolOperandValue(expr, constants)
							if !ok {
								value = unresolvedOperand(expr)
							}
							record("switch case", value)
						}
					}
				case *ast.BinaryExpr:
					if n.Op != token.EQL && n.Op != token.NEQ {
						return true
					}
					for _, pair := range [][2]ast.Expr{{n.X, n.Y}, {n.Y, n.X}} {
						if !isProtocolDerived(pair[0], derived) || isProtocolDerived(pair[1], derived) {
							continue
						}
						value, ok := protocolOperandValue(pair[1], constants)
						if !ok {
							value = unresolvedOperand(pair[1])
						}
						record("comparison", value)
					}
				case *ast.CallExpr:
					sel, ok := n.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "EqualFold" || len(n.Args) != 2 {
						return true
					}
					if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "strings" {
						return true
					}
					for _, pair := range [][2]ast.Expr{{n.Args[0], n.Args[1]}, {n.Args[1], n.Args[0]}} {
						if !isProtocolDerived(pair[0], derived) || isProtocolDerived(pair[1], derived) {
							continue
						}
						value, ok := protocolOperandValue(pair[1], constants)
						if ok {
							value = strings.ToLower(value)
						} else {
							value = unresolvedOperand(pair[1])
						}
						record("strings.EqualFold", value)
					}
				}
				return true
			})
		}
	}
	return uses, switches
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
	uses, switches := protocolValuesCompared(files, packageStringConstants(t, files))
	if switches == 0 {
		t.Fatal("found no switch over an inbound Protocol field")
	}

	var missing []string
	for name, value := range constants {
		if _, ok := siblingGuardUserProtocols[value]; !ok {
			missing = append(missing, "const "+name+" = "+strconv.Quote(value))
		}
	}
	for value, kinds := range uses {
		if value == "" {
			continue
		}
		if _, ok := siblingGuardUserProtocols[value]; ok {
			continue
		}
		for kind := range kinds {
			missing = append(missing, kind+" "+strconv.Quote(value))
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

func TestProtocolValuesComparedReportsUnresolvedOperands(t *testing.T) {
	src := `package xray

var zzTuicName = "tuic"

func zzAddUsers(in *Inbound) {
	if in.Protocol == zzTuicName {
	}
	p := in.Protocol
	if strings.EqualFold(p, zzTuicName) {
	}
	if zzTuicName != p {
	}
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "zz.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	uses, _ := protocolValuesCompared([]*ast.File{file}, map[string]string{})
	for _, kind := range []string{"comparison", "strings.EqualFold"} {
		if !uses["<unresolved zzTuicName>"][kind] {
			t.Errorf("a %s of the protocol with the package variable zzTuicName is not reported as unresolved: %v", kind, uses)
		}
	}
}
