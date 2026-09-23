package mtproto

import (
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestRestartAfterShutdownFailsAndListensOnNothing(t *testing.T) {
	port := unusedListenPorts(t, 1)[0]
	backend := startBackend(t, fmt.Sprintf(`{"instances":[{"tag":"mtproto-tg","port":%d,"fake_tls_domain":"localhost"}]}`, port), nil)
	backend.Shutdown()

	if err := backend.Restart(); err == nil {
		t.Fatal("Restart after Shutdown must fail instead of listening again on a stopped proxy")
	}

	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("a stopped backend must not accept connections after a Restart attempt")
	}
}
