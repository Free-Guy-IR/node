package rest

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

func chunkedBody(t *testing.T, users ...*common.User) []byte {
	t.Helper()
	payload, err := proto.Marshal(&common.UsersChunk{Index: 0, Last: true, Users: users})
	if err != nil {
		t.Fatal(err)
	}
	framed := binary.AppendUvarint(nil, uint64(len(payload)))
	return append(framed, payload...)
}

func TestRESTUserSyncWithNoCoreRunningIsNotAcknowledged(t *testing.T) {
	s := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	user := &common.User{Email: "nobody-home"}

	single, err := proto.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := proto.Marshal(&common.Users{Users: []*common.User{user}})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		handler http.HandlerFunc
		body    []byte
	}{
		{"PUT /user/sync", s.SyncUser, single},
		{"PUT /users/sync", s.SyncUsers, batch},
		{"PUT /users/sync/chunked", s.SyncUsersChunked, chunkedBody(t, user)},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		tc.handler(w, httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(tc.body)))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with no core running answered %d; it must answer 503 so the panel does not drop the update", tc.name, w.Code)
		}
	}
}

func TestRESTBackendStatsWithNoCoreRunningAnswersUnavailable(t *testing.T) {
	s := New(config.NewTestConfig(t.TempDir(), uuid.New()))
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("GET /stats/backend with no core running panicked: %v", r)
			}
		}()
		s.GetBackendStats(w, httptest.NewRequest(http.MethodGet, "/stats/backend", nil))
	}()
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /stats/backend with no core running answered %d, want 503", w.Code)
	}
}
