package preauth_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
)

const (
	helperEnv        = "PREAUTH_NODE_HELPER"
	largestPanelSize = 10000
	inboundsPerUser  = 60
	clientSendLimit  = 128 * 1024 * 1024
)

func TestPreauthNodeHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	addr, certPath := startNode(t, config.NewTestConfig(t.TempDir(), uuid.New()))
	fmt.Printf("READY %s %s\n", addr, certPath)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func rssKiB(pid int) int {
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "VmRSS:" {
				v, _ := strconv.Atoi(fields[1])
				return v
			}
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return -1
	}
	v, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return v
}

func startHelperNode(t *testing.T) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPreauthNodeHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	var addr, certPath string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && fields[0] == "READY" {
			addr, certPath = fields[1], fields[2]
			break
		}
	}
	if addr == "" {
		t.Fatal("helper node never became ready")
	}
	go func() {
		for scanner.Scan() {
		}
	}()
	return cmd.Process.Pid, addr, certPath
}

func TestUnauthenticatedCallerMustNotMakeTheNodeBufferLargeMessages(t *testing.T) {
	pid, addr, certPath := startHelperNode(t)

	const perCall = 40 * 1024 * 1024
	const calls = 4
	conn := dialNode(t, addr, certPath, grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(clientSendLimit)))
	client := common.NewNodeServiceClient(conn)
	payload := strings.Repeat("A", perCall)

	baseline := rssKiB(pid)
	var peak atomic.Int64
	peak.Store(int64(baseline))
	stopSampling := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stopSampling:
				return
			case <-time.After(20 * time.Millisecond):
				if v := int64(rssKiB(pid)); v > peak.Load() {
					peak.Store(v)
				}
			}
		}
	}()

	var wg sync.WaitGroup
	codesSeen := make([]codes.Code, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(withKey(uuid.New().String()), 60*time.Second)
			defer cancel()
			_, err := client.Start(ctx, &common.Backend{Type: common.BackendType_XRAY, Config: payload})
			codesSeen[i] = status.Code(err)
		}(i)
	}
	wg.Wait()
	time.Sleep(300 * time.Millisecond)
	close(stopSampling)
	sampler.Wait()

	for i, c := range codesSeen {
		if c != codes.PermissionDenied {
			t.Fatalf("call %d: expected PermissionDenied for a wrong api key, got %v", i, c)
		}
	}
	growthMiB := (int(peak.Load()) - baseline) / 1024
	t.Logf("node RSS baseline %d MiB, peak %d MiB, growth %d MiB for %d unauthenticated calls of %d MiB", baseline/1024, peak.Load()/1024, growthMiB, calls, perCall/1024/1024)
	if growthMiB > 32 {
		t.Fatalf("the node buffered and decoded %d unauthenticated %d MiB requests before rejecting them: RSS grew %d MiB (baseline %d MiB, peak %d MiB)",
			calls, perCall/1024/1024, growthMiB, baseline/1024, peak.Load()/1024)
	}
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

func panelUser(id int, tags []string) *common.User {
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	wgKey := make([]byte, 32)
	_, _ = rand.Read(wgKey)
	name := strconv.Itoa(id)
	return &common.User{
		Email: name,
		Proxies: &common.Proxy{
			Vmess:       &common.Vmess{Id: uuid.NewString()},
			Vless:       &common.Vless{Id: uuid.NewString(), Flow: "xtls-rprx-vision"},
			Trojan:      &common.Trojan{Password: randomToken(32)},
			Shadowsocks: &common.Shadowsocks{Password: randomToken(32), Method: "chacha20-ietf-poly1305"},
			Wireguard:   &common.Wireguard{PublicKey: base64.StdEncoding.EncodeToString(wgKey), PeerIps: []string{fmt.Sprintf("fd00::%x/128", id)}},
			Hysteria:    &common.Hysteria{Auth: randomToken(32)},
			Hysteria2:   &common.Hysteria2{Password: randomToken(32)},
			OpenVpn:     &common.OpenVpnUser{Username: name, Password: randomToken(32), MaxConcurrentConnections: 3},
			Mtproto:     &common.MtprotoUser{Username: name, Secret: hex.EncodeToString(secret)},
			Tuic:        &common.Tuic{Uuid: uuid.NewString(), Password: randomToken(32)},
			L2Tp:        &common.L2TpUser{Username: name, Password: randomToken(24)},
		},
		Inbounds: tags,
	}
}

func largestPanelUsers(extraTags ...string) []*common.User {
	tags := make([]string, 0, inboundsPerUser+len(extraTags))
	for i := 0; i < inboundsPerUser; i++ {
		tags = append(tags, fmt.Sprintf("VLESS-XHTTP-SPLIT-NODE-%02d-UPLOAD", i))
	}
	tags = append(tags, extraTags...)
	users := make([]*common.User, largestPanelSize)
	for i := range users {
		users[i] = panelUser(100000+i, tags)
	}
	return users
}

func largeXrayConfig() string {
	var b strings.Builder
	b.WriteString(`{"routing":{"rules":[{"type":"field","outboundTag":"BLOCK","domain":[`)
	for i := 0; i < 45000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"domain:blocked-site-%05d.example"`, i)
	}
	b.WriteString(`]}]}}`)
	return b.String()
}

func authenticatedClient(t *testing.T) (common.NodeServiceClient, uuid.UUID) {
	t.Helper()
	key := uuid.New()
	addr, certPath := startNode(t, config.NewTestConfig(t.TempDir(), key))
	conn := dialNode(t, addr, certPath, grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(clientSendLimit)))
	return common.NewNodeServiceClient(conn), key
}

func TestStartCarryingTheLargestPanelFitsTheReceiveLimit(t *testing.T) {
	client, key := authenticatedClient(t)

	ctx, cancel := context.WithTimeout(withKey(key.String()), 60*time.Second)
	defer cancel()
	_, err := client.Start(ctx, &common.Backend{
		Type:      common.BackendType(99),
		Config:    largeXrayConfig(),
		Users:     largestPanelUsers(),
		KeepAlive: 60,
	})
	if status.Code(err) == codes.ResourceExhausted {
		t.Fatalf("a Start carrying %d users with %d inbound tags each and a 1.6 MiB config must fit the node's receive limit, got %v", largestPanelSize, inboundsPerUser, err)
	}
	if err == nil || !strings.Contains(err.Error(), "invalid backend type") {
		t.Fatalf("the node must decode the whole Start and then refuse the unknown backend type, got %v", err)
	}
}

func TestAuthenticatedMessageAboveTheReceiveLimitIsRefused(t *testing.T) {
	client, key := authenticatedClient(t)

	ctx, cancel := context.WithTimeout(withKey(key.String()), 60*time.Second)
	defer cancel()
	_, err := client.Start(ctx, &common.Backend{
		Type:   common.BackendType(99),
		Config: strings.Repeat("A", 56*1024*1024),
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("a 56 MiB Start is larger than anything a panel sends and must be refused before it is decoded, got %v", err)
	}
}

func TestChunkedSyncOfTheLargestPanelPassesTheApiKeyCheck(t *testing.T) {
	client, key := authenticatedClient(t)

	port := unusedListenPort(t)

	ctx, cancel := context.WithTimeout(withKey(key.String()), 60*time.Second)
	defer cancel()
	if _, err := client.Start(ctx, &common.Backend{
		Type:      common.BackendType_MTPROTO,
		Config:    fmt.Sprintf(`{"instances":[{"tag":"preauth","port":%d,"mode":"plain"}]}`, port),
		KeepAlive: 60,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	users := largestPanelUsers("preauth")
	stream, err := client.SyncUsersChunked(ctx)
	if err != nil {
		t.Fatalf("SyncUsersChunked: %v", err)
	}
	const chunkSize = 100
	for index, start := 0, 0; start < len(users); index, start = index+1, start+chunkSize {
		end := min(start+chunkSize, len(users))
		if err := stream.Send(&common.UsersChunk{Users: users[start:end], Index: uint64(index), Last: end == len(users)}); err != nil {
			t.Fatalf("send chunk %d: %v", index, err)
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		t.Fatalf("a chunked sync of %d users must succeed, got %v", len(users), err)
	}
}
