package l2tp

import (
	"testing"
	"time"
)

func TestForgetDuringPollDoesNotBillTwice(t *testing.T) {
	cases := []struct {
		name     string
		readBase bool
		want     int64
	}{
		{"pending read before the race", true, 200},
		{"pending unread at the race", false, 1700},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAcctEnv(t)
			e.session("ppp0", "7", 100, 1000, 1000, 500)
			o := e.instance("7")
			o.poll()
			if c.readBase {
				expectUsers(t, "first poll", takeUsers(t, o), map[string]int64{"7": 1500})
			}

			e.counters("ppp0", 1100, 600)
			done := make(chan struct{})
			prev := beforeApplySamples
			t.Cleanup(func() { beforeApplySamples = prev })
			beforeApplySamples = func() {
				beforeApplySamples = prev
				go func() {
					o.forgetUsers([]string{"7"})
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(100 * time.Millisecond):
				}
			}
			o.poll()

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("forgetUsers did not complete")
			}
			expectUsers(t, "after the race", takeUsers(t, o), map[string]int64{"7": c.want})
		})
	}
}
