package l2tp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pasarguard/node/backend/ipsec"
	"github.com/pasarguard/node/config"
)

func TestL2TPHostPathsDefaultToTheProductionHost(t *testing.T) {
	for name, c := range map[string][2]string{
		"chap-secrets":   {chapSecretsPath, "/etc/ppp/chap-secrets"},
		"swanctl dir":    {swanctlDir, "/etc/swanctl"},
		"ipsec swanctl":  {ipsec.SwanctlDir, "/etc/swanctl"},
		"xl2tpd binary":  {xl2tpdPath, "/usr/sbin/xl2tpd"},
		"xl2tpd checked": {xl2tpdBinary, "/usr/sbin/xl2tpd"},
	} {
		if c[0] != c[1] {
			t.Errorf("%s defaults to %q, want %q", name, c[0], c[1])
		}
	}
	if reflect.ValueOf(loadSwanctl).Pointer() != reflect.ValueOf(ipsec.LoadAll).Pointer() {
		t.Error("the swanctl loader must default to ipsec.LoadAll")
	}

	cfg, err := NewConfig(`{"inbound_tag":"paths","server_addr":"1.2.3.4","pool":"10.31.0.0/24","psk":"paths-shared-key"}`)
	if err != nil {
		t.Fatal(err)
	}
	o := newInstance(cfg, &config.Config{GeneratedConfigPath: t.TempDir(), LogBufferSize: 16})
	if got := o.swanctlFragmentPath(); got != "/etc/swanctl/conf.d/pg-l2tp-paths.conf" {
		t.Errorf("swanctl fragment path = %q, want the production path", got)
	}
	if err := os.MkdirAll(o.config.workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := o.writeXl2tpdConf(); err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile(filepath.Join(o.config.workDir, "xl2tpd.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), "auth file = /etc/ppp/chap-secrets\n") {
		t.Errorf("xl2tpd must read the production chap-secrets:\n%s", conf)
	}
}
