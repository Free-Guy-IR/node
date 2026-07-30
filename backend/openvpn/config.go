package openvpn

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// PKI holds the already-generated (by the panel) certificate/key material an
// OpenVPN instance's rendered config embeds inline (see render.go). This
// package never generates or touches key material itself - see the package
// doc comment in openvpn.go for why.
type PKI struct {
	CACert      string `json:"ca_cert"`
	ServerCert  string `json:"server_cert"`
	ServerKey   string `json:"server_key"`
	TLSCryptKey string `json:"tls_crypt_key"`
}

// InstanceConfig describes one OpenVPN server process ("instance"): one
// listening proto/port/tun-device combination. This is the OpenVPN analogue
// of a sing-box "inbound" or an xray inbound - user.go matches against
// InstanceConfig.Tag exactly the way singbox/xray match a user's Inbounds
// against an inbound tag.
type InstanceConfig struct {
	Tag             string   `json:"tag"`
	Protocol        string   `json:"protocol"`
	Port            int      `json:"port"`
	Network         string   `json:"network"`
	Cipher          string   `json:"cipher"`
	Auth            string   `json:"auth"`
	Keepalive       string   `json:"keepalive"`
	MaxClients      int      `json:"max_clients"`
	DNSServers      []string `json:"dns_servers"`
	RedirectGateway bool     `json:"redirect_gateway"`
	DuplicateCN     bool     `json:"duplicate_cn"`
	Verb            int      `json:"verb"`

	// network is the parsed/validated form of Network, cached once by
	// NewConfig so render.go never has to re-parse or re-validate it.
	network *net.IPNet
}

// Config is the parsed, validated form of the raw JSON the panel sends in
// Backend.config for an OPEN_VPN backend: N server instances plus the shared
// PKI material every instance's rendered config embeds inline (see
// render.go).
type Config struct {
	Instances []*InstanceConfig `json:"instances"`
	PKI       PKI               `json:"pki"`
}

const (
	defaultCipher    = "AES-256-GCM"
	defaultAuth      = "SHA256"
	defaultKeepalive = "10 60"
	defaultVerb      = 3
)

// NewConfig parses and validates raw OpenVPN backend config JSON (see the
// package doc comment in openvpn.go for the exact shape). Mirrors
// singbox.NewConfig's validation style: clear, specific error messages and
// fail fast on anything that would otherwise start a broken or ambiguous set
// of OpenVPN processes.
func NewConfig(raw string) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("parse openvpn config: %w", err)
	}

	if len(cfg.Instances) == 0 {
		return nil, fmt.Errorf("openvpn config: at least one instance is required")
	}

	if err := validatePKI(cfg.PKI); err != nil {
		return nil, err
	}

	tags := make(map[string]struct{}, len(cfg.Instances))
	ports := make(map[int]struct{}, len(cfg.Instances))

	for i, inst := range cfg.Instances {
		if inst == nil {
			return nil, fmt.Errorf("openvpn config: instances[%d] is null", i)
		}
		if err := validateInstance(inst, tags, ports); err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}

func validateInstance(inst *InstanceConfig, tags map[string]struct{}, ports map[int]struct{}) error {
	inst.Tag = strings.TrimSpace(inst.Tag)
	if inst.Tag == "" {
		return fmt.Errorf("openvpn config: an instance is missing a \"tag\"")
	}
	if _, dup := tags[inst.Tag]; dup {
		return fmt.Errorf("openvpn config: duplicate instance tag %q", inst.Tag)
	}
	tags[inst.Tag] = struct{}{}

	inst.Protocol = strings.ToLower(strings.TrimSpace(inst.Protocol))
	if inst.Protocol != "udp" && inst.Protocol != "tcp" {
		return fmt.Errorf("openvpn config: instance %q: \"protocol\" must be \"udp\" or \"tcp\", got %q", inst.Tag, inst.Protocol)
	}

	if inst.Port <= 0 || inst.Port > 65535 {
		return fmt.Errorf("openvpn config: instance %q: invalid \"port\" %d", inst.Tag, inst.Port)
	}
	if _, dup := ports[inst.Port]; dup {
		return fmt.Errorf("openvpn config: duplicate port %d (instance %q)", inst.Port, inst.Tag)
	}
	ports[inst.Port] = struct{}{}

	network := strings.TrimSpace(inst.Network)
	if network == "" {
		return fmt.Errorf("openvpn config: instance %q is missing a \"network\"", inst.Tag)
	}
	ip, ipNet, err := net.ParseCIDR(network)
	if err != nil {
		return fmt.Errorf("openvpn config: instance %q: invalid \"network\" %q: %w", inst.Tag, network, err)
	}
	if ip.To4() == nil {
		return fmt.Errorf("openvpn config: instance %q: \"network\" %q must be an IPv4 CIDR (IPv6 is not supported in this version)", inst.Tag, network)
	}
	inst.network = ipNet

	if inst.Cipher = strings.TrimSpace(inst.Cipher); inst.Cipher == "" {
		inst.Cipher = defaultCipher
	}
	if inst.Auth = strings.TrimSpace(inst.Auth); inst.Auth == "" {
		inst.Auth = defaultAuth
	}
	if inst.Keepalive = strings.TrimSpace(inst.Keepalive); inst.Keepalive == "" {
		inst.Keepalive = defaultKeepalive
	} else if err := validateKeepalive(inst.Keepalive); err != nil {
		return fmt.Errorf("openvpn config: instance %q: %w", inst.Tag, err)
	}
	if inst.MaxClients < 0 {
		return fmt.Errorf("openvpn config: instance %q: \"max_clients\" must not be negative", inst.Tag)
	}
	if inst.Verb <= 0 {
		inst.Verb = defaultVerb
	}

	for _, dns := range inst.DNSServers {
		if net.ParseIP(strings.TrimSpace(dns)) == nil {
			return fmt.Errorf("openvpn config: instance %q: invalid DNS server %q", inst.Tag, dns)
		}
	}

	return nil
}

func validateKeepalive(k string) error {
	parts := strings.Fields(k)
	if len(parts) != 2 {
		return fmt.Errorf("invalid \"keepalive\" %q: expected \"<interval> <timeout>\"", k)
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid \"keepalive\" %q: expected two positive integers", k)
		}
	}
	return nil
}

func validatePKI(pki PKI) error {
	missing := make([]string, 0, 4)
	if strings.TrimSpace(pki.CACert) == "" {
		missing = append(missing, "pki.ca_cert")
	}
	if strings.TrimSpace(pki.ServerCert) == "" {
		missing = append(missing, "pki.server_cert")
	}
	if strings.TrimSpace(pki.ServerKey) == "" {
		missing = append(missing, "pki.server_key")
	}
	if strings.TrimSpace(pki.TLSCryptKey) == "" {
		missing = append(missing, "pki.tls_crypt_key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("openvpn config: missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}
