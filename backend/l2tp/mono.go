package l2tp

import (
	"fmt"
	"strconv"
	"strings"
)

type monoStamp struct {
	boot   string
	centis int64
	ok     bool
}

func parseMono(v string) monoStamp {
	boot, at, found := strings.Cut(v, ":")
	if !found || boot == "" || strings.ContainsAny(boot, " \t") {
		return monoStamp{}
	}
	centis, err := strconv.ParseInt(at, 10, 64)
	if err != nil || centis < 0 {
		return monoStamp{}
	}
	return monoStamp{boot: boot, centis: centis, ok: true}
}

func (m monoStamp) String() string {
	if !m.ok {
		return ""
	}
	return fmt.Sprintf("%s:%d", m.boot, m.centis)
}

func currentBootID() string {
	b, err := readProcFile("sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func currentMono() monoStamp {
	boot := currentBootID()
	b, err := readProcFile("uptime")
	if boot == "" || err != nil {
		return monoStamp{}
	}
	up, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	secs, frac, found := strings.Cut(up, ".")
	if !found || len(frac) != 2 {
		return monoStamp{}
	}
	return parseMono(boot + ":" + secs + frac)
}
