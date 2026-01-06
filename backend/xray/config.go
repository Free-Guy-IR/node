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

// convertClientsMapToSlice converts the internal map-based client storage to a slice
// for JSON serialization. This is called only during ToBytes().
func (i *Inbound) convertClientsMapToSlice() {
	clientsMap, ok := i.Settings["clients"].(map[string]api.Account)
	if !ok {
		// Already a slice or doesn't exist, no conversion needed
		return
	}

	if len(clientsMap) == 0 {
		i.Settings["clients"] = []interface{}{}
		return
	}

	switch i.Protocol {
	case Vmess:
		clients := make([]*api.VmessAccount, 0, len(clientsMap))
		for _, account := range clientsMap {
			if vmessAccount, ok := account.(*api.VmessAccount); ok {
				clients = append(clients, vmessAccount)
			}
		}
		i.Settings["clients"] = clients

	case Vless:
		clients := make([]*api.VlessAccount, 0, len(clientsMap))
		for _, account := range clientsMap {
			if vlessAccount, ok := account.(*api.VlessAccount); ok {
				clients = append(clients, vlessAccount)
			}
		}
		i.Settings["clients"] = clients

	case Trojan:
		clients := make([]*api.TrojanAccount, 0, len(clientsMap))
		for _, account := range clientsMap {
			if trojanAccount, ok := account.(*api.TrojanAccount); ok {
				clients = append(clients, trojanAccount)
			}
		}
		i.Settings["clients"] = clients

	case Shadowsocks:
		method, methodOk := i.Settings["method"].(string)
		if methodOk && strings.HasPrefix(method, "2022-blake3") {
			clients := make([]*api.ShadowsocksAccount, 0, len(clientsMap))
			for _, account := range clientsMap {
				if ssAccount, ok := account.(*api.ShadowsocksAccount); ok {
					clients = append(clients, ssAccount)
				}
			}
			i.Settings["clients"] = clients
		} else {
			clients := make([]*api.ShadowsocksTcpAccount, 0, len(clientsMap))
			for _, account := range clientsMap {
				if ssTcpAccount, ok := account.(*api.ShadowsocksTcpAccount); ok {
					clients = append(clients, ssTcpAccount)
				}
			}
			i.Settings["clients"] = clients
		}
	}
}

// convertClientsSliceToMap converts a slice-based client storage (from JSON) to a map
// for efficient O(1) lookups. This is called during initialization.
func (i *Inbound) convertClientsSliceToMap() {
	clientsMap := make(map[string]api.Account)
	hasClients := false

	switch i.Protocol {
	case Vmess:
		if clients, ok := i.Settings["clients"].([]*api.VmessAccount); ok {
			hasClients = true
			for _, account := range clients {
				if account != nil {
					clientsMap[account.Email] = account
				}
			}
		}

	case Vless:
		if clients, ok := i.Settings["clients"].([]*api.VlessAccount); ok {
			hasClients = true
			for _, account := range clients {
				if account != nil {
					clientsMap[account.Email] = account
				}
			}
		}

	case Trojan:
		if clients, ok := i.Settings["clients"].([]*api.TrojanAccount); ok {
			hasClients = true
			for _, account := range clients {
				if account != nil {
					clientsMap[account.Email] = account
				}
			}
		}

	case Shadowsocks:
		method, methodOk := i.Settings["method"].(string)
		if methodOk && strings.HasPrefix(method, "2022-blake3") {
			if clients, ok := i.Settings["clients"].([]*api.ShadowsocksAccount); ok {
				hasClients = true
				for _, account := range clients {
					if account != nil {
						clientsMap[account.Email] = account
					}
				}
			}
		} else {
			if clients, ok := i.Settings["clients"].([]*api.ShadowsocksTcpAccount); ok {
				hasClients = true
				for _, account := range clients {
					if account != nil {
						clientsMap[account.Email] = account
					}
				}
			}
		}
	}

	// Always set the map, even if empty, to ensure consistent internal representation
	if hasClients {
		i.Settings["clients"] = clientsMap
	}
}

func (i *Inbound) syncUsers(users []*common.User) {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Initialize clients map if it doesn't exist or is still a slice
	if _, ok := i.Settings["clients"].(map[string]api.Account); !ok {
		i.convertClientsSliceToMap()
		if _, ok := i.Settings["clients"].(map[string]api.Account); !ok {
			i.Settings["clients"] = make(map[string]api.Account)
		}
	}

	switch i.Protocol {
	case Vmess:
		// Clear existing clients for this inbound
		newMap := make(map[string]api.Account)
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
				newMap[account.Email] = account
			}
		}
		i.Settings["clients"] = newMap

	case Vless:
		newMap := make(map[string]api.Account)
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
				newMap[newAccount.Email] = &newAccount
			}
		}
		i.Settings["clients"] = newMap

	case Trojan:
		newMap := make(map[string]api.Account)
		for _, user := range users {
			if user.GetProxies().GetTrojan() == nil {
				continue
			}
			if slices.Contains(user.Inbounds, i.Tag) {
				account := api.NewTrojanAccount(user)
				newMap[account.Email] = account
			}
		}
		i.Settings["clients"] = newMap

	case Shadowsocks:
		method, methodOk := i.Settings["method"].(string)
		if methodOk && strings.HasPrefix(method, "2022-blake3") {
			newMap := make(map[string]api.Account)
			for _, user := range users {
				if user.GetProxies().GetShadowsocks() == nil {
					continue
				}
				if slices.Contains(user.Inbounds, i.Tag) {
					account := api.NewShadowsocksAccount(user)
					newAccount := checkShadowsocks2022(method, *account)
					newMap[newAccount.Email] = &newAccount
				}
			}
			i.Settings["clients"] = newMap
		} else {
			newMap := make(map[string]api.Account)
			for _, user := range users {
				if user.GetProxies().GetShadowsocks() == nil {
					continue
				}
				if slices.Contains(user.Inbounds, i.Tag) {
					account := api.NewShadowsocksTcpAccount(user)
					newMap[account.Email] = account
				}
			}
			i.Settings["clients"] = newMap
		}
	}
}

func (i *Inbound) updateUser(account api.Account) {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Ensure clients is a map
	if _, ok := i.Settings["clients"].(map[string]api.Account); !ok {
		i.convertClientsSliceToMap()
		if _, ok := i.Settings["clients"].(map[string]api.Account); !ok {
			i.Settings["clients"] = make(map[string]api.Account)
		}
	}

	clientsMap := i.Settings["clients"].(map[string]api.Account)
	email := account.GetEmail()

	switch account.(type) {
	case *api.VmessAccount:
		clientsMap[email] = account.(*api.VmessAccount)

	case *api.VlessAccount:
		clientsMap[email] = account.(*api.VlessAccount)

	case *api.TrojanAccount:
		clientsMap[email] = account.(*api.TrojanAccount)

	case *api.ShadowsocksTcpAccount:
		clientsMap[email] = account.(*api.ShadowsocksTcpAccount)

	case *api.ShadowsocksAccount:
		method, ok := i.Settings["method"].(string)
		if ok {
			newAccount := checkShadowsocks2022(method, *account.(*api.ShadowsocksAccount))
			clientsMap[email] = &newAccount
		} else {
			clientsMap[email] = account.(*api.ShadowsocksAccount)
		}

	default:
		return
	}
}

func (i *Inbound) removeUser(email string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Ensure clients is a map
	if _, ok := i.Settings["clients"].(map[string]api.Account); !ok {
		i.convertClientsSliceToMap()
		if _, ok := i.Settings["clients"].(map[string]api.Account); !ok {
			return // No clients to remove
		}
	}

	clientsMap := i.Settings["clients"].(map[string]api.Account)
	delete(clientsMap, email)
}

type Stats struct{}

func (c *Config) ToBytes() ([]byte, error) {
	// Convert all inbound client maps to slices for JSON serialization
	// and acquire locks
	for _, i := range c.InboundConfigs {
		i.mu.Lock()
		i.convertClientsMapToSlice()
	}

	// Marshal while holding locks
	b, err := json.Marshal(c)
	if err != nil {
		// Unlock all on error
		for _, i := range c.InboundConfigs {
			i.mu.Unlock()
		}
		return nil, err
	}

	// Convert back to maps after serialization for efficient future operations
	// and unlock
	for _, i := range c.InboundConfigs {
		i.convertClientsSliceToMap()
		i.mu.Unlock()
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
		} else {
			// Convert slices to maps for efficient O(1) operations
			i.mu.Lock()
			i.convertClientsSliceToMap()
			i.mu.Unlock()
		}
	}

	return &xrayConfig, nil
}
