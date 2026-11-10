package xray

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

const bareDirectOutboundJSON = `{"tag": "direct", "protocol": "freedom"}`

func TestAUserInboundReachesAPrivateTargetOnlyThroughAnAllowingFreedomOutbound(t *testing.T) {
	forEachXrayBinary(t, privateTargetNeedsAnAllowingFreedomOutbound)
}

func privateTargetNeedsAnAllowingFreedomOutbound(t *testing.T, binary string) {
	t.Run("without finalRules", func(t *testing.T) {
		source := startByteSource(t)
		aliceID := uuid.New()
		alice := sessionUser("1.alice", aliceID, sessionTestInbound)

		_, port := startSessionBackendWithOutbound(t, binary, bareDirectOutboundJSON, alice)
		if received, _ := receive(openVlessStream(t, port, aliceID, source), time.Second); received != 0 {
			t.Fatalf("a freedom outbound with no finalRules carried %d bytes to %s: the core no longer blocks a private target of a user inbound on its own, so every live config that allows one has to be revisited", received, source)
		}
	})

	t.Run("with finalRules", func(t *testing.T) {
		source := startByteSource(t)
		aliceID := uuid.New()
		alice := sessionUser("1.alice", aliceID, sessionTestInbound)

		_, port := startSessionBackendWithOutbound(t, binary, directOutboundJSON(t), alice)
		requireFlowing(t, openVlessStream(t, port, aliceID, source), "through a freedom outbound that allows the traffic host the harness chose")
	})
}
