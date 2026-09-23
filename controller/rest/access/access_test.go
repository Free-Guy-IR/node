package access_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/controller/rest"
)

func newService(t *testing.T, key uuid.UUID) *rest.Service {
	t.Helper()
	s := rest.New(config.NewTestConfig(t.TempDir(), key))
	t.Cleanup(s.Disconnect)
	return s
}

func serve(s *rest.Service, method, path, key, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("x-api-key", key)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	s.Router.ServeHTTP(rec, req)
	return rec
}

func TestRestNodeWithoutAConfiguredKeyRejectsTheZeroKey(t *testing.T) {
	s := newService(t, uuid.Nil)

	rec := serve(s, http.MethodGet, "/info", uuid.Nil.String(), "127.0.0.1:4321")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a node whose configured key is the nil UUID must refuse a caller that sends the nil UUID with 401, got %d", rec.Code)
	}
}

func TestRestNodeAcceptsItsConfiguredKey(t *testing.T) {
	key := uuid.New()
	s := newService(t, key)

	if rec := serve(s, http.MethodGet, "/info", key.String(), "127.0.0.1:4321"); rec.Code != http.StatusOK {
		t.Fatalf("the configured key must reach /info, got %d", rec.Code)
	}
	if rec := serve(s, http.MethodGet, "/info", uuid.New().String(), "127.0.0.1:4321"); rec.Code != http.StatusForbidden {
		t.Fatalf("a different key must be refused with 403, got %d", rec.Code)
	}
}
