package wireguard

import (
	"testing"
	"time"

	"github.com/pasarguard/node/common"
)

func TestWireGuardBackendCountsAnUnmarkedRecreatedPeerFromZero(t *testing.T) {
	k := newAccountingKernel()
	iface := "wgdip-recreate"
	key := mustGeneratePublicKey(t)
	const email = "recreated@example.com"
	wg := startAccountingBackend(t, k, iface, []*common.User{accountingUser(iface, email, key, 2)})

	k.setPeer(t, key, 1000, 500, time.Now(), loopbackEndpoint())
	tick(wg)
	expectUserTraffic(t, readAccountingStats(t, wg), email, 1000, 500)

	k.setPeer(t, key, 1200, 600, time.Now(), loopbackEndpoint())
	tick(wg)
	k.setPeer(t, key, 300, 200, time.Now(), loopbackEndpoint())
	tick(wg)
	expectUserTraffic(t, readAccountingStats(t, wg), email, 500, 300)

	k.setPeer(t, key, 340, 230, time.Now(), loopbackEndpoint())
	tick(wg)
	expectUserTraffic(t, readAccountingStats(t, wg), email, 40, 30)
}
