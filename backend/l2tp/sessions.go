package l2tp

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var (
	sessionStateDir = "/run/pg-l2tp/sessions"
	sessionFinalDir = "/run/pg-l2tp/final"
)

type l2tpSession struct {
	user     string
	ifname   string
	tunnelIP string
	clientIP string
	tag      string
	pid      int
	started  int64
}

var sessionIsAlive = func(s l2tpSession) bool {
	if s.pid > 0 {
		return processIsPppd(s.pid)
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", s.ifname))
	return err == nil
}

func processIsPppd(pid int) bool {
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return false
	}
	return strings.Contains(string(comm), "pppd")
}

func ownsRecord(recordTag, tag string) bool {
	return recordTag == "" || recordTag == tag
}

func readSessions(tag string) []l2tpSession {
	entries, err := os.ReadDir(sessionStateDir)
	if err != nil {
		return nil
	}
	var out []l2tpSession
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(sessionStateDir, e.Name())
		s := parseSessionFile(path, e.Name())
		if s.user == "" || s.ifname == "" || !ownsRecord(s.tag, tag) {
			continue
		}
		if !sessionIsAlive(s) {
			_ = os.Remove(path)
			continue
		}
		out = append(out, s)
	}
	return out
}

func parseSessionFile(path, ifname string) l2tpSession {
	s := l2tpSession{ifname: ifname}
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "user":
			s.user = v
		case "tunnel_ip":
			s.tunnelIP = v
		case "client":
			s.clientIP = v
		case "tag":
			s.tag = v
		case "pid":
			s.pid, _ = strconv.Atoi(v)
		case "started":
			s.started, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return s
}

type finalRecord struct {
	path   string
	user   string
	tag    string
	ifname string
	rx     int64
	tx     int64
	pid    int64
	epoch  int64
}

func readFinalRecords(tag string) []finalRecord {
	entries, err := os.ReadDir(sessionFinalDir)
	if err != nil {
		return nil
	}
	out := make([]finalRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(sessionFinalDir, e.Name())
		rec, ok := parseFinalRecord(path)
		if !ok {
			_ = os.Remove(path)
			continue
		}
		if !ownsRecord(rec.tag, tag) {
			continue
		}
		rec.pid, rec.epoch = parseFinalName(e.Name())
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].epoch != out[j].epoch {
			return out[i].epoch < out[j].epoch
		}
		if out[i].pid != out[j].pid {
			return out[i].pid < out[j].pid
		}
		return out[i].path < out[j].path
	})
	return out
}

func parseFinalName(name string) (pid, epoch int64) {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return 0, 0
	}
	epoch, _ = strconv.ParseInt(parts[len(parts)-1], 10, 64)
	pid, _ = strconv.ParseInt(parts[len(parts)-2], 10, 64)
	return pid, epoch
}

func parseFinalRecord(path string) (finalRecord, bool) {
	rec := finalRecord{path: path}
	f, err := os.Open(path)
	if err != nil {
		return rec, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "user":
			rec.user = v
		case "tag":
			rec.tag = v
		case "ifname":
			rec.ifname = v
		case "rx":
			rec.rx, _ = strconv.ParseInt(v, 10, 64)
		case "tx":
			rec.tx, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return rec, rec.user != "" && rec.ifname != ""
}

func ifaceBytes(ifname string) (rx int64, tx int64) {
	base := filepath.Join("/sys/class/net", ifname, "statistics")
	return readCounter(filepath.Join(base, "rx_bytes")), readCounter(filepath.Join(base, "tx_bytes"))
}

func readCounter(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}
