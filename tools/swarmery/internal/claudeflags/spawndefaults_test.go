package claudeflags_test

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/dispatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/extract"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/handoff"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/provision"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/retroanalysis"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/routines"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/trajjudge"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
)

// Every headless spawn this daemon makes has to answer two questions before it
// starts: which model, and how hard it thinks. Neither has a safe "unset".
//
//   - No --model does not mean a house default. It means the ACCOUNT default,
//     which on these accounts is Fable — roughly twice the Opus price. The rung
//     nobody chooses was the most expensive one available.
//   - No --effort does not mean the cheap end. It means the CLI's own default,
//     xhigh, the deepest setting there is. A config probe that reads two files
//     was paying maximum reasoning depth.
//
// Both failures are silent: nothing errors, nothing logs, the run just costs
// more than the work was worth. So the defence is this table — one row per spawn
// site, naming the two values, with the whole point being that a row cannot be
// empty. A new engine added without pinning both fails here, and a value changed
// by accident changes a line of this test, which is a line a reviewer reads.
//
// The values themselves are DELIBERATE and phase 7 re-measures them against
// observed cost and quality; what this test pins is that each one was chosen at
// all. Its companion, spawnsites_test.go, proves no spawn site is missing from
// the list — this file proves no listed site is undecided.

func TestEverySpawnSiteDeclaresAModelAndAnEffort(t *testing.T) {
	// Deliberately literal rather than computed. If this table read
	// `planning.DefaultEffort` into its own `want`, a typo'd constant would
	// satisfy the assertion by definition and the test would pin nothing.
	for _, tc := range []struct {
		site   string
		effort string
		model  string
	}{
		// Reasoning-heavy engines: open-ended work, an operator waiting on the
		// result, and a long window over which depth actually pays.
		{"planning", planning.DefaultEffort, planning.DefaultModel},
		{"phaserun", phaserun.DefaultEffort, planning.DefaultModel},
		{"planrun", planrun.DefaultEffort, planrun.DefaultModel()},
		{"provision", provision.DefaultEffort, provision.DefaultModel},
		{"improve", improve.DefaultEffort, improve.DefaultModel},
		{"retroanalysis", retroanalysis.DefaultEffort, retroanalysis.DefaultModel},
		{"resume", api.ResumeEffort, ""}, // model is the session's own; see below

		// Scoped work against a contract the prompt already carries.
		{"dispatch", dispatch.DefaultEffort, dispatch.DefaultModel},
		{"verify", verify.DefaultEffort, verify.DefaultModel},
		{"routines", routines.DefaultEffort, routines.DefaultModel},
		{"extract", extract.DefaultEffort, extract.DefaultModel},

		// Mechanical passes over material the prompt already contains.
		{"handoff", handoff.DefaultEffort, handoff.DefaultModel},
		{"trajjudge", trajjudge.DefaultEffort, trajjudge.DefaultModel},
		{"lessons", lessons.DefaultEffort, lessons.DefaultModel},
		{"probe", api.ProbeEffort, api.ProbeModel},
	} {
		t.Run(tc.site, func(t *testing.T) {
			if tc.effort == "" {
				t.Fatalf("%s declares no default effort — an omitted --effort is the CLI's xhigh, "+
					"so this site would silently run at maximum reasoning depth", tc.site)
			}
			if _, ok := claudeflags.NormalizeEffort(tc.effort); !ok {
				t.Fatalf("%s declares effort %q, which the CLI will reject — the spawn would die before the run starts",
					tc.site, tc.effort)
			}
			// resume is the one site with no model of its own, and that is correct
			// rather than an omission: it continues an EXISTING session and takes
			// that session's model off the sessions row (lookupResumeOrigin). A
			// default here would override the conversation it is continuing.
			if tc.site == "resume" {
				return
			}
			if tc.model == "" {
				t.Fatalf("%s declares no default model — an omitted --model is the ACCOUNT default (Fable, ~2× Opus), "+
					"which is the most expensive outcome and the one nobody picked", tc.site)
			}
		})
	}
}

// TestPinnedDefaultsAreTheAgreedValues is the second half: the table above
// proves nothing is empty, this proves nothing drifted. Split in two so a
// deliberate retune (phase 7) touches ONE list and still has to be a diff
// somebody reads.
func TestPinnedDefaultsAreTheAgreedValues(t *testing.T) {
	for _, tc := range []struct {
		site string
		got  string
		want string
	}{
		{"probe", api.ProbeEffort, "low"},
		{"handoff", handoff.DefaultEffort, "low"},
		{"trajjudge", trajjudge.DefaultEffort, "low"},
		{"lessons", lessons.DefaultEffort, "low"},
		{"extract", extract.DefaultEffort, "medium"},
		{"dispatch", dispatch.DefaultEffort, "medium"},
		{"verify", verify.DefaultEffort, "medium"},
		{"routines", routines.DefaultEffort, "medium"},
		{"planning", planning.DefaultEffort, "high"},
		{"phaserun", phaserun.DefaultEffort, "high"},
		{"planrun", planrun.DefaultEffort, "high"},
		{"provision", provision.DefaultEffort, "high"},
		{"improve", improve.DefaultEffort, "high"},
		{"retroanalysis", retroanalysis.DefaultEffort, "high"},
		{"resume", api.ResumeEffort, "high"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s default effort = %q, want %q — if this retune is deliberate, change the want and say why in the commit",
				tc.site, tc.got, tc.want)
		}
	}
}
