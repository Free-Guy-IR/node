package openvpn

import (
	"strings"
	"testing"
)

const testPKI = `{
	"ca_cert": "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----",
	"server_cert": "-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----",
	"server_key": "-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----",
	"tls_crypt_key": "-----BEGIN OpenVPN Static key V1-----\nSTATIC\n-----END OpenVPN Static key V1-----"
}`

func testConfigJSON(instances string) string {
	return `{"instances": [` + instances + `], "pki": ` + testPKI + `}`
}

func TestNewConfig_ValidSingleInstance(t *testing.T) {
	raw := testConfigJSON(`{
		"tag": "udp-main",
		"protocol": "udp",
		"port": 1194,
		"network": "10.8.0.0/24"
	}`)

	cfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	if len(cfg.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(cfg.Instances))
	}

	inst := cfg.Instances[0]
	if inst.Cipher != defaultCipher {
		t.Errorf("expected default cipher %q, got %q", defaultCipher, inst.Cipher)
	}
	if inst.Auth != defaultAuth {
		t.Errorf("expected default auth %q, got %q", defaultAuth, inst.Auth)
	}
	if inst.Keepalive != defaultKeepalive {
		t.Errorf("expected default keepalive %q, got %q", defaultKeepalive, inst.Keepalive)
	}
	if inst.Verb != defaultVerb {
		t.Errorf("expected default verb %d, got %d", defaultVerb, inst.Verb)
	}
	if inst.network == nil {
		t.Fatal("expected parsed network to be cached on the instance")
	}
}

func TestNewConfig_MissingTag(t *testing.T) {
	raw := testConfigJSON(`{"protocol": "udp", "port": 1194, "network": "10.8.0.0/24"}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for an instance missing a tag")
	}
}

func TestNewConfig_DuplicateTags(t *testing.T) {
	raw := testConfigJSON(`
		{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"},
		{"tag": "a", "protocol": "tcp", "port": 1195, "network": "10.9.0.0/24"}
	`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for duplicate instance tags")
	}
}

func TestNewConfig_DuplicatePorts(t *testing.T) {
	raw := testConfigJSON(`
		{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"},
		{"tag": "b", "protocol": "tcp", "port": 1194, "network": "10.9.0.0/24"}
	`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for duplicate ports")
	}
}

func TestNewConfig_InvalidProtocol(t *testing.T) {
	raw := testConfigJSON(`{"tag": "a", "protocol": "sctp", "port": 1194, "network": "10.8.0.0/24"}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for an invalid protocol")
	}
}

func TestNewConfig_InvalidPort(t *testing.T) {
	raw := testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 70000, "network": "10.8.0.0/24"}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for an out-of-range port")
	}
}

func TestNewConfig_InvalidNetwork(t *testing.T) {
	raw := testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "not-a-cidr"}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for an invalid network")
	}
}

func TestNewConfig_IPv6NetworkRejected(t *testing.T) {
	raw := testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "fd00::/64"}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for an IPv6 network in this version")
	}
}

func TestNewConfig_InvalidDNSServer(t *testing.T) {
	raw := testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24", "dns_servers": ["not-an-ip"]}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for an invalid DNS server IP")
	}
}

func TestNewConfig_InvalidKeepalive(t *testing.T) {
	raw := testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24", "keepalive": "not valid"}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for an invalid keepalive")
	}
}

func TestNewConfig_NoInstances(t *testing.T) {
	raw := `{"instances": [], "pki": ` + testPKI + `}`
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for zero instances")
	}
}

func TestNewConfig_MissingPKIFields(t *testing.T) {
	tests := []struct {
		name string
		pki  string
	}{
		{"missing ca_cert", `{"server_cert": "x", "server_key": "x", "tls_crypt_key": "x"}`},
		{"missing server_cert", `{"ca_cert": "x", "server_key": "x", "tls_crypt_key": "x"}`},
		{"missing server_key", `{"ca_cert": "x", "server_cert": "x", "tls_crypt_key": "x"}`},
		{"missing tls_crypt_key", `{"ca_cert": "x", "server_cert": "x", "server_key": "x"}`},
		{"all empty", `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"instances": [{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"}], "pki": ` + tt.pki + `}`
			if _, err := NewConfig(raw); err == nil {
				t.Fatalf("expected an error for %s", tt.name)
			}
		})
	}
}

func TestNewConfig_MultipleValidInstances(t *testing.T) {
	raw := testConfigJSON(`
		{"tag": "udp-main", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"},
		{"tag": "tcp-fallback", "protocol": "TCP", "port": 443, "network": "10.9.0.0/24"}
	`)

	cfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	if len(cfg.Instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(cfg.Instances))
	}
	if cfg.Instances[1].Protocol != "tcp" {
		t.Errorf("expected protocol to be normalized to lowercase, got %q", cfg.Instances[1].Protocol)
	}
}

func TestNewConfig_MalformedJSON(t *testing.T) {
	if _, err := NewConfig(`{not json`); err == nil {
		t.Fatal("expected an error for malformed JSON")
	} else if !strings.Contains(err.Error(), "parse openvpn config") {
		t.Errorf("expected a parse error, got: %v", err)
	}
}

func TestNewConfig_NegativeMaxClients(t *testing.T) {
	raw := testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24", "max_clients": -1}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for a negative max_clients")
	}
}

func TestNewConfig_WhitespaceTagRejected(t *testing.T) {
	raw := testConfigJSON(`{"tag": "   ", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"}`)
	if _, err := NewConfig(raw); err == nil {
		t.Fatal("expected an error for a whitespace-only tag")
	}
}
