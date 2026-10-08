package controller

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func kernelEphemeralPortRange(t *testing.T) (int, int) {
	t.Helper()
	var raw []byte
	var err error
	switch runtime.GOOS {
	case "linux":
		raw, err = os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	case "darwin", "freebsd":
		raw, err = exec.Command("sysctl", "-n", "net.inet.ip.portrange.first", "net.inet.ip.portrange.last").Output()
	default:
		t.Skipf("no ephemeral port range lookup on %s", runtime.GOOS)
	}
	if err != nil {
		t.Skipf("cannot read the kernel ephemeral port range: %v", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		t.Skipf("unexpected ephemeral port range %q", raw)
	}
	first, errFirst := strconv.Atoi(fields[0])
	last, errLast := strconv.Atoi(fields[1])
	if errFirst != nil || errLast != nil {
		t.Skipf("unexpected ephemeral port range %q", raw)
	}
	return first, last
}

func TestTheMTProtoTestPortIsNeverOneTheKernelHandsToOtherSockets(t *testing.T) {
	first, last := kernelEphemeralPortRange(t)
	for range 50 {
		if port := unusedPort(t); port >= first && port <= last {
			t.Fatalf("unusedPort gave %d, inside the kernel ephemeral range %d-%d: whenever no listener holds it, the kernel can hand it to any other socket's connect or bind(0), so the next mtproto Start on it fails with address already in use",
				port, first, last)
		}
	}
}
