package xray

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/pasarguard/node/backend"
	"github.com/pasarguard/node/backend/xray/api"
)

func credentialClaim(tag string, account api.Account) (string, bool) {
	switch a := account.(type) {
	case *api.VlessAccount:
		id := [16]byte(a.ID)
		id[6], id[7] = 0, 0
		return tag + "\x00" + Vless + "\x00" + string(id[:]), true
	case *api.VmessAccount:
		return tag + "\x00" + Vmess + "\x00" + string(a.ID[:]), true
	case *api.TrojanAccount:
		return tag + "\x00" + Trojan + "\x00" + a.Password, true
	}
	return "", false
}

func credentialConflictReason(claim, holder string) string {
	tag, rest, _ := strings.Cut(claim, "\x00")
	protocol, _, _ := strings.Cut(rest, "\x00")
	kind := "vless id"
	switch protocol {
	case Vmess:
		kind = "vmess id"
	case Trojan:
		kind = "trojan password"
	}
	return fmt.Sprintf("its %s on inbound %s is already held by user %s", kind, tag, holder)
}

func claimOrder(a, b string) int {
	na, errA := strconv.ParseUint(a, 10, 64)
	nb, errB := strconv.ParseUint(b, 10, 64)
	switch {
	case errA == nil && errB == nil && na != nb:
		return cmp.Compare(na, nb)
	case errA == nil && errB != nil:
		return -1
	case errA != nil && errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func (i *Inbound) credentialHoldersLocked(skip map[string]struct{}) map[string]string {
	holders := make(map[string]string, len(i.clients))
	for email, account := range i.clients {
		if _, skipped := skip[email]; skipped {
			continue
		}
		if claim, ok := credentialClaim(i.Tag, account); ok {
			holders[claim] = email
		}
	}
	return holders
}

func (i *Inbound) credentialHolders() map[string]string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.credentialHoldersLocked(nil)
}

func (i *Inbound) waitLocked(account api.Account) {
	if i.waiting == nil {
		i.waiting = make(map[string]api.Account)
	}
	i.waiting[account.GetEmail()] = account
}

func (i *Inbound) wait(account api.Account) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.waitLocked(account)
}

func (i *Inbound) admit(accounts []api.Account, leaving []string, rejections *backend.Rejections) ([]api.Account, []string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	gone := make(map[string]struct{}, len(leaving))
	for _, email := range leaving {
		gone[email] = struct{}{}
		delete(i.waiting, email)
	}
	holders := i.credentialHoldersLocked(gone)
	next := make(map[string]string, len(accounts))
	for _, account := range accounts {
		claim, _ := credentialClaim(i.Tag, account)
		next[account.GetEmail()] = claim
	}
	for claim, holder := range holders {
		if moved, inBatch := next[holder]; inBatch && moved != claim {
			delete(holders, claim)
		}
	}

	refused := make(map[string]struct{})
	for _, account := range slices.SortedStableFunc(slices.Values(accounts), func(a, b api.Account) int {
		return claimOrder(a.GetEmail(), b.GetEmail())
	}) {
		email := account.GetEmail()
		claim, ok := credentialClaim(i.Tag, account)
		if !ok {
			delete(i.waiting, email)
			continue
		}
		if holder, held := holders[claim]; held && holder != email {
			i.waitLocked(account)
			rejections.Add(email, credentialConflictReason(claim, holder))
			refused[email] = struct{}{}
			leaving = append(leaving, email)
			continue
		}
		holders[claim] = email
		delete(i.waiting, email)
	}

	admitted := make([]api.Account, 0, len(accounts))
	for _, account := range accounts {
		if _, no := refused[account.GetEmail()]; !no {
			admitted = append(admitted, account)
		}
	}
	return admitted, leaving
}

func (i *Inbound) admitUser(email string, account api.Account, active bool, rejections *backend.Rejections) (api.Account, bool) {
	if !active {
		i.admit(nil, []string{email}, rejections)
		return nil, false
	}
	admitted, _ := i.admit([]api.Account{account}, nil, rejections)
	if len(admitted) == 0 {
		return nil, false
	}
	return admitted[0], true
}

func (i *Inbound) promotable() []api.Account {
	i.mu.Lock()
	defer i.mu.Unlock()

	if len(i.waiting) == 0 {
		return nil
	}
	holders := i.credentialHoldersLocked(nil)
	var promoted []api.Account
	for _, email := range slices.SortedFunc(maps.Keys(i.waiting), claimOrder) {
		account := i.waiting[email]
		claim, _ := credentialClaim(i.Tag, account)
		if _, held := holders[claim]; held {
			continue
		}
		holders[claim] = email
		delete(i.waiting, email)
		promoted = append(promoted, account)
	}
	return promoted
}

func promoteWaiting(ctx context.Context, handler *api.XrayHandler, inbounds []*Inbound) error {
	var failures []error
	for _, inbound := range inbounds {
		if inbound.exclude {
			continue
		}
		for _, account := range inbound.promotable() {
			if err := pushAccount(ctx, handler, inbound, account, inbound.stageAccount(account)); err != nil {
				inbound.wait(account)
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (i *Inbound) settleCredentials(incumbents map[string]string, forget []string, keepWaiting bool, rejections *backend.Rejections) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !keepWaiting {
		i.waiting = nil
	}
	for _, email := range forget {
		delete(i.waiting, email)
	}
	if i.clients == nil {
		i.clients = make(map[string]api.Account)
	}

	accounts := make(map[string]api.Account, len(i.clients)+len(i.waiting))
	maps.Copy(accounts, i.waiting)
	maps.Copy(accounts, i.clients)
	claimants := make(map[string][]string)
	for email, account := range accounts {
		if claim, ok := credentialClaim(i.Tag, account); ok {
			claimants[claim] = append(claimants[claim], email)
		}
	}

	for _, claim := range slices.Sorted(maps.Keys(claimants)) {
		emails := claimants[claim]
		winner := slices.MinFunc(emails, claimOrder)
		if incumbent := incumbents[claim]; slices.Contains(emails, incumbent) {
			winner = incumbent
		}
		for _, email := range slices.SortedFunc(slices.Values(emails), claimOrder) {
			if email == winner {
				i.clients[email] = accounts[email]
				delete(i.waiting, email)
				continue
			}
			delete(i.clients, email)
			i.waitLocked(accounts[email])
			rejections.Add(email, credentialConflictReason(claim, winner))
		}
	}
}

func (x *Xray) reportRejections(err error) error {
	rejected, systemic := backend.SplitRejected(err)
	for _, user := range rejected {
		line := fmt.Sprintf("xray: user %s rejected: %s", user.Email, user.Reason)
		log.Println(line)
		x.emitNodeLog(line)
	}
	return systemic
}

func (x *Xray) emitNodeLog(line string) {
	x.mu.RLock()
	core := x.core
	x.mu.RUnlock()
	if core != nil {
		core.noteNodeLog(line)
	}
}
