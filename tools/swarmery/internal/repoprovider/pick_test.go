package repoprovider

import (
	"testing"
	"time"
)

func TestPickChangeRequest(t *testing.T) {
	t1 := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	fc := func(n int, state string, merged time.Time) FoundChange {
		return FoundChange{Ref: ChangeRef{Number: n, URL: "u"}, State: state, MergedAt: merged}
	}
	cases := []struct {
		name  string
		cands []FoundChange
		want  int // 0 ⇒ not found
	}{
		{"nothing", nil, 0},
		{"closed only", []FoundChange{fc(1, StateClosed, time.Time{})}, 0},
		{"open beats a later merge", []FoundChange{fc(9, StateMerged, t2), fc(3, StateOpen, time.Time{})}, 3},
		{"highest open", []FoundChange{fc(3, StateOpen, time.Time{}), fc(7, StateOpen, time.Time{}), fc(5, StateOpen, time.Time{})}, 7},
		{"latest merge", []FoundChange{fc(9, StateMerged, t1), fc(4, StateMerged, t2), fc(2, StateClosed, time.Time{})}, 4},
		{"merge tie by number", []FoundChange{fc(4, StateMerged, t1), fc(6, StateMerged, t1)}, 6},
	}
	for _, c := range cases {
		ref, ok := PickChangeRequest(c.cands)
		if got := map[bool]int{true: ref.Number, false: 0}[ok]; got != c.want {
			t.Errorf("%s: got #%d ok=%v, want #%d", c.name, ref.Number, ok, c.want)
		}
	}
}
