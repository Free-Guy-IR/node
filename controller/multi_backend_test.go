package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/common"
)

type recordingBackend struct {
	backend.Backend
	name          string
	syncedUsers   [][]*common.User
	syncedSingles []*common.User
	updatedUsers  [][]*common.User
	shutdowns     int
	stats         []*common.Stat
	statsErr      error
	online        int64
	ips           map[string]int64
}

func (r *recordingBackend) Started() bool { return true }
func (r *recordingBackend) Shutdown()     { r.shutdowns++ }

func (r *recordingBackend) SyncUser(_ context.Context, u *common.User) error {
	r.syncedSingles = append(r.syncedSingles, u)
	return nil
}

func (r *recordingBackend) SyncUsers(_ context.Context, users []*common.User) error {
	r.syncedUsers = append(r.syncedUsers, users)
	return nil
}

func (r *recordingBackend) UpdateUsers(_ context.Context, users []*common.User) error {
	r.updatedUsers = append(r.updatedUsers, users)
	return nil
}

func (r *recordingBackend) GetStats(_ context.Context, _ *common.StatRequest) (*common.StatResponse, error) {
	if r.statsErr != nil {
		return nil, r.statsErr
	}
	return &common.StatResponse{Stats: r.stats}, nil
}

func (r *recordingBackend) GetUserOnlineStats(_ context.Context, email string) (*common.OnlineStatResponse, error) {
	if r.statsErr != nil {
		return nil, r.statsErr
	}
	return &common.OnlineStatResponse{Name: email, Value: r.online}, nil
}

func (r *recordingBackend) GetUserOnlineIpListStats(_ context.Context, _ string) (*common.StatsOnlineIpListResponse, error) {
	if r.statsErr != nil {
		return nil, r.statsErr
	}
	return &common.StatsOnlineIpListResponse{Ips: r.ips}, nil
}

func controllerWithExtras(primary backend.Backend, primaryType common.BackendType, extras ...extraBackend) *Controller {
	return &Controller{backend: primary, primaryType: primaryType, extras: extras}
}

func TestSingleBackendFansOutToExactlyOneBackend(t *testing.T) {
	primary := &recordingBackend{name: "primary"}
	c := controllerWithExtras(primary, common.BackendType_XRAY)

	users := []*common.User{{Email: "a@b"}}
	if err := c.SyncUsersAll(context.Background(), users); err != nil {
		t.Fatalf("SyncUsersAll: %v", err)
	}

	if len(primary.syncedUsers) != 1 {
		t.Fatalf("primary should have been synced once, got %d", len(primary.syncedUsers))
	}
	if len(c.AllBackends()) != 1 {
		t.Fatalf("a node with no extra cores must expose exactly one backend, got %d", len(c.AllBackends()))
	}
}

func TestSingleBackendStatsAreReturnedUnchanged(t *testing.T) {
	stats := []*common.Stat{{Name: "u", Type: "uplink", Value: 42}}
	primary := &recordingBackend{stats: stats}
	c := controllerWithExtras(primary, common.BackendType_XRAY)

	resp, err := c.StatsAll(context.Background(), &common.StatRequest{})
	if err != nil {
		t.Fatalf("StatsAll: %v", err)
	}
	if len(resp.GetStats()) != 1 || resp.GetStats()[0].GetValue() != 42 {
		t.Fatalf("single-backend stats were altered: %v", resp.GetStats())
	}
}

func TestUserSyncReachesEveryBackend(t *testing.T) {
	primary := &recordingBackend{name: "primary"}
	extra := &recordingBackend{name: "extra"}
	c := controllerWithExtras(primary, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: extra})

	users := []*common.User{{Email: "a@b"}, {Email: "c@d"}}
	if err := c.SyncUsersAll(context.Background(), users); err != nil {
		t.Fatalf("SyncUsersAll: %v", err)
	}
	if err := c.SyncUserAll(context.Background(), users[0]); err != nil {
		t.Fatalf("SyncUserAll: %v", err)
	}
	if err := c.UpdateUsersAll(context.Background(), users); err != nil {
		t.Fatalf("UpdateUsersAll: %v", err)
	}

	for _, b := range []*recordingBackend{primary, extra} {
		if len(b.syncedUsers) != 1 || len(b.syncedUsers[0]) != 2 {
			t.Fatalf("%s did not receive the full user list: %v", b.name, b.syncedUsers)
		}
		if len(b.syncedSingles) != 1 {
			t.Fatalf("%s did not receive the single user sync", b.name)
		}
		if len(b.updatedUsers) != 1 {
			t.Fatalf("%s did not receive the chunked update", b.name)
		}
	}
}

func TestStatsAreMergedAcrossBackends(t *testing.T) {
	primary := &recordingBackend{stats: []*common.Stat{{Name: "a", Type: "uplink", Value: 10}}}
	extra := &recordingBackend{stats: []*common.Stat{{Name: "b", Type: "uplink", Value: 5}}}
	c := controllerWithExtras(primary, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: extra})

	resp, err := c.StatsAll(context.Background(), &common.StatRequest{})
	if err != nil {
		t.Fatalf("StatsAll: %v", err)
	}
	if len(resp.GetStats()) != 2 {
		t.Fatalf("expected both backends stats, got %d", len(resp.GetStats()))
	}
}

func TestStatsSurviveOneFailingBackend(t *testing.T) {
	primary := &recordingBackend{stats: []*common.Stat{{Name: "a", Type: "uplink", Value: 10}}}
	extra := &recordingBackend{statsErr: errors.New("backend down")}
	c := controllerWithExtras(primary, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: extra})

	resp, err := c.StatsAll(context.Background(), &common.StatRequest{})
	if err != nil {
		t.Fatalf("a failing extra backend must not lose the primary stats: %v", err)
	}
	if len(resp.GetStats()) != 1 {
		t.Fatalf("expected the surviving backend stats, got %d", len(resp.GetStats()))
	}
}

func TestOnlineStatsAreSummedAcrossBackends(t *testing.T) {
	primary := &recordingBackend{online: 2}
	extra := &recordingBackend{online: 3}
	c := controllerWithExtras(primary, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: extra})

	resp, err := c.UserOnlineStatsAll(context.Background(), "a@b")
	if err != nil {
		t.Fatalf("UserOnlineStatsAll: %v", err)
	}
	if resp.GetValue() != 5 {
		t.Fatalf("expected 5 online connections, got %d", resp.GetValue())
	}
}

func TestOnlineIpListsAreUnionedKeepingTheNewestTimestamp(t *testing.T) {
	primary := &recordingBackend{ips: map[string]int64{"1.1.1.1": 100, "2.2.2.2": 50}}
	extra := &recordingBackend{ips: map[string]int64{"1.1.1.1": 300, "3.3.3.3": 70}}
	c := controllerWithExtras(primary, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: extra})

	resp, err := c.UserOnlineIpListStatsAll(context.Background(), "a@b")
	if err != nil {
		t.Fatalf("UserOnlineIpListStatsAll: %v", err)
	}
	if len(resp.GetIps()) != 3 {
		t.Fatalf("expected 3 distinct ips, got %v", resp.GetIps())
	}
	if resp.GetIps()["1.1.1.1"] != 300 {
		t.Fatalf("expected the newest timestamp for a shared ip, got %d", resp.GetIps()["1.1.1.1"])
	}
}

func TestXrayIsRefusedAsAnExtraBackend(t *testing.T) {
	c := controllerWithExtras(&recordingBackend{}, common.BackendType_SING_BOX)

	err := c.AttachBackend(context.Background(), &common.Backend{Type: common.BackendType_XRAY})
	if !errors.Is(err, ErrBackendTypeNotShareable) {
		t.Fatalf("xray must be refused as an extra backend, got %v", err)
	}
}

func TestDuplicateBackendTypeIsRefused(t *testing.T) {
	c := controllerWithExtras(&recordingBackend{}, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: &recordingBackend{}})

	if err := c.AttachBackend(context.Background(), &common.Backend{Type: common.BackendType_L2TP}); err == nil {
		t.Fatal("a second l2tp backend must be refused")
	}
	if err := c.AttachBackend(context.Background(), &common.Backend{Type: common.BackendType_XRAY}); err == nil {
		t.Fatal("the primary backend type must not be addable as an extra")
	}
}

func TestAttachRefusedWithoutAPrimaryBackend(t *testing.T) {
	c := &Controller{}

	if err := c.AttachBackend(context.Background(), &common.Backend{Type: common.BackendType_L2TP}); err == nil {
		t.Fatal("attaching to a node with no primary backend must fail")
	}
}

func TestDetachShutsTheBackendDownAndForgetsIt(t *testing.T) {
	extra := &recordingBackend{}
	c := controllerWithExtras(&recordingBackend{}, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: extra})

	if err := c.DetachBackend(common.BackendType_L2TP); err != nil {
		t.Fatalf("DetachBackend: %v", err)
	}
	if extra.shutdowns != 1 {
		t.Fatalf("expected the detached backend to be shut down once, got %d", extra.shutdowns)
	}
	if len(c.ExtraBackendTypes()) != 0 {
		t.Fatalf("detached backend still listed: %v", c.ExtraBackendTypes())
	}
	if err := c.DetachBackend(common.BackendType_L2TP); err == nil {
		t.Fatal("detaching an absent backend must fail")
	}
}

func TestBackendTypesListsPrimaryFirst(t *testing.T) {
	c := controllerWithExtras(&recordingBackend{}, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: &recordingBackend{}},
		extraBackend{backendType: common.BackendType_SING_BOX, backend: &recordingBackend{}})

	types := c.BackendTypes()
	want := []common.BackendType{common.BackendType_XRAY, common.BackendType_L2TP, common.BackendType_SING_BOX}
	if len(types) != len(want) {
		t.Fatalf("expected %d backends, got %v", len(want), types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("backend %d: expected %s, got %s", i, want[i], types[i])
		}
	}
}

func TestShutdownExtrasStopsEveryExtraBackend(t *testing.T) {
	first := &recordingBackend{}
	second := &recordingBackend{}
	primary := &recordingBackend{}
	c := controllerWithExtras(primary, common.BackendType_XRAY,
		extraBackend{backendType: common.BackendType_L2TP, backend: first},
		extraBackend{backendType: common.BackendType_SING_BOX, backend: second})

	c.shutdownExtras()

	if first.shutdowns != 1 || second.shutdowns != 1 {
		t.Fatalf("every extra backend must be shut down, got %d and %d", first.shutdowns, second.shutdowns)
	}
	if primary.shutdowns != 0 {
		t.Fatal("shutdownExtras must not touch the primary backend")
	}
	if len(c.extras) != 0 {
		t.Fatalf("extras must be cleared, got %d", len(c.extras))
	}
}
