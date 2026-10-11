package xray

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	statsService "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func controlPort(t *testing.T, back *Xray, tag string) int {
	t.Helper()
	cfg := back.activeConfig()
	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag != tag {
			continue
		}
		switch p := inbound.Port.(type) {
		case int:
			return p
		case float64:
			return int(p)
		}
	}
	t.Fatalf("inbound %q not found in active config", tag)
	return 0
}

func dialVlessStreamDomain(inboundPort int, id uuid.UUID, domain string, targetPort int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(inboundPort)), 3*time.Second)
	if err != nil {
		return nil, err
	}

	request := []byte{0}
	request = append(request, id[:]...)
	request = append(request, 0, 1)
	request = binary.BigEndian.AppendUint16(request, uint16(targetPort))
	request = append(request, 2)
	request = append(request, byte(len(domain)))
	request = append(request, []byte(domain)...)
	if _, err := conn.Write(request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

type vlessResponseConn struct {
	net.Conn
	headerRead bool
}

func (c *vlessResponseConn) Read(b []byte) (int, error) {
	if !c.headerRead {
		header := make([]byte, 2)
		if _, err := io.ReadFull(c.Conn, header); err != nil {
			return 0, err
		}
		if header[1] > 0 {
			if _, err := io.CopyN(io.Discard, c.Conn, int64(header[1])); err != nil {
				return 0, err
			}
		}
		c.headerRead = true
	}
	return c.Conn.Read(b)
}

type tunnelDialer func() (net.Conn, error)

func attemptQueryStatsReset(ctx context.Context, dial tunnelDialer) error {
	clientConn, err := grpc.NewClient(
		"passthrough:///attacker",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			conn, err := dial()
			if err != nil {
				return nil, err
			}
			return &vlessResponseConn{Conn: conn}, nil
		}),
	)
	if err != nil {
		return err
	}
	defer clientConn.Close()

	client := statsService.NewStatsServiceClient(clientConn)
	_, err = client.QueryStats(ctx, &statsService.QueryStatsRequest{Pattern: "", Reset_: true})
	return err
}

func directUserTraffic(t *testing.T, apiPort int, email string) int64 {
	t.Helper()

	clientConn, err := grpc.NewClient(
		"passthrough:///"+net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort)))
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := statsService.NewStatsServiceClient(clientConn)
	resp, err := client.QueryStats(ctx, &statsService.QueryStatsRequest{Pattern: email, Reset_: false})
	if err != nil {
		t.Fatalf("admin loopback stats read failed: %v", err)
	}

	var total int64
	for _, stat := range resp.GetStat() {
		total += stat.GetValue()
	}
	return total
}

func TestLiveProxyUserCannotReachXrayControlAPI(t *testing.T) {
	forEachXrayBinary(t, proxyUserCannotReachXrayControlAPI)
}

func proxyUserCannotReachXrayControlAPI(t *testing.T, binary string) {
	source := startByteSource(t)
	aliceID := uuid.New()
	alice := sessionUser("1.alice", aliceID, sessionTestInbound)
	back, inboundPort := startSessionBackend(t, binary, alice)

	apiPort := controlPort(t, back, "API_INBOUND")

	warm := openVlessStream(t, inboundPort, aliceID, source)
	requireFlowing(t, warm, "warm-up traffic before the attack")
	_ = warm.Close()

	before := directUserTraffic(t, apiPort, "1.alice")
	if before == 0 {
		t.Fatal("expected the user to have a non-zero traffic counter before the attack")
	}
	t.Logf("user traffic counter before the attack: %d", before)

	vectors := []struct {
		name string
		dial tunnelDialer
	}{
		{"ip-127.0.0.1", func() (net.Conn, error) {
			return dialVlessStream(inboundPort, aliceID, &net.TCPAddr{IP: net.ParseIP("127.0.0.1").To4(), Port: apiPort})
		}},
		{"domain-localhost", func() (net.Conn, error) {
			return dialVlessStreamDomain(inboundPort, aliceID, "localhost", apiPort)
		}},
		{"domain-decimal-ip", func() (net.Conn, error) {
			return dialVlessStreamDomain(inboundPort, aliceID, "2130706433", apiPort)
		}},
		{"domain-dotted-short", func() (net.Conn, error) {
			return dialVlessStreamDomain(inboundPort, aliceID, "127.1", apiPort)
		}},
		{"domain-hex-ip", func() (net.Conn, error) {
			return dialVlessStreamDomain(inboundPort, aliceID, "0x7f000001", apiPort)
		}},
	}

	for _, vector := range vectors {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := attemptQueryStatsReset(ctx, vector.dial)
		cancel()
		if err == nil {
			t.Errorf("attack vector %q reached the xray gRPC API through the proxy and ran QueryStats(reset=true)", vector.name)
		} else {
			t.Logf("attack vector %q was blocked: %v", vector.name, err)
		}
	}

	after := directUserTraffic(t, apiPort, "1.alice")
	t.Logf("user traffic counter after the attack: %d", after)
	if after < before {
		t.Errorf("the proxy user reset billing counters through the API: before=%d after=%d", before, after)
	}
}
