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
	}
	for name, c := range cases {
		cfg, err := NewConfig(withRelayIdentities(relayBaseConfig, c.list))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
			continue
		}
		got := (&L2TP{config: cfg}).swanctlConfig()
		for _, line := range append([]string{"l2tp-de-relay-0 {"}, c.want...) {
			if !strings.Contains(got, line) {
				t.Errorf("%s: generated swanctl lacks %q\n%s", name, line, got)
			}
		}
	}
}
