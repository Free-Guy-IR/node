package openvpn

import (
	"strings"
	"testing"
)

func mustConfig(t *testing.T, raw string) *Config {
	t.Helper()
	cfg, err := NewConfig(raw)
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	return cfg
}

func TestRenderInstanceConfig_BasicDirectives(t *testing.T) {
	cfg := mustConfig(t, testConfigJSON(`{
		"tag": "udp-main",
		"protocol": "udp",
		"port": 1194,
		"network": "10.8.0.0/24",
		"cipher": "AES-256-GCM",
		"auth": "SHA256",
		"keepalive": "10 60",
		"max_clients": 500,
		"verb": 3
	}`))

	text, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 0, "/var/lib/pg-node/generated/openvpn/instance-0/management.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}

	for _, want := range []string{
		"proto udp\n",
		"port 1194\n",
		"dev tun0\n",
		"topology subnet\n",
		"server 10.8.0.0 255.255.255.0\n",
		"keepalive 10 60\n",
		"cipher AES-256-GCM\n",
		"auth SHA256\n",
		"max-clients 500\n",
		"management /var/lib/pg-node/generated/openvpn/instance-0/management.sock unix\n",
		"management-client-auth\n",
		"verify-client-cert none\n",
		"username-as-common-name\n",
		"verb 3\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected rendered config to contain %q; got:\n%s", want, text)
		}
	}

	if strings.Contains(strings.ToLower(text), "comp-lzo") || strings.Contains(strings.ToLower(text), "compress") {
		t.Error("rendered config must never contain a compression directive")
	}
}

func TestRenderInstanceConfig_TunIndexIsPerInstance(t *testing.T) {
	cfg := mustConfig(t, testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"}`))

	text, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 7, "/tmp/mgmt.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}
	if !strings.Contains(text, "dev tun7\n") {
		t.Errorf("expected dev tun7 in rendered config, got:\n%s", text)
	}
}

func TestRenderInstanceConfig_DuplicateCN(t *testing.T) {
	withDup := mustConfig(t, testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24", "duplicate_cn": true}`))
	text, err := renderInstanceConfig(withDup.Instances[0], withDup.PKI, 0, "/tmp/mgmt.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}
	if !strings.Contains(text, "duplicate-cn\n") {
		t.Error("expected duplicate-cn directive when DuplicateCN is set")
	}

	withoutDup := mustConfig(t, testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"}`))
	text2, err := renderInstanceConfig(withoutDup.Instances[0], withoutDup.PKI, 0, "/tmp/mgmt.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}
	if strings.Contains(text2, "duplicate-cn\n") {
		t.Error("did not expect duplicate-cn directive when DuplicateCN is unset")
	}
}

func TestRenderInstanceConfig_RedirectGatewayAndDNS(t *testing.T) {
	cfg := mustConfig(t, testConfigJSON(`{
		"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24",
		"redirect_gateway": true,
		"dns_servers": ["1.1.1.1", "8.8.8.8"]
	}`))

	text, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 0, "/tmp/mgmt.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}

	if !strings.Contains(text, `push "redirect-gateway def1 bypass-dhcp"`+"\n") {
		t.Error("expected redirect-gateway push directive")
	}
	if !strings.Contains(text, `push "dhcp-option DNS 1.1.1.1"`+"\n") {
		t.Error("expected DNS push directive for 1.1.1.1")
	}
	if !strings.Contains(text, `push "dhcp-option DNS 8.8.8.8"`+"\n") {
		t.Error("expected DNS push directive for 8.8.8.8")
	}
}

func TestRenderInstanceConfig_NoRedirectGatewayOrDNSWhenUnset(t *testing.T) {
	cfg := mustConfig(t, testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"}`))
	text, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 0, "/tmp/mgmt.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}
	if strings.Contains(text, "redirect-gateway") {
		t.Error("did not expect redirect-gateway directive when unset")
	}
	if strings.Contains(text, "dhcp-option DNS") {
		t.Error("did not expect any DNS push directive when unset")
	}
}

func TestRenderInstanceConfig_PKIBlocksInlined(t *testing.T) {
	cfg := mustConfig(t, testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24"}`))
	text, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 0, "/tmp/mgmt.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}

	for _, tag := range []string{"ca", "cert", "key", "tls-crypt"} {
		open := "<" + tag + ">"
		closeTag := "</" + tag + ">"
		if !strings.Contains(text, open) || !strings.Contains(text, closeTag) {
			t.Errorf("expected inline %s block in rendered config", tag)
		}
	}

	if !strings.Contains(text, "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----") {
		t.Error("expected CA cert PEM content to appear verbatim inside <ca> block")
	}
	if !strings.Contains(text, "-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----") {
		t.Error("expected server key PEM content to appear verbatim inside <key> block")
	}
}

func TestRenderInstanceConfig_TCPProtocol(t *testing.T) {
	cfg := mustConfig(t, testConfigJSON(`{"tag": "a", "protocol": "tcp", "port": 443, "network": "10.8.0.0/24"}`))
	text, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 0, "/tmp/mgmt.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}
	if !strings.Contains(text, "proto tcp\n") {
		t.Error("expected proto tcp in rendered config")
	}
}

func TestRenderInstanceConfig_NoCompressionDirectiveEver(t *testing.T) {
	cfg := mustConfig(t, testConfigJSON(`{"tag": "a", "protocol": "udp", "port": 1194, "network": "10.8.0.0/24", "redirect_gateway": true, "duplicate_cn": true, "dns_servers": ["1.1.1.1"]}`))
	text, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 3, "/tmp/mgmt.sock")
	if err != nil {
		t.Fatalf("renderInstanceConfig() error = %v", err)
	}
	for _, bad := range []string{"comp-lzo", "compress"} {
		if strings.Contains(text, bad) {
			t.Errorf("rendered config must never contain %q", bad)
		}
	}
}
