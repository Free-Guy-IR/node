package rest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/pasarguard/node/common"
)

func restFreeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free TCP port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func mtprotoTestConfig(t *testing.T, port int) string {
	t.Helper()
	return fmt.Sprintf(`{"instances":[{"tag":"rest-extra","port":%d,"mode":"plain"}]}`, port)
}

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

func requireSingBoxBinary(t *testing.T) {
	t.Helper()
	if p := os.Getenv("SINGBOX_SMOKE_BINARY"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return
		}
	}
	if _, err := os.Stat("/usr/local/bin/sing-box"); err == nil {
		return
	}
	t.Skip("no sing-box binary found; set SINGBOX_SMOKE_BINARY=/path/to/sing-box to run it")
}

func listBackends(t *testing.T) []common.BackendType {
	t.Helper()
	response := &common.BackendList{}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodGet, "/backends", nil, response); err != nil {
		t.Fatalf("GET /backends: %v", err)
	}
	return response.GetTypes()
}

func addExtraBackend(t *testing.T, backendType common.BackendType, config string) {
	t.Helper()
	add := &common.Backend{Type: backendType, Config: config, Users: []*common.User{}}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodPost, "/backend", add, &common.Empty{}); err != nil {
		t.Fatalf("POST /backend (%v): %v", backendType, err)
	}
	t.Cleanup(func() {
		_ = sharedTestCtx.createAuthenticatedRequest(
			http.MethodDelete, "/backend",
			&common.RemoveBackendRequest{Type: backendType}, &common.Empty{},
		)
	})
}

func TestREST_ListBackendsReportsThePrimaryAlone(t *testing.T) {
	types := listBackends(t)
	if len(types) != 1 || types[0] != common.BackendType_XRAY {
		t.Fatalf("a node with no extra cores must report exactly its primary, got %v", types)
	}
}

func TestREST_AddRemoveBackend(t *testing.T) {
	addExtraBackend(t, common.BackendType_MTPROTO, mtprotoTestConfig(t, restFreeTCPPort(t)))

	types := listBackends(t)
	if len(types) != 2 {
		t.Fatalf("expected the primary plus one extra backend, got %v", types)
	}
	if types[0] != common.BackendType_XRAY || types[1] != common.BackendType_MTPROTO {
		t.Fatalf("expected [XRAY MTPROTO] with the primary first, got %v", types)
	}

	remove := &common.RemoveBackendRequest{Type: common.BackendType_MTPROTO}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodDelete, "/backend", remove, &common.Empty{}); err != nil {
		t.Fatalf("DELETE /backend: %v", err)
	}

	types = listBackends(t)
	if len(types) != 1 || types[0] != common.BackendType_XRAY {
		t.Fatalf("removing the extra backend must leave the primary alone, got %v", types)
	}
}

func TestREST_StatsStillWorkWithTwoBackends(t *testing.T) {
	addExtraBackend(t, common.BackendType_MTPROTO, mtprotoTestConfig(t, restFreeTCPPort(t)))

	stats := &common.StatResponse{}
	if err := sharedTestCtx.createAuthenticatedRequest(http.MethodGet, "/stats?type=Outbounds&reset=false", nil, stats); err != nil {
		t.Fatalf("GET /stats with two backends: %v", err)
	}
}

func TestREST_AddRemoveSingBoxBackend(t *testing.T) {
	requireSingBoxBinary(t)

	addExtraBackend(t, common.BackendType_SING_BOX, singBoxTestConfig(t, restFreeTCPPort(t)))

	types := listBackends(t)
	if len(types) != 2 || types[0] != common.BackendType_XRAY || types[1] != common.BackendType_SING_BOX {
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
