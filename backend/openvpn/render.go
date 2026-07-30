package openvpn

import (
	"fmt"
	"net"
	"strings"
)

// renderInstanceConfig renders a complete, self-contained OpenVPN server
// "--config" file for one instance: every setting needed to run is inlined
// (including PKI material - see the PKI doc comment in config.go), so the
// process can be started with nothing but this single file.
//
// Deliberately excluded: any compression directive (comp-lzo/compress).
// Compression on a VPN tunnel enables the VORACLE-style class of compression
// oracle attacks and is deprecated upstream; this backend never emits one and
// there is no config knob that turns it on.
func renderInstanceConfig(inst *InstanceConfig, pki PKI, tunIndex int, managementSocketPath string) (string, error) {
	if inst.network == nil {
		return "", fmt.Errorf("openvpn: instance %q has no validated network (NewConfig must be used to construct instances)", inst.Tag)
	}

	var b strings.Builder

	fmt.Fprintf(&b, "proto %s\n", inst.Protocol)
	fmt.Fprintf(&b, "port %d\n", inst.Port)
	fmt.Fprintf(&b, "dev tun%d\n", tunIndex)
	b.WriteString("topology subnet\n")

	mask := net.IP(inst.network.Mask)
	fmt.Fprintf(&b, "server %s %s\n", inst.network.IP.String(), mask.String())

	fmt.Fprintf(&b, "keepalive %s\n", inst.Keepalive)
	fmt.Fprintf(&b, "cipher %s\n", inst.Cipher)
	fmt.Fprintf(&b, "auth %s\n", inst.Auth)

	// "dh none": OpenVPN in server mode requires classic Diffie-Hellman
	// parameters (--dh <file>) unless told not to. This package deliberately
	// never generates or receives a dh.pem (PKI generation - including any DH
	// params - is the panel's job, not this package's, and the PKI shape it
	// sends has no field for one), so every rendered config opts out of
	// classic DH entirely and relies on the TLS library's own ECDHE key
	// exchange instead, which is the modern, recommended replacement (OpenVPN
	// 2.4+, TLS 1.2/1.3) and avoids needing a DH params file at all.
	b.WriteString("dh none\n")

	if inst.MaxClients > 0 {
		fmt.Fprintf(&b, "max-clients %d\n", inst.MaxClients)
	}

	fmt.Fprintf(&b, "management %s unix\n", managementSocketPath)
	b.WriteString("management-client-auth\n")
	b.WriteString("verify-client-cert none\n")
	b.WriteString("username-as-common-name\n")

	if inst.DuplicateCN {
		b.WriteString("duplicate-cn\n")
	}

	if inst.RedirectGateway {
		b.WriteString("push \"redirect-gateway def1 bypass-dhcp\"\n")
	}

	for _, dns := range inst.DNSServers {
		fmt.Fprintf(&b, "push \"dhcp-option DNS %s\"\n", strings.TrimSpace(dns))
	}

	writePEMBlock(&b, "ca", pki.CACert)
	writePEMBlock(&b, "cert", pki.ServerCert)
	writePEMBlock(&b, "key", pki.ServerKey)
	writePEMBlock(&b, "tls-crypt", pki.TLSCryptKey)

	fmt.Fprintf(&b, "verb %d\n", inst.Verb)

	return b.String(), nil
}

func writePEMBlock(b *strings.Builder, tag, pem string) {
	pem = strings.TrimSpace(pem)
	fmt.Fprintf(b, "<%s>\n%s\n</%s>\n", tag, pem, tag)
}
