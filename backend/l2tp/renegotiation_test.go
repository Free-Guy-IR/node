package l2tp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIPCPRenegotiationOnTheSamePppdIsNotBilledTwice(t *testing.T) {
	e := newAcctEnv(t)
	e.session("ppp0", "7", 100, 1000, 1000, 500)
	a := e.instance("7")
	a.poll()
	expectUsers(t, "first poll", takeUsers(t, a), map[string]int64{"7": 1500})

	e.counters("ppp0", 1200, 600)
	e.final("ppp0", "7", 100, 1000, 1200, 600)
	if err := os.Remove(filepath.Join(sessionStateDir, "ppp0")); err != nil {
		t.Fatal(err)
	}
	e.session("ppp0", "7", 100, 1007, 1200, 600)
	a.poll()
	expectUsers(t, "ip-down and ip-up of one renegotiation", takeUsers(t, a), map[string]int64{"7": 300})

	e.counters("ppp0", 1300, 650)
	a.poll()
	expectUsers(t, "growth after the renegotiation", takeUsers(t, a), map[string]int64{"7": 150})
}
