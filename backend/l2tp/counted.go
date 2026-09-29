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
	"time"
)

var wallClock = time.Now

const (
	countedStateVersion = "v2"
	countedGraceRounds  = 360

	countedConsumedPrefix = "consumed "
	countedFinalPrefix    = "final "
)

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
	entries  map[sessionIdentity]countedBytes
	misses   map[sessionIdentity]int
	consumed map[sessionIdentity]bool
	counted  map[string]bool
	dirty    bool
	failing  bool

	pendingSessions bool
	pendingFinals   bool
}

func newCountedState() *countedState {
	return &countedState{
		entries:  make(map[sessionIdentity]countedBytes),
		misses:   make(map[sessionIdentity]int),
		consumed: make(map[sessionIdentity]bool),
		counted:  make(map[string]bool),
	}
}

func (c *countedState) lookup(id sessionIdentity) (countedBytes, bool) {
	b, ok := c.entries[id]
	return b, ok
}

func (c *countedState) inherit(id sessionIdentity, now countedBytes) countedBytes {
	if id.pid == 0 {
		return countedBytes{}
	}
	var prev sessionIdentity
	found := false
	for w := range c.entries {
		if w.ifname == id.ifname && w.pid == id.pid && w.started < id.started && (!found || w.started > prev.started) {
			prev, found = w, true
		}
	}
	if !found {
		return countedBytes{}
	}
	base := c.entries[prev]
	if now.rx < base.rx || now.tx < base.tx {
		return countedBytes{}
	}
	c.raise(prev, now)
	return base
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
	name := filepath.Base(rec.path)
	if c.counted[name] {
		delete(c.counted, name)
		c.dirty = true
		if rec.hasIdentity {
			c.markConsumed(rec.identity, countedBytes{rx: rec.rx, tx: rec.tx})
		}
		return countedBytes{rx: rec.rx, tx: rec.tx}
	}
	id, ok := c.match(rec)
	if !ok {
		if !rec.hasIdentity {
			return countedBytes{}
		}
		final := countedBytes{rx: rec.rx, tx: rec.tx}
		var base countedBytes
		if rec.proc == "" {
			base = c.inherit(rec.identity, final)
		}
		c.markConsumed(rec.identity, final)
		return base
	}
	base := c.entries[id]
	c.markConsumed(id, countedBytes{rx: rec.rx, tx: rec.tx})
	return base
}

func (c *countedState) markConsumed(id sessionIdentity, final countedBytes) {
	c.raise(id, final)
	if !c.consumed[id] {
		c.consumed[id] = true
		c.dirty = true
	}
	delete(c.misses, id)
}

func (c *countedState) match(rec finalRecord) (sessionIdentity, bool) {
	if rec.hasIdentity {
		_, ok := c.entries[rec.identity]
		return rec.identity, ok
	}
	var found []sessionIdentity
	for id := range c.entries {
		if id.ifname == rec.ifname && !c.consumed[id] && (!rec.hasPid || id.pid == rec.identity.pid) {
			found = append(found, id)
		}
	}
	if len(found) == 0 {
		return sessionIdentity{}, false
	}
	sort.Slice(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if c.misses[a] != c.misses[b] {
			return c.misses[a] < c.misses[b]
		}
		if a.started != b.started {
			return a.started > b.started
		}
		return a.pid > b.pid
	})
	return found[0], true
}

func (c *countedState) hold(rec finalRecord, keep map[sessionIdentity]struct{}) {
	if id, ok := c.match(rec); ok {
		keep[id] = struct{}{}
		return
	}
	if !rec.hasIdentity || rec.proc != "" || rec.identity.pid == 0 {
		return
	}
	for id := range c.entries {
		if id.ifname == rec.ifname && id.pid == rec.identity.pid {
			keep[id] = struct{}{}
		}
	}
}

func (c *countedState) holdAll(keep map[sessionIdentity]struct{}) {
	for id := range c.entries {
		keep[id] = struct{}{}
	}
}

func (c *countedState) holdInterface(ifname string, keep map[sessionIdentity]struct{}) {
	for id := range c.entries {
		if id.ifname == ifname {
			keep[id] = struct{}{}
		}
	}
}

func (c *countedState) retain(keep map[sessionIdentity]struct{}) {
	for id := range c.entries {
		if _, ok := keep[id]; ok {
			delete(c.misses, id)
			continue
		}
		c.misses[id]++
		if c.misses[id] > countedGraceRounds {
			delete(c.entries, id)
			delete(c.misses, id)
			delete(c.consumed, id)
			c.dirty = true
		}
	}
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
		if c.consumed[id] {
			b.WriteString(countedConsumedPrefix)
		}
		fmt.Fprintf(&b, "%s %d %d %d %d\n", id.ifname, id.pid, id.started, e.rx, e.tx)
	}
	names := make([]string, 0, len(c.counted))
	for name := range c.counted {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString(countedFinalPrefix + name + "\n")
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
		if name, ok := strings.CutPrefix(line, countedFinalPrefix); ok {
			if name == "" || strings.ContainsAny(name, " /") {
				return nil, fmt.Errorf("line %d: malformed final record name %q", i+2, name)
			}
			c.counted[name] = true
			continue
		}
		entry, consumed := strings.CutPrefix(line, countedConsumedPrefix)
		id, b, err := parseCountedLine(entry)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+2, err)
		}
		if _, dup := c.entries[id]; dup {
			return nil, fmt.Errorf("line %d: duplicate session %s", i+2, id.ifname)
		}
		c.entries[id] = b
		if consumed {
			c.consumed[id] = true
		}
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

func presentAsCounted(tag string, _ int64) *countedState {
	c := newCountedState()
	c.takePresent(scanSessions(tag), scanFinalRecords(tag))
	c.dirty = true
	return c
}

func (c *countedState) takePresent(sessions sessionScan, finals finalScan) {
	c.takeSessions(sessions)
	c.takeFinals(finals)
	c.pendingSessions = !sessions.complete()
	c.pendingFinals = finals.listErr != nil
}

func (c *countedState) takeSessions(sessions sessionScan) {
	for _, s := range sessions.sessions {
		id := s.identity()
		if _, known := c.entries[id]; known {
			continue
		}
		if rx, tx, ok := sessionBytes(s); ok {
			c.raise(id, countedBytes{rx: rx, tx: tx})
		}
	}
}

func (c *countedState) takeFinals(finals finalScan) {
	for _, rec := range finals.records {
		if _, known := c.entries[rec.identity]; known && rec.hasIdentity {
			continue
		}
		c.counted[filepath.Base(rec.path)] = true
	}
	for _, u := range finals.unreadable {
		c.counted[filepath.Base(u.path)] = true
	}
}

func (c *countedState) takeLatePresent(sessions sessionScan, finals finalScan) {
	if c.pendingSessions && sessions.complete() {
		c.takeSessions(sessions)
		c.pendingSessions = false
		c.dirty = true
	}
	if c.pendingFinals && finals.listErr == nil {
		c.takeFinals(finals)
		c.pendingFinals = false
		c.dirty = true
	}
}

func (o *L2TP) loadCountedLocked() {
	tag := o.config.InboundTag
	counted, err := loadCountedState(tag)
	if err != nil {
		o.emitLogf("Error", "l2tp: counted state of %s is unreadable, counting only growth from now on: %v", tag, err)
		counted = presentAsCounted(tag, wallClock().Unix())
		if !counted.pendingSessions && !counted.pendingFinals && counted.save(tag) == nil {
			counted.dirty = false
		}
	}
	o.counted = counted
}

func (o *L2TP) persistCountedLocked() {
	if !o.counted.dirty || o.counted.pendingSessions || o.counted.pendingFinals {
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
