package l2tp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var swanctlGoldenConfigs = []struct {
	golden string
	raw    string
}{
	{"swanctl-default.golden", `{"inbound_tag":"l2tp-de","server_addr":"203.0.113.10","pool":"10.31.0.0/24","psk":"correct-horse-battery"}`},
	{"swanctl-legacy.golden", `{"inbound_tag":"l2tp-legacy","pool":"10.32.0.0/24","psk":"correct-horse-battery","legacy_clients":true}`},
	{"swanctl-custom.golden", `{"inbound_tag":"l2tp-custom","pool":"10.33.0.0/24","psk":"correct-horse-battery","ike_proposals":["AES256-SHA256-MODP2048"," aes128-sha1-modp1024"],"esp_proposals":["aes256-sha256"]}`},
}

func swanctlTestInstance(t *testing.T, raw string) *L2TP {
	t.Helper()
	cfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig(%s): %v", raw, err)
	}
	return &L2TP{config: cfg}
}

func withRelayIdentities(raw, list string) string {
	return strings.TrimSuffix(raw, "}") + `,"relay_identities":` + list + "}"
}

func readSwanctlGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwanctlWithoutRelayIdentitiesIsByteIdenticalToThePreChangeOutput(t *testing.T) {
	for _, c := range swanctlGoldenConfigs {
		want := readSwanctlGolden(t, c.golden)
		for _, raw := range []string{c.raw, withRelayIdentities(c.raw, "[]"), withRelayIdentities(c.raw, "null")} {
			if got := swanctlTestInstance(t, raw).swanctlConfig(); got != want {
				t.Fatalf("%s from %s: generated swanctl differs from the pre-change output\n--- got ---\n%s\n--- want ---\n%s", c.golden, raw, got, want)
			}
		}
	}
}

const relayConnectionTemplate = `    NAME {
        version = 1
        local_addrs = ADDR
        proposals = IKE
        encap = yes
        fragmentation = yes
        dpd_delay = 30s
        local {
            auth = psk
            id = IDENTITY
        }
        remote {
            auth = psk
        }
        children {
            NAME {
                esp_proposals = ESP
                local_ts = dynamic[udp/1701]
                remote_ts = dynamic[udp]
                mode = transport
                dpd_action = clear
            }
        }
    }
`

func expectedRelayConnection(o *L2TP, index int, localAddr, id string) string {
	return strings.NewReplacer(
		"NAME", o.config.InboundTag+"-relay-"+strconv.Itoa(index),
		"ADDR", localAddr,
		"IDENTITY", id,
		"IKE", strings.Join(o.config.IKEProposals, ","),
		"ESP", strings.Join(o.config.ESPProposals, ","),
	).Replace(relayConnectionTemplate)
}

func goldenWithRelayConnections(t *testing.T, golden string, connections ...string) string {
	t.Helper()
	const end = "    }\n}\n\nsecrets {"
	if strings.Count(golden, end) != 1 {
		t.Fatalf("golden has %d connection-block endings, want 1", strings.Count(golden, end))
	}
	return strings.Replace(golden, end, "    }\n"+strings.Join(connections, "")+"}\n\nsecrets {", 1)
}

func TestSwanctlEmitsARelayConnectionThatPresentsTheRelayIdentity(t *testing.T) {
	o := swanctlTestInstance(t, `{"inbound_tag":"l2tp-de","server_addr":"203.0.113.10","pool":"10.31.0.0/24","psk":"correct-horse-battery","relay_identities":[{"local_addr":"10.0.0.2","id":"198.51.100.10"}]}`)
	relay := `    l2tp-de-relay-0 {
        version = 1
        local_addrs = 10.0.0.2
        proposals = aes256-sha256-modp2048,aes256-sha1-modp2048,aes256-sha1-modp1024,aes128-sha1-modp1024
        encap = yes
        fragmentation = yes
        dpd_delay = 30s
        local {
            auth = psk
            id = 198.51.100.10
        }
        remote {
            auth = psk
        }
        children {
            l2tp-de-relay-0 {
                esp_proposals = aes256-sha256,aes256-sha1,aes128-sha1
                local_ts = dynamic[udp/1701]
                remote_ts = dynamic[udp]
                mode = transport
                dpd_action = clear
            }
        }
    }
`
	want := goldenWithRelayConnections(t, readSwanctlGolden(t, "swanctl-default.golden"), relay)
	if got := o.swanctlConfig(); got != want {
		t.Fatalf("relay connection missing or wrong\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSwanctlEmitsOneRelayConnectionPerEntryInListOrder(t *testing.T) {
	o := swanctlTestInstance(t, withRelayIdentities(swanctlGoldenConfigs[0].raw,
		`[{"local_addr":"10.0.0.2","id":"198.51.100.10"},{"local_addr":"2001:db8::2","id":"relay.example.com"},{"local_addr":"10.0.0.3","id":"2001:db8::10"}]`))
	want := goldenWithRelayConnections(t, readSwanctlGolden(t, "swanctl-default.golden"),
		expectedRelayConnection(o, 0, "10.0.0.2", "198.51.100.10"),
		expectedRelayConnection(o, 1, "2001:db8::2", "relay.example.com"),
		expectedRelayConnection(o, 2, "10.0.0.3", "2001:db8::10"),
	)
	if got := o.swanctlConfig(); got != want {
		t.Fatalf("relay connections wrong\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSwanctlRelayConnectionsFollowTheMainConnectionProposalsAndKeepOneSecret(t *testing.T) {
	for _, c := range swanctlGoldenConfigs {
		o := swanctlTestInstance(t, withRelayIdentities(c.raw, `[{"local_addr":"10.0.0.2","id":"198.51.100.10"}]`))
		want := goldenWithRelayConnections(t, readSwanctlGolden(t, c.golden), expectedRelayConnection(o, 0, "10.0.0.2", "198.51.100.10"))
		got := o.swanctlConfig()
		if got != want {
			t.Fatalf("%s: relay connection does not mirror the main one\n--- got ---\n%s\n--- want ---\n%s", c.golden, got, want)
		}
		if strings.Count(got, "secrets {") != 1 || strings.Count(got, "secret = ") != 1 {
			t.Fatalf("%s: the secrets section must stay a single shared secret\n%s", c.golden, got)
		}
	}
}
