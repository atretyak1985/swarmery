package phasegate

import "testing"

// A phase with no acceptance criteria is unprovable on its own row — and must
// not veto its PLAN. Two live plans sat at 102/102 and 13/13 ticked while
// reading `active`, held there by one prose-only phase doc each, with no action
// available that would ever clear it: the doc has no criteria to tick, and
// adding some retroactively would invent an agreement nobody made.
func TestHoldsPlanBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Input
		want bool
	}{
		{
			name: "no criteria at all does not hold the plan",
			in:   Input{CriteriaTotal: 0},
			want: false,
		},
		{
			name: "no criteria, but it asked to be verified and was not — still holds",
			in:   Input{CriteriaTotal: 0, VerifyMode: "strict", Dispatched: true},
			want: true,
		},
		{
			name: "every criterion ticked by hand, verify asked, never dispatched — holds nothing",
			in:   Input{CriteriaDone: 7, CriteriaTotal: 7, VerifyMode: "strict"},
			want: false,
		},
		{
			name: "criteria half done holds the plan",
			in:   Input{CriteriaDone: 1, CriteriaTotal: 3},
			want: true,
		},
		{
			name: "criteria met holds nothing",
			in:   Input{CriteriaDone: 3, CriteriaTotal: 3},
			want: false,
		},
		{
			name: "dispatched and unreported holds the plan",
			in: Input{CriteriaDone: 3, CriteriaTotal: 3, Ran: true,
				ClosureRequired: true, CompletionReport: ""},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Check(tc.in).HoldsPlanBack(tc.in); got != tc.want {
				t.Errorf("HoldsPlanBack = %v, want %v (state %q)", got, tc.want, Check(tc.in).State)
			}
		})
	}

	// The phase's OWN row keeps telling the truth: unprovable is still
	// unprovable, it just no longer speaks for the whole plan.
	if got := Check(Input{CriteriaTotal: 0}); got.State != StateIncomplete {
		t.Errorf("a zero-criteria phase now reads %q on its own row, want incomplete", got.State)
	}
}
