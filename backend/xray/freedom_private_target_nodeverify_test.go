package xray

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
)

const privateTargetTestConfig = `{
	"log": {"loglevel": "warning"},
	"inbounds": [{
		"tag": "VLESS TCP SESSION",
		"listen": "127.0.0.1",
		"port": %d,
		"protocol": "vless",
		"settings": {"clients": [], "decryption": "none"}
	}],
	"outbounds": [{"tag": "direct", "protocol": "freedom"%s}]
}`

const privateTargetAllowingSettings = `, "settings": {"finalRules": [{"action": "allow", "ip": ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"]}]}`

func startPrivateTargetBackend(t *testing.T, binary, outboundSettings string, users ...*common.User) int {
	t.Helper()

	inboundPort := netutil.FindFreePort()
	xrayConfig, err := NewConfig(fmt.Sprintf(privateTargetTestConfig, inboundPort, outboundSettings), nil)
	if err != nil {
		t.Fatal(err)
	}

	back, err := New(
		context.Background(),
		xrayConfig,
		users,
		netutil.FindFreePort(),
		netutil.FindFreePort(),
		&config.Config{
			XrayExecutablePath:  binary,
			XrayAssetsPath:      assetsPath,
			GeneratedConfigPath: t.TempDir(),
			LogBufferSize:       1000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(back.Shutdown)
	t.Logf("core under test: %s", back.Version())
	return inboundPort
}

func TestAUserInboundReachesAPrivateTargetOnlyThroughAnAllowingFreedomOutbound(t *testing.T) {
	forEachXrayBinary(t, privateTargetNeedsAnAllowingFreedomOutbound)
}

func privateTargetNeedsAnAllowingFreedomOutbound(t *testing.T, binary string) {
	source := startByteSource(t)
	aliceID := uuid.New()
	alice := sessionUser("1.alice", aliceID, sessionTestInbound)

	barePort := startPrivateTargetBackend(t, binary, "", alice)
	if received, _ := receive(openVlessStream(t, barePort, aliceID, source), time.Second); received != 0 {
		t.Fatalf("a freedom outbound with no finalRules carried %d bytes to %s: the core no longer blocks a private target of a user inbound on its own, so every live config that allows one has to be revisited", received, source)
	}

	allowingPort := startPrivateTargetBackend(t, binary, privateTargetAllowingSettings, alice)
	requireFlowing(t, openVlessStream(t, allowingPort, aliceID, source), "through a freedom outbound that allows the private ranges findTrafficHost picks from")
}
