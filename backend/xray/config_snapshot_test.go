package xray

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/pasarguard/node/backend/xray/api"
	"github.com/pasarguard/node/common"
)

const liveConfigInbounds = `{"log":{"loglevel":"warning","access":"/var/log/xray/access.log","error":"/var/log/xray/error.log"},"inbounds":[
	{"tag":"vless","protocol":"vless","settings":{"clients":[],"decryption":"none"}},
	{"tag":"trojan","protocol":"trojan"},
	{"tag":"ss2022","protocol":"shadowsocks","settings":{"clients":[],"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg=="}},
	{"tag":"held","protocol":"vmess","settings":{"clients":[]}}
]}`

type liveConfigShape struct {
	log         conf.LogConfig
	settings    []map[string]any
	nilSettings []bool
}

func shapeOf(cfg *Config) liveConfigShape {
	shape := liveConfigShape{log: *cfg.LogConfig}
	for _, inbound := range cfg.InboundConfigs {
		shape.settings = append(shape.settings, maps.Clone(inbound.Settings))
		shape.nilSettings = append(shape.nilSettings, inbound.Settings == nil)
	}
	return shape
}

func TestSerialisingTheConfigLeavesTheLiveConfigUntouched(t *testing.T) {
	cfg, err := NewConfig(liveConfigInbounds, []string{"held"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.syncUsers([]*common.User{syncTestUser("1.alice", "vless", "trojan", "ss2022", "held")})

	before := shapeOf(cfg)
	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	if after := shapeOf(cfg); !reflect.DeepEqual(after, before) {
		t.Fatalf("serialising the config changed the live config:\n before %#v\n after  %#v", before, after)
	}

	emitted := emittedClients(t, raw)
	for _, tag := range []string{"vless", "trojan", "ss2022"} {
		if !slices.Equal(emitted[tag], []string{"1.alice"}) {
			t.Fatalf("the serialised config lists %v on %s, want the synced user", emitted[tag], tag)
		}
	}
	if len(emitted["held"]) != 0 {
		t.Fatalf("the serialised config lists %v on the excluded inbound", emitted["held"])
	}

	var parsed struct {
		Log conf.LogConfig `json:"log"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Log.AccessLog != "" || parsed.Log.ErrorLog != "" || parsed.Log.LogLevel != "warning" {
		t.Fatalf("the serialised log section is %+v, want the configured level without the node-owned log files", parsed.Log)
	}
}

func TestAConfigWithoutALogSectionSerialisesAtTheDefaultLevelAndStaysUntouched(t *testing.T) {
	cfg, err := NewConfig(syncTestInbounds, nil)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := cfg.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Log *conf.LogConfig `json:"log"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Log == nil || parsed.Log.LogLevel != "info" || parsed.Log.AccessLog != "" || parsed.Log.ErrorLog != "" {
		t.Fatalf("a config without a log section serialised its log as %+v, want the info level", parsed.Log)
	}

	accessFile, errorFile := cfg.GetLogFiles()
	if accessFile != "" || errorFile != "" || cfg.LogConfig != nil {
		t.Fatalf("reading the log files of a config without a log section returned %q, %q and left %+v", accessFile, errorFile, cfg.LogConfig)
	}
}

func TestSerialisingAnInboundKeepsEveryExportedField(t *testing.T) {
	inbound := &Inbound{clients: make(map[string]api.Account)}
	value := reflect.ValueOf(inbound).Elem()
	fields := value.Type()
	for index := range fields.NumField() {
		field := fields.Field(index)
		if !field.IsExported() {
			continue
		}
		target := value.Field(index)
		switch target.Kind() {
		case reflect.String:
			target.SetString(field.Name + "-value")
		case reflect.Interface:
			target.Set(reflect.ValueOf(field.Name + "-value"))
		case reflect.Map:
			target.Set(reflect.ValueOf(map[string]any{field.Name: "value"}))
		default:
			t.Fatalf("inbound field %s has kind %s, which this test cannot fill", field.Name, target.Kind())
		}
	}

	raw, err := (&Config{LogConfig: &conf.LogConfig{}, InboundConfigs: []*Inbound{inbound}}).ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 1 {
		t.Fatalf("the serialised config has %d inbounds, want 1", len(parsed.Inbounds))
	}

	emitted := parsed.Inbounds[0]
	for index := range fields.NumField() {
		field := fields.Field(index)
		if !field.IsExported() {
			continue
		}
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		want := any(field.Name + "-value")
		if field.Type.Kind() == reflect.Map {
			filled := map[string]any{field.Name: "value"}
			if field.Name == "Settings" {
				filled["clients"] = []any{}
			}
			want = filled
		}
		if got, present := emitted[key]; !present || !reflect.DeepEqual(got, want) {
			t.Fatalf("the serialised inbound carries %s (%q) as %#v, want %#v", field.Name, key, got, want)
		}
	}
}

func TestSerialisingTheLiveConfigRacesNoUserOperation(t *testing.T) {
	core := newModelCore(false)
	x := newModelXray(t, core, orderTestInbounds)
	ctx := syncTestContext(t)
	live := x.config

	stop := make(chan struct{})
	serialised := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				serialised <- nil
				return
			default:
			}
			if _, err := live.ToBytes(); err != nil {
				serialised <- err
				return
			}
		}
	}()

	for round := range 200 {
		version := fmt.Sprintf("v%d", round)
		var err error
		switch round % 4 {
		case 0:
			err = x.SyncUser(ctx, versionedUser("1.alice", version, syncTestTags...))
		case 1:
			err = x.UpdateUsers(ctx, []*common.User{
				versionedUser("2.bob", version, syncTestTags...),
				versionedUser("1.alice", version, "vless", "ss2022"),
			})
		case 2:
			err = x.SyncUser(ctx, versionedUser("2.bob", "disabled"))
		case 3:
			_, err = live.Clone()
		}
		if err != nil {
			close(stop)
			<-serialised
			t.Fatalf("round %d: %v", round, err)
		}
	}
	close(stop)
	if err := <-serialised; err != nil {
		t.Fatal(err)
	}
	requireCoreMatchesLocal(t, x, core)
}
