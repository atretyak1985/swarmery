package triage

// Advisor recommendation classes: the policy table's advisor/<class> keys.
// The list is closed and lives in code — a new advisor rule is plain until it
// is added to one of the tables below.
const (
	ClassInformational = "informational"
	ClassCard          = "card"
	ClassImprove       = "improve"
	ClassPlain         = "plain"
)

// informationalRules describe a session that is already over: nothing in it
// can change, so a run dismisses them on its own.
var informationalRules = map[string]bool{"R9": true}

// cardRules are fixable with one task in the project's repository.
var cardRules = map[string]bool{"R3": true, "R7": true, "R10": true, "T1": true}

// improveRules point at an agent's or skill's own instructions.
var improveRules = map[string]bool{"R2": true, "R4": true, "R8": true, "R11": true, "T2": true}

// AdvisorClass maps a recommendation's rule to its policy class. A card needs a
// project to put the card in; an improve needs a target the improve flow can
// rewrite. Either missing → plain. Unknown rules are plain.
func AdvisorClass(rule string, hasProject, improvable bool) string {
	switch {
	case informationalRules[rule]:
		return ClassInformational
	case cardRules[rule] && hasProject:
		return ClassCard
	case improveRules[rule] && improvable:
		return ClassImprove
	default:
		return ClassPlain
	}
}
