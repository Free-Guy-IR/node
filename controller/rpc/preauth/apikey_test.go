package preauth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

func TestNodeMustNotAcceptTheZeroApiKeyWhenApiKeyIsUnset(t *testing.T) {
	t.Setenv("API_KEY", "")
	cfg, err := config.Load()
	if err != nil {
		t.Logf("config.Load refused an unset API_KEY: %v", err)
		return
	}
	cfg.GeneratedConfigPath = t.TempDir()

	addr, certPath := startNode(t, cfg)
	client := common.NewNodeServiceClient(dialNode(t, addr, certPath))

	ctx, cancel := context.WithTimeout(withKey(uuid.Nil.String()), 5*time.Second)
	defer cancel()
	info, err := client.GetBaseInfo(ctx, &common.Empty{})
	if err == nil {
		t.Fatalf("with API_KEY unset the node booted (config.Load returned no error, key=%s) and authenticated a caller that sent the all-zero key: GetBaseInfo succeeded, node_version=%s", cfg.ApiKey, info.GetNodeVersion())
	}
}

func TestNodeWithoutAConfiguredKeyRejectsTheZeroKey(t *testing.T) {
	addr, certPath := startNode(t, config.NewTestConfig(t.TempDir(), uuid.Nil))
	client := common.NewNodeServiceClient(dialNode(t, addr, certPath))

	ctx, cancel := context.WithTimeout(withKey(uuid.Nil.String()), 5*time.Second)
	defer cancel()
	_, err := client.GetBaseInfo(ctx, &common.Empty{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a node whose configured key is the nil UUID must refuse a caller that sends the nil UUID with Unauthenticated, got %v", err)
	}
}
