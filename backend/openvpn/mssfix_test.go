package openvpn

import "testing"

func TestMssfixDefaultsToARelaySafeValue(t *testing.T) {
	inst := &InstanceConfig{}
	if got := inst.EffectiveMssfix(); got != defaultMssfix {
		t.Fatalf("unset mssfix should fall back to %d, got %d", defaultMssfix, got)
	}
}

func TestMssfixHonoursAnExplicitValue(t *testing.T) {
	inst := &InstanceConfig{Mssfix: 1200}
	if got := inst.EffectiveMssfix(); got != 1200 {
		t.Fatalf("explicit mssfix ignored, got %d", got)
	}
}

func TestRenderedConfigCarriesMssfix(t *testing.T) {
	cfg, err := NewConfig((`{
		"pki": {"ca_cert": "ca", "server_cert": "sc", "server_key": "sk", "tls_crypt_key": "tc"},
		"instances": [{"tag": "t", "protocol": "udp", "port": 1194, "network": "10.90.0.0/24",
		               "cipher": "AES-256-GCM", "auth": "SHA256", "keepalive": "10 60"}]
	}`))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	out, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 0, "/tmp/x.sock")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !contains(out, "mssfix 1300") {
		t.Fatalf("rendered config has no mssfix line:\n%s", out)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestUdpInstanceGetsTunMtuAndFragment(t *testing.T) {
	cfg, err := NewConfig(`{
		"pki": {"ca_cert": "ca", "server_cert": "sc", "server_key": "sk", "tls_crypt_key": "tc"},
		"instances": [{"tag": "t", "protocol": "udp", "port": 1194, "network": "10.90.0.0/24",
		               "cipher": "AES-256-GCM", "auth": "SHA256", "keepalive": "10 60",
		               "tun_mtu": 1300, "fragment": 1300}]
	}`)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	out, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 0, "/tmp/x.sock")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !contains(out, "tun-mtu 1300") {
		t.Fatalf("tun-mtu missing:\n%s", out)
	}
	if !contains(out, "fragment 1300") {
		t.Fatalf("fragment missing on a udp instance:\n%s", out)
	}
}

func TestTcpInstanceNeverGetsFragment(t *testing.T) {
	cfg, err := NewConfig(`{
		"pki": {"ca_cert": "ca", "server_cert": "sc", "server_key": "sk", "tls_crypt_key": "tc"},
		"instances": [{"tag": "t", "protocol": "tcp", "port": 1195, "network": "10.91.0.0/24",
		               "cipher": "AES-256-GCM", "auth": "SHA256", "keepalive": "10 60",
		               "tun_mtu": 1300, "fragment": 1300}]
	}`)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	out, err := renderInstanceConfig(cfg.Instances[0], cfg.PKI, 0, "/tmp/x.sock")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if contains(out, "fragment ") {
		t.Fatalf("openvpn rejects fragment on tcp, it must not be emitted:\n%s", out)
	}
	if !contains(out, "tun-mtu 1300") {
		t.Fatalf("tun-mtu should still apply on tcp:\n%s", out)
	}
}

func TestDefaultsLeaveMtuAlone(t *testing.T) {
	inst := &InstanceConfig{}
	if inst.EffectiveTunMtu() != 0 || inst.EffectiveFragment() != 0 {
		t.Fatal("an unconfigured instance must not force an MTU")
	}
}
