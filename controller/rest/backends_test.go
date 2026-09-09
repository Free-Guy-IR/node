package rest

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pasarguard/node/common"
)

func singBoxTestConfig(t *testing.T, port int) string {
	t.Helper()
	cfg := map[string]any{
		"log": map[string]any{"level": "error"},
		"inbounds": []any{map[string]any{
			"type":        "shadowsocks",
			"tag":         "ss-extra",
			"listen":      "127.0.0.1",
			"listen_port": port,
			"method":      "chacha20-ietf-poly1305",
			"password":    "TFpVSnpFOEJqSEJEY0kyQQ==",
			"users":       []any{},
		}},
		"outbounds": []any{map[string]any{"type": "direct"}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal sing-box config: %v", err)
	}
	return string(raw)
}

func listBackends(t *testing.T) []common.BackendType {
	t.Helper()
	response := &common.BackendList{}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodGet, "/backends", nil, response); err != nil {
		t.Fatalf("GET /backends: %v", err)
	}
	return response.GetTypes()
}

func TestREST_ListBackendsReportsThePrimaryAlone(t *testing.T) {
	types := listBackends(t)
	if len(types) != 1 || types[0] != common.BackendType_XRAY {
		t.Fatalf("a node with no extra cores must report exactly its primary, got %v", types)
	}
}

func TestREST_AddRemoveBackend(t *testing.T) {
	add := &common.Backend{
		Type:   common.BackendType_SING_BOX,
		Config: singBoxTestConfig(t, 19443),
		Users:  []*common.User{},
	}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodPost, "/backend", add, &common.Empty{}); err != nil {
		t.Fatalf("POST /backend: %v", err)
	}
	t.Cleanup(func() {
		_ = sharedTestCtx.createAuthenticatedRequest(
			http.MethodDelete, "/backend",
			&common.RemoveBackendRequest{Type: common.BackendType_SING_BOX}, &common.Empty{},
		)
	})

	types := listBackends(t)
	if len(types) != 2 {
		t.Fatalf("expected the primary plus one extra backend, got %v", types)
	}
	if types[0] != common.BackendType_XRAY || types[1] != common.BackendType_SING_BOX {
		t.Fatalf("expected [XRAY SING_BOX] with the primary first, got %v", types)
	}

	remove := &common.RemoveBackendRequest{Type: common.BackendType_SING_BOX}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodDelete, "/backend", remove, &common.Empty{}); err != nil {
		t.Fatalf("DELETE /backend: %v", err)
	}

	types = listBackends(t)
	if len(types) != 1 || types[0] != common.BackendType_XRAY {
		t.Fatalf("removing the extra backend must leave the primary alone, got %v", types)
	}
}

func TestREST_StatsStillWorkWithTwoBackends(t *testing.T) {
	add := &common.Backend{
		Type:   common.BackendType_SING_BOX,
		Config: singBoxTestConfig(t, 19444),
		Users:  []*common.User{},
	}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodPost, "/backend", add, &common.Empty{}); err != nil {
		t.Fatalf("POST /backend: %v", err)
	}
	t.Cleanup(func() {
		_ = sharedTestCtx.createAuthenticatedRequest(
			http.MethodDelete, "/backend",
			&common.RemoveBackendRequest{Type: common.BackendType_SING_BOX}, &common.Empty{},
		)
	})

	stats := &common.StatResponse{}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodGet, "/stats?type=Outbounds&reset=false", nil, stats); err != nil {
		t.Fatalf("GET /stats with two backends: %v", err)
	}
}
