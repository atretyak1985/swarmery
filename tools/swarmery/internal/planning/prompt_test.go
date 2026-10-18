package planning

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// BuildPrompt
// ---------------------------------------------------------------------------

func TestBuildPrompt(t *testing.T) {
	p := BuildPrompt("  add a dark mode toggle  ", "")

	// Idea is interpolated verbatim (trimmed).
	if want := "The user's idea:\nadd a dark mode toggle"; !strings.Contains(p, want) {
		t.Errorf("prompt missing trimmed idea; got tail:\n%s", tailStr(p, 200))
	}
	// Wizard-protocol invariants.
	for _, must := range []string{
		"never call the AskUserQuestion tool", // headless: hook does not fire
		"PHASE A",                             // interview phase marker
		"PHASE B",                             // planning phase marker
		"PROCEED",                             // terminal instruction
		"PRIVATE WORKSPACE",                   // plan lands in the workspace
		"PLAN SAVED:",                         // unambiguous completion signal
		"workspace/working/<YYYY>/<MM>/<DD>/<slug>/plan/README.md", // literal scanner-visible path template (#188)
		"workspace/plans/",  // the frozen tree is explicitly forbidden (#188)
		"@planner",          // core 3.0 consolidated planning agent
		"fenced json block", // structured-output contract
		"runningPlan",       // running plan required every turn
		"plan/spec.md",      // spec artifact required in PHASE B
		"**Covers:**",       // per-phase spec-coverage line
	} {
		if !strings.Contains(p, must) {
			t.Errorf("prompt missing required instruction %q", must)
		}
	}
}

func TestBuildPromptEmptyIdea(t *testing.T) {
	p := BuildPrompt("", "")
	if !strings.Contains(p, "The user's idea:") {
		t.Fatalf("empty idea dropped the frame:\n%s", p)
	}
}

// A planner told only the SHAPE of the plan path has to infer the root, and the
// documented default (~/swarmery-workspace) is wrong on any machine whose
// workspace was moved — the plan is then written successfully into a tree the
// scanner never walks. So the resolved root must reach the prompt verbatim.
func TestBuildPromptInterpolatesWorkspaceRoot(t *testing.T) {
	p := BuildPrompt("idea", "/Volumes/Work/ws")
	if want := "/Volumes/Work/ws/<project>/workspace/working/"; !strings.Contains(p, want) {
		t.Errorf("prompt missing resolved plan path %q", want)
	}
	if strings.Contains(p, rootPlaceholder) {
		t.Errorf("prompt still carries %s although a root was passed", rootPlaceholder)
	}
}

// "" is the bare-unit-test shape: keep the placeholder rather than emitting a
// bogus absolute path the planner would use verbatim.
func TestBuildPromptWithoutRootKeepsPlaceholder(t *testing.T) {
	if p := BuildPrompt("idea", "  "); !strings.Contains(p, rootPlaceholder+"/<project>/workspace/working/") {
		t.Errorf("prompt lost the root placeholder:\n%s", tailStr(p, 200))
	}
}

// ---------------------------------------------------------------------------
// BuildAnswerMessage
// ---------------------------------------------------------------------------

func TestBuildAnswerMessageSingleSelect(t *testing.T) {
	q := PlanningQuestion{
		ID:   "approach",
		Type: "single_select",
		Options: []PlanningOption{
			{ID: "opt-a", Label: "Option A"},
			{ID: "opt-b", Label: "Option B"},
		},
	}
	msg := BuildAnswerMessage(q, []string{"opt-a"}, "")

	mustContain := []string{
		`"approach"`,               // question id
		"opt-a — Option A",         // id — label
		"Rebuild the running plan", // closing instruction
		"continue the interview",   // protocol reminder
		"fenced json block",        // format hint
	}
	for _, want := range mustContain {
		if !strings.Contains(msg, want) {
			t.Errorf("BuildAnswerMessage missing %q\nmessage:\n%s", want, msg)
		}
	}
	// The non-selected option must not appear.
	if strings.Contains(msg, "opt-b") {
		t.Errorf("BuildAnswerMessage should not include non-selected opt-b")
	}
}

func TestBuildAnswerMessageWithOther(t *testing.T) {
	q := PlanningQuestion{
		ID:   "scope",
		Type: "single_select",
		Options: []PlanningOption{
			{ID: "small", Label: "Small"},
			{ID: "other", Label: "Other", IsOther: true},
		},
	}
	msg := BuildAnswerMessage(q, []string{"other"}, "Use the existing API layer")

	if !strings.Contains(msg, "Other: Use the existing API layer") {
		t.Errorf("BuildAnswerMessage missing other text\nmessage:\n%s", msg)
	}
}

func TestBuildAnswerMessageMultiSelect(t *testing.T) {
	q := PlanningQuestion{
		ID:   "features",
		Type: "multi_select",
		Options: []PlanningOption{
			{ID: "feat-a", Label: "Feature A"},
			{ID: "feat-b", Label: "Feature B"},
			{ID: "feat-c", Label: "Feature C"},
		},
	}
	msg := BuildAnswerMessage(q, []string{"feat-a", "feat-c"}, "")

	if !strings.Contains(msg, "feat-a — Feature A") {
		t.Errorf("BuildAnswerMessage missing feat-a\nmessage:\n%s", msg)
	}
	if !strings.Contains(msg, "feat-c — Feature C") {
		t.Errorf("BuildAnswerMessage missing feat-c\nmessage:\n%s", msg)
	}
	if strings.Contains(msg, "feat-b") {
		t.Errorf("BuildAnswerMessage should not include non-selected feat-b")
	}
}

func TestBuildAnswerMessageUnknownID(t *testing.T) {
	// An unknown option id (defensive) should fall back to printing the id itself.
	q := PlanningQuestion{
		ID:      "q1",
		Type:    "single_select",
		Options: []PlanningOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
	}
	msg := BuildAnswerMessage(q, []string{"unknown-id"}, "")
	if !strings.Contains(msg, "unknown-id — unknown-id") {
		t.Errorf("BuildAnswerMessage should echo unknown id as fallback\nmessage:\n%s", msg)
	}
}

// ---------------------------------------------------------------------------
// BuildRefineMessage
// ---------------------------------------------------------------------------

func TestBuildRefineMessage(t *testing.T) {
	msg := BuildRefineMessage("Focus only on the backend layer")

	mustContain := []string{
		"Focus only on the backend layer", // verbatim quote
		"Rebuild affected plan fields",    // rebuild instruction
		"accumulated decisions",           // reference to history
		"exactly one next question",       // interview continuation
		"per the protocol",                // format reminder
	}
	for _, want := range mustContain {
		if !strings.Contains(msg, want) {
			t.Errorf("BuildRefineMessage missing %q\nmessage:\n%s", want, msg)
		}
	}
}

// ---------------------------------------------------------------------------
// BuildProceedMessage
// ---------------------------------------------------------------------------

func TestBuildProceedMessage(t *testing.T) {
	msg := BuildProceedMessage()

	mustContain := []string{
		"PROCEED — write the plan now.", // literal terminal marker
		"PHASE B",                       // phase reference
	}
	for _, want := range mustContain {
		if !strings.Contains(msg, want) {
			t.Errorf("BuildProceedMessage missing %q\nmessage:\n%s", want, msg)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers (local, dependency-free)
// ---------------------------------------------------------------------------

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
