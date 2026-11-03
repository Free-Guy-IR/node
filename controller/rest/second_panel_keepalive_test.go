package rest

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	keepAliveOwnerIP       = "10.20.30.1"
	keepAliveSecondPanelIP = "10.20.30.2"
)

func serveFromPanel(s *Service, ip, key, method, path string) int {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = ip + ":40000"
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	rec := httptest.NewRecorder()
	s.Router.ServeHTTP(rec, req)
	return rec.Code
}

func TestASecondPanelsRequestsNeverKeepTheOwnerPanelsSessionAlive(t *testing.T) {
	s, key := newKeepAliveTestService(t)
	s.Connect(keepAliveOwnerIP, 3600)
	before := s.LastRequest()
	time.Sleep(5 * time.Millisecond)

	cases := []struct {
		method, path, key string
		wantFail          bool
	}{
		{http.MethodGet, "/stats/backend", key.String(), true},
		{http.MethodGet, "/logs", key.String(), true},
		{http.MethodGet, "/no/such/route", key.String(), true},
		{http.MethodGet, "/info", uuid.NewString(), true},
		{http.MethodGet, "/info", "not-a-uuid", true},
		{http.MethodGet, "/info", "", true},
		{http.MethodGet, "/info", key.String(), false},
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 25; n++ {
				for _, c := range cases {
					code := serveFromPanel(s, keepAliveSecondPanelIP, c.key, c.method, c.path)
					if c.wantFail && code < 400 {
						t.Errorf("%s %s: status %d, want failure", c.method, c.path, code)
					}
				}
			}
		}()
	}
	wg.Wait()
	if !s.LastRequest().Equal(before) {
		t.Fatalf("a second panel's requests refreshed the owner's keep-alive: %v -> %v", before, s.LastRequest())
	}
	if s.Ip() != keepAliveOwnerIP {
		t.Fatalf("owner changed to %q", s.Ip())
	}
}

func TestOwnerRequestsRejectedBeforeAuthNeverRefreshKeepAlive(t *testing.T) {
	s, _ := newKeepAliveTestService(t)
	s.Connect(keepAliveOwnerIP, 3600)
	before := s.LastRequest()
	time.Sleep(5 * time.Millisecond)
	for _, k := range []string{"", "not-a-uuid", uuid.NewString()} {
		for _, path := range []string{"/info", "/stats/backend", "/no/such/route"} {
			if code := serveFromPanel(s, keepAliveOwnerIP, k, http.MethodGet, path); code < 400 {
				t.Fatalf("%q %s: status %d, want auth error", k, path, code)
			}
		}
	}
	if !s.LastRequest().Equal(before) {
		t.Fatal("an owner request with a bad api key refreshed keep-alive")
	}
}
