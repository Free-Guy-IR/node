package controller

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func TestStoppingAnMTProtoCoreThatJustAcceptedConnectionsMustNotRaceOrPanic(t *testing.T) {
	c := newWatchdogController(t)
	for round := 0; round < 300; round++ {
		var port int
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			port = unusedPort(t)
			err = panelStart(c, panelIP, &common.Backend{
				Type:      common.BackendType_MTPROTO,
				Config:    fmt.Sprintf(`{"instances":[{"tag":"accepting","port":%d,"mode":"plain"}]}`, port),
				KeepAlive: 60,
			})
			if err == nil {
				break
			}
		}
		if err != nil {
			t.Fatalf("round %d Start: %v", round, err)
		}
		for i := 0; i < 4; i++ {
			if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
				conn.Close()
			}
		}
		stopCore(c)
	}
}

func stopCore(c *Controller) {
	c.LockControl()
	defer c.UnlockControl()
	c.Disconnect()
}
