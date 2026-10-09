package xray

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

const (
	progressingBulkUsers = 26
	progressingAddDelay  = 500 * time.Millisecond
)

func TestAHealthRestartDoesNotAbortABulkUpdateThatIsStillApplyingUsers(t *testing.T) {
	h := newRestartHarness(t, syncTestInbounds)

	reached := make(chan struct{})
	var once sync.Once
	h.core.setFault(func(op, tag, email string) (bool, error) {
		if op == addOperation {
			once.Do(func() { close(reached) })
			time.Sleep(progressingAddDelay)
		}
		return false, nil
	})

	users := make([]*common.User, 0, progressingBulkUsers)
	for index := range progressingBulkUsers {
		users = append(users, syncTestUser(fmt.Sprintf("%d.bulk", index+1), syncTestTags...))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	updated := make(chan error, 1)
	go func() { updated <- h.x.UpdateUsers(ctx, users) }()
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the bulk update never reached the core")
	}

	restarted := make(chan error, 1)
	go func() { restarted <- h.x.Restart() }()

	var updateErr error
	select {
	case updateErr = <-updated:
	case <-time.After(4 * time.Minute):
		t.Fatal("the bulk update never returned")
	}
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("the health restart never finished")
	}

	local := localState(h.x)
	for _, tag := range syncTestTags {
		if held := len(local[tag]); held != progressingBulkUsers {
			t.Errorf("after the health restart the node holds %d of the %d bulk users on %s", held, progressingBulkUsers, tag)
		}
	}
	h.requireWrittenMatchesLocal(t)
	if updateErr != nil {
		t.Fatalf("a health restart aborted a bulk update whose every core call was still answering within %s, and %d core calls failed", progressingAddDelay, strings.Count(updateErr.Error(), "\n"))
	}
}
