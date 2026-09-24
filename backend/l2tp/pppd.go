package l2tp

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

type processState uint8

const (
	processUnknown processState = iota
	processRunning
	processGone
)

const statStartTimeField = 19

func pppdState(pid int, fingerprint string) processState {
	stat, err := readProcFile(strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return processGone
		}
		return processUnknown
	}
	open, end := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return processUnknown
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) == 0 {
		return processUnknown
	}
	if !bytes.Contains(stat[open+1:end], []byte("pppd")) || fields[0] == "Z" || fields[0] == "X" {
		return processGone
	}
	boot, started, ok := strings.Cut(fingerprint, ":")
	if !ok {
		return processRunning
	}
	if len(fields) <= statStartTimeField {
		return processUnknown
	}
	if fields[statStartTimeField] != started {
		return processGone
	}
	current, err := readProcFile("sys/kernel/random/boot_id")
	if err == nil && strings.TrimSpace(string(current)) != boot {
		return processGone
	}
	return processRunning
}

var setAsideSeq atomic.Int64

func removeIfUnchanged(path string, seen []byte) {
	hidden := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.reap.%d.%d", filepath.Base(path), os.Getpid(), setAsideSeq.Add(1)))
	if err := os.Rename(path, hidden); err != nil {
		return
	}
	now, err := os.ReadFile(hidden)
	if err == nil && bytes.Equal(now, seen) {
		_ = os.Remove(hidden)
		return
	}
	if err := os.Link(hidden, path); err == nil || errors.Is(err, fs.ErrExist) {
		_ = os.Remove(hidden)
		return
	}
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		_ = os.Rename(hidden, path)
	}
}

func (o *L2TP) sweepPppdRecordsLocked() {
	entries, err := os.ReadDir(pppdRecordDir)
	if err != nil {
		return
	}
	if o.pppdGone == nil {
		o.pppdGone = make(map[string]int)
	}
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		name := e.Name()
		pid, err := strconv.Atoi(name)
		if e.IsDir() || err != nil || pid <= 0 {
			continue
		}
		path := filepath.Join(pppdRecordDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rec, _ := parseSession(raw, "")
		if !ownsRecord(rec.tag, o.config.InboundTag) {
			continue
		}
		seen[name] = struct{}{}
		if pppdState(pid, rec.proc) != processGone {
			delete(o.pppdGone, name)
			continue
		}
		o.pppdGone[name]++
		if o.pppdGone[name] > countedGraceRounds {
			removeIfUnchanged(path, raw)
			delete(o.pppdGone, name)
		}
	}
	for name := range o.pppdGone {
		if _, ok := seen[name]; !ok {
			delete(o.pppdGone, name)
		}
	}
}
