package l2tp

import (
	"strings"
	"testing"
)

type swanctlChildBlock struct {
	connection string
	name       string
	settings   map[string][]string
}

func swanctlConnectionsAndChildren(t *testing.T, conf string) ([]string, []swanctlChildBlock) {
	t.Helper()
	var path, connections []string
	var children []swanctlChildBlock
	inChild := func() bool { return len(path) == 4 && path[0] == "connections" && path[2] == "children" }
	for n, raw := range strings.Split(conf, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasSuffix(line, "{"):
			path = append(path, strings.TrimSpace(strings.TrimSuffix(line, "{")))
			if len(path) == 2 && path[0] == "connections" {
				connections = append(connections, path[1])
			}
			if inChild() {
				children = append(children, swanctlChildBlock{connection: path[1], name: path[3], settings: map[string][]string{}})
			}
		case line == "}":
			if len(path) == 0 {
				t.Fatalf("line %d closes a section that was never opened\n%s", n+1, conf)
			}
			path = path[:len(path)-1]
		case line != "" && inChild():
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				t.Fatalf("line %d in child %s is not a key = value setting: %q\n%s", n+1, path[3], line, conf)
			}
			key = strings.TrimSpace(key)
			child := &children[len(children)-1]
			child.settings[key] = append(child.settings[key], strings.TrimSpace(value))
		}
	}
	if len(path) != 0 {
		t.Fatalf("sections %v are never closed\n%s", path, conf)
	}
	return connections, children
}

func TestSwanctlEveryChildLeavesRekeyingToTheClient(t *testing.T) {
	relays := `[{"local_addr":"10.0.0.2","id":"198.51.100.10"},{"local_addr":"2001:db8::2","id":"relay.example.com"}]`
	for _, c := range swanctlGoldenConfigs {
		for _, raw := range []string{c.raw, withRelayIdentities(c.raw, relays)} {
			o := swanctlTestInstance(t, raw)
			conf := o.swanctlConfig()
			connections, children := swanctlConnectionsAndChildren(t, conf)
			if want := 1 + len(o.config.RelayIdentities); len(connections) != want || len(children) != want {
				t.Fatalf("%s: %d connections and %d children, want %d of each\n%s", raw, len(connections), len(children), want, conf)
			}
			for i, child := range children {
				if child.connection != connections[i] || child.name != connections[i] {
					t.Fatalf("%s: child %s sits in connection %s, want child %s in connection %s\n%s", raw, child.name, child.connection, connections[i], connections[i], conf)
				}
				if got := child.settings["rekey_time"]; len(got) != 1 || got[0] != "0s" {
					t.Fatalf("%s: child %s has rekey_time %q, want exactly one rekey_time = 0s: a Quick Mode rekey that charon starts offers only the PFS proposals, which a client without PFS or with another DH group refuses\n%s", raw, child.name, got, conf)
				}
			}
			if got := strings.Count(conf, "rekey_time"); got != len(children) {
				t.Fatalf("%s: rekey_time appears %d times for %d children; it belongs in the children only, the IKE SA keeps its own timer\n%s", raw, got, len(children), conf)
			}
		}
	}
}

func TestSwanctlEveryChildEndsAnSAItsClientNeverRekeysAfterADay(t *testing.T) {
	relays := `[{"local_addr":"10.0.0.2","id":"198.51.100.10"},{"local_addr":"2001:db8::2","id":"relay.example.com"}]`
	for _, c := range swanctlGoldenConfigs {
		for _, raw := range []string{c.raw, withRelayIdentities(c.raw, relays)} {
			o := swanctlTestInstance(t, raw)
			conf := o.swanctlConfig()
			connections, children := swanctlConnectionsAndChildren(t, conf)
			if want := 1 + len(o.config.RelayIdentities); len(connections) != want || len(children) != want {
				t.Fatalf("%s: %d connections and %d children, want %d of each\n%s", raw, len(connections), len(children), want, conf)
			}
			for _, child := range children {
				if got := child.settings["rekey_time"]; len(got) != 1 || got[0] != "0s" {
					t.Fatalf("%s: child %s has rekey_time %q, want exactly one rekey_time = 0s\n%s", raw, child.name, got, conf)
				}
				if got := child.settings["life_time"]; len(got) != 1 || got[0] != "24h" {
					t.Fatalf("%s: child %s has life_time %q, want exactly one life_time = 24h, so that charon ends a CHILD_SA whose client never rekeys\n%s", raw, child.name, got, conf)
				}
			}
			for _, key := range []string{"rekey_time", "life_time"} {
				if got := strings.Count(conf, key); got != len(children) {
					t.Fatalf("%s: %s appears %d times for %d children; it belongs in the children only\n%s", raw, key, got, len(children), conf)
				}
			}
		}
	}
}
