package phasegate

import (
	"reflect"
	"testing"
)

func TestPlanningTurnLink(t *testing.T) {
	if got, want := PlanningTurnLink("my proj", 7, 3), "/p/my%20proj/plans?tab=new&planningSession=7&turn=3#planning-turn-3"; got != want {
		t.Errorf("link = %q, want %q", got, want)
	}
	for _, c := range []struct {
		slug    string
		session int64
		seq     int
	}{{"", 7, 3}, {"p", 0, 3}, {"p", 7, 0}} {
		if got := PlanningTurnLink(c.slug, c.session, c.seq); got != "" {
			t.Errorf("PlanningTurnLink(%q, %d, %d) = %q, want empty", c.slug, c.session, c.seq, got)
		}
	}
}

// The hold names each holding decision with the planning turn that asked it; a
// README-born decision has no turn; cheap ones never appear.
func TestHoldRefs(t *testing.T) {
	ds := []Decision{
		{ID: "D-2", Question: "TTL?", Cost: "cheap", Status: "recommended", Seq: 2, PlanningSessionID: 7},
		{ID: "D-3", Question: "Which\nstore?", Cost: "expensive", Status: "undecided", Seq: 3, PlanningSessionID: 7},
		{ID: "D-8", Question: "Split?", Cost: "expensive", Status: "undecided", Seq: 8},
	}
	got := HoldRefs("p", []string{"D-2", "D-3", "D-8"}, ds)
	want := []HoldRef{
		{DecisionID: "D-3", Question: "Which store?", Seq: 3, PlanningSessionID: 7,
			PlanningTurn: "/p/p/plans?tab=new&planningSession=7&turn=3#planning-turn-3"},
		{DecisionID: "D-8", Question: "Split?", Seq: 8},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HoldRefs =\n%+v\nwant\n%+v", got, want)
	}
	if HoldRefs("p", []string{"D-2"}, ds) != nil {
		t.Error("a cheap recommended decision produced a hold ref")
	}
}
