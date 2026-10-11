package l2tp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	chapKeptComment = "# kept by every inbound"
	chapForeignLine = `"bob" another-inbound "pw-bob" *`
)

func chapEnv(t *testing.T) *acctEnv {
	t.Helper()
	e := newAcctEnv(t)
	prev := chapSecretsPath
	chapSecretsPath = filepath.Join(t.TempDir(), "ppp", "chap-secrets")
	t.Cleanup(func() { chapSecretsPath = prev })
	e.write(chapSecretsPath, chapKeptComment+"\n"+chapForeignLine+"\n")
	return e
}

func expectChapSecrets(t *testing.T, step string, e *acctEnv, usernames ...string) {
	t.Helper()
	raw, err := os.ReadFile(chapSecretsPath)
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
	var tagged []string
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	for _, line := range lines {
		if f := strings.Fields(line); len(f) >= 2 && f[1] == e.tag {
			tagged = append(tagged, strings.Trim(f[0], `"`))
		}
	}
	sort.Strings(tagged)
	want := append([]string(nil), usernames...)
	sort.Strings(want)
	if !slices.Equal(tagged, want) {
		t.Fatalf("%s: chap-secrets holds users %v of %s, want %v:\n%s", step, tagged, e.tag, want, raw)
	}
	if !slices.Contains(lines, chapKeptComment) || !slices.Contains(lines, chapForeignLine) {
		t.Fatalf("%s: chap-secrets lost the lines of other inbounds:\n%s", step, raw)
	}
}

type userSyncCall struct {
	name string
	call func(o *L2TP, e *acctEnv) error
}

func userSyncCalls() []userSyncCall {
	ctx := context.Background()
	return []userSyncCall{
		{"SyncUser", func(o *L2TP, e *acctEnv) error { return o.SyncUser(ctx, e.users("8")[0]) }},
		{"SyncUsers", func(o *L2TP, e *acctEnv) error { return o.SyncUsers(ctx, e.users("7", "8")) }},
		{"UpdateUsers", func(o *L2TP, e *acctEnv) error { return o.UpdateUsers(ctx, e.users("8")) }},
		{"UpdateUsersAndRestart", func(o *L2TP, e *acctEnv) error { return o.UpdateUsersAndRestart(ctx, e.users("8")) }},
	}
}

func runningWithChapSecrets(t *testing.T, e *acctEnv, usernames ...string) *L2TP {
	t.Helper()
	o := e.instance(usernames...)
	if err := o.SyncUsers(context.Background(), e.users(usernames...)); err != nil {
		t.Fatalf("SyncUsers on a running instance: %v", err)
	}
	expectChapSecrets(t, "running", e, usernames...)
	return o
}

func TestUserSyncAfterShutdownLeavesTheInboundOutOfTheChapSecrets(t *testing.T) {
	for _, c := range userSyncCalls() {
		t.Run(c.name, func(t *testing.T) {
			e := chapEnv(t)
			o := runningWithChapSecrets(t, e, "7")
			o.Shutdown()
			expectChapSecrets(t, "after the shutdown", e)
			if err := c.call(o, e); !errors.Is(err, errNotStarted) {
				t.Fatalf("%s after the shutdown returned %v, want %v", c.name, err, errNotStarted)
			}
			expectChapSecrets(t, c.name+" after the shutdown", e)
		})
	}
}

func TestUserSyncOnAShutDownInstanceNeverTouchesTheReplacementsCredentials(t *testing.T) {
	e := chapEnv(t)
	a := runningWithChapSecrets(t, e, "7")
	a.Shutdown()
	b := runningWithChapSecrets(t, e, "9")
	for _, c := range userSyncCalls() {
		_ = c.call(a, e)
		expectChapSecrets(t, c.name+" on the shut-down instance while its replacement runs", e, "9")
	}
	if err := b.UpdateUsers(context.Background(), e.users("9", "10")); err != nil {
		t.Fatalf("UpdateUsers on the replacement: %v", err)
	}
	expectChapSecrets(t, "the replacement's own sync", e, "9", "10")
}

func TestUserSyncWritingWhenShutdownStartsIsRemovedByTheShutdown(t *testing.T) {
	e := chapEnv(t)
	o := runningWithChapSecrets(t, e, "7")
	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	prev := beforeChapSecretsWrite
	t.Cleanup(func() { beforeChapSecretsWrite = prev })
	beforeChapSecretsWrite = func() {
		once.Do(func() {
			close(parked)
			<-release
		})
	}
	synced := make(chan error, 1)
	go func() { synced <- o.UpdateUsers(context.Background(), e.users("8")) }()
	<-parked
	stopped := make(chan struct{})
	go func() {
		o.Shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	for _, done := range []<-chan struct{}{waitErr(synced), stopped} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the sync or the shutdown did not finish")
		}
	}
	expectChapSecrets(t, "after a sync that was writing when the shutdown started", e)
}

func waitErr(c <-chan error) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		<-c
		close(done)
	}()
	return done
}

func TestUserSyncsRacingShutdownAndAReplacementKeepTheChapSecretsConsistent(t *testing.T) {
	iterations := 40
	if testing.Short() {
		iterations = 8
	}
	ctx := context.Background()
	for i := 0; i < iterations; i++ {
		e := chapEnv(t)
		a := runningWithChapSecrets(t, e, "7")
		racing := []func(o *L2TP){
			func(o *L2TP) { _ = o.UpdateUsers(ctx, e.users("8")) },
			func(o *L2TP) { _ = o.SyncUser(ctx, e.users("11")[0]) },
			func(o *L2TP) { _ = o.SyncUsers(ctx, e.users("7", "8")) },
			func(o *L2TP) { _ = o.UpdateUsers(ctx, e.users("7", "12")) },
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < 6; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for k := 0; k < 4; k++ {
					racing[(g+k)%len(racing)](a)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a.Shutdown()
		}()
		close(start)
		wg.Wait()
		expectChapSecrets(t, "after syncs that raced the shutdown", e)

		b := runningWithChapSecrets(t, e, "9")
		start = make(chan struct{})
		for g := 0; g < 6; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for k := 0; k < 4; k++ {
					racing[(g+k)%len(racing)](a)
					_ = b.UpdateUsers(ctx, e.users("9"))
				}
			}()
		}
		close(start)
		wg.Wait()
		expectChapSecrets(t, "after late syncs of the shut-down instance raced its replacement", e, "9")
		if t.Failed() {
			return
		}
	}
}
