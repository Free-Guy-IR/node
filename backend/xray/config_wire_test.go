package xray

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/pkg/fsutil"
)

func mutatingSerialise(c *Config) ([]byte, error) {
	if c.LogConfig == nil {
		c.LogConfig = &conf.LogConfig{LogLevel: "info"}
	}

	for _, i := range c.InboundConfigs {
		i.mu.RLock()
	}

	for _, i := range c.InboundConfigs {
		if i.exclude {
			continue
		}

		if i.Settings == nil {
			i.Settings = make(map[string]any)
		}

		if len(i.clients) == 0 {
			if placeholder := shadowsocks2022Placeholder(i); placeholder != nil {
				i.Settings["clients"] = []*api.ShadowsocksAccount{placeholder}
			} else {
				i.Settings["clients"] = []any{}
			}
			continue
		}

		accounts := i.accountsByEmailLocked()
		switch i.Protocol {
		case Vmess:
			clients := make([]*api.VmessAccount, 0, len(i.clients))
			for _, account := range accounts {
				if vmessAccount, ok := account.(*api.VmessAccount); ok {
					clients = append(clients, vmessAccount)
				}
			}
			i.Settings["clients"] = clients

		case Vless:
			clients := make([]*api.VlessAccount, 0, len(i.clients))
			for _, account := range accounts {
				if vlessAccount, ok := account.(*api.VlessAccount); ok {
					clients = append(clients, vlessAccount)
				}
			}
			i.Settings["clients"] = clients

		case Trojan:
			clients := make([]*api.TrojanAccount, 0, len(i.clients))
			for _, account := range accounts {
				if trojanAccount, ok := account.(*api.TrojanAccount); ok {
					clients = append(clients, trojanAccount)
				}
			}
			i.Settings["clients"] = clients

		case Shadowsocks:
			method, methodOk := i.Settings["method"].(string)
			if methodOk && strings.HasPrefix(method, "2022-blake3") {
				clients := make([]*api.ShadowsocksAccount, 0, len(i.clients))
				for _, account := range accounts {
					if ssAccount, ok := account.(*api.ShadowsocksAccount); ok {
						clients = append(clients, ssAccount)
					}
				}
				i.Settings["clients"] = clients
			} else {
				clients := make([]*api.ShadowsocksTcpAccount, 0, len(i.clients))
				for _, account := range accounts {
					if ssTcpAccount, ok := account.(*api.ShadowsocksTcpAccount); ok {
						clients = append(clients, ssTcpAccount)
					}
				}
				i.Settings["clients"] = clients
			}

		case Hysteria:
			clients := make([]*api.HysteriaAccount, 0, len(i.clients))
			for _, account := range accounts {
				if hyAccount, ok := account.(*api.HysteriaAccount); ok {
					clients = append(clients, hyAccount)
				}
			}
			i.Settings["clients"] = clients
		}
	}

	aLog := c.LogConfig.AccessLog
	eLog := c.LogConfig.ErrorLog
	c.LogConfig.AccessLog = ""
	c.LogConfig.ErrorLog = ""

	b, err := json.Marshal(c)

	c.LogConfig.AccessLog = aLog
	c.LogConfig.ErrorLog = eLog

	for _, i := range c.InboundConfigs {
		i.mu.RUnlock()
	}

	if err != nil {
		return nil, err
	}
	return b, nil
}

type wireCase struct {
	name    string
	raw     string
	exclude []string
	api     bool
	users   []*common.User
	resync  []*common.User
}

func wireCases(t *testing.T) []wireCase {
	t.Helper()

	file, err := fsutil.ReadFileAsString(jsonFile)
	if err != nil {
		t.Fatal(err)
	}
	fileUsers := []*common.User{
		versionedUser("1.alice", "wire", "Shadowsocks 2022", "VMESS TCP NOTLS", "VLESS TCP REALITY", "TROJAN TCP NOTLS", "Shadowsocks TCP", "VLESS TCP Header NoTLS", "VLESS TCP NOTLS"),
		versionedUser("2.bob", "wire", "VLESS TCP REALITY", "Shadowsocks TCP"),
	}
	fileResync := []*common.User{
		versionedUser("2.bob", "resync", "TROJAN TCP NOTLS", "Shadowsocks 2022"),
		versionedUser("3.carol", "resync", "VMESS TCP NOTLS"),
	}
	sparse := `{"log":{"loglevel":"debug","access":"/var/log/xray/access.log","error":"/var/log/xray/error.log"},"inbounds":[{"tag":"trojan","protocol":"trojan"},{"tag":"socks","protocol":"socks"},{"tag":"vless","protocol":"vless"}]}`

	return []wireCase{
		{name: "file", raw: file, api: true, users: fileUsers, resync: fileResync},
		{name: "file without users", raw: file, api: true, resync: fileResync},
		{name: "file with an excluded inbound", raw: file, exclude: []string{"VLESS TCP REALITY"}, api: true, users: fileUsers, resync: fileResync},
		{name: "every protocol", raw: syncTestInbounds, users: orderTestUsers(), resync: orderTestUsers()[5:12]},
		{name: "every protocol with the api", raw: syncTestInbounds, api: true, users: orderTestUsers(), resync: []*common.User{versionedUser("1.user", "resync", "ss", "hysteria")}},
		{name: "every protocol with a log section", raw: orderTestInbounds, users: orderTestUsers(), resync: nil},
		{name: "inbounds without settings", raw: sparse, users: []*common.User{versionedUser("1.alice", "wire", "trojan", "vless")}, resync: []*common.User{versionedUser("2.bob", "wire", "vless")}},
		{name: "an excluded inbound without settings", raw: `{"inbounds":[{"tag":"trojan","protocol":"trojan"},{"tag":"socks","protocol":"socks"}]}`, exclude: []string{"trojan"}, resync: []*common.User{versionedUser("1.alice", "wire", "trojan")}},
	}
}

func (c wireCase) build(t *testing.T) *Config {
	t.Helper()

	cfg, err := NewConfig(c.raw, c.exclude)
	if err != nil {
		t.Fatalf("%s: %v", c.name, err)
	}
	if c.api {
		if err := cfg.ApplyAPI(20000, 20001); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	cfg.syncUsers(c.users)
	return cfg
}

func requireSameWire(t *testing.T, name string, got, want []byte) {
	t.Helper()

	if bytes.Equal(got, want) {
		return
	}
	var gotIndented, wantIndented bytes.Buffer
	_ = json.Indent(&gotIndented, got, "", "  ")
	_ = json.Indent(&wantIndented, want, "", "  ")
	t.Fatalf("%s: the snapshot serialises differently from the mutating serialiser:\n got  %s\n want %s", name, gotIndented.String(), wantIndented.String())
}

func TestTheSnapshotSerialisesExactlyWhatTheMutatingSerialiserDid(t *testing.T) {
	for _, c := range wireCases(t) {
		live := c.build(t)
		legacy := c.build(t)

		live.GetLogFiles()
		got, err := live.ToBytes()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		want, err := mutatingSerialise(legacy)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		requireSameWire(t, c.name, got, want)

		again, err := live.ToBytes()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		requireSameWire(t, c.name+", serialised twice", again, got)

		liveCandidate, err := live.Clone()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		legacyCandidate, err := legacy.Clone()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		liveCandidate.syncUsers(c.resync)
		legacyCandidate.syncUsers(c.resync)

		liveCandidate.GetLogFiles()
		got, err = liveCandidate.ToBytes()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		want, err = mutatingSerialise(legacyCandidate)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		requireSameWire(t, c.name+", after a full sync of a clone", got, want)
	}
}
