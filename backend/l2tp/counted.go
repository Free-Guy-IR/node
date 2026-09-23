package l2tp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const countedStateVersion = "v1"

type sessionIdentity struct {
	ifname  string
	pid     int
	started int64
}

type countedBytes struct {
	rx int64
	tx int64
}

type countedState struct {
	entries map[sessionIdentity]countedBytes
	dirty   bool
	failing bool
}

func newCountedState() *countedState {
	return &countedState{entries: make(map[sessionIdentity]countedBytes)}
}

func (c *countedState) get(id sessionIdentity) countedBytes {
	return c.entries[id]
}

func (c *countedState) set(id sessionIdentity, b countedBytes) {
	if prev, ok := c.entries[id]; ok && prev == b {
		return
	}
	c.entries[id] = b
	c.dirty = true
}

func (c *countedState) raise(id sessionIdentity, b countedBytes) {
	prev := c.entries[id]
	c.set(id, countedBytes{rx: max(prev.rx, b.rx), tx: max(prev.tx, b.tx)})
}

func (c *countedState) consume(rec finalRecord) countedBytes {
	id, ok := c.match(rec)
	if !ok {
		return countedBytes{}
	}
	b := c.entries[id]
	delete(c.entries, id)
	c.dirty = true
	return b
}

func (c *countedState) match(rec finalRecord) (sessionIdentity, bool) {
	if rec.hasIdentity {
		_, ok := c.entries[rec.identity]
		return rec.identity, ok
	}
	bare := sessionIdentity{ifname: rec.ifname}
	if _, ok := c.entries[bare]; ok {
		return bare, true
	}
	var found []sessionIdentity
	for id := range c.entries {
		if id.ifname == rec.ifname {
			found = append(found, id)
		}
	}
	if len(found) == 0 {
		return sessionIdentity{}, false
	}
	sortIdentities(found)
	return found[0], true
}

func (c *countedState) retain(live map[sessionIdentity]struct{}) {
	for id := range c.entries {
		if _, ok := live[id]; !ok {
			delete(c.entries, id)
			c.dirty = true
		}
	}
}

func sortIdentities(ids []sessionIdentity) {
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].started != ids[j].started {
			return ids[i].started < ids[j].started
		}
		if ids[i].pid != ids[j].pid {
			return ids[i].pid < ids[j].pid
		}
		return ids[i].ifname < ids[j].ifname
	})
}

func (c *countedState) encode() []byte {
	ids := make([]sessionIdentity, 0, len(c.entries))
	for id := range c.entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].ifname != ids[j].ifname {
			return ids[i].ifname < ids[j].ifname
		}
		if ids[i].pid != ids[j].pid {
			return ids[i].pid < ids[j].pid
		}
		return ids[i].started < ids[j].started
	})
	var b strings.Builder
	b.WriteString(countedStateVersion)
	b.WriteByte('\n')
	for _, id := range ids {
		e := c.entries[id]
		fmt.Fprintf(&b, "%s %d %d %d %d\n", id.ifname, id.pid, id.started, e.rx, e.tx)
	}
	return []byte(b.String())
}

func decodeCountedState(data []byte) (*countedState, error) {
	lines := strings.Split(string(data), "\n")
	if lines[0] != countedStateVersion {
		return nil, fmt.Errorf("unsupported counted state version %q", lines[0])
	}
	c := newCountedState()
	for i, line := range lines[1:] {
		if line == "" && i == len(lines)-2 {
			continue
		}
		id, b, err := parseCountedLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+2, err)
		}
		if _, dup := c.entries[id]; dup {
			return nil, fmt.Errorf("line %d: duplicate session %s", i+2, id.ifname)
		}
		c.entries[id] = b
	}
	return c, nil
}

func parseCountedLine(line string) (sessionIdentity, countedBytes, error) {
	f := strings.Split(line, " ")
	if len(f) != 5 || f[0] == "" {
		return sessionIdentity{}, countedBytes{}, fmt.Errorf("malformed entry %q", line)
	}
	pid, err := strconv.Atoi(f[1])
	if err != nil {
		return sessionIdentity{}, countedBytes{}, err
	}
	var nums [3]int64
	for i, s := range f[2:] {
		if nums[i], err = strconv.ParseInt(s, 10, 64); err != nil {
			return sessionIdentity{}, countedBytes{}, err
		}
	}
	if nums[1] < 0 || nums[2] < 0 {
		return sessionIdentity{}, countedBytes{}, fmt.Errorf("negative counter in %q", line)
	}
	return sessionIdentity{ifname: f[0], pid: pid, started: nums[0]}, countedBytes{rx: nums[1], tx: nums[2]}, nil
}

func countedStatePath(tag string) string {
	return filepath.Join(sessionCountedDir, tag)
}

func loadCountedState(tag string) (*countedState, error) {
	data, err := os.ReadFile(countedStatePath(tag))
	if errors.Is(err, fs.ErrNotExist) {
		return newCountedState(), nil
	}
	if err != nil {
		return nil, err
	}
	return decodeCountedState(data)
}

func (c *countedState) save(tag string) error {
	if err := os.MkdirAll(sessionCountedDir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(countedStatePath(tag), c.encode(), 0o600)
}

func presentAsCounted(tag string) *countedState {
	c := newCountedState()
	for _, s := range readSessions(tag) {
		rx, tx := ifaceBytes(s.ifname)
		c.raise(s.identity(), countedBytes{rx: rx, tx: tx})
	}
	for _, rec := range readFinalRecords(tag) {
		c.raise(rec.countedKey(), countedBytes{rx: rec.rx, tx: rec.tx})
	}
	c.dirty = true
	return c
}

func (o *L2TP) loadCountedLocked() {
	tag := o.config.InboundTag
	counted, err := loadCountedState(tag)
	if err != nil {
		o.emitLogf("Error", "l2tp: counted state of %s is unreadable, counting only growth from now on: %v", tag, err)
		counted = presentAsCounted(tag)
	}
	o.counted = counted
}

func (o *L2TP) persistCountedLocked() {
	if !o.counted.dirty {
		return
	}
	if err := o.counted.save(o.config.InboundTag); err != nil {
		if !o.counted.failing {
			o.emitLogf("Error", "l2tp: cannot persist the counted state of %s: %v", o.config.InboundTag, err)
		}
		o.counted.failing = true
		return
	}
	o.counted.dirty = false
	o.counted.failing = false
}
