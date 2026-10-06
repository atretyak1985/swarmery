package triage

import "testing"

func TestPolicyTable(t *testing.T) {
	allowed := []string{"a", "b", "dismiss", "fix-card", "improve", "track", "accept", "not-useful",
		"stop", "keep", "noise", "fixable", "file"}
	p := Part{Ref: "r1", Allowed: allowed}
	cases := []struct {
		kind, class, value, want string
	}{
		{"classifier", "", "a", DecisionAuto},
		{"classifier", "", "b", DecisionAuto},
		{"classifier", "any", "a", DecisionAuto}, // falls back to the kind rule
		{"advisor", "informational", "dismiss", DecisionAuto},
		{"advisor", "informational", "track", DecisionReject},
		{"advisor", "card", "fix-card", DecisionSuggest},
		{"advisor", "card", "dismiss", DecisionSuggest},
		{"advisor", "improve", "improve", DecisionSuggest},
		{"advisor", "improve", "dismiss", DecisionSuggest},
		{"advisor", "plain", "track", DecisionSuggest},
		{"advisor", "plain", "dismiss", DecisionSuggest},
		{"advisor", "", "dismiss", DecisionReject}, // no bare advisor rule
		{"advisor", "unknown", "dismiss", DecisionReject},
		{"lesson", "", "accept", DecisionSuggest},
		{"lesson", "", "not-useful", DecisionSuggest},
		{"retire", "", "stop", DecisionSuggest},
		{"retire", "", "keep", DecisionSuggest},
		{"friction", "", "noise", DecisionAuto},
		{"friction", "", "fixable", DecisionAuto},
		{"friction", "", "file", DecisionReject},
		{"agent", "", "file", DecisionAuto},
		{"agent", "", "noise", DecisionReject},
		// skip is always legal for a known kind and never applies
		{"classifier", "", ValueSkip, DecisionSkip},
		{"lesson", "", ValueSkip, DecisionSkip},
		// value outside Part.Allowed
		{"classifier", "", "zzz", DecisionReject},
		{"friction", "", "", DecisionReject},
		// unknown kind
		{"mystery", "", "a", DecisionReject},
		{"mystery", "", ValueSkip, DecisionReject},
		// denied kinds always reject, even skip
		{"approval", "", "a", DecisionReject},
		{"proposal", "", "a", DecisionReject},
		{"alert", "", "a", DecisionReject},
		{"approval", "", ValueSkip, DecisionReject},
	}
	for _, c := range cases {
		if got := decide(c.kind, c.class, p, c.value); got != c.want {
			t.Errorf("decide(%q,%q,%q) = %q, want %q", c.kind, c.class, c.value, got, c.want)
		}
	}
}

func TestPolicyOutsideAllowedRejects(t *testing.T) {
	p := Part{Ref: "r", Allowed: []string{"noise"}}
	if got := decide("friction", "", p, "fixable"); got != DecisionReject {
		t.Fatalf("fixable not in Allowed: got %q", got)
	}
}

func TestPolicySampleMax(t *testing.T) {
	if got := sampleMax("classifier"); got != 5 {
		t.Fatalf("classifier sample = %d", got)
	}
	t.Setenv("SWARMERY_TRIAGE_SAMPLE", "2")
	if got := sampleMax("classifier"); got != 2 {
		t.Fatalf("override sample = %d", got)
	}
	if got := sampleMax("friction"); got != 0 {
		t.Fatalf("friction sample = %d", got)
	}
	if got := sampleMax("nope"); got != 0 {
		t.Fatalf("unknown sample = %d", got)
	}
}
