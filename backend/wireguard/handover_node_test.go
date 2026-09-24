package wireguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func interfaceStatRequests(interfaceName string, reset bool) map[string]*common.StatRequest {
	return map[string]*common.StatRequest{
		"node level":    {Type: common.StatType_Outbounds, Reset_: reset},
		"per interface": {Type: common.StatType_Outbound, Name: interfaceName, Reset_: reset},
	}
}

func readInterfaceTraffic(t *testing.T, wg *WireGuard, interfaceName string, request *common.StatRequest) (int64, int64) {
	t.Helper()

	resp, err := wg.GetStats(context.Background(), request)
	if err != nil {
		t.Fatalf("%v read failed: %v", request.GetType(), err)
	}
	return interfaceTraffic(t, resp, interfaceName)
}

func interfaceTraffic(t *testing.T, resp *common.StatResponse, interfaceName string) (int64, int64) {
	t.Helper()

	var uplink, downlink int64
	for _, stat := range resp.GetStats() {
		if stat.GetName() != interfaceName || stat.GetLink() != "interface" {
			t.Fatalf("interface stat labelled %q/%q, want %q/interface", stat.GetName(), stat.GetLink(), interfaceName)
		}
		switch stat.GetType() {
		case "uplink":
			uplink += stat.GetValue()
		case "downlink":
			downlink += stat.GetValue()
		default:
			t.Fatalf("unexpected interface stat type %q", stat.GetType())
		}
	}
	return uplink, downlink
}

func expectInterfaceTraffic(t *testing.T, wg *WireGuard, interfaceName string, request *common.StatRequest, wantUplink, wantDownlink int64) {
	t.Helper()

	uplink, downlink := readInterfaceTraffic(t, wg, interfaceName, request)
	if uplink != wantUplink || downlink != wantDownlink {
		t.Fatalf("%v read reported uplink %d downlink %d, want uplink %d downlink %d", request.GetType(), uplink, downlink, wantUplink, wantDownlink)
	}
}

func TestAccountingInterfaceCountsTheNewInterfaceFromZero(t *testing.T) {
	for kind, request := range interfaceStatRequests("wgacct-ifzero", true) {
		t.Run(kind, func(t *testing.T) {
			k := newAccountingKernel()
			iface := "wgacct-ifzero"
			wg := startAccountingBackend(t, k, iface, []*common.User{accountingUser(iface, "zero@example.com", mustGeneratePublicKey(t), 2)})

			k.addInterfaceTraffic(t, 1000, 400)
			expectInterfaceTraffic(t, wg, iface, request, 1000, 400)
			expectInterfaceTraffic(t, wg, iface, request, 0, 0)

			k.addInterfaceTraffic(t, 30, 70)
			expectInterfaceTraffic(t, wg, iface, request, 30, 70)
		})
	}
}

func TestAccountingInterfaceHandoverAcrossShutdownAndNew(t *testing.T) {
	for kind, request := range interfaceStatRequests("wgacct-ifhand", true) {
		t.Run(kind, func(t *testing.T) {
			k := newAccountingKernel()
			iface := "wgacct-ifhand"
			key := mustGeneratePublicKey(t)
			users := []*common.User{accountingUser(iface, "carried@example.com", key, 2)}

			first := startAccountingBackend(t, k, iface, users)
			expectInterfaceTraffic(t, first, iface, request, 0, 0)
			k.addInterfaceTraffic(t, 1000, 400)
			expectInterfaceTraffic(t, first, iface, request, 1000, 400)

			k.addInterfaceTraffic(t, 300, 100)
			first.Shutdown()

			second := startAccountingBackend(t, k, iface, users)
			peek := interfaceStatRequests(iface, false)[kind]
			expectInterfaceTraffic(t, second, iface, peek, 300, 100)

			k.addInterfaceTraffic(t, 20, 10)
			expectInterfaceTraffic(t, second, iface, peek, 300+20, 100+10)
			expectInterfaceTraffic(t, second, iface, request, 300+20, 100+10)
			expectInterfaceTraffic(t, second, iface, request, 0, 0)

			k.addInterfaceTraffic(t, 5, 6)
			expectInterfaceTraffic(t, second, iface, request, 5, 6)

			if _, err := first.GetStats(context.Background(), request); err == nil {
				t.Fatal("expected an interface read on the stopped backend to fail")
			}
		})
	}
}

func TestAccountingInterfaceHandoverSurvivesAFailedNew(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-iffail"
	users := []*common.User{accountingUser(iface, "failed@example.com", mustGeneratePublicKey(t), 2)}
	request := &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}

	first := startAccountingBackend(t, k, iface, users)
	k.addInterfaceTraffic(t, 700, 300)
	first.Shutdown()

	k.failNextConfigure(errors.New("configure refused"))
	if _, err := newAccountingBackend(t, k, iface, users); err == nil {
		t.Fatal("expected the backend start to fail")
	}
	k.failNextConfigure(nil)

	second := startAccountingBackend(t, k, iface, users)
	k.addInterfaceTraffic(t, 7, 3)
	expectInterfaceTraffic(t, second, iface, request, 707, 303)
}

func TestAccountingInterfaceHandoverSurvivesAFailedManager(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-ifmgr"
	users := []*common.User{accountingUser(iface, "manager@example.com", mustGeneratePublicKey(t), 2)}
	request := &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}

	first := startAccountingBackend(t, k, iface, users)
	k.addInterfaceTraffic(t, 900, 100)
	first.Shutdown()

	privateKey, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	wgCfg := mustNewWireGuardConfigForLifecycle(t, `{"interface_name":"`+iface+`","listen_port":51820,"private_key":"`+privateKey+`"}`)
	failing := func(string) (*Manager, error) { return nil, errors.New("no wgctrl") }
	if _, err := newWithManagerFactory(accountingNodeConfig(), wgCfg, users, failing); err == nil {
		t.Fatal("expected the backend start to fail")
	}

	second := startAccountingBackend(t, k, iface, users)
	k.addInterfaceTraffic(t, 1, 2)
	expectInterfaceTraffic(t, second, iface, request, 901, 102)
}

func TestAccountingInterfaceHandoverIsKeyedByInterfaceName(t *testing.T) {
	kA := newAccountingKernel()
	kB := newAccountingKernel()
	ifaceA := "wgacct-ifkey-a"
	ifaceB := "wgacct-ifkey-b"
	key := mustGeneratePublicKey(t)
	request := &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}

	first := startAccountingBackend(t, kA, ifaceA, []*common.User{accountingUser(ifaceA, "keyed@example.com", key, 2)})
	kA.addInterfaceTraffic(t, 800, 200)
	first.Shutdown()

	other := startAccountingBackend(t, kB, ifaceB, []*common.User{accountingUser(ifaceB, "keyed@example.com", key, 2)})
	kB.addInterfaceTraffic(t, 5, 6)
	expectInterfaceTraffic(t, other, ifaceB, request, 5, 6)

	second := startAccountingBackend(t, kA, ifaceA, []*common.User{accountingUser(ifaceA, "keyed@example.com", key, 2)})
	expectInterfaceTraffic(t, second, ifaceA, request, 800, 200)
}

func TestAccountingInterfaceReadInFlightDuringShutdownIsCountedOnce(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-ifflight"
	users := []*common.User{accountingUser(iface, "flight@example.com", mustGeneratePublicKey(t), 2)}
	request := &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}

	first := startAccountingBackend(t, k, iface, users)
	k.addInterfaceTraffic(t, 100, 50)

	gate := k.gateNextLinkRead()
	inFlight := make(chan *common.StatResponse, 1)
	go func() {
		resp, err := first.GetStats(context.Background(), request)
		if err != nil {
			resp = &common.StatResponse{}
		}
		inFlight <- resp
	}()

	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the interface read never reached the device")
	}

	shutdown := make(chan struct{})
	go func() {
		first.Shutdown()
		close(shutdown)
	}()

	k.addInterfaceTraffic(t, 30, 20)
	close(gate.release)

	var inFlightResp *common.StatResponse
	select {
	case inFlightResp = <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight interface read never finished")
	}
	select {
	case <-shutdown:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never finished")
	}
	inFlightUplink, inFlightDownlink := interfaceTraffic(t, inFlightResp, iface)

	second := startAccountingBackend(t, k, iface, users)
	k.addInterfaceTraffic(t, 5, 5)
	uplink, downlink := readInterfaceTraffic(t, second, iface, request)

	if inFlightUplink+uplink != 135 || inFlightDownlink+downlink != 75 {
		t.Fatalf("in-flight read %d/%d plus successor %d/%d, want 135/75 in total", inFlightUplink, inFlightDownlink, uplink, downlink)
	}
}

func TestAccountingInterfaceReadsSampledOutOfOrderAreCountedOnce(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-iforder"
	users := []*common.User{accountingUser(iface, "order@example.com", mustGeneratePublicKey(t), 2)}
	request := &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true}

	wg := startAccountingBackend(t, k, iface, users)
	expectInterfaceTraffic(t, wg, iface, request, 0, 0)
	k.addInterfaceTraffic(t, 100, 50)

	read := func(into chan<- *common.StatResponse) {
		resp, err := wg.GetStats(context.Background(), request)
		if err != nil {
			resp = &common.StatResponse{}
		}
		into <- resp
	}

	gate := k.gateNextLinkRead()
	slow := make(chan *common.StatResponse, 1)
	go read(slow)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow interface read never reached the device")
	}

	k.addInterfaceTraffic(t, 20, 10)
	fast := make(chan *common.StatResponse, 1)
	go read(fast)
	select {
	case resp := <-fast:
		fast <- resp
	case <-time.After(200 * time.Millisecond):
	}
	close(gate.release)

	var uplink, downlink int64
	for _, results := range []chan *common.StatResponse{slow, fast} {
		select {
		case resp := <-results:
			readUplink, readDownlink := interfaceTraffic(t, resp, iface)
			uplink += readUplink
			downlink += readDownlink
		case <-time.After(5 * time.Second):
			t.Fatal("an interface read never finished")
		}
	}
	lastUplink, lastDownlink := readInterfaceTraffic(t, wg, iface, request)
	uplink += lastUplink
	downlink += lastDownlink

	if uplink != 120 || downlink != 60 {
		t.Fatalf("interface reads reported uplink %d downlink %d, the interface received 120 and sent 60", uplink, downlink)
	}
}

func TestAccountingInterfaceUplinkIsWhatClientsSentAcrossHandover(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgacct-ifdir"
	users := []*common.User{accountingUser(iface, "dir@example.com", mustGeneratePublicKey(t), 2)}

	first := startAccountingBackend(t, k, iface, users)
	k.addInterfaceTraffic(t, 10, 90)
	first.Shutdown()

	second := startAccountingBackend(t, k, iface, users)
	resp, err := second.GetStats(context.Background(), &common.StatRequest{Type: common.StatType_Outbounds, Reset_: true})
	if err != nil {
		t.Fatalf("Outbounds read failed: %v", err)
	}
	values := map[string]int64{}
	for _, stat := range resp.GetStats() {
		values[stat.GetType()] += stat.GetValue()
	}
	if values["uplink"] != 10 || values["downlink"] != 90 {
		t.Fatalf("interface received 10 and sent 90 before the hand-over, got %v", values)
	}
}
