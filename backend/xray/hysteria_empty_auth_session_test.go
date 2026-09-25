package xray

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func refusedBytes(t *testing.T, clientPort int) int64 {
	t.Helper()

	probe := openStreamProbe(t, clientPort)
	time.Sleep(revokeReconnectProbe)
	received := probe.received.Load()
	probe.close()
	return received
}

func TestAnEmptyHysteriaAuthIsNeverAccepted(t *testing.T) {
	const bobAuth = "bob-own-hysteria-secret"
	node := startHysteriaNode(t, []*common.User{hysteriaUser("1", ""), hysteriaUser("2", bobAuth)})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if emitted := node.generatedClients(t); !slices.Equal(emitted, []string{"2"}) {
		t.Fatalf("the config handed to xray lists the hysteria clients as %v, want only the user with an auth", emitted)
	}

	emptyClient := node.client(t, "")
	node.drainTraffic(t)
	if received := refusedBytes(t, emptyClient); received != 0 {
		t.Fatalf("a client with an empty hysteria auth moved %d bytes (%v)", received, node.drainTraffic(t))
	}

	bob := openStreamProbe(t, node.client(t, bobAuth))
	bob.waitForBytes(t, "the user with an auth", revokeWarmupBytes)
	bob.close()
	<-bob.ended

	if err := node.backend.SyncUser(ctx, hysteriaUser("2", "")); err != nil {
		t.Fatalf("emptying the auth failed the sync: %v", err)
	}
	if received := refusedBytes(t, node.client(t, bobAuth)); received != 0 {
		t.Fatalf("the user whose auth became empty still connected with the old auth and moved %d bytes", received)
	}
	if received := refusedBytes(t, node.client(t, "")); received != 0 {
		t.Fatalf("a client with an empty hysteria auth moved %d bytes after a user's auth became empty", received)
	}
}
