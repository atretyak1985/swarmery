package dispatch

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

func TestBuildPromptContainsContractVerbatim(t *testing.T) {
	got := BuildPrompt("Do the thing.", "swarm/T-abc123", "T-abc123", []string{"src/api", "web/src"})

	// The task body leads.
	if !strings.HasPrefix(got, "Do the thing.\n\n") {
		t.Errorf("prompt should start with the task body + blank line; got:\n%s", got)
	}
	// Contract markers + normative lines present verbatim.
	mustContain := []string{
		"--- EXECUTION CONTRACT (swarmery dispatcher) ---",
		"You are running unattended inside a dedicated git worktree on branch swarm/T-abc123.",
		// The fence is now stated as a rule the agent can obey, not just a slogan:
		// one root, and no absolute path outside it. The old wording said "Work
		// ONLY here" and then, two lines later, handed over an out-of-tree
		// absolute path to edit.
		"This worktree is your ONE root: every path you read or write must be inside it.",
		"Swarm-Task-Id: T-abc123",
		"Stay within this file scope if declared: src/api, web/src.",
		"End your reply with: PREMISE STALE: <evidence>.",
		"If no changes are needed, end with: NO-OP: <reason>.",
		"end with: BLOCKED: <reason>.",
		"Do not push, do not create PRs, do not switch branches.",
		"--- END CONTRACT ---",
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("prompt missing contract line:\n%q\nfull prompt:\n%s", s, got)
		}
	}
}

func TestBuildPromptEmptyScopeText(t *testing.T) {
	got := BuildPrompt("body", "swarm/T-x", "T-x", nil)
	if !strings.Contains(got, "(none declared — the whole worktree)") {
		t.Errorf("empty scope should render explicit none-declared text; got:\n%s", got)
	}
}

func TestClassifySentinel(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantKind string
	}{
		{"none", "I made the change and committed it.", ""},
		{"premise stale", "PREMISE STALE: HEAD already has the guard at auth.go:42", "done"},
		{"noop hyphen form", "NO-OP: nothing to change here", "done"},
		{"noop no hyphen", "NOOP: already satisfied", "done"},
		{"duplicate", "DUPLICATE: T-999888", "done"},
		{"redundant", "REDUNDANT: covered by the other task", "done"},
		{"blocked", "BLOCKED: need a schema change outside my file scope", "blocked"},
		{"sentinel after prose on last line", "Looked into it.\n\nPREMISE STALE: nothing to do", "done"},
		{"markdown-wrapped bold blocked", "**BLOCKED:** waiting on API", "blocked"},
		{"case insensitive", "premise stale: lower case still counts", "done"},
		{"blocked wins when last", "NO-OP: x\nthen realized\nBLOCKED: actually stuck", "blocked"},
		{"last done wins over earlier", "DUPLICATE: T-1\nactually\nNO-OP: clean", "done"},
		{"not a leading sentinel", "This is NOT a NO-OP situation, I did work.", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifySentinel(tc.text)
			if got.Kind != tc.wantKind {
				t.Errorf("ClassifySentinel(%q).Kind = %q, want %q (line=%q)", tc.text, got.Kind, tc.wantKind, got.Line)
			}
			if tc.wantKind != "" && got.Line == "" {
				t.Errorf("ClassifySentinel(%q) matched but returned empty Line", tc.text)
			}
		})
	}
}

// Both branches of the report paragraph must name a destination. The doc branch
// names the lent phase doc; the docless branch used to name nothing at all, so a
// card without a plan doc was told to report and never told where — and its
// report landed in the reply, which the dashboard does not read.
func TestBuildPrompt_ReportDestinationInBothBranches(t *testing.T) {
	t.Run("doc present", func(t *testing.T) {
		got := BuildStagePromptDoc("body", "swarm/T-1", "T-1", ".swarmery/plan/phase-2.md", nil)
		if !strings.Contains(got, "THIS CARD HAS A PLAN DOCUMENT, lent into this worktree at .swarmery/plan/phase-2.md") {
			t.Errorf("doc branch does not name the lent doc:\n%s", got)
		}
		if !strings.Contains(got, "## Completion Report") {
			t.Errorf("doc branch does not name the report section:\n%s", got)
		}
		if strings.Contains(got, "NO PLAN DOCUMENT") {
			t.Errorf("both branches rendered:\n%s", got)
		}
	})

	t.Run("doc absent", func(t *testing.T) {
		got := BuildStagePromptDoc("body", "swarm/T-1", "T-1", "", nil)
		if !strings.Contains(got, "THIS CARD HAS NO PLAN DOCUMENT") {
			t.Errorf("docless branch is missing:\n%s", got)
		}
		if !strings.Contains(got, worktree.ReportPath) {
			t.Errorf("docless branch does not name a report destination (%s):\n%s", worktree.ReportPath, got)
		}
		// The destination is worktree-relative for the same reason TaskDoc is: the
		// contract's first line makes the worktree the agent's one root.
		if strings.Contains(got, "/"+worktree.ReportPath) {
			t.Errorf("docless report destination is absolute:\n%s", got)
		}
		// Both branches promise the same thing in the same words, so an agent is
		// never left guessing whether a report matters on this card.
		if !strings.Contains(got, "ONLY summary the operator's dashboard shows for this card") {
			t.Errorf("docless branch does not state the report is the only summary:\n%s", got)
		}
		if !strings.Contains(got, "blocked path") {
			t.Errorf("docless branch does not cover the blocked path:\n%s", got)
		}
	})

	// The failure this guards: rendering the doc branch's text with an empty path,
	// i.e. an instruction about a file that does not exist.
	t.Run("no empty path is ever rendered", func(t *testing.T) {
		got := BuildStagePromptDoc("body", "swarm/T-1", "T-1", "", nil)
		if strings.Contains(got, "lent into this worktree at  ") || strings.Contains(got, "at (relative") {
			t.Errorf("an empty doc path was rendered into the contract:\n%s", got)
		}
	})
}

// TestContract_CarriesTheStandingInstructionExactlyOnce is step 3.4's acceptance
// criterion for the dispatcher. Dispatch carries NO budget line: a dispatched
// stage's deadline lives with the dispatcher (runcore.Spec.Timeout is 0 here), so
// there is no wall clock that could honestly be stated to the executor.
func TestContract_CarriesTheStandingInstructionExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"with a plan doc", BuildStagePromptDoc("body", "swarm/T-1", "T-1", "plan/phase-1.md", nil)},
		{"without a plan doc", BuildPrompt("body", "swarm/T-1", "T-1", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := strings.Count(tc.got, "HOW YOUR TURN ENDS"); n != 1 {
				t.Errorf("standing instruction appears %d times, want exactly 1:\n%s", n, tc.got)
			}
			if strings.Contains(tc.got, "Budget: ") {
				t.Errorf("dispatch must state no budget it does not own:\n%s", tc.got)
			}
			// It must sit INSIDE the contract block, not after it.
			if strings.Index(tc.got, "HOW YOUR TURN ENDS") > strings.Index(tc.got, "--- END CONTRACT ---") {
				t.Error("the standing instruction must be inside the contract block")
			}
			// And the pre-existing sentinel vocabulary must survive beside it.
			for _, want := range []string{"BLOCKED:", "PREMISE STALE:", "NO-OP:", "Swarm-Task-Id:"} {
				if !strings.Contains(tc.got, want) {
					t.Errorf("contract lost %q", want)
				}
			}
		})
	}
}
