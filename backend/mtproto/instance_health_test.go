package mtproto

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func instanceStateOf(b *Backend, tag string) (InstanceState, bool) {
	for _, state := range b.InstanceStates() {
		if state.Tag == tag {
			return state, true
		}
	}
	return InstanceState{}, false
}

func awaitInstanceState(b *Backend, tag string, within time.Duration, match func(InstanceState) bool) (InstanceState, bool) {
	deadline := time.Now().Add(within)
	for {
		state, found := instanceStateOf(b, tag)
		if found && match(state) {
			return state, true
		}
		if time.Now().After(deadline) {
			return state, false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func countLinesContaining(lines []string, fragment string) int {
	count := 0
	for _, line := range lines {
		if strings.Contains(line, fragment) {
			count++
		}
	}
	return count
}

func TestInstanceHealthReportsADeadListenerWithItsReasonAndClearsOnRecovery(t *testing.T) {
	ports := unusedListenPorts(t, 2)
	raw := fmt.Sprintf(`{"instances":[{"tag":"mtproto-dead","port":%d,"mode":"plain"},{"tag":"mtproto-alive","port":%d,"mode":"plain"}]}`, ports[0], ports[1])
	b := startBackend(t, raw, nil)
	t.Cleanup(b.Shutdown)

	for _, tag := range []string{"mtproto-dead", "mtproto-alive"} {
		state, found := instanceStateOf(b, tag)
		if !found {
			t.Fatalf("InstanceStates does not list instance %q", tag)
		}
		if state.Dead || !state.DeadSince.IsZero() {
			t.Fatalf("instance %q is reported dead before any fault: %+v", tag, state)
		}
	}
	drainedLogs(b)

	release := holdInstanceDead(t, b, "mtproto-dead", ports[0])
	before := time.Now()
	dead, ok := awaitInstanceState(b, "mtproto-dead", 3*time.Second, func(s InstanceState) bool {
		return s.Dead && s.LastError != "" && !s.DeadSince.IsZero() && s.RelistenAttempts >= 2
	})
	if !ok {
		release()
		t.Fatalf("a dead listener whose port is taken is not reported dead with its reason and its relisten attempts: %+v", dead)
	}
	if dead.DeadSince.After(before) {
		release()
		t.Fatalf("DeadSince %v is later than the moment the instance was already dead (%v)", dead.DeadSince, before)
	}
	if alive, _ := instanceStateOf(b, "mtproto-alive"); alive.Dead {
		release()
		t.Fatalf("the healthy instance is reported dead too: %+v", alive)
	}

	release()
	recovered, ok := awaitInstanceState(b, "mtproto-dead", 5*time.Second, func(s InstanceState) bool { return !s.Dead })
	if !ok {
		t.Fatalf("the instance is still reported dead after its port was freed: %+v", recovered)
	}
	if !recovered.DeadSince.IsZero() {
		t.Fatalf("a recovered instance still carries a dead-since time: %+v", recovered)
	}
	if !awaitServed(ports[0], 5*time.Second) {
		t.Fatal("the recovered instance does not serve")
	}

	time.Sleep(50 * time.Millisecond)
	lines := drainedLogs(b)
	if got := countLinesContaining(lines, `mtproto instance "mtproto-dead" listener dead: `); got != 1 {
		t.Fatalf("want exactly one dead line for one dead window, got %d in %q", got, lines)
	}
	if got := countLinesContaining(lines, `mtproto instance "mtproto-dead" listener recovered`); got != 1 {
		t.Fatalf("want exactly one recovered line for one recovery, got %d in %q", got, lines)
	}
	if got := countLinesContaining(lines, `"mtproto-alive" listener`); got != 0 {
		t.Fatalf("the healthy instance logged a state change: %q", lines)
	}
}
