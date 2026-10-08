package l2tp

import "testing"

func TestFallbackWindowRemembersABootWhoseProcessNeverPolledBeforeTheNextReboot(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	a := e.instance("8")
	a.poll()
	*blocked = false

	middle := "6b5c4d3e-2f10-4b2c-8d9e-8f7a6b5c4d3e"
	e.bootAt(middle, 1500)
	e.instance("8")
	e.finalWithMono("ppp1", "8", 200, fallbackLoad+60, middle+":3000", 70, 30)

	e.bootAt("7c6d5e4f-3021-4c3d-9eaf-9a8b7c6d5e4f", 1500)
	c := e.instance("8")
	c.poll()
	expectUsers(t, "the final of a session of a boot whose process never polled before the next reboot", takeUsers(t, c), map[string]int64{"8": 100})
}

func TestFinalOfABootWhoseIDWasNeverReadIsBilledAfterTheNextReboot(t *testing.T) {
	e := newAcctEnv(t)
	blocked := openFallbackWindow(t, e)
	a := e.instance("8")
	a.poll()
	*blocked = false

	middle := "6b5c4d3e-2f10-4b2c-8d9e-8f7a6b5c4d3e"
	e.bootAt(middle, 1500)
	bootHidden := true
	blockProcReads(t, func(name string) bool { return bootHidden && name == "sys/kernel/random/boot_id" })
	b := e.instance("8")
	e.finalWithMono("ppp1", "8", 200, fallbackLoad+60, middle+":3000", 70, 30)
	b.poll()
	expectUsers(t, "while the boot id cannot be read", takeUsers(t, b), map[string]int64{})

	e.bootAt("7c6d5e4f-3021-4c3d-9eaf-9a8b7c6d5e4f", 1500)
	bootHidden = false
	c := e.instance("8")
	c.poll()
	expectUsers(t, "the final of a boot whose id was never read, after the next reboot", takeUsers(t, c), map[string]int64{"8": 100})
}
