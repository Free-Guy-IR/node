package xray

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"

	"github.com/xtls/xray-core/infra/conf"
)

type Protocol string

const (
	Vmess       = "vmess"
	Vless       = "vless"
	Trojan      = "trojan"
	Shadowsocks = "shadowsocks"
)

// extractIDFromEmail extracts the ID part from an email in the format "id.username"
// If no dot is found, returns the full email string
func extractIDFromEmail(email string) string {
	if idx := strings.Index(email, "."); idx != -1 {
		return email[:idx]
	}
	return email
}

type Config struct {
	LogConfig        *conf.LogConfig        `json:"log"`
	RouterConfig     *conf.RouterConfig     `json:"routing"`
	DNSConfig        map[string]interface{} `json:"dns"`
	InboundConfigs   []*Inbound             `json:"inbounds"`
	OutboundConfigs  interface{}            `json:"outbounds"`
	Policy           *conf.PolicyConfig     `json:"policy"`
	API              *conf.APIConfig        `json:"api"`
	Metrics          map[string]interface{} `json:"metrics,omitempty"`
	Stats            Stats                  `json:"stats"`
	Reverse          map[string]interface{} `json:"reverse,omitempty"`
	FakeDNS          map[string]interface{} `json:"fakeDns,omitempty"`
	Observatory      map[string]interface{} `json:"observatory,omitempty"`
	BurstObservatory map[string]interface{} `json:"burstObservatory,omitempty"`
}

type Inbound struct {
	Tag            string                 `json:"tag"`
	Listen         string                 `json:"listen,omitempty"`
	Port           interface{}            `json:"port,omitempty"`
	Protocol       string                 `json:"protocol"`
	Settings       map[string]interface{} `json:"settings"`
	StreamSettings map[string]interface{} `json:"streamSettings,omitempty"`
	Sniffing       interface{}            `json:"sniffing,omitempty"`
	Allocation     map[string]interface{} `json:"allocate,omitempty"`
	mu             sync.RWMutex
	exclude        bool
}

func (c *Config) syncUsers(users []*common.User) {
	for _, i := range c.InboundConfigs {
		if i.exclude {
			continue
		}
		i.syncUsers(users)
	}
}

func (i *Inbound) syncUsers(users []*common.User) {
	i.mu.Lock()
	defer i.mu.Unlock()

	switch i.Protocol {
	case Vmess:
		clients := make([]*api.VmessAccount, 0, len(users))
		for _, user := range users {
			if user.GetProxies().GetVmess() == nil {
				continue
			}
			if slices.Contains(user.Inbounds, i.Tag) {
				account, err := api.NewVmessAccount(user)
				if err != nil {
					log.Println("error for user", user.GetEmail(), ":", err)
					continue
				}
				clients = append(clients, account)
			}
		}
		// Keep sorted by ID (part before dot in email) for binary search
		slices.SortFunc(clients, func(a, b *api.VmessAccount) int {
			return strings.Compare(extractIDFromEmail(a.Email), extractIDFromEmail(b.Email))
		})
		i.Settings["clients"] = clients

	case Vless:
		clients := make([]*api.VlessAccount, 0, len(users))
		for _, user := range users {
			if user.GetProxies().GetVless() == nil {
				continue
			}
			if slices.Contains(user.Inbounds, i.Tag) {
				account, err := api.NewVlessAccount(user)
				if err != nil {
					log.Println("error for user", user.GetEmail(), ":", err)
					continue
				}
				newAccount := checkVless(i, *account)
				clients = append(clients, &newAccount)
			}
		}
		// Keep sorted by ID (part before dot in email) for binary search
		slices.SortFunc(clients, func(a, b *api.VlessAccount) int {
			return strings.Compare(extractIDFromEmail(a.Email), extractIDFromEmail(b.Email))
		})
		i.Settings["clients"] = clients

	case Trojan:
		clients := make([]*api.TrojanAccount, 0, len(users))
		for _, user := range users {
			if user.GetProxies().GetTrojan() == nil {
				continue
			}
			if slices.Contains(user.Inbounds, i.Tag) {
				clients = append(clients, api.NewTrojanAccount(user))
			}
		}
		// Keep sorted by ID (part before dot in email) for binary search
		slices.SortFunc(clients, func(a, b *api.TrojanAccount) int {
			return strings.Compare(extractIDFromEmail(a.Email), extractIDFromEmail(b.Email))
		})
		i.Settings["clients"] = clients

	case Shadowsocks:
		method, methodOk := i.Settings["method"].(string)
		if methodOk && strings.HasPrefix(method, "2022-blake3") {
			clients := make([]*api.ShadowsocksAccount, 0, len(users))
			for _, user := range users {
				if user.GetProxies().GetShadowsocks() == nil {
					continue
				}
				if slices.Contains(user.Inbounds, i.Tag) {
					account := api.NewShadowsocksAccount(user)
					newAccount := checkShadowsocks2022(method, *account)
					clients = append(clients, &newAccount)
				}
			}
			// Keep sorted by ID (part before dot in email) for binary search
			slices.SortFunc(clients, func(a, b *api.ShadowsocksAccount) int {
				return strings.Compare(extractIDFromEmail(a.Email), extractIDFromEmail(b.Email))
			})
			i.Settings["clients"] = clients
		} else {
			clients := make([]*api.ShadowsocksTcpAccount, 0, len(users))
			for _, user := range users {
				if user.GetProxies().GetShadowsocks() == nil {
					continue
				}
				if slices.Contains(user.Inbounds, i.Tag) {
					clients = append(clients, api.NewShadowsocksTcpAccount(user))
				}
			}
			// Keep sorted by ID (part before dot in email) for binary search
			slices.SortFunc(clients, func(a, b *api.ShadowsocksTcpAccount) int {
				return strings.Compare(extractIDFromEmail(a.Email), extractIDFromEmail(b.Email))
			})
			i.Settings["clients"] = clients
		}
	}
}

func (i *Inbound) updateUser(account api.Account) {
	i.mu.Lock()
	defer i.mu.Unlock()

	email := account.GetEmail()
	searchID := extractIDFromEmail(email)
	switch a := account.(type) {
	case *api.VmessAccount:
		clients, _ := i.Settings["clients"].([]*api.VmessAccount)
		if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.VmessAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		}); found {
			clients = append(clients[:idx], clients[idx+1:]...)
		}
		insertIdx, _ := slices.BinarySearchFunc(clients, searchID, func(c *api.VmessAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		})
		clients = append(clients, nil)
		copy(clients[insertIdx+1:], clients[insertIdx:])
		clients[insertIdx] = a
		i.Settings["clients"] = clients

	case *api.VlessAccount:
		clients, _ := i.Settings["clients"].([]*api.VlessAccount)
		if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.VlessAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		}); found {
			clients = append(clients[:idx], clients[idx+1:]...)
		}
		insertIdx, _ := slices.BinarySearchFunc(clients, searchID, func(c *api.VlessAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		})
		clients = append(clients, nil)
		copy(clients[insertIdx+1:], clients[insertIdx:])
		clients[insertIdx] = a
		i.Settings["clients"] = clients

	case *api.TrojanAccount:
		clients, _ := i.Settings["clients"].([]*api.TrojanAccount)
		if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.TrojanAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		}); found {
			clients = append(clients[:idx], clients[idx+1:]...)
		}
		insertIdx, _ := slices.BinarySearchFunc(clients, searchID, func(c *api.TrojanAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		})
		clients = append(clients, nil)
		copy(clients[insertIdx+1:], clients[insertIdx:])
		clients[insertIdx] = a
		i.Settings["clients"] = clients

	case *api.ShadowsocksTcpAccount:
		clients, _ := i.Settings["clients"].([]*api.ShadowsocksTcpAccount)
		if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.ShadowsocksTcpAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		}); found {
			clients = append(clients[:idx], clients[idx+1:]...)
		}
		insertIdx, _ := slices.BinarySearchFunc(clients, searchID, func(c *api.ShadowsocksTcpAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		})
		clients = append(clients, nil)
		copy(clients[insertIdx+1:], clients[insertIdx:])
		clients[insertIdx] = a
		i.Settings["clients"] = clients

	case *api.ShadowsocksAccount:
		clients, _ := i.Settings["clients"].([]*api.ShadowsocksAccount)
		if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.ShadowsocksAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		}); found {
			clients = append(clients[:idx], clients[idx+1:]...)
		}
		method, ok := i.Settings["method"].(string)
		var newAccount *api.ShadowsocksAccount
		if ok {
			na := checkShadowsocks2022(method, *a)
			newAccount = &na
		} else {
			newAccount = a
		}
		insertIdx, _ := slices.BinarySearchFunc(clients, searchID, func(c *api.ShadowsocksAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		})
		clients = append(clients, nil)
		copy(clients[insertIdx+1:], clients[insertIdx:])
		clients[insertIdx] = newAccount
		i.Settings["clients"] = clients
	}
}

func (i *Inbound) removeUser(email string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	searchID := extractIDFromEmail(email)
	switch Protocol(i.Protocol) {
	case Vmess:
		clients, _ := i.Settings["clients"].([]*api.VmessAccount)
		if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.VmessAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		}); found {
			clients = append(clients[:idx], clients[idx+1:]...)
		}
		i.Settings["clients"] = clients

	case Vless:
		clients, _ := i.Settings["clients"].([]*api.VlessAccount)
		if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.VlessAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		}); found {
			clients = append(clients[:idx], clients[idx+1:]...)
		}
		i.Settings["clients"] = clients

	case Trojan:
		clients, _ := i.Settings["clients"].([]*api.TrojanAccount)
		if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.TrojanAccount, id string) int {
			return strings.Compare(extractIDFromEmail(c.Email), id)
		}); found {
			clients = append(clients[:idx], clients[idx+1:]...)
		}
		i.Settings["clients"] = clients

	case Shadowsocks:
		method, methodOk := i.Settings["method"].(string)
		if methodOk && strings.HasPrefix(method, "2022-blake3") {
			clients, _ := i.Settings["clients"].([]*api.ShadowsocksAccount)
			if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.ShadowsocksAccount, id string) int {
				return strings.Compare(extractIDFromEmail(c.Email), id)
			}); found {
				clients = append(clients[:idx], clients[idx+1:]...)
			}
			i.Settings["clients"] = clients
		} else {
			clients, _ := i.Settings["clients"].([]*api.ShadowsocksTcpAccount)
			if idx, found := slices.BinarySearchFunc(clients, searchID, func(c *api.ShadowsocksTcpAccount, id string) int {
				return strings.Compare(extractIDFromEmail(c.Email), id)
			}); found {
				clients = append(clients[:idx], clients[idx+1:]...)
			}
			i.Settings["clients"] = clients
		}
	}
}

type Stats struct{}

func (c *Config) ToBytes() ([]byte, error) {
	// Acquire read locks for all inbounds
	for _, i := range c.InboundConfigs {
		i.mu.RLock()
	}

	b, err := json.Marshal(c)

	// Release all locks
	for _, i := range c.InboundConfigs {
		i.mu.RUnlock()
	}

	if err != nil {
		return nil, err
	}
	return b, nil
}

func filterRules(rules []json.RawMessage, apiTag string) ([]json.RawMessage, error) {
	if rules == nil {
		rules = []json.RawMessage{}
	}

	filtered := make([]json.RawMessage, 0, len(rules))
	for _, raw := range rules {
		var obj map[string]interface{}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("invalid JSON in rule: %w", err)
		}

		// Check if outboundTag exists and matches apiTag
		if outboundTagValue, ok := obj["outboundTag"].(string); ok && outboundTagValue == apiTag {
			continue
		}

		filtered = append(filtered, raw)
	}

	return filtered, nil
}

func (c *Config) ApplyAPI(apiPort int) (err error) {
	// Remove the existing inbound with the API_INBOUND tag
	for i, inbound := range c.InboundConfigs {
		if inbound.Tag == "API_INBOUND" {
			c.InboundConfigs = append(c.InboundConfigs[:i], c.InboundConfigs[i+1:]...)
		}
	}

	apiTag := "API"

	c.API = &conf.APIConfig{
		Services: []string{"HandlerService", "LoggerService", "StatsService"},
		Tag:      apiTag,
	}

	if c.RouterConfig == nil {
		c.RouterConfig = &conf.RouterConfig{}
	}

	rules := c.RouterConfig.RuleList
	c.RouterConfig.RuleList, err = filterRules(rules, apiTag)

	c.checkPolicy()

	inbound := &Inbound{
		Listen:   "127.0.0.1",
		Port:     apiPort,
		Protocol: "dokodemo-door",
		Settings: map[string]interface{}{"address": "127.0.0.1"},
		Tag:      "API_INBOUND",
	}

	c.InboundConfigs = append([]*Inbound{inbound}, c.InboundConfigs...)

	rule := map[string]interface{}{
		"inboundTag":  []string{"API_INBOUND"},
		"source":      []string{"127.0.0.1"},
		"outboundTag": "API",
		"type":        "field",
	}

	rawBytes, err := json.Marshal(rule)
	if err != nil {
		return err
	}

	newRaw := json.RawMessage(rawBytes)

	c.RouterConfig.RuleList = append([]json.RawMessage{newRaw}, c.RouterConfig.RuleList...)

	return nil
}

func (c *Config) checkPolicy() {
	if c.Policy == nil {
		c.Policy = &conf.PolicyConfig{Levels: make(map[uint32]*conf.Policy)}
		c.Policy.Levels[0] = &conf.Policy{StatsUserUplink: true, StatsUserDownlink: true}
		// StatsUserOnline is not set, which will default to false
	} else {
		if c.Policy.Levels == nil {
			c.Policy.Levels = make(map[uint32]*conf.Policy)
		}

		zero, ok := c.Policy.Levels[0]
		if !ok {
			c.Policy.Levels[0] = &conf.Policy{StatsUserUplink: true, StatsUserDownlink: true}
		} else {
			zero.StatsUserDownlink = true
			zero.StatsUserUplink = true
			// Don't modify StatsUserOnline, respect the value that's already there
		}
	}

	if c.Policy.System == nil {
		c.Policy.System = &conf.SystemPolicy{
			StatsInboundDownlink:  false,
			StatsInboundUplink:    false,
			StatsOutboundDownlink: true,
			StatsOutboundUplink:   true,
		}
	} else {
		c.Policy.System.StatsOutboundDownlink = true
		c.Policy.System.StatsOutboundUplink = true
	}
}

func (c *Config) RemoveLogFiles() (accessFile, errorFile string) {
	accessFile = c.LogConfig.AccessLog
	c.LogConfig.AccessLog = ""
	errorFile = c.LogConfig.ErrorLog
	c.LogConfig.ErrorLog = ""

	return accessFile, errorFile
}

func NewXRayConfig(config string, exclude []string) (*Config, error) {
	var xrayConfig Config
	err := json.Unmarshal([]byte(config), &xrayConfig)
	if err != nil {
		return nil, err
	}

	for _, i := range xrayConfig.InboundConfigs {
		if slices.Contains(exclude, i.Tag) {
			i.mu.Lock()
			i.exclude = true
			i.mu.Unlock()
		}
	}

	return &xrayConfig, nil
}
