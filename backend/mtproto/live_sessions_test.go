package mtproto

import "testing"

func TestLiveSessionsEpochAtAuthentication(t *testing.T) {
	registry := &liveSessions{}
	var closedBefore, closedAfter, closedOther int

	registry.Begin("started-before-revoke")
	registry.Begin("other-user")
	registry.revoke([]string{"a@x"})
	registry.Begin("started-after-revoke")

	registry.Authenticated("started-before-revoke", "a@x", func() { closedBefore++ })
	registry.Authenticated("started-after-revoke", "a@x", func() { closedAfter++ })
	registry.Authenticated("other-user", "b@x", func() { closedOther++ })

	if closedBefore != 1 {
		t.Fatalf("a handshake that began before a@x was revoked must be cut at authentication, closed %d times", closedBefore)
	}
	if closedAfter != 0 || closedOther != 0 {
		t.Fatalf("sessions begun after the revoke or of another user must survive, closed %d/%d", closedAfter, closedOther)
	}

	registry.revoke([]string{"a@x"})
	if closedAfter != 1 || closedOther != 0 {
		t.Fatalf("a second revoke must cut only a@x's registered session, closed %d/%d", closedAfter, closedOther)
	}

	registry.End("started-before-revoke")
	registry.End("started-after-revoke")
	registry.End("other-user")
	registry.revoke([]string{"c@x"})
	if len(registry.startEpoch) != 0 || len(registry.userOf) != 0 || len(registry.byUser) != 0 {
		t.Fatalf("finished streams must leave no state behind: %d %d %d", len(registry.startEpoch), len(registry.userOf), len(registry.byUser))
	}
	if len(registry.revokedAt) != 1 {
		t.Fatalf("revocations no live stream can match must be pruned, kept %v", registry.revokedAt)
	}
}
