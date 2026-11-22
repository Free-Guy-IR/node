package xray

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	bareDirectOutboundJSON = `{"tag": "direct", "protocol": "freedom"}`
	ipsBlockedDefaultSince = "26.4.15"
	finalRulesSince        = "26.5.3"
)

type privateTargetPin struct {
	version              string
	blocksPrivateTargets bool
	forkGuard            bool
}

func (p privateTargetPin) String() string {
	if p.forkGuard {
		return fmt.Sprintf("core %s with the fork's private-target guard (it reads privateAllow, so freedom blocks a private target of a user inbound unless finalRules or privateAllow allow it, as v%s and later do through finalRules)", p.version, finalRulesSince)
	}
	if p.blocksPrivateTargets {
		return fmt.Sprintf("core %s (v%s or later: the default rule of freedom blocks a private target of a user inbound unless finalRules allow it)", p.version, finalRulesSince)
	}
	return fmt.Sprintf("core %s (before v%s, like the v26.3.27 node v0.8.22 ships: freedom has no default rule against a private target of a user inbound other than the local host and does not read finalRules)", p.version, ipsBlockedDefaultSince)
}

func xrayRelease(t *testing.T, version string) []int {
	t.Helper()

	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		t.Fatalf("xray version %q is not major.minor.patch, so whether it blocks a private target of a user inbound is unknown", version)
	}
	release := make([]int, len(parts))
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			t.Fatalf("xray version %q is not major.minor.patch, so whether it blocks a private target of a user inbound is unknown: %v", version, err)
		}
		release[i] = number
	}
	return release
}

func privateTargetPinOf(t *testing.T, binary string) privateTargetPin {
	t.Helper()

	core, err := NewXRayCore(binary, assetsPath, t.TempDir(), 1, 1)
	if err != nil {
		t.Fatalf("the version of %s cannot be read, so whether it blocks a private target of a user inbound is unknown: %v", binary, err)
	}
	pin := privateTargetPin{version: core.Version()}
	release := xrayRelease(t, pin.version)
	switch {
	case slices.Compare(release, xrayRelease(t, finalRulesSince)) >= 0:
		pin.blocksPrivateTargets = true
	case slices.Compare(release, xrayRelease(t, ipsBlockedDefaultSince)) >= 0:
		t.Fatalf("core %s (v%s or later but before v%s): freedom blocks a private target of a user inbound through its ipsBlocked default and ignores finalRules, so a user inbound reaches the traffic host neither as on the held line nor through the allow every live config carries", pin.version, ipsBlockedDefaultSince, finalRulesSince)
	case readsPrivateAllow(t, binary):
		pin.blocksPrivateTargets, pin.forkGuard = true, true
	}
	return pin
}

func blockingDirectOutboundJSON(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(
		`{"tag": "direct", "protocol": "freedom", "settings": {"finalRules": [{"action": "block", "ip": [%q]}, {"action": "allow", "ip": [%q]}]}}`,
		trafficHostPrefix(t),
		trafficHostPrefix(t),
	)
}

func requireThePrivateTargetIsReached(t *testing.T, pin privateTargetPin, binary, outbound, through, changed string) {
	t.Helper()

	source := startByteSource(t)
	aliceID := uuid.New()
	_, port := startSessionBackendWithOutbound(t, binary, outbound, sessionUser("1.alice", aliceID, sessionTestInbound))
	if received, err := receivePayload(openVlessStream(t, port, aliceID, source), 500*time.Millisecond); err != nil || received == 0 {
		t.Fatalf("%s: %s carried %d bytes to %s (err %v): %s", pin, through, received, source, err, changed)
	}
}

func requireNothingReachesThePrivateTarget(t *testing.T, pin privateTargetPin, binary, outbound, through, changed string) {
	t.Helper()

	source := startByteSource(t)
	aliceID := uuid.New()
	_, port := startSessionBackendWithOutbound(t, binary, outbound, sessionUser("1.alice", aliceID, sessionTestInbound))
	if received, _ := receivePayload(openVlessStream(t, port, aliceID, source), time.Second); received != 0 {
		t.Fatalf("%s: %s carried %d bytes to %s: %s", pin, through, received, source, changed)
	}
}

func TestTheCoreVersionDecidesWhetherAUserInboundReachesAPrivateTarget(t *testing.T) {
	forEachXrayBinary(t, privateTargetFollowsTheCoreVersion)
}

func privateTargetFollowsTheCoreVersion(t *testing.T, binary string) {
	pin := privateTargetPinOf(t, binary)
	t.Logf("expecting the private-target behaviour of %s", pin)

	t.Run("without finalRules", func(t *testing.T) {
		const through = "a freedom outbound with no finalRules"
		if pin.blocksPrivateTargets {
			requireNothingReachesThePrivateTarget(t, pin, binary, bareDirectOutboundJSON, through,
				"the core no longer blocks a private target of a user inbound on its own, so every live config that allows one has to be revisited")
			return
		}
		requireThePrivateTargetIsReached(t, pin, binary, bareDirectOutboundJSON, through,
			"the core now keeps a user inbound off a private target that the v26.3.27 node v0.8.22 ships lets it reach, so rolling it out would block user traffic v0.8.22 carries")
	})

	t.Run("with finalRules", func(t *testing.T) {
		t.Run("that allow the target", func(t *testing.T) {
			requireThePrivateTargetIsReached(t, pin, binary, directOutboundJSON(t),
				"a freedom outbound whose finalRules allow the traffic host the harness chose",
				"the allow every live config carries no longer lets a user inbound reach that private target")
		})

		t.Run("that block the target", func(t *testing.T) {
			const through = "a freedom outbound whose finalRules block the traffic host the harness chose ahead of an allow for it"
			if pin.blocksPrivateTargets {
				requireNothingReachesThePrivateTarget(t, pin, binary, blockingDirectOutboundJSON(t), through,
					"the core no longer applies a finalRules block ahead of a later allow, so finalRules are no longer what decides whether a user inbound reaches a private target")
				return
			}
			requireThePrivateTargetIsReached(t, pin, binary, blockingDirectOutboundJSON(t), through,
				"the core now reads finalRules or keeps a user inbound off that target some other way, which the v26.3.27 node v0.8.22 ships does not, so the allow every live config carries is no longer inert on this line")
		})
	})
}
