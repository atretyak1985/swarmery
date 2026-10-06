package triage

import "testing"

func TestAdvisorClassTable(t *testing.T) {
	// want[rule] = {neither, hasProject only, improvable only, both}
	want := map[string][4]string{
		"R1":  {ClassPlain, ClassPlain, ClassPlain, ClassPlain},
		"R2":  {ClassPlain, ClassPlain, ClassImprove, ClassImprove},
		"R3":  {ClassPlain, ClassCard, ClassPlain, ClassCard},
		"R4":  {ClassPlain, ClassPlain, ClassImprove, ClassImprove},
		"R5":  {ClassPlain, ClassPlain, ClassPlain, ClassPlain},
		"R6":  {ClassPlain, ClassPlain, ClassPlain, ClassPlain},
		"R7":  {ClassPlain, ClassCard, ClassPlain, ClassCard},
		"R8":  {ClassPlain, ClassPlain, ClassImprove, ClassImprove},
		"R9":  {ClassInformational, ClassInformational, ClassInformational, ClassInformational},
		"R10": {ClassPlain, ClassCard, ClassPlain, ClassCard},
		"R11": {ClassPlain, ClassPlain, ClassImprove, ClassImprove},
		"R12": {ClassPlain, ClassPlain, ClassPlain, ClassPlain},
		"T1":  {ClassPlain, ClassCard, ClassPlain, ClassCard},
		"T2":  {ClassPlain, ClassPlain, ClassImprove, ClassImprove},
		"Z99": {ClassPlain, ClassPlain, ClassPlain, ClassPlain},
		"":    {ClassPlain, ClassPlain, ClassPlain, ClassPlain},
	}
	combos := [4][2]bool{{false, false}, {true, false}, {false, true}, {true, true}}
	for rule, classes := range want {
		for i, c := range combos {
			if got := AdvisorClass(rule, c[0], c[1]); got != classes[i] {
				t.Errorf("AdvisorClass(%q, hasProject=%v, improvable=%v) = %q, want %q",
					rule, c[0], c[1], got, classes[i])
			}
		}
	}
}

func TestAdvisorClassTablesDisjoint(t *testing.T) {
	for rule := range cardRules {
		if improveRules[rule] || informationalRules[rule] {
			t.Errorf("rule %s is in more than one class table", rule)
		}
	}
	for rule := range improveRules {
		if informationalRules[rule] {
			t.Errorf("rule %s is in more than one class table", rule)
		}
	}
}

func TestAdvisorClassesHavePolicyRules(t *testing.T) {
	for _, class := range []string{ClassInformational, ClassCard, ClassImprove, ClassPlain} {
		// "advisor" alone has no rule, so a hit here is the advisor/<class> key.
		if _, ok := ruleFor("advisor", class); !ok {
			t.Errorf("no policy rule for advisor/%s", class)
		}
		if _, ok := policy["advisor/"+class]; !ok {
			t.Errorf("policy table has no advisor/%s key", class)
		}
	}
}
