package preauth_test

import (
	"math/rand/v2"
	"net"
	"testing"
)

const (
	firstTestPort = 20000
	testPortSpan  = 10000
)

func unusedListenPort(t *testing.T) int {
	t.Helper()
	for range 1000 {
		port := firstTestPort + rand.IntN(testPortSpan)
		if listenPortIsFree(port) {
			return port
		}
	}
	t.Fatalf("no port between %d and %d is free on both 127.0.0.1 and the wildcard", firstTestPort, firstTestPort+testPortSpan-1)
	return 0
}

func listenPortIsFree(port int) bool {
	for _, addr := range []*net.TCPAddr{{IP: net.IPv4(127, 0, 0, 1), Port: port}, {Port: port}} {
		l, err := net.ListenTCP("tcp", addr)
		if err != nil {
			return false
		}
		_ = l.Close()
	}
	return true
}
