package backend

import (
	"errors"
	"reflect"
	"testing"
)

func TestSplitRejectedSeparatesRejectionsFromSystemicErrors(t *testing.T) {
	var none Rejections
	if rejected, err := SplitRejected(none.Join(nil)); rejected != nil || err != nil {
		t.Fatalf("no rejection and no error must split to nothing, got %v %v", rejected, err)
	}

	var only Rejections
	only.Add("b@x", "bad key")
	only.Add("a@x", "bad ip")
	only.Add("a@x", "bad ip")
	only.Add("a@x", "taken")
	rejected, err := SplitRejected(only.Join(nil))
	if err != nil {
		t.Fatalf("a pure rejection must not surface as an error, got %v", err)
	}
	want := []RejectedUser{{Email: "a@x", Reason: "bad ip; taken"}, {Email: "b@x", Reason: "bad key"}}
	if !reflect.DeepEqual(rejected, want) {
		t.Fatalf("rejections = %+v, want %+v", rejected, want)
	}

	systemic := errors.New("core api unreachable")
	rejected, err = SplitRejected(errors.Join(errors.New("other"), only.Join(systemic)))
	if len(rejected) != 2 {
		t.Fatalf("rejections nested in a join must be found, got %+v", rejected)
	}
	if !errors.Is(err, systemic) || err.Error() != "other\ncore api unreachable" {
		t.Fatalf("the systemic errors must survive the split intact, got %v", err)
	}

	rejected, err = SplitRejected(systemic)
	if rejected != nil || err != systemic {
		t.Fatalf("a plain error must pass through untouched, got %v %v", rejected, err)
	}
}

func TestConflictingClaimsKeepsTheCurrentOwner(t *testing.T) {
	stays := func(email string) bool { return email == "owner@x" }
	owners := map[string]string{"key:k1": "owner@x", "key:k2": "mover@x", "ip:10": "mover@x"}
	claims := map[string][]string{
		"incoming@x": {"key:k1"},
		"mover@x":    {"key:k3"},
		"taker@x":    {"key:k2"},
		"a@x":        {"ip:20"},
		"b@x":        {"ip:20"},
		"c@x":        {"ip:10"},
		"d@x":        {"ip:10"},
	}
	got := ConflictingClaims(claims, owners, stays)
	want := []ClaimConflict{
		{Email: "c@x", Resource: "ip:10"},
		{Email: "d@x", Resource: "ip:10"},
		{Email: "a@x", Resource: "ip:20"},
		{Email: "b@x", Resource: "ip:20"},
		{Email: "incoming@x", Resource: "key:k1", Owner: "owner@x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conflicts = %+v, want %+v", got, want)
	}

	got = ConflictingClaims(map[string][]string{"owner@x": {"key:k1"}, "rival@x": {"key:k1"}}, owners, func(string) bool { return false })
	want = []ClaimConflict{{Email: "rival@x", Resource: "key:k1", Owner: "owner@x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("an owner that re-claims its own resource must keep it, got %+v", got)
	}
}
