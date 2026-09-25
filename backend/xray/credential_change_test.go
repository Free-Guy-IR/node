package xray

import (
	"testing"

	"github.com/pasarguard/node/common"
)

func TestAShadowsocksMethodChangeTouchesOnlyInboundsWhoseAccountCarriesIt(t *testing.T) {
	for name, apply := range userAppliers() {
		t.Run(name, func(t *testing.T) {
			core := newModelCore(false)
			x := newModelXray(t, core, syncTestInbounds)
			ctx := syncTestContext(t)
			user := syncTestUser("1.alice", "ss", "ss2022")

			if err := apply(ctx, x, user); err != nil {
				t.Fatal(err)
			}
			core.take()

			user.Proxies.Shadowsocks.Method = "aes-256-gcm"
			if err := apply(ctx, x, user); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), rotatedOn(false, false, "ss")...)
			requireCoreMatchesLocal(t, x, core)
		})
	}
}

func TestAVlessFlowChangeRevokesTheOldAccount(t *testing.T) {
	for name, apply := range userAppliers() {
		t.Run(name, func(t *testing.T) {
			core := newModelCore(false)
			x := newModelXray(t, core, syncTestInbounds)
			ctx := syncTestContext(t)
			user := &common.User{
				Email:    "1.alice",
				Inbounds: []string{"vless"},
				Proxies:  &common.Proxy{Vless: &common.Vless{Id: "0b6f2a4e-6a54-4a51-9a3a-6f1e2b1f7c11"}},
			}

			if err := apply(ctx, x, user); err != nil {
				t.Fatal(err)
			}
			core.take()

			user.Proxies.Vless.Flow = "xtls-rprx-vision"
			if err := apply(ctx, x, user); err != nil {
				t.Fatal(err)
			}
			requireOperations(t, core.take(), rotatedOn(false, false, "vless")...)
			requireCoreMatchesLocal(t, x, core)
		})
	}
}
