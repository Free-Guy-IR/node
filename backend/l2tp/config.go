package l2tp

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
)

var (
	inboundTagRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	proposalRe   = regexp.MustCompile(`^[a-z0-9-]+$`)
	ifaceRe      = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,15}$`)
	relayIDRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

const (
	pskMinLength = 8
	pskMaxLength = 128
)

const (
	maxRelayIdentities = 16
	maxRelayIDLength   = 253
)

var (
	defaultDNS          = []string{"1.1.1.1", "8.8.8.8"}
	defaultIKEProposals = []string{
		"aes256-sha256-modp2048",
		"aes256-sha1-modp2048",
		"aes256-sha1-modp1024",
		"aes128-sha1-modp1024",
	}
	defaultESPProposals = []string{
		"aes256-sha256",
		"aes256-sha1",
		"aes128-sha1",
		"aes256-sha256-modp2048",
		"aes256-sha1-modp2048",
		"aes128-sha1-modp2048",
		"aes256-sha1-modp1536",
		"aes128-sha1-modp1536",
		"aes256-sha1-modp1024",
		"aes128-sha1-modp1024",
	}
	legacyIKEProposals = []string{"3des-sha1-modp1024"}
	legacyESPProposals = []string{"3des-sha1", "3des-sha1-modp1024"}
)

type RelayIdentity struct {
	LocalAddr string `json:"local_addr"`
	ID        string `json:"id"`
}

type Config struct {
	InboundTag      string   `json:"inbound_tag"`
	ServerAddr      string   `json:"server_addr"`
	PSK             string   `json:"psk"`
	Pool            string   `json:"pool"`
	LocalIP         string   `json:"local_ip"`
	EgressInterface string   `json:"egress_interface"`
	DNS             []string `json:"dns"`
	IKEProposals    []string `json:"ike_proposals"`
	ESPProposals    []string `json:"esp_proposals"`
	LegacyClients   bool     `json:"legacy_clients"`

	RelayIdentities []RelayIdentity `json:"relay_identities"`

	workDir string
	poolNet *net.IPNet
}

func NewConfig(configStr string) (*Config, error) {
	if strings.TrimSpace(configStr) == "" {
		return nil, fmt.Errorf("l2tp config string must not be empty")
	}
	cfg := &Config{}
	if err := json.Unmarshal([]byte(configStr), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse l2tp config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("l2tp config: %w", err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	c.InboundTag = strings.TrimSpace(c.InboundTag)
	if !inboundTagRe.MatchString(c.InboundTag) {
		return fmt.Errorf("inbound_tag %q must start with a letter or digit and contain only letters, digits, '_', '.', '-' (max 64)", c.InboundTag)
	}

	if err := validatePSK(c.PSK); err != nil {
		return err
	}

	_, poolNet, err := net.ParseCIDR(strings.TrimSpace(c.Pool))
	if err != nil {
		return fmt.Errorf("pool %q is not a valid CIDR: %w", c.Pool, err)
	}
	if poolNet.IP.To4() == nil {
		return fmt.Errorf("pool %q must be an IPv4 network", c.Pool)
	}
	if ones, _ := poolNet.Mask.Size(); ones < 8 || ones > 29 {
		return fmt.Errorf("pool %q prefix must be between /8 and /29", c.Pool)
	}
	c.Pool = poolNet.String()
	c.poolNet = poolNet

	c.LocalIP = strings.TrimSpace(c.LocalIP)
	if c.LocalIP == "" {
		c.LocalIP = firstHost(poolNet)
	}
	localIP := net.ParseIP(c.LocalIP)
	if localIP == nil || localIP.To4() == nil || !poolNet.Contains(localIP) {
		return fmt.Errorf("local_ip %q must be an IPv4 address inside the pool %s", c.LocalIP, c.Pool)
	}

	c.EgressInterface = strings.TrimSpace(c.EgressInterface)
	if c.EgressInterface != "" && !ifaceRe.MatchString(c.EgressInterface) {
		return fmt.Errorf("egress_interface %q is not a valid interface name", c.EgressInterface)
	}

	if len(c.DNS) == 0 {
		c.DNS = append([]string(nil), defaultDNS...)
	}
	for i, d := range c.DNS {
		d = strings.TrimSpace(d)
		ip := net.ParseIP(d)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("dns entry %q must be an IPv4 address", d)
		}
		c.DNS[i] = d
	}

	if len(c.IKEProposals) == 0 {
		c.IKEProposals = append([]string(nil), defaultIKEProposals...)
		if c.LegacyClients {
			c.IKEProposals = append(c.IKEProposals, legacyIKEProposals...)
		}
	}
	if len(c.ESPProposals) == 0 {
		c.ESPProposals = append([]string(nil), defaultESPProposals...)
		if c.LegacyClients {
			c.ESPProposals = append(c.ESPProposals, legacyESPProposals...)
		}
	}
	for _, list := range [][]string{c.IKEProposals, c.ESPProposals} {
		for i, p := range list {
			p = strings.ToLower(strings.TrimSpace(p))
			if !proposalRe.MatchString(p) {
				return fmt.Errorf("proposal %q contains characters strongSwan does not accept", p)
			}
			list[i] = p
		}
	}

	c.ServerAddr = strings.TrimSpace(c.ServerAddr)

	return validateRelayIdentities(c.RelayIdentities, net.ParseIP(c.ServerAddr))
}

func validateRelayIdentities(relays []RelayIdentity, serverIP net.IP) error {
	if len(relays) > maxRelayIdentities {
		return fmt.Errorf("relay_identities has %d entries, at most %d are allowed", len(relays), maxRelayIdentities)
	}
	seen := make(map[string]int, len(relays))
	for i := range relays {
		r := &relays[i]
		addr := unicastIP(r.LocalAddr)
		if addr == nil {
			return fmt.Errorf("relay_identities[%d].local_addr %q must be a unicast IPv4 or IPv6 address", i, r.LocalAddr)
		}
		r.LocalAddr = addr.String()
		if serverIP != nil && addr.Equal(serverIP) {
			return fmt.Errorf("relay_identities[%d].local_addr %s is the server_addr; direct clients would be answered with the relay identity", i, r.LocalAddr)
		}
		if first, dup := seen[r.LocalAddr]; dup {
			return fmt.Errorf("relay_identities[%d].local_addr %s is already used by relay_identities[%d]", i, r.LocalAddr, first)
		}
		seen[r.LocalAddr] = i
		id, ok := canonicalRelayID(r.ID)
		if !ok {
			return fmt.Errorf("relay_identities[%d].id %q must be a unicast IPv4/IPv6 address or a host name of letters, digits, '-' and '.' whose last label is not all digits (max %d characters)", i, r.ID, maxRelayIDLength)
		}
		r.ID = id
	}
	return nil
}

func unicastIP(s string) net.IP {
	ip := net.ParseIP(s)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return nil
	}
	return ip
}

func canonicalRelayID(id string) (string, bool) {
	if net.ParseIP(id) != nil {
		ip := unicastIP(id)
		if ip == nil {
			return "", false
		}
		return ip.String(), true
	}
	if len(id) > maxRelayIDLength || !relayIDRe.MatchString(id) {
		return "", false
	}
	if lastLabel := id[strings.LastIndexByte(id, '.')+1:]; strings.Trim(lastLabel, "0123456789") == "" {
		return "", false
	}
	return id, true
}

func validatePSK(psk string) error {
	if len(psk) < pskMinLength || len(psk) > pskMaxLength {
		return fmt.Errorf("psk must be between %d and %d characters", pskMinLength, pskMaxLength)
	}
	for _, r := range psk {
		if r <= ' ' || r > '~' {
			return fmt.Errorf("psk must contain only printable ASCII characters without spaces")
		}
		switch r {
		case '"', '\\', '#', '{', '}':
			return fmt.Errorf("psk must not contain %q", r)
		}
	}
	return nil
}

func firstHost(n *net.IPNet) string {
	ip := dup4(n.IP.Mask(n.Mask))
	inc(ip)
	return ip.String()
}
