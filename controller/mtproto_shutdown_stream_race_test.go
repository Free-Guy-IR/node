package controller

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func TestStoppingAnMTProtoCoreThatJustAcceptedConnectionsMustNotRaceOrPanic(t *testing.T) {
	t.Skip("pending the mtg re-pin in go.mod: the pinned mtg races Proxy.Shutdown's WaitGroup.Wait against ServeConn's Add, so -race fails this test; remove this skip when go.mod pins the fixed mtg")
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
