package l2tp

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	countedPriorPrefix    = "prior "
	countedSincePrefix    = "since "
	countedSeenMark       = "+"
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

	since      int64
	sinceMono  monoStamp
	sinceBoots []string
	sinceClean int
	sinceSeen  bool
	sincePrior map[string]bool
	bootKnown  bool
}

func newCountedState() *countedState {
	return &countedState{
		entries:    make(map[sessionIdentity]countedBytes),
		misses:     make(map[sessionIdentity]int),
		consumed:   make(map[sessionIdentity]bool),
		counted:    make(map[string]bool),
		sincePrior: make(map[string]bool),
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
	prior := c.sincePrior[name]
	if prior {
		delete(c.sincePrior, name)
		c.dirty = true
	}
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
		final := countedBytes{rx: rec.rx, tx: rec.tx}
		if c.startedBeforeFallback(rec, prior) {
			if rec.hasIdentity {
				c.markConsumed(rec.identity, final)
			}
			return final
		}
		if !rec.hasIdentity {
			return countedBytes{}
		}
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
	for _, name := range slices.Sorted(maps.Keys(c.sincePrior)) {
		b.WriteString(countedPriorPrefix + name + "\n")
	}
	if c.since > 0 {
		fmt.Fprintf(&b, "%s%d", countedSincePrefix, c.since)
		if c.sinceMono.ok {
			b.WriteString(" " + c.sinceMono.String())
			for _, boot := range c.sinceBoots {
				b.WriteString(" " + boot)
			}
			if c.sinceSeen {
				b.WriteString(" " + countedSeenMark)
			}
		}
		b.WriteByte('\n')
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
		if name, ok := strings.CutPrefix(line, countedPriorPrefix); ok {
			if name == "" || strings.ContainsAny(name, " /") {
				return nil, fmt.Errorf("line %d: malformed prior final record name %q", i+2, name)
			}
			c.sincePrior[name] = true
			continue
		}
		if at, ok := strings.CutPrefix(line, countedSincePrefix); ok {
			fields := strings.Split(at, " ")
			since, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil || since <= 0 || c.since != 0 {
				return nil, fmt.Errorf("line %d: malformed fallback time %q", i+2, at)
			}
			c.since = since
			if len(fields) > 1 {
				c.sinceMono = parseMono(fields[1])
				if !c.sinceMono.ok {
					return nil, fmt.Errorf("line %d: malformed fallback boot time %q", i+2, at)
				}
			}
			for _, boot := range fields[min(len(fields), 2):] {
				if boot == countedSeenMark {
					c.sinceSeen = true
					continue
				}
				if boot == "" || strings.Contains(boot, ":") {
					return nil, fmt.Errorf("line %d: malformed later boot %q", i+2, at)
				}
				c.sinceBoots = append(c.sinceBoots, boot)
			}
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

func presentAsCounted(tag string, now int64, mono monoStamp) *countedState {
	c := newCountedState()
	sessions, finals := scanSessions(tag), scanFinalRecords(tag)
	resolved := true
	for _, s := range sessions.sessions {
		if rx, tx, ok := sessionBytes(s); ok {
			c.raise(s.identity(), countedBytes{rx: rx, tx: tx})
		} else {
			resolved = false
		}
	}
	for _, rec := range finals.records {
		c.counted[filepath.Base(rec.path)] = true
	}
	if !resolved || !sessions.complete() || finals.listErr != nil || len(finals.unreadable) > 0 {
		c.since = now
		c.sinceMono = mono
	}
	c.dirty = true
	return c
}

func (c *countedState) startedBefore(started int64, mono monoStamp, live bool) bool {
	if c.sinceMono.ok && mono.ok {
		if mono.boot == c.sinceMono.boot {
			return mono.centis < c.sinceMono.centis
		}
		return !live && !slices.Contains(c.sinceBoots, mono.boot)
	}
	return started < c.since
}

func (c *countedState) startedBeforeFallback(rec finalRecord, prior bool) bool {
	if c.since > 0 && rec.hasIdentity && c.sinceSeen && !prior && c.unplaced(rec.mono) {
		return false
	}
	return c.since > 0 && (!rec.hasIdentity || c.startedBefore(rec.identity.started, rec.mono, false))
}

func (c *countedState) unplaced(mono monoStamp) bool {
	return c.sinceMono.ok && mono.ok && mono.boot != c.sinceMono.boot && !slices.Contains(c.sinceBoots, mono.boot)
}

func (c *countedState) undecided(rec finalRecord) bool {
	if c.since == 0 || c.bootKnown || !rec.hasIdentity || !c.unplaced(rec.mono) {
		return false
	}
	_, known := c.match(rec)
	return !known
}

func (c *countedState) takeStartedBeforeFallback(sessions []l2tpSession) bool {
	if c.since == 0 {
		return true
	}
	resolved := true
	for _, s := range sessions {
		id := s.identity()
		if _, known := c.entries[id]; known || !c.startedBefore(s.started, s.mono, true) {
			continue
		}
		if rx, tx, ok := sessionBytes(s); ok {
			c.raise(id, countedBytes{rx: rx, tx: tx})
		} else {
			resolved = false
		}
	}
	return resolved
}

func (c *countedState) settleFallback(clean bool) {
	if c.since == 0 {
		return
	}
	if !clean {
		c.sinceClean = 0
		return
	}
	c.sinceClean++
	if c.sinceClean > countedGraceRounds {
		c.since = 0
		c.sinceMono = monoStamp{}
		c.sinceBoots = nil
		c.sinceClean = 0
		c.sinceSeen = false
		clear(c.sincePrior)
		c.dirty = true
	}
}

func (o *L2TP) loadCountedLocked() {
	tag := o.config.InboundTag
	counted, err := loadCountedState(tag)
	if err != nil {
		o.emitLogf("Error", "l2tp: counted state of %s is unreadable, counting only growth from now on: %v", tag, err)
		counted = presentAsCounted(tag, wallClock().Unix(), currentMono())
	}
	counted.noteBoot(currentBootID())
	if counted.awaitsSeen() {
		counted.noteSeen(scanFinalRecords(tag))
	}
	if counted.dirty && counted.save(tag) == nil {
		counted.dirty = false
	}
	o.counted = counted
}

func (c *countedState) awaitsSeen() bool {
	return c.since > 0 && c.sinceMono.ok && !c.sinceSeen
}

func (c *countedState) noteSeen(scan finalScan) {
	if scan.listErr != nil {
		return
	}
	paths := make([]string, 0, len(scan.records)+len(scan.unreadable))
	for _, rec := range scan.records {
		paths = append(paths, rec.path)
	}
	for _, u := range scan.unreadable {
		paths = append(paths, u.path)
	}
	for _, path := range paths {
		if name := filepath.Base(path); !c.counted[name] {
			c.sincePrior[name] = true
		}
	}
	c.sinceSeen = true
	c.dirty = true
}

func (c *countedState) noteBoot(boot string) {
	if boot == "" {
		return
	}
	c.bootKnown = true
	if c.since == 0 || !c.sinceMono.ok || boot == c.sinceMono.boot || slices.Contains(c.sinceBoots, boot) {
		return
	}
	c.sinceBoots = append(c.sinceBoots, boot)
	c.dirty = true
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
