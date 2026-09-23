package openvpn

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

func newTrackersBackend(t *testing.T) (*Backend, *ManagementClient) {
	t.Helper()

	instance := newInstanceProcess(&InstanceConfig{Tag: "ovpn"}, PKI{}, 0, t.TempDir(), "openvpn", nil, 0)
	mgmt := newManagementClient("ovpn", "", nil)
	instance.mgmt = mgmt

	return &Backend{
		instances:    map[string]*instanceProcess{"ovpn": instance},
		statsTracker: stats.New(),
	}, mgmt
}

func setTrackersTotals(mgmt *ManagementClient, email string, down, up uint64) {
	mgmt.stateMu.Lock()
	defer mgmt.stateMu.Unlock()
	mgmt.closedTotals[email] = ClientStats{Uplink: up, Downlink: down}
}

func growTrackersTotals(mgmt *ManagementClient, email string, down, up uint64) {
	mgmt.stateMu.Lock()
	defer mgmt.stateMu.Unlock()
	totals := mgmt.closedTotals[email]
	totals.Downlink += down
	totals.Uplink += up
	mgmt.closedTotals[email] = totals
}

func trackersRead(b *Backend, statType common.StatType, name string, reset bool) ([]string, error) {
	resp, err := b.GetStats(context.Background(), &common.StatRequest{Type: statType, Name: name, Reset_: reset})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.GetStats()))
	for _, stat := range resp.GetStats() {
		out = append(out, fmt.Sprintf("%s|%s|%s|%d", stat.GetName(), stat.GetLink(), stat.GetType(), stat.GetValue()))
	}
	sort.Strings(out)
	return out, nil
}

func expectTrackers(t *testing.T, b *Backend, statType common.StatType, name string, reset bool, want ...string) {
	t.Helper()
	got, err := trackersRead(b, statType, name, reset)
	if err != nil {
		t.Fatalf("GetStats(%s, %q, reset=%v): %v", statType, name, reset, err)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("GetStats(%s, %q, reset=%v) = %v, want %v", statType, name, reset, got, want)
	}
}

func TestTrackersFirstReadsSetBaselines(t *testing.T) {
	b, mgmt := newTrackersBackend(t)
	setTrackersTotals(mgmt, "alice", 5000, 6000)

	expectTrackers(t, b, common.StatType_Outbounds, "", true)
	expectTrackers(t, b, common.StatType_Inbounds, "", true)
}

func TestTrackersOutboundsThenInboundsEachReportAllTraffic(t *testing.T) {
	b, mgmt := newTrackersBackend(t)
	setTrackersTotals(mgmt, "alice", 5000, 6000)
	expectTrackers(t, b, common.StatType_Outbounds, "", true)
	expectTrackers(t, b, common.StatType_Inbounds, "", true)

	growTrackersTotals(mgmt, "alice", 1000, 2000)

	expectTrackers(t, b, common.StatType_Outbounds, "", true, "ovpn|ovpn|uplink|1000", "ovpn|ovpn|downlink|2000")
	expectTrackers(t, b, common.StatType_Inbounds, "", true, "ovpn|ovpn|uplink|1000", "ovpn|ovpn|downlink|2000")

	expectTrackers(t, b, common.StatType_Outbounds, "", true)
	expectTrackers(t, b, common.StatType_Inbounds, "", true)
}

func TestTrackersInboundsThenOutboundsEachReportAllTraffic(t *testing.T) {
	b, mgmt := newTrackersBackend(t)
	setTrackersTotals(mgmt, "alice", 5000, 6000)
	expectTrackers(t, b, common.StatType_Inbounds, "", true)
	expectTrackers(t, b, common.StatType_Outbounds, "", true)

	growTrackersTotals(mgmt, "alice", 1000, 2000)

	expectTrackers(t, b, common.StatType_Inbounds, "", true, "ovpn|ovpn|uplink|1000", "ovpn|ovpn|downlink|2000")
	expectTrackers(t, b, common.StatType_Outbounds, "", true, "ovpn|ovpn|uplink|1000", "ovpn|ovpn|downlink|2000")
}

func TestTrackersSingleInstanceByName(t *testing.T) {
	b, mgmt := newTrackersBackend(t)
	setTrackersTotals(mgmt, "alice", 5000, 6000)
	expectTrackers(t, b, common.StatType_Outbound, "ovpn", true)
	expectTrackers(t, b, common.StatType_Inbound, "ovpn", true)

	growTrackersTotals(mgmt, "alice", 1000, 2000)

	expectTrackers(t, b, common.StatType_Outbound, "ovpn", true, "ovpn|ovpn|uplink|1000", "ovpn|ovpn|downlink|2000")
	expectTrackers(t, b, common.StatType_Inbound, "ovpn", true, "ovpn|ovpn|uplink|1000", "ovpn|ovpn|downlink|2000")

	growTrackersTotals(mgmt, "alice", 10, 20)

	expectTrackers(t, b, common.StatType_Inbound, "ovpn", false, "ovpn|ovpn|uplink|10", "ovpn|ovpn|downlink|20")
	expectTrackers(t, b, common.StatType_Outbounds, "", true, "ovpn|ovpn|uplink|10", "ovpn|ovpn|downlink|20")
	expectTrackers(t, b, common.StatType_Inbounds, "", true, "ovpn|ovpn|uplink|10", "ovpn|ovpn|downlink|20")
}

func TestTrackersUnknownInstanceStillErrors(t *testing.T) {
	b, _ := newTrackersBackend(t)

	for _, statType := range []common.StatType{common.StatType_Inbound, common.StatType_Outbound} {
		if _, err := trackersRead(b, statType, "missing", true); err == nil {
			t.Fatalf("GetStats(%s, missing) returned no error", statType)
		}
	}
}

func TestTrackersUsersStatIsUnchanged(t *testing.T) {
	b, mgmt := newTrackersBackend(t)
	setTrackersTotals(mgmt, "alice", 1000, 2000)
	setTrackersTotals(mgmt, "bob", 300, 400)

	expectTrackers(t, b, common.StatType_Outbounds, "", true)
	expectTrackers(t, b, common.StatType_Inbounds, "", true)

	expectTrackers(t, b, common.StatType_UsersStat, "", true,
		"alice|ovpn|uplink|1000", "alice|ovpn|downlink|2000",
		"bob|ovpn|uplink|300", "bob|ovpn|downlink|400")

	growTrackersTotals(mgmt, "alice", 50, 60)

	expectTrackers(t, b, common.StatType_Outbounds, "", true, "ovpn|ovpn|uplink|50", "ovpn|ovpn|downlink|60")
	expectTrackers(t, b, common.StatType_UserStat, "alice", false, "alice|ovpn|uplink|50", "alice|ovpn|downlink|60")
	expectTrackers(t, b, common.StatType_UsersStat, "", true, "alice|ovpn|uplink|50", "alice|ovpn|downlink|60")
	expectTrackers(t, b, common.StatType_UsersStat, "", true)
	expectTrackers(t, b, common.StatType_UserStat, "bob", true)
}
