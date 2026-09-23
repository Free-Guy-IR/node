package openvpn

import (
	"context"
	"testing"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

func directionOpenVPN(t *testing.T) (*Backend, *ManagementClient) {
	t.Helper()
	instance := newInstanceProcess(&InstanceConfig{Tag: "ovpn-dir"}, PKI{}, 0, t.TempDir(), "openvpn", nil, 0)
	mgmt := newManagementClient("ovpn-dir", "", nil)
	instance.mgmt = mgmt
	return &Backend{
		instances:    map[string]*instanceProcess{"ovpn-dir": instance},
		statsTracker: stats.New(),
	}, mgmt
}

func directionConnected(mgmt *ManagementClient, cid, email string) {
	mgmt.stateMu.Lock()
	defer mgmt.stateMu.Unlock()
	mgmt.cidIdentity[cid] = clientIdentity{username: email, email: email}
}

func directionFeed(mgmt *ManagementClient, lines ...string) {
	var pending *clientEvent
	for _, line := range lines {
		pending = mgmt.handleLine(line, pending)
	}
}

func directionRead(t *testing.T, b *Backend, request *common.StatRequest) (map[string]map[string]int64, map[string]int64) {
	t.Helper()
	resp, err := b.GetStats(context.Background(), request)
	if err != nil {
		t.Fatalf("GetStats(%v): %v", request.GetType(), err)
	}
	byName := map[string]map[string]int64{}
	sumByName := map[string]int64{}
	for _, s := range resp.GetStats() {
		if byName[s.GetName()] == nil {
			byName[s.GetName()] = map[string]int64{}
		}
		if _, dup := byName[s.GetName()][s.GetType()]; dup {
			t.Fatalf("GetStats(%v): %s/%s reported twice", request.GetType(), s.GetName(), s.GetType())
		}
		byName[s.GetName()][s.GetType()] = s.GetValue()
		sumByName[s.GetName()] += s.GetValue()
	}
	return byName, sumByName
}

func TestOpenVPNUserUplinkIsWhatTheClientSent(t *testing.T) {
	b, mgmt := directionOpenVPN(t)
	directionConnected(mgmt, "7", "live@example.com")
	directionConnected(mgmt, "8", "closed@example.com")
	directionFeed(mgmt,
		">BYTECOUNT_CLI:7,10,90",
		">CLIENT:DISCONNECT,8",
		">CLIENT:ENV,bytes_received=10",
		">CLIENT:ENV,bytes_sent=90",
		">CLIENT:ENV,END",
	)

	for _, email := range []string{"live@example.com", "closed@example.com"} {
		got, sum := directionRead(t, b, &common.StatRequest{Type: common.StatType_UserStat, Name: email})
		if got[email]["uplink"] != 10 || got[email]["downlink"] != 90 {
			t.Fatalf("UserStat %s: client sent 10 and received 90, got %v", email, got[email])
		}
		if sum[email] != 100 {
			t.Fatalf("UserStat %s: per-user sum %v, want 100", email, sum)
		}
	}

	got, sum := directionRead(t, b, &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	for _, email := range []string{"live@example.com", "closed@example.com"} {
		if got[email]["uplink"] != 10 || got[email]["downlink"] != 90 {
			t.Fatalf("UsersStat %s: client sent 10 and received 90, got %v", email, got[email])
		}
		if sum[email] != 100 {
			t.Fatalf("UsersStat %s: per-user sum %v, want 100", email, sum)
		}
	}
}

func TestOpenVPNInstanceUplinkIsWhatClientsSent(t *testing.T) {
	b, mgmt := directionOpenVPN(t)
	directionRead(t, b, &common.StatRequest{Type: common.StatType_Inbounds, Reset_: true})
	directionRead(t, b, &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true})

	directionConnected(mgmt, "7", "live@example.com")
	directionFeed(mgmt, ">BYTECOUNT_CLI:7,10,90")

	for _, request := range []*common.StatRequest{
		{Type: common.StatType_Inbound, Name: "ovpn-dir"},
		{Type: common.StatType_Outbound, Name: "ovpn-dir"},
		{Type: common.StatType_Inbounds, Reset_: true},
		{Type: common.StatType_Outbounds, Reset_: true},
	} {
		got, sum := directionRead(t, b, request)
		if got["ovpn-dir"]["uplink"] != 10 || got["ovpn-dir"]["downlink"] != 90 {
			t.Fatalf("%v: clients sent 10 and received 90, got %v", request.GetType(), got["ovpn-dir"])
		}
		if sum["ovpn-dir"] != 100 {
			t.Fatalf("%v: sum %v, want 100", request.GetType(), sum)
		}
	}
}
