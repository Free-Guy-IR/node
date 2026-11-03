package openvpn

import (
	"context"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func panelMainRemovalShapes() []struct {
	name  string
	apply func(ctx context.Context, b *Backend, email string) error
} {
	return []struct {
		name  string
		apply func(ctx context.Context, b *Backend, email string) error
	}{
		{"UpdateUsers-empty-proxies", func(ctx context.Context, b *Backend, email string) error {
			return b.UpdateUsers(ctx, []*common.User{{Email: email, Proxies: &common.Proxy{}}})
		}},
		{"UpdateUsers-email-only", func(ctx context.Context, b *Backend, email string) error {
			return b.UpdateUsers(ctx, []*common.User{{Email: email}})
		}},
		{"SyncUser-empty-proxies-no-inbounds", func(ctx context.Context, b *Backend, email string) error {
			return b.SyncUser(ctx, &common.User{Email: email, Proxies: &common.Proxy{}, Inbounds: []string{}})
		}},
	}
}

func ownedEntries(b *Backend, tag, email string) []string {
	var owned []string
	for username, value := range authEntries(b, tag) {
		if strings.HasPrefix(value, email+"/") {
			owned = append(owned, username)
		}
	}
	return owned
}

func TestAPanelMainRemovalWithoutAnOpenVPNBlockRevokesTheUserAndKillsItsSession(t *testing.T) {
	for _, shape := range panelMainRemovalShapes() {
		t.Run(shape.name, func(t *testing.T) {
			ctx := context.Background()
			h := newEvHarness(t, nil, evTagA, evTagB)
			if err := h.b.SyncUsers(ctx, []*common.User{
				ovpnUser(evAlice, evAlice, evAlicePW, evTagA, evTagB),
				ovpnUser(evBob, evBob, evBobPW, evTagA, evTagB),
			}); err != nil {
				t.Fatalf("initial sync: %v", err)
			}
			h.login(t, evTagA, "1", evAlice, evAlicePW)
			h.login(t, evTagB, "2", evBob, evBobPW)

			from := h.servers[evTagA].mark()
			if err := shape.apply(ctx, h.b, evAlice); err != nil {
				t.Fatalf("panel removal payload: %v", err)
			}
			if _, killed := h.servers[evTagA].await(from, evQuiet, evIs("client-kill 1")); !killed {
				t.Errorf("the removal left the removed user's live session running, saw %v", h.servers[evTagA].texts(from))
			}
			for _, tag := range []string{evTagA, evTagB} {
				if owned := ownedEntries(h.b, tag, evAlice); len(owned) != 0 {
					t.Errorf("the removal left the removed user's OpenVPN credential on %s under %v", tag, owned)
				}
				if owned := ownedEntries(h.b, tag, evBob); len(owned) != 1 {
					t.Errorf("the removal of alice touched bob's credential on %s: %v", tag, owned)
				}
			}
			if got := h.connect(t, evTagA, "3", evAlice, evAlicePW); got != "deny" {
				t.Errorf("a removed user must no longer log in on %s, got %s", evTagA, got)
			}
			if got := h.connect(t, evTagB, "4", evAlice, evAlicePW); got != "deny" {
				t.Errorf("a removed user must no longer log in on %s, got %s", evTagB, got)
			}
			if got := h.connect(t, evTagB, "5", evBob, evBobPW); got != "auth" {
				t.Errorf("bob was not removed and must still log in, got %s", got)
			}
			if h.servers[evTagB].count(0, evIs("client-kill 2")) != 0 {
				t.Errorf("bob's session was killed by alice's removal")
			}
		})
	}
}

func TestAPanelMainRemovalOfARenamedUserDropsEveryUsernameItOwned(t *testing.T) {
	ctx := context.Background()
	h := newEvHarness(t, nil, evTagA)
	if err := h.b.SyncUsers(ctx, []*common.User{ovpnUser(evAlice, "alice-old", evAlicePW, evTagA)}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if err := h.b.UpdateUsers(ctx, []*common.User{ovpnUser(evAlice, "alice-new", evAlicePW, evTagA)}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := ownedEntries(h.b, evTagA, evAlice); len(got) != 1 || got[0] != "alice-new" {
		t.Fatalf("a rename must leave exactly the new username, got %v", got)
	}
	if err := h.b.UpdateUsers(ctx, []*common.User{{Email: evAlice}}); err != nil {
		t.Fatalf("removal: %v", err)
	}
	if got := ownedEntries(h.b, evTagA, evAlice); len(got) != 0 {
		t.Fatalf("the removal left %v", got)
	}
	if got := h.connect(t, evTagA, "1", "alice-old", evAlicePW); got != "deny" {
		t.Errorf("the old username must not log in, got %s", got)
	}
	if got := h.connect(t, evTagA, "2", "alice-new", evAlicePW); got != "deny" {
		t.Errorf("the new username must not log in, got %s", got)
	}
}

func TestAnUpdateOfAUserWithoutAnEmailLeavesEveryOtherUserWithoutAnEmailAlone(t *testing.T) {
	ctx := context.Background()
	h := newEvHarness(t, nil, evTagA)
	if err := h.b.SyncUsers(ctx, []*common.User{
		ovpnUser("", "first", evAlicePW, evTagA),
		ovpnUser("", "second", evBobPW, evTagA),
	}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if err := h.b.UpdateUsers(ctx, []*common.User{ovpnUser("", "first", evAlicePW, evTagA)}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := h.b.UpdateUsers(ctx, []*common.User{{Proxies: &common.Proxy{}}}); err != nil {
		t.Fatalf("update without identity: %v", err)
	}
	entries := authEntries(h.b, evTagA)
	if _, ok := entries["second"]; !ok {
		t.Fatalf("an update of one user without an email dropped another user without an email: %v", entries)
	}
	if _, ok := entries["first"]; !ok {
		t.Fatalf("an update of a user without an email dropped that user: %v", entries)
	}
}

func TestPanelMainRemovalsRacingReaddsFullSyncsAndLoginsLeaveNoStaleUserAndLoseNoRemoval(t *testing.T) {
	ctx := context.Background()
	shapes := panelMainRemovalShapes()
	for round := range 6 {
		h := newEvHarness(t, nil, evTagA, evTagB)
		if err := h.b.SyncUsers(ctx, []*common.User{
			ovpnUser(evAlice, evAlice, evAlicePW, evTagA, evTagB),
			ovpnUser(evBob, evBob, evBobPW, evTagA, evTagB),
		}); err != nil {
			t.Fatalf("initial sync: %v", err)
		}

		var owners sync.Map
		var nextCID atomic.Int64
		nextCID.Store(100)
		stop := make(chan struct{})
		var storm sync.WaitGroup
		running := func() bool {
			select {
			case <-stop:
				return false
			default:
				return true
			}
		}

		for _, tag := range []string{evTagA, evTagB} {
			for _, user := range []struct{ name, password string }{{evAlice, evAlicePW}, {evBob, evBobPW}} {
				server := h.servers[tag]
				storm.Go(func() {
					for running() {
						cid := strconv.FormatInt(nextCID.Add(1), 10)
						owners.Store(cid, user.name)
						from := server.mark()
						server.writeToClient(">CLIENT:CONNECT,"+cid+",0", ">CLIENT:ENV,username="+user.name, ">CLIENT:ENV,password="+user.password, ">CLIENT:ENV,END")
						answer, ok := server.await(from, 2*time.Second, evAnswerTo(cid))
						if ok && strings.HasPrefix(answer.text, "client-auth ") {
							server.writeToClient(">CLIENT:ESTABLISHED,"+cid, ">CLIENT:ENV,trusted_ip=127.0.0.1", ">CLIENT:ENV,END")
						}
					}
				})
			}
		}
		storm.Go(func() {
			for running() {
				switch rand.IntN(5) {
				case 0, 1:
					_ = shapes[rand.IntN(len(shapes))].apply(ctx, h.b, evAlice)
				case 2:
					_ = h.b.UpdateUsers(ctx, []*common.User{ovpnUser(evAlice, evAlice, evAlicePW, evTagA, evTagB)})
				case 3:
					_ = h.b.SyncUser(ctx, ovpnUser(evAlice, evAlice, evAlicePW, evTagA))
				case 4:
					users := []*common.User{ovpnUser(evBob, evBob, evBobPW, evTagA, evTagB)}
					if rand.IntN(2) == 0 {
						users = append(users, ovpnUser(evAlice, evAlice, evAlicePW, evTagA, evTagB))
					}
					_ = h.b.SyncUsers(ctx, users)
				}
				time.Sleep(time.Duration(rand.IntN(8)) * time.Millisecond)
			}
		})
		storm.Go(func() {
			for running() {
				_, _ = h.b.GetUserOnlineStats(ctx, evAlice)
				_, _ = h.b.GetStats(ctx, &common.StatRequest{Type: common.StatType_UsersStat, Reset_: true})
			}
		})

		time.Sleep(700 * time.Millisecond)
		close(stop)
		storm.Wait()

		for _, tag := range []string{evTagA, evTagB} {
			if got := h.connect(t, tag, "q"+strconv.Itoa(round), "nobody", "none"); got != "deny" {
				t.Fatalf("round %d: quiescence probe on %s got %s", round, tag, got)
			}
		}

		if err := h.b.UpdateUsers(ctx, []*common.User{ovpnUser(evAlice, evAlice, evAlicePW, evTagA, evTagB)}); err != nil {
			t.Fatalf("round %d: re-add: %v", round, err)
		}
		if got := h.connect(t, evTagB, "r"+strconv.Itoa(round), evAlice, evAlicePW); got != "auth" {
			t.Fatalf("round %d: a re-add that returned must let alice log in, got %s", round, got)
		}
		lastA := "8000" + strconv.Itoa(round)
		h.login(t, evTagA, lastA, evAlice, evAlicePW)
		owners.Store(lastA, evAlice)

		var final sync.WaitGroup
		final.Go(func() { _ = shapes[round%len(shapes)].apply(ctx, h.b, evAlice) })
		final.Go(func() { _ = h.b.UpdateUsers(ctx, []*common.User{ovpnUser(evBob, evBob, evBobPW, evTagA, evTagB)}) })
		final.Wait()

		for _, tag := range []string{evTagA, evTagB} {
			server := h.servers[tag]
			aliceLive := func() []string {
				var stray []string
				for _, cid := range server.liveCIDs() {
					if owner, _ := owners.Load(cid); owner == evAlice {
						stray = append(stray, cid)
					}
				}
				return stray
			}
			deadline := time.Now().Add(evWait)
			for len(aliceLive()) > 0 || h.mgmt(tag).OnlineUserCount(evAlice) > 0 {
				if time.Now().After(deadline) {
					t.Fatalf("round %d: alice was removed without her OpenVPN block, yet %s still runs %v and the node counts %d online", round, tag, aliceLive(), h.mgmt(tag).OnlineUserCount(evAlice))
				}
				time.Sleep(5 * time.Millisecond)
			}
			if owned := ownedEntries(h.b, tag, evAlice); len(owned) != 0 {
				t.Fatalf("round %d: alice's credential survived her removal on %s under %v", round, tag, owned)
			}
			if got := h.connect(t, tag, "9999"+strconv.Itoa(round), evAlice, evAlicePW); got != "deny" {
				t.Fatalf("round %d: removed alice logged in on %s, got %s", round, tag, got)
			}
			if got := h.connect(t, tag, "9998"+strconv.Itoa(round), evBob, evBobPW); got != "auth" {
				t.Fatalf("round %d: bob must still log in on %s, got %s", round, tag, got)
			}
			for _, line := range server.texts(0) {
				cid, isKill := strings.CutPrefix(line, "client-kill ")
				if !isKill {
					continue
				}
				if owner, _ := owners.Load(cid); owner == evBob {
					t.Fatalf("round %d: bob was never removed, yet his session %s on %s was killed", round, cid, tag)
				}
			}
		}
	}
}

func (s *evServer) writeToClient(lines ...string) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		s.writeTo(conn, lines...)
	}
}
