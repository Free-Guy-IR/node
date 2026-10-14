package l2tp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const relayBaseConfig = `{"inbound_tag":"l2tp-de","pool":"10.31.0.0/24","psk":"correct-horse-battery"}`

func relayEntries(n int) string {
	entries := make([]string, n)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"local_addr":"10.0.0.%d","id":"198.51.100.%d"}`, i+1, i+1)
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func relayEntry(localAddr, id string) string {
	b, err := json.Marshal([]map[string]string{{"local_addr": localAddr, "id": id}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func longRelayID(length int) string {
	labels := []string{strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63)}
	return strings.Join(labels, ".") + "." + strings.Repeat("d", length-3*63-3)
}

func TestLongRelayIDHelperBuildsTheRequestedLength(t *testing.T) {
	for _, n := range []int{253, 254} {
		if got := len(longRelayID(n)); got != n {
			t.Fatalf("longRelayID(%d) has %d characters", n, got)
		}
	}
}

func TestNewConfigRejectsInvalidRelayIdentities(t *testing.T) {
	cases := map[string]string{
		"local_addr missing":               `[{"id":"198.51.100.10"}]`,
		"local_addr empty":                 relayEntry("", "198.51.100.10"),
		"local_addr not an address":        relayEntry("relay-host", "198.51.100.10"),
		"local_addr out of range":          relayEntry("10.0.0.256", "198.51.100.10"),
		"local_addr is a network":          relayEntry("10.0.0.0/24", "198.51.100.10"),
		"local_addr with a zone":           relayEntry("fe80::1%eth0", "198.51.100.10"),
		"local_addr with whitespace":       relayEntry(" 10.0.0.2", "198.51.100.10"),
		"local_addr unspecified ipv4":      relayEntry("0.0.0.0", "198.51.100.10"),
		"local_addr unspecified ipv6":      relayEntry("::", "198.51.100.10"),
		"local_addr multicast":             relayEntry("ff02::1", "198.51.100.10"),
		"local_addr broadcast":             relayEntry("255.255.255.255", "198.51.100.10"),
		"local_addr duplicated":            `[{"local_addr":"10.0.0.2","id":"198.51.100.10"},{"local_addr":"10.0.0.2","id":"198.51.100.11"}]`,
		"local_addr duplicated other form": `[{"local_addr":"2001:db8::2","id":"198.51.100.10"},{"local_addr":"2001:DB8:0::2","id":"198.51.100.11"}]`,
		"local_addr duplicate mapped form": `[{"local_addr":"10.0.0.2","id":"198.51.100.10"},{"local_addr":"::ffff:10.0.0.2","id":"198.51.100.11"}]`,
		"id missing":                       `[{"local_addr":"10.0.0.2"}]`,
		"id empty":                         relayEntry("10.0.0.2", ""),
		"id with a space":                  relayEntry("10.0.0.2", "relay example.com"),
		"id with a leading space":          relayEntry("10.0.0.2", " relay.example.com"),
		"id with a tab":                    relayEntry("10.0.0.2", "relay\texample.com"),
		"id with a newline":                relayEntry("10.0.0.2", "relay.example.com\n        }"),
		"id with a double quote":           relayEntry("10.0.0.2", `relay"example.com`),
		"id with a single quote":           relayEntry("10.0.0.2", "relay'example.com"),
		"id with an opening brace":         relayEntry("10.0.0.2", "relay{example.com"),
		"id with a closing brace":          relayEntry("10.0.0.2", "relay}example.com"),
		"id with a hash":                   relayEntry("10.0.0.2", "relay#example.com"),
		"id with an underscore":            relayEntry("10.0.0.2", "relay_x.example.com"),
		"id with an equals sign":           relayEntry("10.0.0.2", "relay=example.com"),
		"id with an empty label":           relayEntry("10.0.0.2", "relay..example.com"),
		"id with a trailing dot":           relayEntry("10.0.0.2", "relay.example.com."),
		"id with a leading hyphen":         relayEntry("10.0.0.2", "-relay.example.com"),
		"id too long":                      relayEntry("10.0.0.2", longRelayID(254)),
		"id unspecified ipv4":              relayEntry("10.0.0.2", "0.0.0.0"),
		"id unspecified ipv6":              relayEntry("10.0.0.2", "::"),
		"id unspecified ipv6 zero form":    relayEntry("10.0.0.2", "0::0"),
		"id unspecified ipv6 full form":    relayEntry("10.0.0.2", "0:0:0:0:0:0:0:0"),
		"id unspecified mapped ipv4":       relayEntry("10.0.0.2", "::ffff:0.0.0.0"),
		"id multicast ipv4":                relayEntry("10.0.0.2", "224.0.0.1"),
		"id multicast ipv6":                relayEntry("10.0.0.2", "ff02::1"),
		"id broadcast":                     relayEntry("10.0.0.2", "255.255.255.255"),
		"id ipv4 octet out of range":       relayEntry("10.0.0.2", "198.51.100.300"),
		"id ipv4 octet with leading zero":  relayEntry("10.0.0.2", "198.51.100.010"),
		"id three numeric labels":          relayEntry("10.0.0.2", "1.2.3"),
		"id five numeric labels":           relayEntry("10.0.0.2", "1.2.3.4.5"),
		"id single numeric label":          relayEntry("10.0.0.2", "999"),
		"id ipv4 range":                    relayEntry("10.0.0.2", "10.0.0.1-10.0.0.5"),
		"id with a numeric last label":     relayEntry("10.0.0.2", "relay.example.123"),
		"17 entries":                       relayEntries(17),
		"not a list":                       `{"local_addr":"10.0.0.2","id":"198.51.100.10"}`,
	}
	for name, list := range cases {
		raw := withRelayIdentities(relayBaseConfig, list)
		cfg, err := NewConfig(raw)
		if err == nil {
			t.Errorf("%s: expected rejection for %s", name, raw)
			continue
		}
		if cfg != nil {
			t.Errorf("%s: a rejected config must not be returned", name)
		}
		if !strings.Contains(err.Error(), "relay_identities") {
			t.Errorf("%s: error must name relay_identities: %v", name, err)
		}
	}
}

func TestNewConfigAcceptsValidRelayIdentities(t *testing.T) {
	cases := map[string]struct {
		list string
		want []string
	}{
		"ipv4 address and ipv4 id": {
			relayEntry("10.0.0.2", "198.51.100.10"),
			[]string{"local_addrs = 10.0.0.2", "id = 198.51.100.10"},
		},
		"ipv6 local_addr": {
			relayEntry("2001:db8::2", "198.51.100.10"),
			[]string{"local_addrs = 2001:db8::2", "id = 198.51.100.10"},
		},
		"fqdn id": {
			relayEntry("10.0.0.2", "relay.example.com"),
			[]string{"local_addrs = 10.0.0.2", "id = relay.example.com"},
		},
		"single label id": {
			relayEntry("10.0.0.2", "relay-1"),
			[]string{"id = relay-1"},
		},
		"ipv6 id": {
			relayEntry("10.0.0.2", "2001:db8::10"),
			[]string{"id = 2001:db8::10"},
		},
		"id of 253 characters": {
			relayEntry("10.0.0.2", longRelayID(253)),
			[]string{"id = " + longRelayID(253)},
		},
		"16 entries": {
			relayEntries(16),
			[]string{"l2tp-de-relay-15 {", "local_addrs = 10.0.0.16", "id = 198.51.100.16"},
		},
		"ipv4-mapped local_addr is emitted as ipv4": {
			relayEntry("::ffff:10.0.0.2", "198.51.100.10"),
			[]string{"local_addrs = 10.0.0.2"},
		},
		"non-canonical ipv6 local_addr is emitted canonical": {
			relayEntry("2001:DB8:0::2", "198.51.100.10"),
			[]string{"local_addrs = 2001:db8::2"},
		},
		"ipv4-mapped id is emitted as ipv4": {
			relayEntry("10.0.0.2", "::ffff:198.51.100.10"),
			[]string{"id = 198.51.100.10"},
		},
		"non-canonical ipv6 id is emitted canonical": {
			relayEntry("10.0.0.2", "2001:DB8:0::10"),
			[]string{"id = 2001:db8::10"},
		},
		"numeric first label id": {
			relayEntry("10.0.0.2", "1.example.com"),
			[]string{"id = 1.example.com"},
		},
		"address-like labels with a non-numeric last label": {
			relayEntry("10.0.0.2", "10-0-0-2.relay1"),
			[]string{"id = 10-0-0-2.relay1"},
		},
	}
	for name, c := range cases {
		cfg, err := NewConfig(withRelayIdentities(relayBaseConfig, c.list))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
			continue
		}
		got := (&L2TP{config: cfg}).swanctlConfig()
		lines := make(map[string]bool)
		for _, l := range strings.Split(got, "\n") {
			lines[strings.TrimSpace(l)] = true
		}
		for _, line := range append([]string{"l2tp-de-relay-0 {"}, c.want...) {
			if !lines[line] {
				t.Errorf("%s: generated swanctl has no line %q\n%s", name, line, got)
			}
		}
	}
}

func TestNewConfigRejectsARelayLocalAddrEqualToTheServerAddr(t *testing.T) {
	cases := map[string]struct{ serverAddr, localAddr string }{
		"same ipv4":                        {"203.0.113.10", "203.0.113.10"},
		"ipv4-mapped form of server_addr":  {"203.0.113.10", "::ffff:203.0.113.10"},
		"server_addr with whitespace":      {" 203.0.113.10 ", "203.0.113.10"},
		"same ipv6 in another form":        {"2001:db8::1", "2001:DB8:0::1"},
		"ipv4-mapped server_addr and ipv4": {"::ffff:203.0.113.10", "203.0.113.10"},
	}
	for name, c := range cases {
		raw := fmt.Sprintf(`{"inbound_tag":"l2tp-de","server_addr":%q,"pool":"10.31.0.0/24","psk":"correct-horse-battery","relay_identities":%s}`,
			c.serverAddr, relayEntry(c.localAddr, "198.51.100.10"))
		cfg, err := NewConfig(raw)
		if err == nil {
			t.Errorf("%s: expected rejection for %s", name, raw)
			continue
		}
		if cfg != nil {
			t.Errorf("%s: a rejected config must not be returned", name)
		}
		if !strings.Contains(err.Error(), "relay_identities[0].local_addr") || !strings.Contains(err.Error(), "server_addr") {
			t.Errorf("%s: error must name relay_identities[0].local_addr and server_addr: %v", name, err)
		}
	}
}

func TestNewConfigAcceptsARelayLocalAddrThatDiffersFromTheServerAddr(t *testing.T) {
	for _, serverAddr := range []string{"", "203.0.113.10", "2001:db8::1", "vpn.example.com", "10.0.0.2.example.com"} {
		raw := fmt.Sprintf(`{"inbound_tag":"l2tp-de","server_addr":%q,"pool":"10.31.0.0/24","psk":"correct-horse-battery","relay_identities":%s}`,
			serverAddr, `[{"local_addr":"10.0.0.2","id":"198.51.100.10"},{"local_addr":"2001:db8::2","id":"relay.example.com"}]`)
		if _, err := NewConfig(raw); err != nil {
			t.Errorf("server_addr %q: unexpected error: %v", serverAddr, err)
		}
	}
}
