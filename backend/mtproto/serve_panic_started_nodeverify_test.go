package mtproto

import (
	"net"
	"testing"

	"github.com/9seconds/mtg/v2/mtglib"
)

func TestNodeVerifyAServeLoopThatPanickedIsNotStarted(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	address := listener.Addr().String()

	pi := &proxyInstance{tag: backstopTag, proxy: &mtglib.Proxy{}, record: func(string) {}}
	b := &Backend{instances: map[string]*proxyInstance{backstopTag: pi}, logsChan: make(chan string, 64)}
	pi.serve(listener)

	_ = dialAndDrop(t, address)
	if !pi.awaitServeExit() {
		t.Fatal("serve loop did not exit")
	}
	if err := dialAndDrop(t, address); err == nil {
		t.Fatal("listener still accepts")
	}
	if b.Started() {
		t.Fatal("Started() reports true for an instance whose serve loop panicked and closed its listener")
	}
}
