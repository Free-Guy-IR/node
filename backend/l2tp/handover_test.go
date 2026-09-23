package l2tp

import "testing"

func TestStoppedInstanceCannotTouchTheAccountingItHandedOver(t *testing.T) {
	cases := []struct {
		name     string
		readBase bool
		want     int64
	}{
		{"pending read before the hand-over", true, 200},
		{"pending unread at the hand-over", false, 1700},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 1000, 500)
			a := e.instance("7")
			a.poll()
			if c.readBase {
				expectUsers(t, "first instance", takeUsers(t, a), map[string]int64{"7": 1500})
			}
			stopAccounting(a)

			b := e.instance("7")
			a.forgetUsers([]string{"7"})
			e.counters("ppp0", 1100, 600)
			b.poll()
			expectUsers(t, "new instance after the old one forgot the user", takeUsers(t, b), map[string]int64{"7": c.want})
		})
	}
}
