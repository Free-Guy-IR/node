package l2tp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func (o *L2TP) sortOutFinalRecordsLocked(scan finalScan, keep map[sessionIdentity]struct{}) {
	if o.finalUnreadable == nil {
		o.finalUnreadable = make(map[string]int)
	}
	asideFailed := false
	for _, path := range scan.unusable {
		if !o.setAsideLocked(path, "has no user, no interface or a malformed value") {
			asideFailed = true
		}
	}
	current := make(map[string]struct{}, len(scan.unreadable))
	for _, u := range scan.unreadable {
		current[u.path] = struct{}{}
		if !o.finalReadFailing {
			o.emitLogf("Error", "l2tp: cannot read final record %s, it is retried at the next poll: %v", u.path, u.err)
			o.finalReadFailing = true
		}
		o.finalUnreadable[u.path]++
		if o.finalUnreadable[u.path] > countedGraceRounds {
			if o.setAsideLocked(u.path, "could not be read for the whole grace") {
				delete(o.finalUnreadable, u.path)
				continue
			}
			asideFailed = true
		}
		if ifname := finalNameInterface(filepath.Base(u.path)); ifname != "" {
			o.counted.holdInterface(ifname, keep)
		}
	}
	for path := range o.finalUnreadable {
		if _, ok := current[path]; !ok {
			delete(o.finalUnreadable, path)
		}
	}
	if len(scan.unreadable) == 0 {
		o.finalReadFailing = false
	}
	o.setAsideFailing = asideFailed
}

func (o *L2TP) setAsideLocked(path, reason string) bool {
	aside := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".unreadable")
	if err := os.Rename(path, aside); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		if !o.setAsideFailing {
			o.emitLogf("Error", "l2tp: final record %s %s and cannot be set aside: %v", path, reason, err)
		}
		return false
	}
	o.emitLogf("Error", "l2tp: final record %s %s, kept as %s and not billed", path, reason, aside)
	return true
}
