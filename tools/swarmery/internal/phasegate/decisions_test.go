package phasegate

import "testing"

func TestHoldsForDecisions(t *testing.T) {
	ds := []Decision{
		{ID: "D-1", Question: "Key shape?", Cost: "expensive", Status: "decided"},
		{ID: "D-2", Question: "TTL?", Cost: "cheap", Status: "undecided"},
		{ID: "D-3", Question: "Which  store?", Cost: "expensive", Status: "undecided"},
		{ID: "D-4", Question: "Name?", Cost: "cheap", Status: "recommended"},
		{ID: "D-5", Question: "Split?", Cost: "expensive", Status: "undecided"},
	}
	cases := []struct {
		name   string
		deps   []string
		hold   bool
		reason string
	}{
		{"no deps", nil, false, ""},
		{"decided expensive", []string{"D-1"}, false, ""},
		{"cheap undecided never holds", []string{"D-2"}, false, ""},
		{"recommended", []string{"D-4"}, false, ""},
		{"undecided expensive", []string{"D-1", "D-3"}, true, "decision-undecided: D-3 (Which store?)"},
		{"two holding, listed order", []string{"D-5", "D-2", "d-3"}, true, "decision-undecided: D-5 (Split?), D-3 (Which store?)"},
		{"unknown id does not hold", []string{"D-9"}, false, ""},
	}
	for _, c := range cases {
		hold, reason := HoldsForDecisions(c.deps, ds)
		if hold != c.hold || reason != c.reason {
			t.Errorf("%s: (%v, %q), want (%v, %q)", c.name, hold, reason, c.hold, c.reason)
		}
	}
}

// A holding decision that knows its planning turn names it in the hold detail —
// the "decide now" link every surface shows.
func TestHoldsForDecisionsCarriesTheTurnLink(t *testing.T) {
	link := PlanningTurnLink("my proj", 7, 3)
	if link != "/p/my%20proj/plans?tab=new&planningSession=7&turn=3#planning-turn-3" {
		t.Fatalf("PlanningTurnLink = %q", link)
	}
	for _, bad := range []string{PlanningTurnLink("", 7, 3), PlanningTurnLink("p", 0, 3), PlanningTurnLink("p", 7, 0)} {
		if bad != "" {
			t.Errorf("an unknown slug/session/seq built a link: %q", bad)
		}
	}
	ds := []Decision{{ID: "D-3", Question: "Which store?", Cost: "expensive", Status: "undecided", Link: link}}
	hold, reason := HoldsForDecisions([]string{"D-3"}, ds)
	if want := "decision-undecided: D-3 (Which store?) — decide now: " + link; !hold || reason != want {
		t.Errorf("(%v, %q), want (true, %q)", hold, reason, want)
	}
}
