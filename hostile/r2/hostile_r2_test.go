package freedom

import (
	"context"
	gonet "net"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing/common/uot"
	"github.com/xtls/xray-core/app/proxyman"
	proxyoutbound "github.com/xtls/xray-core/app/proxyman/outbound"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/pipe"
	"golang.org/x/net/dns/dnsmessage"
)

func hostileFreedom(t *testing.T, tag string, config *Config, sender *proxyman.SenderConfig) (context.Context, outbound.Handler, *Handler) {
	t.Helper()
	instance, err := core.New(&core.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.AddFeature((outbound.Manager)(new(proxyoutbound.Manager))); err != nil {
		t.Fatal(err)
	}
	if sender == nil {
		sender = &proxyman.SenderConfig{}
	}
	ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
	handler, err := proxyoutbound.NewHandler(ctx, &core.OutboundHandlerConfig{Tag: tag, SenderSettings: serial.ToTypedMessage(sender), ProxySettings: serial.ToTypedMessage(config)})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, handler, handler.(*proxyoutbound.Handler).GetOutbound().(*Handler)
}

func tcpOnlyCIDRRule(ip gonet.IP) *PrivateTargetRule {
	rule := cidrRule(false, hostCIDR(ip))
	rule.Networks = []net.Network{net.Network_TCP}
	return rule
}

func TestHostileR2UoTDatagramsAreJudgedAsTCPByFinalRules(t *testing.T) {
	serveScriptedDNS(t, &scriptedDNS{decide: nowhere})
	inbound := &session.Inbound{Name: "vless", Tag: "users"}
	target, addr := listenUDPOnPrivateHost(t)
	ctx, handler, _ := hostileFreedom(t, "DIRECT", &Config{FinalRules: []*PrivateTargetRule{tcpOnlyCIDRRule(addr.IP)}}, nil)

	dispatchThrough(ctx, handler, inbound, net.UDPDestination(net.IPAddress(addr.IP), net.Port(addr.Port)), []byte("datagram"))
	plainUDP := receivedDatagram(target, 700*time.Millisecond)

	dispatchThrough(ctx, handler, inbound, net.TCPDestination(net.DomainAddress(uot.MagicAddress), 0), uotConnectFrame(t, addr, []byte("datagram")))
	overUoT := receivedDatagram(target, time.Second)

	t.Logf("finalRules allow %s for network tcp only: plain UDP delivered=%v, UDP-over-TCP delivered=%v", addr, plainUDP, overUoT)
	if plainUDP {
		t.Fatalf("plain UDP to %s was delivered although the only allow is for tcp", addr)
	}
	if overUoT {
		t.Errorf("a UDP-over-TCP datagram reached %s, which a tcp-only finalRules allow covers and plain UDP to the same address does not", addr)
	}
}

func TestHostileR2UoTDatagramsAreJudgedAsTCPByTheRoutersFirstMatch(t *testing.T) {
	serveScriptedDNS(t, &scriptedDNS{decide: nowhere})
	inbound := &session.Inbound{Name: "vless", Tag: "users"}
	target, addr := listenUDPOnPrivateHost(t)
	tcpRoute := routingRule("lan-tcp", "DIRECT", nil, hostCIDR(addr.IP))
	tcpRoute.Networks = []net.Network{net.Network_TCP}
	r := routerOf(t, tcpRoute)
	ctx, handler, freedom := hostileFreedom(t, "DIRECT", &Config{PrivateAllow: []*PrivateTargetRule{allowEntry("lan-tcp", hostCIDR(addr.IP))}}, nil)
	freedom.router = r

	dispatchThrough(ctx, handler, inbound, net.UDPDestination(net.IPAddress(addr.IP), net.Port(addr.Port)), []byte("datagram"))
	plainUDP := receivedDatagram(target, 700*time.Millisecond)

	dispatchThrough(ctx, handler, inbound, net.TCPDestination(net.DomainAddress(uot.MagicAddress), 0), uotConnectFrame(t, addr, []byte("datagram")))
	overUoT := receivedDatagram(target, time.Second)

	t.Logf("routing rule lan-tcp sends %s to DIRECT for network tcp only: plain UDP delivered=%v, UDP-over-TCP delivered=%v", addr, plainUDP, overUoT)
	if plainUDP {
		t.Fatalf("plain UDP to %s was delivered although the routing rule only covers tcp", addr)
	}
	if overUoT {
		t.Errorf("a UDP-over-TCP datagram reached %s through a privateAllow whose routing rule only covers tcp", addr)
	}
}

func serveMappedAAAA(t *testing.T, name string, mapped gonet.IP) {
	t.Helper()
	conn, err := gonet.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fqdn := strings.ToLower(name) + "."
	go func() {
		packet := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFrom(packet)
			if err != nil {
				return
			}
			var parser dnsmessage.Parser
			header, err := parser.Start(packet[:n])
			if err != nil {
				continue
			}
			question, err := parser.Question()
			if err != nil {
				continue
			}
			rcode := dnsmessage.RCodeNameError
			if strings.ToLower(question.Name.String()) == fqdn {
				rcode = dnsmessage.RCodeSuccess
			}
			builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true, RCode: rcode})
			builder.StartQuestions()
			builder.Question(question)
			builder.StartAnswers()
			if rcode == dnsmessage.RCodeSuccess && question.Type == dnsmessage.TypeAAAA {
				builder.AAAAResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET}, dnsmessage.AAAAResource{AAAA: [16]byte(mapped.To16())})
			}
			reply, err := builder.Finish()
			if err != nil {
				continue
			}
			conn.WriteTo(reply, from)
		}
	}()
	useStubSystemResolver(t, conn.LocalAddr().String())
}

func TestHostileR2AnIPv4MappedAAAAAnswerIsJudgedAsIPv4InControl(t *testing.T) {
	listener := listenOnPrivateHost(t)
	serveMappedAAAA(t, "mapped-r2.pgtest", listener.ip.To16())
	if addrs, err := gonet.DefaultResolver.LookupIPAddr(context.Background(), "mapped-r2.pgtest"); err != nil || len(addrs) == 0 {
		t.Fatalf("the stub resolver gives %v %v", addrs, err)
	} else {
		t.Logf("the stub resolver gives %v", addrs)
	}
	ctx, handler, _ := hostileFreedom(t, "DIRECT", &Config{}, nil)
	dispatchThrough(ctx, handler, &session.Inbound{Name: "vless", Tag: "users"}, net.TCPDestination(net.DomainAddress("mapped-r2.pgtest"), listener.port), []byte("payload"))
	requireUntouched(t, "a name whose only answer is the IPv4-mapped AAAA of a private listener", listener, nil)
}

func TestHostileR2SocketOptionsAndFragmentKeepTheConnectCheck(t *testing.T) {
	listener := listenOnPrivateHost(t)
	dns := &scriptedDNS{decide: publicThenPrivate(listener.ip)}
	serveScriptedDNS(t, dns, "sockopt-r2.pgtest")
	for _, c := range []struct {
		name   string
		config *Config
		sender *proxyman.SenderConfig
	}{
		{"tcpFastOpen", &Config{}, &proxyman.SenderConfig{StreamSettings: &internet.StreamConfig{SocketSettings: &internet.SocketConfig{Tfo: 256}}}},
		{"mark and keepalive", &Config{}, &proxyman.SenderConfig{StreamSettings: &internet.StreamConfig{SocketSettings: &internet.SocketConfig{Mark: 7, TcpKeepAliveIdle: 30}}}},
		{"tproxy", &Config{}, &proxyman.SenderConfig{StreamSettings: &internet.StreamConfig{SocketSettings: &internet.SocketConfig{Tproxy: internet.SocketConfig_TProxy}}}},
		{"tcpMptcp", &Config{}, &proxyman.SenderConfig{StreamSettings: &internet.StreamConfig{SocketSettings: &internet.SocketConfig{TcpMptcp: true}}}},
		{"fragment", &Config{Fragment: &Fragment{PacketsFrom: 0, PacketsTo: 1, LengthMin: 1, LengthMax: 3}}, nil},
	} {
		ctx, handler, _ := hostileFreedom(t, "DIRECT", c.config, c.sender)
		for range 2 {
			dispatchThrough(ctx, handler, &session.Inbound{Name: "vless", Tag: "users"}, net.TCPDestination(net.DomainAddress("sockopt-r2.pgtest"), listener.port), []byte("payload"))
		}
		requireUntouched(t, c.name+": a rebinding name (DNS "+dns.answers()+")", listener, nil)
	}
}

func TestHostileR2ARebindingNameBehindADualStackAnswerIsCheckedOnEveryFamily(t *testing.T) {
	listener := listenOnPrivateHost(t)
	dns := &scriptedDNS{decide: always(documentationIPv6, listener.ip)}
	serveScriptedDNS(t, dns, "dual-r2.pgtest")
	ctx, handler, _ := hostileFreedom(t, "DIRECT", &Config{}, nil)
	started := time.Now()
	dispatchThrough(ctx, handler, &session.Inbound{Name: "vless", Tag: "users"}, net.TCPDestination(net.DomainAddress("dual-r2.pgtest"), listener.port), []byte("payload"))
	t.Logf("a dual-stack name (DNS %s) ended after %v", dns.answers(), time.Since(started))
	requireUntouched(t, "a dual-stack name whose IPv4 answer is the private listener", listener, nil)
}

func dispatchDatagrams(ctx context.Context, handler outbound.Handler, inbound *session.Inbound, first net.Destination, timeoutOnly bool, to net.Destination, count int) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx = session.ContextWithInbound(ctx, inbound)
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: first, Tag: handler.Tag()}})
	if timeoutOnly {
		ctx = session.ContextWithTimeoutOnly(ctx, true)
	}
	uplinkReader, uplinkWriter := pipe.New()
	downlinkReader, downlinkWriter := pipe.New()
	for range count {
		b := buf.New()
		b.WriteString("datagram")
		dest := to
		b.UDP = &dest
		uplinkWriter.WriteMultiBuffer(buf.MultiBuffer{b})
	}
	go func() {
		time.Sleep(time.Second)
		uplinkWriter.Close()
	}()
	go func() {
		for {
			if _, err := downlinkReader.ReadMultiBuffer(); err != nil {
				return
			}
		}
	}()
	handler.Dispatch(ctx, &transport.Link{Reader: uplinkReader, Writer: downlinkWriter})
}

func countDatagrams(conn gonet.PacketConn, within time.Duration) int {
	deadline := time.Now().Add(within)
	packet := make([]byte, 64)
	got := 0
	for {
		conn.SetReadDeadline(deadline)
		if _, _, err := conn.ReadFrom(packet); err != nil {
			return got
		}
		got++
	}
}

func TestHostileR2AnXUDPFlowLosesThePrivateAllowOfItsDatagrams(t *testing.T) {
	inbound := &session.Inbound{Name: "vless", Tag: "users"}
	target, addr := listenUDPOnPrivateHost(t)
	allowed := net.UDPDestination(net.IPAddress(addr.IP), net.Port(addr.Port))
	r := routerOf(t, routingRule("lan", "DIRECT", nil, hostCIDR(addr.IP)))
	ctx, handler, freedom := hostileFreedom(t, "DIRECT", &Config{PrivateAllow: []*PrivateTargetRule{allowEntry("lan", hostCIDR(addr.IP))}}, nil)
	freedom.router = r
	results := map[bool]int{}
	for _, timeoutOnly := range []bool{false, true} {
		dispatchDatagrams(ctx, handler, inbound, allowed, timeoutOnly, allowed, 3)
		results[timeoutOnly] = countDatagrams(target, 700*time.Millisecond)
		t.Logf("a UDP flow to %s that routing rule lan sends to DIRECT and privateAllow lists, TimeoutOnly=%v (true for every XUDP flow the mux server opens): %d of 3 datagrams delivered", allowed, timeoutOnly, results[timeoutOnly])
	}
	if results[false] == 0 {
		t.Fatalf("the allowed private UDP destination %s received nothing even without TimeoutOnly", allowed)
	}
	if results[true] == 0 {
		t.Errorf("with the TimeoutOnly context of an XUDP flow, every datagram to the allowed private destination %s is dropped: freedom swaps ctx for a fresh background context before the per-packet check reads the session outbounds", allowed)
	}
}

func TestHostileR2AFreedomWithMuxNeverDialsTheUserTarget(t *testing.T) {
	listener := listenOnPrivateHost(t)
	ctx, handler, _ := hostileFreedom(t, "DIRECT", &Config{}, &proxyman.SenderConfig{MultiplexSettings: &proxyman.MultiplexingConfig{Enabled: true, Concurrency: 8}})
	for range 2 {
		dispatchThrough(ctx, handler, &session.Inbound{Name: "vless", Tag: "users"}, listener.destination(), []byte("payload"))
	}
	requireUntouched(t, "a private literal through a freedom with mux enabled", listener, nil)
	handler.Close()
	time.Sleep(4 * time.Second)
}
