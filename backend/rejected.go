package backend

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type RejectedUser struct {
	Email   string `json:"email"`
	Backend string `json:"backend"`
	Reason  string `json:"reason"`
}

type RejectedUsersError struct {
	Users []RejectedUser
}

func (e *RejectedUsersError) Error() string {
	parts := make([]string, 0, len(e.Users))
	for _, user := range e.Users {
		parts = append(parts, fmt.Sprintf("%s: %s", user.Email, user.Reason))
	}
	return fmt.Sprintf("%d user(s) rejected: %s", len(e.Users), strings.Join(parts, "; "))
}

func SplitRejected(err error) ([]RejectedUser, error) {
	switch e := err.(type) {
	case nil:
		return nil, nil
	case *RejectedUsersError:
		return append([]RejectedUser(nil), e.Users...), nil
	case interface{ Unwrap() []error }:
		var rejected []RejectedUser
		var rest []error
		for _, inner := range e.Unwrap() {
			found, other := SplitRejected(inner)
			rejected = append(rejected, found...)
			if other != nil {
				rest = append(rest, other)
			}
		}
		return rejected, errors.Join(rest...)
	default:
		return nil, err
	}
}

func JoinRejected(err error, rejected []RejectedUser) error {
	if len(rejected) == 0 {
		return err
	}
	report := &RejectedUsersError{Users: rejected}
	if err == nil {
		return report
	}
	return errors.Join(err, report)
}

type Rejections struct {
	reasons map[string][]string
}

func (r *Rejections) Add(email, reason string) {
	if r.reasons == nil {
		r.reasons = make(map[string][]string)
	}
	for _, existing := range r.reasons[email] {
		if existing == reason {
			return
		}
	}
	r.reasons[email] = append(r.reasons[email], reason)
}

func (r *Rejections) Has(email string) bool {
	_, ok := r.reasons[email]
	return ok
}

func (r *Rejections) Forget(email string) {
	delete(r.reasons, email)
}

func (r *Rejections) Len() int {
	return len(r.reasons)
}

func (r *Rejections) Users() []RejectedUser {
	emails := make([]string, 0, len(r.reasons))
	for email := range r.reasons {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	users := make([]RejectedUser, 0, len(emails))
	for _, email := range emails {
		users = append(users, RejectedUser{Email: email, Reason: strings.Join(r.reasons[email], "; ")})
	}
	return users
}

func (r *Rejections) Join(err error) error {
	if r == nil || len(r.reasons) == 0 {
		return err
	}
	return JoinRejected(err, r.Users())
}

type ClaimConflict struct {
	Email    string
	Resource string
	Owner    string
}

func ConflictingClaims(claims map[string][]string, owners map[string]string, ownerStays func(string) bool) []ClaimConflict {
	claimants := make(map[string][]string)
	for email, resources := range claims {
		seen := make(map[string]struct{}, len(resources))
		for _, resource := range resources {
			if _, dup := seen[resource]; dup {
				continue
			}
			seen[resource] = struct{}{}
			claimants[resource] = append(claimants[resource], email)
		}
	}

	resources := make([]string, 0, len(claimants))
	for resource := range claimants {
		resources = append(resources, resource)
	}
	sort.Strings(resources)

	var conflicts []ClaimConflict
	for _, resource := range resources {
		emails := claimants[resource]
		sort.Strings(emails)
		owner, owned := owners[resource]
		ownerClaims := false
		if owned {
			for _, email := range emails {
				if email == owner {
					ownerClaims = true
					break
				}
			}
		}
		switch {
		case ownerClaims || (owned && ownerStays(owner)):
			for _, email := range emails {
				if email != owner {
					conflicts = append(conflicts, ClaimConflict{Email: email, Resource: resource, Owner: owner})
				}
			}
		case len(emails) > 1:
			for _, email := range emails {
				conflicts = append(conflicts, ClaimConflict{Email: email, Resource: resource})
			}
		}
	}
	return conflicts
}
