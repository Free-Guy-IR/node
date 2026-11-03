package xray

import (
	"testing"
	"time"
)

func TestAnUntunedXrayKeepsTheProductionReadinessBounds(t *testing.T) {
	x := &Xray{}
	for name, bound := range map[string]struct{ got, want time.Duration }{
		"full sync readiness": {x.fullSyncReadyLimit(), 3 * time.Minute},
		"restore readiness":   {x.restoreReadyLimit(), 30 * time.Second},
		"restart readiness":   {x.restartReadyLimit(), 30 * time.Second},
		"stuck sync grace":    {x.stuckSyncLimit(), 60 * time.Second},
		"readiness poll":      {x.readyPollInterval(), time.Second},
	} {
		if bound.got != bound.want {
			t.Errorf("%s is %s without tuning, want the production %s", name, bound.got, bound.want)
		}
	}
}

func TestATunedXrayUsesItsOwnReadinessBounds(t *testing.T) {
	x := &Xray{fullSyncReady: time.Second, restoreReady: 2 * time.Second, restartReady: 3 * time.Second}
	if x.fullSyncReadyLimit() != time.Second || x.restoreReadyLimit() != 2*time.Second || x.restartReadyLimit() != 3*time.Second {
		t.Fatalf("tuned bounds ignored: full sync %s, restore %s, restart %s", x.fullSyncReadyLimit(), x.restoreReadyLimit(), x.restartReadyLimit())
	}
}
