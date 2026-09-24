package xray

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pasarguard/node/common"
)

const rotationCutBound = 3 * time.Second

func waitClosed(conn net.Conn, bound time.Duration) (int64, bool) {
	deadline := time.Now().Add(bound)
	var total int64
	buffer := make([]byte, 64*1024)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, err := conn.Read(buffer)
		total += int64(n)
		if err != nil {
			return total, !errors.Is(err, os.ErrDeadlineExceeded)
		}
	}
	return total, false
}

type rotationStep struct {
	name   string
	rotate func(ctx context.Context, back *Xray, user *common.User) error
}

func rotationSteps() []rotationStep {
	return []rotationStep{
		{name: "SyncUser", rotate: func(ctx context.Context, back *Xray, user *common.User) error { return back.SyncUser(ctx, user) }},
		{name: "UpdateUsers", rotate: func(ctx context.Context, back *Xray, user *common.User) error {
			return back.UpdateUsers(ctx, []*common.User{user})
		}},
	}
}

func TestRotatingAUsersCredentialClosesTheOldCredentialsConnections(t *testing.T) {
	if _, err := os.Stat(executablePath); err != nil {
		t.Skipf("xray binary not found at %s", executablePath)
	}
	rotateCredential(t, executablePath, true)
}

func TestRotatingAUsersCredentialOnAStockCoreStillSwapsTheCredential(t *testing.T) {
	stock := os.Getenv("XRAY_STOCK_EXECUTABLE_PATH")
	if stock == "" {
		t.Skip("XRAY_STOCK_EXECUTABLE_PATH is not set")
	}
	rotateCredential(t, stock, false)
}

func rotateCredential(t *testing.T, binary string, revokes bool) {
	source := startByteSource(t)
	oldID := uuid.New()
	bystanderID := uuid.New()
	alice := sessionUser("1.alice", oldID, sessionTestInbound)
	bystander := sessionUser("2.bystander", bystanderID, sessionTestInbound)
	back, inboundPort := startSessionBackend(t, binary, alice, bystander)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bystanderStream := openVlessStream(t, inboundPort, bystanderID, source)
	requireFlowing(t, bystanderStream, "before any rotation")

	currentID := oldID
	for _, step := range rotationSteps() {
		old := openVlessStream(t, inboundPort, currentID, source)
		requireFlowing(t, old, "before the rotation through "+step.name)

		nextID := uuid.New()
		rotatedAt := time.Now()
		if err := step.rotate(ctx, back, sessionUser("1.alice", nextID, sessionTestInbound)); err != nil {
			t.Fatalf("rotate through %s: %v", step.name, err)
		}

		received, closed := waitClosed(old, rotationCutBound)
		switch {
		case revokes && !closed:
			t.Fatalf("the connection opened with the rotated-out credential was still open %s after the rotation through %s and carried %d more bytes",
				rotationCutBound, step.name, received)
		case revokes:
			t.Logf("the rotated-out credential's connection closed %s after the rotation through %s", time.Since(rotatedAt).Round(time.Millisecond), step.name)
		default:
			t.Logf("stock core: the rotated-out credential's connection closed=%v and carried %d bytes in %s after the rotation through %s", closed, received, rotationCutBound, step.name)
		}

		if received, _ := receive(openVlessStream(t, inboundPort, currentID, source), time.Second); received != 0 {
			t.Fatalf("the rotated-out credential opened a new connection after the rotation through %s and received %d bytes", step.name, received)
		}
		requireFlowing(t, openVlessStream(t, inboundPort, nextID, source), "on the new credential after the rotation through "+step.name)
		requireFlowing(t, bystanderStream, "for a bystander on the same inbound after the rotation through "+step.name)
		currentID = nextID
	}
}
