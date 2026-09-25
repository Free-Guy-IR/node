package xray

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/pasarguard/node/common"
)

const orderTestInbounds = `{"log":{"loglevel":"warning"},"inbounds":[
	{"tag":"vless","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
	{"tag":"vmess","protocol":"vmess","settings":{"clients":[]}},
	{"tag":"trojan","protocol":"trojan","settings":{"clients":[]}},
	{"tag":"ss","protocol":"shadowsocks","settings":{"clients":[],"method":"aes-128-gcm","network":"tcp,udp"}},
	{"tag":"ss2022","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg==","network":"tcp,udp"}},
	{"tag":"hysteria","protocol":"hysteria","settings":{"clients":[]}}
]}`

func orderTestUsers() []*common.User {
	users := make([]*common.User, 0, 40)
	for i := range 40 {
		users = append(users, versionedUser(fmt.Sprintf("%d.user", i), "order", syncTestTags...))
	}
	return users
}

func emittedClients(t *testing.T, raw []byte) map[string][]string {
	t.Helper()

	var parsed struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	out := make(map[string][]string, len(parsed.Inbounds))
	for _, inbound := range parsed.Inbounds {
		for _, client := range inbound.Settings.Clients {
			out[inbound.Tag] = append(out[inbound.Tag], client.Email)
		}
	}
	return out
}

func TestConfigEmitsEveryInboundsClientsInEmailOrder(t *testing.T) {
	users := orderTestUsers()
	var reference []byte

	for run := range 100 {
		cfg, err := NewConfig(orderTestInbounds, nil)
		if err != nil {
			t.Fatal(err)
		}
		shuffled := slices.Clone(users)
		random := rand.New(rand.NewPCG(uint64(run), 7))
		random.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if run%2 == 0 {
			cfg.syncUsers(shuffled)
		} else {
			for _, user := range shuffled {
				cfg.updateUsers([]*common.User{user})
			}
		}

		raw, err := cfg.ToBytes()
		if err != nil {
			t.Fatal(err)
		}
		for tag, emails := range emittedClients(t, raw) {
			if len(emails) != len(users) || !slices.IsSorted(emails) {
				t.Fatalf("run %d: inbound %s lists its %d clients out of email order: %v", run, tag, len(emails), emails)
			}
		}
		if reference == nil {
			reference = raw
			continue
		}
		if !bytes.Equal(raw, reference) {
			t.Fatalf("run %d: the same users inserted in another order produced a different config", run)
		}
	}
}

func TestUpdateUsersAddsEachInboundsUsersInEmailOrder(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, syncTestInbounds)
	ctx := syncTestContext(t)

	users := orderTestUsers()
	slices.Reverse(users)
	if err := x.UpdateUsers(ctx, users); err != nil {
		t.Fatal(err)
	}

	added := make(map[string][]string)
	for _, entry := range core.allEntries() {
		if entry.op == addOperation {
			added[entry.tag] = append(added[entry.tag], entry.email)
		}
	}
	for _, tag := range syncTestTags {
		if len(added[tag]) != len(users) || !slices.IsSorted(added[tag]) {
			t.Fatalf("inbound %s received its %d adds out of email order: %v", tag, len(added[tag]), added[tag])
		}
	}
}
