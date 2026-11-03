package openvpn

import (
	"context"
	"slices"
	"testing"

	"github.com/pasarguard/node/common"
)

const reconnectMB = int64(1 << 20)

func openTrackersSession(mgmt *ManagementClient, cid, email string, down, up int64) {
	mgmt.stateMu.Lock()
	defer mgmt.stateMu.Unlock()
	mgmt.cidIdentity[cid] = clientIdentity{username: email, email: email}
	mgmt.sessionStats[cid] = ClientStats{Uplink: uint64(up), Downlink: uint64(down)}
}

func pullTrackersValues(t *testing.T, b *Backend, email string) []int64 {
	t.Helper()
	resp, err := b.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
	if err != nil {
		t.Fatalf("UsersStat pull failed: %v", err)
	}
	var values []int64
	for _, stat := range resp.GetStats() {
		if stat.GetName() == email {
			values = append(values, stat.GetValue())
		}
	}
	slices.Sort(values)
	return values
}

func TestTrackersManagementReconnectDoesNotRebillClosedTotals(t *testing.T) {
	b, mgmt := newTrackersBackend(t)
	const email = "reconnect@example.com"

	setTrackersTotals(mgmt, email, uint64(10240*reconnectMB), uint64(1024*reconnectMB))
	pullTrackersValues(t, b, email)

	openTrackersSession(mgmt, "1", email, 50*reconnectMB, 5*reconnectMB)
	b.sampleUserStats()
	mgmt.resetVolatileState()
	b.sampleUserStats()
	openTrackersSession(mgmt, "2", email, 3*reconnectMB, 1*reconnectMB)

	got := pullTrackersValues(t, b, email)
	var total int64
	for _, value := range got {
		total += value
	}
	want := []int64{6 * reconnectMB, 53 * reconnectMB}
	if total > 59*reconnectMB || !slices.Equal(got, want) {
		t.Fatalf("reported %v bytes (%d MB) after a management reconnect, the clients moved at most %v (59 MB)", got, total/reconnectMB, want)
	}

	openTrackersSession(mgmt, "2", email, 5*reconnectMB, 2*reconnectMB)
	if got := pullTrackersValues(t, b, email); !slices.Equal(got, []int64{1 * reconnectMB, 2 * reconnectMB}) {
		t.Fatalf("reported %v bytes after the new session grew, want 2 MB and 1 MB", got)
	}
}
