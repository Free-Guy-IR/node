package l2tp

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var (
	sessionStateDir   = "/run/pg-l2tp/sessions"
	sessionFinalDir   = "/run/pg-l2tp/final"
	sessionCountedDir = "/run/pg-l2tp/counted"
	sysClassNet       = "/sys/class/net"
	procRoot          = "/proc"
	pppdRecordDir     = "/run/pg-l2tp/pppd"
)

var (
	readProcFile = func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(procRoot, name))
	}
	openFinalRecord = func(path string) (io.ReadCloser, error) {
		return os.Open(path)
	}
)

type l2tpSession struct {
	user     string
	ifname   string
	tunnelIP string
	clientIP string
	tag      string
	pid      int
	started  int64
	ifindex  string
	proc     string
}

func (s l2tpSession) identity() sessionIdentity {
	return sessionIdentity{ifname: s.ifname, pid: s.pid, started: s.started}
}

var sessionIsAlive = pppdSessionIsAlive

func pppdSessionIsAlive(s l2tpSession) bool {
	if s.pid > 0 {
		return pppdState(s.pid, s.proc) != processGone
	}
	_, err := os.Stat(filepath.Join(sysClassNet, s.ifname))
	return !errors.Is(err, fs.ErrNotExist)
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
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(sessionStateDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s, complete := parseSession(raw, e.Name())
		if !complete || s.user == "" || s.ifname == "" || !ownsRecord(s.tag, tag) {
			continue
		}
		if !sessionIsAlive(s) {
			removeIfUnchanged(path, raw)
			continue
		}
		out = append(out, s)
	}
	return out
}

func parseSession(raw []byte, ifname string) (l2tpSession, bool) {
	s := l2tpSession{ifname: ifname}
	hasStarted := false
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
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
			hasStarted = true
		case "ifindex":
			s.ifindex = v
		case "proc":
			s.proc = v
		}
	}
	return s, hasStarted
}

type finalRecord struct {
	path        string
	user        string
	tag         string
	ifname      string
	rx          int64
	tx          int64
	pid         int64
	epoch       int64
	identity    sessionIdentity
	hasIdentity bool
	hasPid      bool
	proc        string
}

type unreadableFinal struct {
	path string
	err  error
}

type finalScan struct {
	records    []finalRecord
	unreadable []unreadableFinal
	unusable   []string
}

func readFinalRecords(tag string) []finalRecord {
	return scanFinalRecords(tag).records
}

func scanFinalRecords(tag string) finalScan {
	var scan finalScan
	entries, err := os.ReadDir(sessionFinalDir)
	if err != nil {
		return scan
	}
	scan.records = make([]finalRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(sessionFinalDir, e.Name())
		rec, usable, err := parseFinalRecord(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			scan.unreadable = append(scan.unreadable, unreadableFinal{path: path, err: err})
			continue
		case !ownsRecord(rec.tag, tag):
			continue
		case !usable:
			scan.unusable = append(scan.unusable, path)
			continue
		}
		rec.pid, rec.epoch = parseFinalName(e.Name())
		scan.records = append(scan.records, rec)
	}
	sort.Slice(scan.records, func(i, j int) bool {
		a, b := scan.records[i], scan.records[j]
		if a.epoch != b.epoch {
			return a.epoch < b.epoch
		}
		if a.pid != b.pid {
			return a.pid < b.pid
		}
		return a.path < b.path
	})
	return scan
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

func finalNameInterface(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], ".")
}

func parseFinalRecord(path string) (finalRecord, bool, error) {
	rec := finalRecord{path: path}
	f, err := openFinalRecord(path)
	if err != nil {
		return rec, false, err
	}
	defer f.Close()
	var hasPid, hasStarted, hasRx, hasTx, malformed bool
	number := func(v string, emptyIsZero bool) int64 {
		if v == "" && emptyIsZero {
			return 0
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			malformed = true
		}
		return n
	}
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
			rec.rx, hasRx = number(v, false), true
		case "tx":
			rec.tx, hasTx = number(v, false), true
		case "pid":
			rec.identity.pid, hasPid = int(number(v, true)), true
		case "started":
			rec.identity.started, hasStarted = number(v, true), true
		case "proc":
			rec.proc = v
		}
	}
	if err := sc.Err(); err != nil {
		return rec, false, err
	}
	rec.identity.ifname = rec.ifname
	rec.hasIdentity = hasPid && hasStarted
	rec.hasPid = hasPid && !hasStarted && rec.identity.pid > 0
	return rec, !malformed && hasRx && hasTx && rec.user != "" && rec.ifname != "", nil
}

func sessionBytes(s l2tpSession) (rx int64, tx int64, ok bool) {
	if s.ifindex == "" {
		rx, tx = ifaceBytes(s.ifname)
		return rx, tx, true
	}
	if !sameInterface(s.ifname, s.ifindex) {
		return 0, 0, false
	}
	rx, tx = ifaceBytes(s.ifname)
	return rx, tx, sameInterface(s.ifname, s.ifindex)
}

func sameInterface(ifname, ifindex string) bool {
	b, err := os.ReadFile(filepath.Join(sysClassNet, ifname, "ifindex"))
	return err == nil && strings.TrimSpace(string(b)) == ifindex
}

func ifaceBytes(ifname string) (rx int64, tx int64) {
	base := filepath.Join(sysClassNet, ifname, "statistics")
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
