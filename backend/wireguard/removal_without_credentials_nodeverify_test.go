package wireguard

import (
	"context"
	"encoding/hex"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/stats"
)

func nvWGPanelRemoval(t *testing.T) *common.User {
	t.Helper()
	raw, err := hex.DecodeString("0a0234321200")
	if err != nil {
		t.Fatal(err)
	}
	user := &common.User{}
	if err := proto.Unmarshal(raw, user); err != nil {
		t.Fatal(err)
	}
	if user.GetEmail() != "42" || user.GetProxies().GetWireguard() != nil || len(user.GetInbounds()) != 0 {
		t.Fatalf("decoded removal is not the panel shape: %v", user)
	}
	return user
}

func TestNodeVerifyWireGuardPanelRemovalWithoutCredentialsRemovesThePeer(t *testing.T) {
	for name, apply := range map[string]func(*WireGuard, *common.User) error{
		"SyncUser": func(wg *WireGuard, u *common.User) error { return wg.SyncUser(context.Background(), u) },
		"UpdateUsers": func(wg *WireGuard, u *common.User) error {
			return wg.UpdateUsers(context.Background(), []*common.User{u})
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := NewConfig(`{"interface_name":"wg-test","listen_port":51820,"address":["10.72.0.1/24"]}`)
			if err != nil {
				t.Fatal(err)
			}
			_, gone, err := GenerateKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			_, kept, err := GenerateKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			ps := NewPeerStore()
			ps.ReplaceAll([]*PeerInfo{
				mustPeerInfo("42", gone, []string{"10.72.0.2/32"}),
				mustPeerInfo("420", kept, []string{"10.72.0.3/32"}),
			})
			var applied []wgtypes.PeerConfig
			wg := &WireGuard{
				config:       cfg,
				peerStore:    ps,
				statsTracker: stats.New(),
				manager: &Manager{
					iFaceName: "wg-test",
					client: &fakeWGClient{
						configureDeviceFn: func(_ string, c wgtypes.Config) error {
							applied = append(applied, c.Peers...)
							return nil
						},
					},
				},
				state: lifecycleRunning,
			}
			if err := apply(wg, nvWGPanelRemoval(t)); err != nil {
				t.Fatal(err)
			}
			if len(applied) != 1 || !applied[0].Remove || applied[0].PublicKey.String() != gone {
				t.Fatalf("expected exactly one Remove of user 42's peer, got %+v", applied)
			}
			if ps.GetByEmail("42") != nil {
				t.Fatal("user 42's peer is still in the peer store after the panel removed it")
			}
			if ps.GetByEmail("420") == nil {
				t.Fatal("removing user 42 dropped user 420's peer")
			}
		})
	}
}
