package xray

import (
	"sync"
	"testing"

	"github.com/pasarguard/node/common"
)

const toBytesRaceConfig = `{
	"log":{"access":"/var/log/xray/access.log","error":"/var/log/xray/error.log","loglevel":"warning"},
	"inbounds":[
		{"tag":"static","protocol":"vless","settings":{"decryption":"none","clients":[{"id":"6b1d1f0e-5c1d-4c3a-9f6e-111111111111","email":"hand-written"}]}},
		{"tag":"managed","protocol":"vless","settings":{"decryption":"none","clients":[]}},
		{"tag":"aes128","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg==","network":"tcp,udp"}}
	]
}`

func newToBytesRaceConfig(t *testing.T) *Config {
	t.Helper()

	cfg, err := NewConfig(toBytesRaceConfig, []string{"static"})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func toBytesRaceUsers() []*common.User {
	return []*common.User{
		{
			Email:    "1",
			Inbounds: []string{"static", "managed"},
			Proxies:  &common.Proxy{Vless: &common.Vless{Id: "6b1d1f0e-5c1d-4c3a-9f6e-222222222222"}},
		},
		{
			Email:    "2",
			Inbounds: []string{"aes128"},
			Proxies:  &common.Proxy{Shadowsocks: &common.Shadowsocks{Password: "example-user-two-key"}},
		},
	}
}

func TestToBytesConcurrentCalls(t *testing.T) {
	cfg := newToBytesRaceConfig(t)
	cfg.syncUsers(toBytesRaceUsers())

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if _, err := cfg.ToBytes(); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for round := range 20 {
			if round%2 == 0 {
				cfg.syncUsers(toBytesRaceUsers())
			} else {
				cfg.syncUsers(nil)
			}
		}
	})
	wg.Wait()
}

func TestToBytesWhileCloning(t *testing.T) {
	cfg := newToBytesRaceConfig(t)
	cfg.syncUsers(toBytesRaceUsers())

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			if _, err := cfg.ToBytes(); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Go(func() {
		for range 50 {
			if _, err := cfg.Clone(); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
}
