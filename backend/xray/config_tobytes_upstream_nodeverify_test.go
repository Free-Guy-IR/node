package xray

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	applog "github.com/xtls/xray-core/app/log"
	clog "github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/pasarguard/node/common"
)

const toBytesConfig = `{
	"log":{"access":"/var/log/xray/access.log","error":"/var/log/xray/error.log","loglevel":"warning"},
	"inbounds":[
		{"tag":"static","protocol":"vless","settings":{"decryption":"none","clients":[{"id":"6b1d1f0e-5c1d-4c3a-9f6e-111111111111","email":"hand-written"}]}},
		{"tag":"managed","protocol":"vless","settings":{"decryption":"none","clients":[]}}
	]
}`

func newToBytesConfig(t *testing.T) *Config {
	t.Helper()

	cfg, err := NewConfig(toBytesConfig, []string{"static"})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func toBytesUsers() []*common.User {
	return []*common.User{
		{
			Email:    "1",
			Inbounds: []string{"static", "managed"},
			Proxies:  &common.Proxy{Vless: &common.Vless{Id: "6b1d1f0e-5c1d-4c3a-9f6e-222222222222"}},
		},
	}
}

type serializedConfig struct {
	Log      map[string]any `json:"log"`
	Inbounds []struct {
		Tag      string         `json:"tag"`
		Settings map[string]any `json:"settings"`
	} `json:"inbounds"`
}

func serializeConfig(t *testing.T, cfg *Config) serializedConfig {
	t.Helper()

	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var out serializedConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (s serializedConfig) settings(t *testing.T, tag string) map[string]any {
	t.Helper()

	for _, inbound := range s.Inbounds {
		if inbound.Tag == tag {
			return inbound.Settings
		}
	}
	t.Fatalf("inbound %q missing from serialized config", tag)
	return nil
}

func emails(t *testing.T, settings map[string]any, key string) []string {
	t.Helper()

	entries, ok := settings[key].([]any)
	if !ok {
		t.Fatalf("settings[%q] is %T, want a list", key, settings[key])
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		fields, _ := entry.(map[string]any)
		email, _ := fields["email"].(string)
		out = append(out, email)
	}
	return out
}

func TestToBytesKeepsExcludedInboundClients(t *testing.T) {
	cfg := newToBytesConfig(t)
	cfg.syncUsers(toBytesUsers())

	got := emails(t, serializeConfig(t, cfg).settings(t, "static"), "clients")
	if len(got) != 1 || got[0] != "hand-written" {
		t.Fatalf("excluded inbound clients = %v, want only the hand-written client", got)
	}
}

func TestToBytesWritesSyncedClientsAndPeers(t *testing.T) {
	cfg := newToBytesConfig(t)
	cfg.syncUsers(toBytesUsers())
	out := serializeConfig(t, cfg)

	if got := emails(t, out.settings(t, "managed"), "clients"); len(got) != 1 || got[0] != "1" {
		t.Fatalf("managed inbound clients = %v, want [1]", got)
	}
}

func TestToBytesWritesEmptyListsWithoutUsers(t *testing.T) {
	out := serializeConfig(t, newToBytesConfig(t))

	if got := emails(t, out.settings(t, "managed"), "clients"); len(got) != 0 {
		t.Fatalf("managed inbound clients = %v, want none", got)
	}
}

func TestToBytesDropsLogFilesWithoutChangingTheConfig(t *testing.T) {
	cfg := newToBytesConfig(t)
	out := serializeConfig(t, cfg)

	if access, _ := out.Log["access"].(string); access != "" {
		t.Fatalf("serialized access log = %q, want empty", access)
	}
	if errorLog, _ := out.Log["error"].(string); errorLog != "" {
		t.Fatalf("serialized error log = %q, want empty", errorLog)
	}
	if severity := wireLogSeverity(t, cfg); severity != clog.Severity_Warning {
		t.Fatalf("the core reads the serialized log level as %v, want %v", severity, clog.Severity_Warning)
	}
	if cfg.LogConfig.AccessLog != "/var/log/xray/access.log" || cfg.LogConfig.ErrorLog != "/var/log/xray/error.log" {
		t.Fatalf("ToBytes changed the stored log files: access %q, error %q", cfg.LogConfig.AccessLog, cfg.LogConfig.ErrorLog)
	}
}

func wireLogSeverity(t *testing.T, cfg *Config) clog.Severity {
	t.Helper()

	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Log *conf.LogConfig `json:"log"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Log == nil {
		t.Fatal("the serialized config carries no log section")
	}
	return wire.Log.Build().GetErrorLogLevel()
}

func TestTheCoreReadsTheLogLevelUnderEitherSpelling(t *testing.T) {
	for _, spelling := range []string{"loglevel", "logLevel"} {
		t.Run(spelling, func(t *testing.T) {
			cfg, err := NewConfig(fmt.Sprintf(`{"log":{%q:"debug"},"inbounds":[]}`, spelling), nil)
			if err != nil {
				t.Fatal(err)
			}
			if severity := wireLogSeverity(t, cfg); severity != clog.Severity_Debug {
				t.Fatalf("a config written with %q reaches the core as %v, want %v", spelling, severity, clog.Severity_Debug)
			}
		})
	}
}

func TestTheCoreReadsALogLevelThatSilencesXray(t *testing.T) {
	cfg, err := NewConfig(`{"log":{"loglevel":"none"},"inbounds":[]}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Log *conf.LogConfig `json:"log"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	built := wire.Log.Build()
	if built.GetErrorLogType() != applog.LogType_None || built.GetAccessLogType() != applog.LogType_None {
		t.Fatalf("loglevel none reaches the core as error %v and access %v, want both none", built.GetErrorLogType(), built.GetAccessLogType())
	}
}

func TestToBytesDoesNotWriteInboundSettings(t *testing.T) {
	cfg := newToBytesConfig(t)
	cfg.syncUsers(toBytesUsers())

	if _, err := cfg.ToBytes(); err != nil {
		t.Fatal(err)
	}

	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag != "managed" {
			continue
		}
		clients, ok := inbound.Settings["clients"].([]any)
		if !ok || len(clients) != 0 {
			t.Fatalf("ToBytes wrote the managed inbound's stored settings: clients = %#v", inbound.Settings["clients"])
		}
	}
}

func TestToBytesSerializesExcludedInboundUnchanged(t *testing.T) {
	const inbound = `{"tag":"static","listen":"127.0.0.1","port":8443,"protocol":"vless",` +
		`"settings":{"decryption":"none","clients":[{"id":"6b1d1f0e-5c1d-4c3a-9f6e-111111111111","email":"hand-written"}]},` +
		`"streamSettings":{"network":"tcp","security":"none"},` +
		`"sniffing":{"enabled":true,"destOverride":["http","tls"]},` +
		`"allocate":{"strategy":"always"}}`

	cfg, err := NewConfig(`{"inbounds":[`+inbound+`]}`, []string{"static"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}

	var want map[string]any
	if err := json.Unmarshal([]byte(inbound), &want); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Inbounds) != 1 || !reflect.DeepEqual(out.Inbounds[0], want) {
		t.Fatalf("excluded inbound changed by ToBytes:\n got  %v\n want %v", out.Inbounds, want)
	}
}

func TestSnapshotInboundCopiesEveryExportedField(t *testing.T) {
	original := &Inbound{exclude: true}
	value := reflect.ValueOf(original).Elem()
	for idx := 0; idx < value.NumField(); idx++ {
		field := value.Field(idx)
		if !value.Type().Field(idx).IsExported() {
			continue
		}
		switch field.Kind() {
		case reflect.String:
			field.SetString("value-" + value.Type().Field(idx).Name)
		case reflect.Map:
			field.Set(reflect.ValueOf(map[string]any{"key": value.Type().Field(idx).Name}))
		case reflect.Interface:
			field.Set(reflect.ValueOf(value.Type().Field(idx).Name))
		default:
			t.Fatalf("Inbound.%s has kind %s: extend this test and the snapshot copy", value.Type().Field(idx).Name, field.Kind())
		}
	}

	original.mu.RLock()
	snapshot := original.snapshotLocked()
	original.mu.RUnlock()

	snap := reflect.ValueOf(snapshot).Elem()
	for idx := 0; idx < value.NumField(); idx++ {
		name := value.Type().Field(idx).Name
		if !value.Type().Field(idx).IsExported() {
			continue
		}
		if !reflect.DeepEqual(snap.Field(idx).Interface(), value.Field(idx).Interface()) {
			t.Fatalf("snapshot did not copy Inbound.%s: got %v, want %v", name, snap.Field(idx).Interface(), value.Field(idx).Interface())
		}
	}
}
