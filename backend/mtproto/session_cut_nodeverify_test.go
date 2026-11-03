package mtproto

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/9seconds/mtg/v2/essentials"
	"github.com/9seconds/mtg/v2/mtglib"
	"github.com/9seconds/mtg/v2/mtglib/obfuscation"

	"github.com/pasarguard/node/common"
)

const (
	nvTag        = "mtproto-tg-plain"
	nvAlice      = "nv-alice"
	nvBob        = "nv-bob"
	nvTelegramDC = 2
	nvChunk      = 64
	nvCutBound   = 3 * time.Second
)

type nvDCSession struct {
	received atomic.Int64
	closed   chan struct{}
}

type nvFakeDC struct {
	listener net.Listener
	mu       sync.Mutex
	sessions map[uint64]*nvDCSession
	conns    []net.Conn
	dials    atomic.Int64
	failures atomic.Int64
}

func nvStartFakeDC(t *testing.T) *nvFakeDC {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake telegram dc: %v", err)
	}
	dc := &nvFakeDC{listener: listener, sessions: make(map[uint64]*nvDCSession)}
	go dc.accept()
	t.Cleanup(func() {
		listener.Close()
		dc.mu.Lock()
		defer dc.mu.Unlock()
		for _, conn := range dc.conns {
			conn.Close()
		}
	})
	return dc
}

func (d *nvFakeDC) accept() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		d.mu.Lock()
		d.conns = append(d.conns, conn)
		d.mu.Unlock()
		go d.serve(conn.(*net.TCPConn))
	}
}

func (d *nvFakeDC) serve(raw *net.TCPConn) {
	defer raw.Close()
	_, conn, err := (obfuscation.Obfuscator{}).ReadHandshake(raw)
	if err != nil {
		d.failures.Add(1)
		return
	}
	var id [8]byte
	if _, err := io.ReadFull(conn, id[:]); err != nil {
		return
	}
	session := &nvDCSession{closed: make(chan struct{})}
	session.received.Add(int64(len(id)))
	defer close(session.closed)
	d.mu.Lock()
	d.sessions[binary.BigEndian.Uint64(id[:])] = session
	d.mu.Unlock()
	if _, err := conn.Write(id[:]); err != nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			session.received.Add(int64(n))
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (d *nvFakeDC) await(id uint64, wait time.Duration) *nvDCSession {
	deadline := time.Now().Add(wait)
	for {
		d.mu.Lock()
		session := d.sessions[id]
		d.mu.Unlock()
		if session != nil || time.Now().After(deadline) {
			return session
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type nvDCRedirect struct {
	mtglib.Network
	address string
	dials   *atomic.Int64
}

func (n nvDCRedirect) Dial(network, address string) (essentials.Conn, error) {
	return n.DialContext(context.Background(), network, address)
}

func (n nvDCRedirect) DialContext(ctx context.Context, _, _ string) (essentials.Conn, error) {
	n.dials.Add(1)
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", n.address)
	if err != nil {
		return nil, err
	}
	return conn.(*net.TCPConn), nil
}

func nvRedirectDCs(t *testing.T, b *Backend, dc *nvFakeDC) {
	t.Helper()
	b.mu.RLock()
	redirected := 0
	for tag, inst := range b.instances {
		if inst.proxy == nil {
			continue
		}
		field := reflect.ValueOf(inst.proxy).Elem().FieldByName("network")
		if !field.IsValid() {
			b.mu.RUnlock()
			t.Fatalf("instance %q: mtglib.Proxy has no network field any more, the DC redirect cannot be installed", tag)
		}
		target := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
		original, ok := target.Interface().(mtglib.Network)
		if !ok || original == nil {
			b.mu.RUnlock()
			t.Fatalf("instance %q: mtglib.Proxy.network is %s, not a usable mtglib.Network", tag, field.Type())
		}
		redirect := nvDCRedirect{Network: original, address: dc.listener.Addr().String(), dials: &dc.dials}
		if !target.CanSet() || !reflect.TypeOf(redirect).AssignableTo(field.Type()) {
			b.mu.RUnlock()
			t.Fatalf("instance %q: mtglib.Proxy.network of type %s cannot take the DC redirect", tag, field.Type())
		}
		target.Set(reflect.ValueOf(redirect))
		redirected++
	}
	b.mu.RUnlock()
	if redirected == 0 {
		t.Fatal("no mtglib instance to redirect")
	}
	b.pushSecretsToInstances()
}

type nvSession struct {
	id   uint64
	raw  *net.TCPConn
	conn essentials.Conn
	dc   *nvDCSession
}

func nvOpen(port int, secret string, dc *nvFakeDC, id uint64) (*nvSession, error) {
	key, err := hex.DecodeString(secret)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		return nil, err
	}
	raw := conn.(*net.TCPConn)
	enc, err := (obfuscation.Obfuscator{Secret: key}).SendHandshake(raw, nvTelegramDC)
	if err != nil {
		raw.Close()
		return nil, err
	}
	payload := make([]byte, nvChunk)
	binary.BigEndian.PutUint64(payload, id)
	raw.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := enc.Write(payload); err != nil {
		raw.Close()
		return nil, fmt.Errorf("session %d: write: %w", id, err)
	}
	raw.SetWriteDeadline(time.Time{})
	session := dc.await(id, 2*time.Second)
	if session == nil {
		raw.Close()
		return nil, fmt.Errorf("session %d never reached the telegram dc", id)
	}
	echo := make([]byte, nvChunk)
	raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(enc, echo); err != nil {
		raw.Close()
		return nil, fmt.Errorf("session %d: no echo from the dc: %w", id, err)
	}
	raw.SetReadDeadline(time.Time{})
	if got := binary.BigEndian.Uint64(echo); got != id {
		raw.Close()
		return nil, fmt.Errorf("session %d: the dc echoed session %d", id, got)
	}
	return &nvSession{id: id, raw: raw, conn: enc, dc: session}, nil
}

func (s *nvSession) deliversToDC(wait time.Duration) bool {
	before := s.dc.received.Load()
	s.raw.SetWriteDeadline(time.Now().Add(time.Second))
	_, err := s.conn.Write(make([]byte, nvChunk))
	s.raw.SetWriteDeadline(time.Time{})
	if err != nil {
		return false
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if s.dc.received.Load() >= before+nvChunk {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return s.dc.received.Load() >= before+nvChunk
}

func (s *nvSession) roundTrip(wait time.Duration) error {
	select {
	case <-s.dc.closed:
		return errors.New("its telegram dc connection is closed")
	default:
	}
	if !s.deliversToDC(wait) {
		return errors.New("bytes written by the client never reached the telegram dc")
	}
	s.raw.SetReadDeadline(time.Now().Add(wait))
	defer s.raw.SetReadDeadline(time.Time{})
	if _, err := io.ReadFull(s.conn, make([]byte, nvChunk)); err != nil {
		return fmt.Errorf("the client read no echo: %w", err)
	}
	return nil
}

func (s *nvSession) clientSideClosed(wait time.Duration) bool {
	s.raw.SetReadDeadline(time.Now().Add(wait))
	defer s.raw.SetReadDeadline(time.Time{})
	buf := make([]byte, 4096)
	for {
		_, err := s.conn.Read(buf)
		if err == nil {
			continue
		}
		var netErr net.Error
		return !(errors.As(err, &netErr) && netErr.Timeout())
	}
}

func (s *nvSession) dcSideClosed(wait time.Duration) bool {
	select {
	case <-s.dc.closed:
		return true
	case <-time.After(wait):
		return false
	}
}

type nvEnv struct {
	backend *Backend
	port    int
	dc      *nvFakeDC
	ids     atomic.Uint64
}

func nvStart(t *testing.T) *nvEnv {
	t.Helper()
	dc := nvStartFakeDC(t)
	port := unusedListenPorts(t, 1)[0]
	raw := fmt.Sprintf(`{"instances":[{"tag":%q,"port":%d,"mode":"plain"}]}`, nvTag, port)
	backend := startBackend(t, raw, []*common.User{
		credentialedUser(nvAlice, initialSecretA, nvTag),
		credentialedUser(nvBob, initialSecretB, nvTag),
	})
	t.Cleanup(backend.Shutdown)
	nvRedirectDCs(t, backend, dc)
	return &nvEnv{backend: backend, port: port, dc: dc}
}

func (e *nvEnv) open(t *testing.T, secret string) *nvSession {
	t.Helper()
	session, err := nvOpen(e.port, secret, e.dc, e.ids.Add(1))
	if err != nil {
		t.Fatalf("precondition: the session never relayed through the proxy (dc dials=%d, dc handshake failures=%d): %v", e.dc.dials.Load(), e.dc.failures.Load(), err)
	}
	t.Cleanup(func() { session.raw.Close() })
	return session
}

type nvRemoval struct {
	name  string
	apply func(context.Context, *Backend) error
}

func nvRemovals() []nvRemoval {
	return []nvRemoval{
		{"SyncUser-no-inbounds", func(ctx context.Context, b *Backend) error {
			return b.SyncUser(ctx, credentialedUser(nvAlice, initialSecretA))
		}},
		{"UpdateUsers-no-mtproto-credential", func(ctx context.Context, b *Backend) error {
			return b.UpdateUsers(ctx, []*common.User{{Email: nvAlice, Proxies: &common.Proxy{}}})
		}},
		{"UpdateUsersAndRestart-no-mtproto-credential", func(ctx context.Context, b *Backend) error {
			return b.UpdateUsersAndRestart(ctx, []*common.User{{Email: nvAlice, Proxies: &common.Proxy{}}})
		}},
		{"SyncUsers-full-replace-without-alice", func(ctx context.Context, b *Backend) error {
			return b.SyncUsers(ctx, []*common.User{credentialedUser(nvBob, initialSecretB, nvTag)})
		}},
	}
}

func TestNodeVerifyRemovalCutsTheRemovedUsersLiveSession(t *testing.T) {
	for _, removal := range nvRemovals() {
		t.Run(removal.name, func(t *testing.T) {
			env := nvStart(t)
			alice := env.open(t, initialSecretA)
			bob := env.open(t, initialSecretB)
			if err := alice.roundTrip(2 * time.Second); err != nil {
				t.Fatalf("precondition: alice's session must relay both ways before the removal: %v", err)
			}
			if err := bob.roundTrip(2 * time.Second); err != nil {
				t.Fatalf("precondition: bob's session must relay both ways before the removal: %v", err)
			}

			if err := removal.apply(context.Background(), env.backend); err != nil {
				t.Fatalf("%s: %v", removal.name, err)
			}
			removedAt := time.Now()

			if !alice.dcSideClosed(nvCutBound) {
				t.Errorf("%s: alice's upstream to the telegram dc is still open %s after the removal returned", removal.name, nvCutBound)
			}
			time.Sleep(time.Until(removedAt.Add(nvCutBound)))
			before := alice.dc.received.Load()
			if !alice.clientSideClosed(300 * time.Millisecond) {
				t.Errorf("%s: alice's client connection is still open %s after the removal returned", removal.name, nvCutBound)
			}
			if alice.deliversToDC(500 * time.Millisecond) {
				t.Errorf("%s: alice's session still relayed client bytes to the telegram dc %s after the removal returned (dc counter %d -> %d)", removal.name, time.Since(removedAt).Round(time.Millisecond), before, alice.dc.received.Load())
			}

			if err := bob.roundTrip(2 * time.Second); err != nil {
				t.Errorf("%s: bob was not removed, yet his live session stopped relaying: %v", removal.name, err)
			}
			if _, err := nvOpen(env.port, initialSecretA, env.dc, env.ids.Add(1)); err == nil {
				t.Errorf("%s: a fresh handshake with alice's removed secret still reached the telegram dc", removal.name)
			}
			fresh := env.open(t, initialSecretB)
			if err := fresh.roundTrip(2 * time.Second); err != nil {
				t.Errorf("%s: a fresh bob session must relay: %v", removal.name, err)
			}
		})
	}
}

type nvOpened struct {
	session *nvSession
	user    string
}

func TestNodeVerifyInterleavedRemovalCutsEveryAliceSessionAndNoBobSession(t *testing.T) {
	env := nvStart(t)
	ctx := context.Background()
	aliceOn := credentialedUser(nvAlice, initialSecretA, nvTag)
	aliceOff := credentialedUser(nvAlice, initialSecretA)

	var (
		mu       sync.Mutex
		opened   []nvOpened
		bobFails atomic.Int64
		openers  sync.WaitGroup
	)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, o := range opened {
			o.session.raw.Close()
		}
	})
	stop := make(chan struct{})
	for worker := range 18 {
		user, secret := nvAlice, initialSecretA
		if worker%2 == 1 {
			user, secret = nvBob, initialSecretB
		}
		openers.Go(func() {
			for range 12 {
				select {
				case <-stop:
					return
				default:
				}
				session, err := nvOpen(env.port, secret, env.dc, env.ids.Add(1))
				if err != nil {
					if user == nvBob {
						bobFails.Add(1)
					}
					continue
				}
				mu.Lock()
				opened = append(opened, nvOpened{session: session, user: user})
				mu.Unlock()
				time.Sleep(time.Duration(rand.IntN(25)) * time.Millisecond)
			}
		})
	}

	time.Sleep(150 * time.Millisecond)
	if err := env.backend.SyncUser(ctx, aliceOff); err != nil {
		t.Fatalf("first removal: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := env.backend.SyncUser(ctx, aliceOn); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	readded := env.open(t, initialSecretA)
	if err := readded.roundTrip(2 * time.Second); err != nil {
		t.Fatalf("precondition: alice's session opened after the re-add must relay: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := env.backend.SyncUser(ctx, aliceOff); err != nil {
		t.Fatalf("final removal: %v", err)
	}
	removedAt := time.Now()
	time.Sleep(100 * time.Millisecond)
	close(stop)
	openers.Wait()
	time.Sleep(time.Until(removedAt.Add(nvCutBound)))

	if bobFails.Load() > 0 {
		t.Errorf("%d bob sessions failed to open although bob was never removed", bobFails.Load())
	}
	if readded.deliversToDC(500 * time.Millisecond) {
		t.Errorf("alice's session opened after the re-add still relays to the telegram dc %s after the final removal returned", nvCutBound)
	}

	mu.Lock()
	sessions := append([]nvOpened(nil), opened...)
	mu.Unlock()
	var (
		check        sync.WaitGroup
		aliceLive    atomic.Int64
		aliceTotal   atomic.Int64
		bobBroken    atomic.Int64
		bobTotal     atomic.Int64
		firstBobFail sync.Once
		bobFailure   error
	)
	for _, o := range sessions {
		check.Go(func() {
			if o.user == nvAlice {
				aliceTotal.Add(1)
				if o.session.deliversToDC(500 * time.Millisecond) {
					aliceLive.Add(1)
				}
				return
			}
			bobTotal.Add(1)
			if err := o.session.roundTrip(2 * time.Second); err != nil {
				bobBroken.Add(1)
				firstBobFail.Do(func() { bobFailure = err })
			}
		})
	}
	check.Wait()
	if aliceTotal.Load() == 0 || bobTotal.Load() == 0 {
		t.Fatalf("the storm opened %d alice and %d bob sessions, the interleaving was not exercised", aliceTotal.Load(), bobTotal.Load())
	}
	if aliceLive.Load() > 0 {
		t.Errorf("%d of %d alice sessions opened during the remove/re-add/remove storm still relay to the telegram dc %s after the final removal returned", aliceLive.Load(), aliceTotal.Load(), nvCutBound)
	}
	if bobBroken.Load() > 0 {
		t.Errorf("%d of %d bob sessions stopped relaying although bob was never removed: %v", bobBroken.Load(), bobTotal.Load(), bobFailure)
	}
	if _, err := nvOpen(env.port, initialSecretA, env.dc, env.ids.Add(1)); err == nil {
		t.Errorf("a fresh alice handshake still reached the telegram dc after the final removal")
	}
}
