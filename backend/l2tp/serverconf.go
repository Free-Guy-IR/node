package l2tp

import (
	"bufio"
	"context"
	"fmt"
	"github.com/pasarguard/node/backend"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pasarguard/node/backend/ipsec"
)

var (
	xl2tpdVersionRe        = regexp.MustCompile(`([0-9]+\.[0-9]+\.[0-9]+)`)
	chapSecretsMu          sync.Mutex
	chapSecretsPath        = "/etc/ppp/chap-secrets"
	beforeChapSecretsWrite = func() {}
	swanctlDir             = ipsec.SwanctlDir
	xl2tpdPath             = xl2tpdBinary
	loadSwanctl            = ipsec.LoadAll
)

var versionProbeTimeout = 10 * time.Second

func DetectVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, xl2tpdBinary, "-v")
	backend.ConfigureProbe(cmd)

	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return "unknown"
	}
	if m := xl2tpdVersionRe.FindStringSubmatch(string(out)); len(m) == 2 {
		return m[1]
	}
	return "unknown"
}

func (o *L2TP) confFileName() string { return "pg-l2tp-" + o.config.InboundTag + ".conf" }
func (o *L2TP) swanctlFragmentPath() string {
	return filepath.Join(swanctlDir, "conf.d", o.confFileName())
}
func (o *L2TP) xl2tpdConfPath() string    { return filepath.Join(o.config.workDir, "xl2tpd.conf") }
func (o *L2TP) pppOptionsPath() string    { return filepath.Join(o.config.workDir, "ppp-options") }
func (o *L2TP) ipUpScriptPath() string    { return filepath.Join(o.config.workDir, "ip-up") }
func (o *L2TP) ipDownScriptPath() string  { return filepath.Join(o.config.workDir, "ip-down") }
func (o *L2TP) controlSocketPath() string { return filepath.Join(o.config.workDir, "l2tp-control") }
func (o *L2TP) pidPath() string           { return filepath.Join(o.config.workDir, "xl2tpd.pid") }

func (o *L2TP) writeConfig() error {
	if err := os.MkdirAll(o.config.workDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(swanctlDir, "conf.d"), 0o700); err != nil {
		return err
	}
	for _, step := range []func() error{o.writeSwanctl, o.writeXl2tpdConf, o.writePPPOptions, o.writeIPScripts, o.writeChapSecrets} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

const scriptHelpers = `load() {
  r_user=; r_tag=; r_pid=; r_started=; r_ifname=; r_ifindex=; r_proc=; r_mono=
  [ -f "$1" ] || return 1
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in *=*) ;; *) continue ;; esac
    k="${line%%=*}"
    v="${line#*=}"
    case "$k" in
      user) r_user="$v" ;;
      tag) r_tag="$v" ;;
      pid) r_pid="$v" ;;
      started) r_started="$v" ;;
      ifname) r_ifname="$v" ;;
      ifindex) r_ifindex="$v" ;;
      proc) r_proc="$v" ;;
      mono) r_mono="$v" ;;
    esac
  done 2>/dev/null < "$1"
  [ -n "$r_user$r_pid$r_started" ]
}
readone() {
  r=
  { read -r r < "$1"; } 2>/dev/null
}
isnum() {
  case "$1" in ''|*[!0-9]*) return 1 ;; esac
}`

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (o *L2TP) writeIPScripts() error {
	up := strings.Join([]string{
		"#!/bin/sh",
		`IF="${IFNAME:-$PPP_IFACE}"`,
		`REMOTE="${IPREMOTE:-$PPP_REMOTE}"`,
		`[ -n "$PEERNAME" ] || exit 0`,
		`[ -n "$IF" ] || exit 0`,
		"set -f",
		"d=" + shellQuote(sessionStateDir),
		"own=" + shellQuote(pppdRecordDir),
		"sys=" + shellQuote(sysClassNet),
		"proc=" + shellQuote(procRoot),
		scriptHelpers,
		`mkdir -p "$d" "$own"; umask 077`,
		`p="${PPPD_PID:-0}"`,
		`readone "$sys/$IF/ifindex"`,
		`ifindex="$r"`,
		"fp=",
		"mono=",
		`readone "$proc/sys/kernel/random/boot_id"`,
		`boot="$r"`,
		`readone "$proc/uptime"`,
		`up="${r%% *}"`,
		`case "$up" in *.[0-9][0-9]) isnum "${up%.*}${up#*.}" && [ -n "$boot" ] && mono="$boot:${up%.*}${up#*.}" ;; esac`,
		`if [ "$p" != 0 ]; then`,
		`  readone "$proc/$p/stat"`,
		`  set -- ${r##*") "}`,
		`  if [ -n "$boot" ] && [ $# -ge 20 ]; then`,
		"    shift 19",
		`    fp="$boot:$1"`,
		"  fi",
		"fi",
		"started=$(date +%s)",
		`if [ "$p" != 0 ] && load "$own/$p" && isnum "$r_started"; then`,
		`  if [ -n "$fp" ] && [ "$r_proc" = "$fp" ] && [ "$r_ifname" = "$IF" ] &&`,
		`    { [ -z "$ifindex" ] || [ -z "$r_ifindex" ] || [ "$r_ifindex" = "$ifindex" ]; }; then`,
		`    started="$r_started"`,
		`    [ -z "$r_mono" ] || mono="$r_mono"`,
		`  elif ! isnum "$started" || [ "$r_started" -ge "$started" ]; then`,
		`    started=$((r_started + 1))`,
		"  fi",
		"fi",
		"record() {",
		`  printf '%s\n' "user=$PEERNAME" "tunnel_ip=$REMOTE" "client=$REMOTENUMBER" "tag=` + o.config.InboundTag + `" \`,
		`    "pid=$p" "started=$started" "ifname=$IF" "ifindex=$ifindex" "proc=$fp" "mono=$mono"`,
		"}",
		`[ "$p" = 0 ] || { record > "$own/.$p.$$" && mv -f "$own/.$p.$$" "$own/$p"; }`,
		`tmp="$d/.$IF.$$"`,
		`{ record; } > "$tmp" && mv -f "$tmp" "$d/$IF"`,
		"",
	}, "\n")

	down := strings.Join([]string{
		"#!/bin/sh",
		`IF="${IFNAME:-$PPP_IFACE}"`,
		`[ -n "$IF" ] || exit 0`,
		"set -f",
		"d=" + shellQuote(sessionStateDir),
		"own=" + shellQuote(pppdRecordDir),
		"sys=" + shellQuote(sysClassNet),
		"fin=" + shellQuote(sessionFinalDir),
		scriptHelpers,
		`p="${PPPD_PID:-0}"`,
		`f="$d/$IF"`,
		"src=",
		`if load "$f" && { [ "$p" = 0 ] || [ "${r_pid:-0}" = 0 ] || [ "$r_pid" = "$p" ]; }; then`,
		`  src="$f"`,
		`elif [ "$p" != 0 ] && [ -n "$PEERNAME" ] && load "$own/$p" && [ "$r_ifname" = "$IF" ] && [ "$r_user" = "$PEERNAME" ]; then`,
		`  src="$own/$p"`,
		`elif [ "$p" != 0 ] && [ -n "$PEERNAME" ]; then`,
		`  r_user="$PEERNAME"; r_tag="` + o.config.InboundTag + `"; r_pid="$p"; r_started=; r_ifindex=; r_proc=; r_mono=`,
		"  src=env",
		"fi",
		`[ -n "$src" ] || exit 0`,
		`s="$sys/$IF"`,
		"rx=; tx=",
		`if [ -n "$r_ifindex" ]; then`,
		`  readone "$s/ifindex"`,
		`  if [ "$r" = "$r_ifindex" ]; then`,
		`    readone "$s/statistics/rx_bytes"; rx="$r"`,
		`    readone "$s/statistics/tx_bytes"; tx="$r"`,
		`    readone "$s/ifindex"`,
		`    [ "$r" = "$r_ifindex" ] || { rx=; tx=; }`,
		"  fi",
		"fi",
		`if [ -z "$rx" ] || [ -z "$tx" ]; then`,
		`  if [ -n "$BYTES_RCVD$BYTES_SENT" ]; then`,
		`    rx="${BYTES_RCVD:-0}"; tx="${BYTES_SENT:-0}"`,
		`  elif [ -z "$r_ifindex" ] && [ "$src" != env ]; then`,
		`    readone "$s/statistics/rx_bytes"; rx="$r"`,
		`    readone "$s/statistics/tx_bytes"; tx="$r"`,
		"  fi",
		"fi",
		`[ -n "$rx" ] || rx=0`,
		`[ -n "$tx" ] || tx=0`,
		`mkdir -p "$fin"; umask 077`,
		`tmp="$fin/.$IF.$$"`,
		"{",
		`  printf '%s\n' "user=$r_user" "tag=$r_tag" "ifname=$IF"`,
		`  [ -z "$r_pid" ] || printf '%s\n' "pid=$r_pid"`,
		`  [ -z "$r_started" ] || printf '%s\n' "started=$r_started"`,
		`  [ -z "$r_proc" ] || printf '%s\n' "proc=$r_proc"`,
		`  [ -z "$r_mono" ] || printf '%s\n' "mono=$r_mono"`,
		`  printf '%s\n' "rx=$rx" "tx=$tx"`,
		`} > "$tmp" && mv -f "$tmp" "$fin/$IF.$$.$(date +%s)"`,
		`if [ "$src" = "$f" ]; then`,
		`  q_pid="$r_pid"; q_started="$r_started"`,
		`  g="$d/.$IF.down.$$"`,
		`  if mv -f "$f" "$g" 2>/dev/null; then`,
		`    if ! load "$g" || [ "$r_pid" != "$q_pid" ] || [ "$r_started" != "$q_started" ]; then`,
		`      ln "$g" "$f" 2>/dev/null || [ -e "$f" ] || mv "$g" "$f"`,
		"    fi",
		`    rm -f "$g"`,
		"  fi",
		"fi",
		"exit 0",
		"",
	}, "\n")

	for path, content := range map[string]string{o.ipUpScriptPath(): up, o.ipDownScriptPath(): down} {
		if err := writeFileAtomic(path, []byte(content), 0o755); err != nil {
			return err
		}
	}
	for _, dir := range []string{sessionFinalDir, pppdRecordDir, sessionStateDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (o *L2TP) writeSwanctl() error {
	tag := o.config.InboundTag
	var b strings.Builder
	fmt.Fprintf(&b, "connections {\n")
	fmt.Fprintf(&b, "    %s {\n", tag)
	fmt.Fprintf(&b, "        version = 1\n")
	fmt.Fprintf(&b, "        proposals = %s\n", strings.Join(o.config.IKEProposals, ","))
	fmt.Fprintf(&b, "        encap = yes\n")
	fmt.Fprintf(&b, "        fragmentation = yes\n")
	fmt.Fprintf(&b, "        dpd_delay = 30s\n")
	fmt.Fprintf(&b, "        local {\n            auth = psk\n        }\n")
	fmt.Fprintf(&b, "        remote {\n            auth = psk\n        }\n")
	fmt.Fprintf(&b, "        children {\n")
	fmt.Fprintf(&b, "            %s {\n", tag)
	fmt.Fprintf(&b, "                esp_proposals = %s\n", strings.Join(o.config.ESPProposals, ","))
	fmt.Fprintf(&b, "                local_ts = dynamic[udp/1701]\n")
	fmt.Fprintf(&b, "                remote_ts = dynamic[udp]\n")
	fmt.Fprintf(&b, "                mode = transport\n")
	fmt.Fprintf(&b, "                dpd_action = clear\n")
	fmt.Fprintf(&b, "            }\n")
	fmt.Fprintf(&b, "        }\n")
	fmt.Fprintf(&b, "    }\n")
	fmt.Fprintf(&b, "}\n\n")
	fmt.Fprintf(&b, "secrets {\n")
	fmt.Fprintf(&b, "    ike-%s {\n", tag)
	fmt.Fprintf(&b, "        secret = \"%s\"\n", o.config.PSK)
	fmt.Fprintf(&b, "    }\n")
	fmt.Fprintf(&b, "}\n")
	return os.WriteFile(o.swanctlFragmentPath(), []byte(b.String()), 0o600)
}

func (o *L2TP) writeXl2tpdConf() error {
	start, end := poolRange(o.config.poolNet, o.config.LocalIP)
	var b strings.Builder
	fmt.Fprintf(&b, "[global]\n")
	fmt.Fprintf(&b, "port = 1701\n")
	fmt.Fprintf(&b, "access control = no\n")
	fmt.Fprintf(&b, "auth file = %s\n\n", chapSecretsPath)
	fmt.Fprintf(&b, "[lns default]\n")
	fmt.Fprintf(&b, "ip range = %s-%s\n", start, end)
	fmt.Fprintf(&b, "local ip = %s\n", o.config.LocalIP)
	fmt.Fprintf(&b, "require chap = yes\n")
	fmt.Fprintf(&b, "refuse pap = yes\n")
	fmt.Fprintf(&b, "require authentication = yes\n")
	fmt.Fprintf(&b, "name = %s\n", o.config.InboundTag)
	fmt.Fprintf(&b, "pppoptfile = %s\n", o.pppOptionsPath())
	fmt.Fprintf(&b, "length bit = yes\n")
	return os.WriteFile(o.xl2tpdConfPath(), []byte(b.String()), 0o600)
}

func (o *L2TP) writePPPOptions() error {
	var b strings.Builder
	fmt.Fprintf(&b, "require-mschap-v2\n")
	for _, d := range o.config.DNS {
		fmt.Fprintf(&b, "ms-dns %s\n", d)
	}
	fmt.Fprintf(&b, "asyncmap 0\n")
	fmt.Fprintf(&b, "auth\n")
	fmt.Fprintf(&b, "hide-password\n")
	fmt.Fprintf(&b, "name %s\n", o.config.InboundTag)
	fmt.Fprintf(&b, "proxyarp\n")
	fmt.Fprintf(&b, "lcp-echo-interval 30\n")
	fmt.Fprintf(&b, "lcp-echo-failure 4\n")
	fmt.Fprintf(&b, "nodefaultroute\n")
	fmt.Fprintf(&b, "mtu 1400\n")
	fmt.Fprintf(&b, "mru 1400\n")
	fmt.Fprintf(&b, "noccp\n")
	fmt.Fprintf(&b, "noipv6\n")
	fmt.Fprintf(&b, "novj\n")
	fmt.Fprintf(&b, "novjccomp\n")
	fmt.Fprintf(&b, "connect-delay 5000\n")
	fmt.Fprintf(&b, "ip-up-script %s\n", o.ipUpScriptPath())
	fmt.Fprintf(&b, "ip-down-script %s\n", o.ipDownScriptPath())
	return os.WriteFile(o.pppOptionsPath(), []byte(b.String()), 0o600)
}

func chapQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func (o *L2TP) writeChapSecrets() error {
	beforeChapSecretsWrite()
	chapSecretsMu.Lock()
	defer chapSecretsMu.Unlock()
	kept, err := o.chapSecretsWithoutTag()
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, line := range kept {
		b.WriteString(line)
		b.WriteString("\n")
	}
	for _, entry := range o.users.snapshot() {
		fmt.Fprintf(&b, "%s %s %s *\n", chapQuote(entry.username), o.config.InboundTag, chapQuote(entry.password))
	}
	return writeFileAtomic(chapSecretsPath, []byte(b.String()), 0o600)
}

func (o *L2TP) removeChapSecrets() {
	chapSecretsMu.Lock()
	defer chapSecretsMu.Unlock()
	kept, err := o.chapSecretsWithoutTag()
	if err != nil {
		return
	}
	content := strings.Join(kept, "\n")
	if content != "" {
		content += "\n"
	}
	_ = writeFileAtomic(chapSecretsPath, []byte(content), 0o600)
}

func (o *L2TP) chapSecretsWithoutTag() ([]string, error) {
	f, err := os.Open(chapSecretsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var kept []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || len(fields) < 2 || fields[1] != o.config.InboundTag {
			kept = append(kept, line)
		}
	}
	return kept, sc.Err()
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func poolRange(n *net.IPNet, localIP string) (string, string) {
	network := dup4(n.IP.Mask(n.Mask))
	bcast := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		bcast[i] = network[i] | ^n.Mask[i]
	}
	start := dup4(network)
	inc(start)
	if start.String() == localIP {
		inc(start)
	}
	end := dup4(bcast)
	dec(end)
	if bytesCompare(start, end) > 0 {
		return localIP, localIP
	}
	return start.String(), end.String()
}

func dup4(ip net.IP) net.IP {
	out := make(net.IP, 4)
	copy(out, ip.To4())
	return out
}

func inc(ip net.IP) {
	for i := 3; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

func dec(ip net.IP) {
	for i := 3; i >= 0; i-- {
		if ip[i] != 0 {
			ip[i]--
			break
		}
		ip[i] = 255
	}
}

func bytesCompare(a, b net.IP) int {
	for i := 0; i < 4; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
