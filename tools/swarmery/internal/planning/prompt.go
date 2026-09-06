package planning

import (
	"fmt"
	"strings"
	"text/template"
)

// phaseAProtocol is the shared PHASE A interview protocol — one constant used
// verbatim by BOTH wizard templates (plan and revise), so the interview loop
// (question format, options contract, running plan) can never drift between the
// two modes.
//
// The inner ```json fence is part of the prompt text and is preserved using
// backtick-concatenation so the outer Go raw-string literal is not broken.
const phaseAProtocol = `You are NOT in an interactive terminal: never call the AskUserQuestion tool. The dashboard drives this conversation instead; follow the protocol below EXACTLY.

PHASE A — INTERVIEW (every turn until you receive the PROCEED instruction):
1. Research first. Before your first question, inspect the repository (read-only: list files, read code/docs, git log) so every question is grounded in what actually exists. Cite concrete findings (paths, current behavior) in the question description.
2. Iterative narrowing. Ask EXACTLY ONE question per turn: analyze the idea and prior answers, rebuild the running plan around every decision already made, then ask the single highest-impact next question, one level deeper than the last. Never ask a generic question; never silently pick a direction yourself.
3. Options. Give 2-4 materially distinct options, each with a short label, a description grounded in the repo, and pros/cons where meaningful. ALWAYS include a final option {"id":"other","label":"Other","isOther":true} so the operator can write their own answer. Mark the ONE option you would choose yourself with "recommended":true (never more than one, never the Other option) and make its description say why — the dashboard highlights it so the operator can take your lean in one click.
4. Response format. End EVERY interview turn with exactly one fenced json block and NOTHING after it:

` + "```json" + `
{"type":"question","data":{"id":"kebab-unique-id","type":"single_select","question":"…","description":"…","options":[{"id":"opt-a","label":"…","description":"…","pros":["…"],"cons":["…"],"recommended":true},{"id":"other","label":"Other","isOther":true}],"runningPlan":{"title":"…","description":"…","proposedChanges":["specific change"],"acceptanceCriteria":["observable outcome"],"suggestedSize":"S"}}}
` + "```" + `

   Use "multi_select" when choices are not mutually exclusive. Keep runningPlan present and current on every turn. Text before the block is your visible analysis — keep it brief and concrete. Match the operator's language (the idea below) in question/option prose.`

// promptTemplate is the Interactive Planning v2 wizard prompt.  It instructs
// the agent to research the repo first, then run PHASE A (structured interview
// — one JSON question per turn) until it receives the PROCEED instruction,
// and only then write the plan to the private workspace (PHASE B).
//
// text/template so {{.Idea}} interpolates without any prompt-side format bug.
var promptTemplate = template.Must(template.New("planner").Parse(
	`You are the swarmery planning assistant, running headlessly to turn a rough idea into a structured, executable plan for THIS project (your current working directory is the project repo).

` + phaseAProtocol + `

PHASE B — PLAN (only after the operator sends the PROCEED instruction):
- Stop asking questions. Plan with the @planner approach (core's consolidated planning agent): size the phase count to the scope — 1-3 phases for < ~1 week of work, a fuller multi-phase DAG for larger efforts.
- Write the plan to the PRIVATE WORKSPACE, never into a code repo. The task dir MUST match this exact shape — the dashboard's epic scanner walks ONLY workspace/{working,archive}/<YYYY>/<MM>/<DD>/<slug>/, so a plan saved anywhere else silently never appears on the Plans page:
  {{.WorkspaceRoot}}/<project>/workspace/working/<YYYY>/<MM>/<DD>/<slug>/plan/README.md
  where the root above is the one THIS daemon scans — use it verbatim, never a path you inferred from documentation or $HOME — <project> is the workspace dir for this repo (list the root to confirm which one), <YYYY>/<MM>/<DD> is today's date as three separate directories, and <slug> is a lowercase kebab slug WITHOUT a date prefix. NEVER save under workspace/plans/ — that tree is frozen history, even if the directory exists on disk.
- Plan contents per the workspace convention (CLAUDE.md section 11 / core pack): plan/README.md (objective, real file paths, phase sequencing table, risks, Definition of Done) plus phase-N docs, each with a self-contained copy-paste agent prompt, measurable acceptance criteria, and — as the doc's LAST section — an empty ` + "`## Completion Report`" + ` stub for the executor to fill at phase end (the dashboard renders exactly that section as the phase's summary, so a doc without the stub leaves the operator with "no summary of the work written"). Honor every decision and the final running plan from the interview.
- Before the phase docs, write plan/spec.md — the WHAT/WHY: a short problem statement, user stories, and an "## Acceptance criteria" section whose items are checkboxes shaped exactly ` + "`- [ ] **SC-1** — <criterion>`" + ` (stable SC-n ids, one behavior each).
- Every phase doc's header block must carry a ` + "`**Covers:** SC-…`" + ` line naming the spec criteria that phase delivers; every SC id must be covered by at least one phase, and no phase may cover an id the spec does not declare.
- Do NOT implement anything and do NOT create git branches — planning only.
- Finish your FINAL message with this exact line on its own:
  PLAN SAVED: <absolute path to the plan dir>

The user's idea:
{{.Idea}}`))

// SeedDoc is one existing plan document interpolated into the revise prompt
// seed (name + full current content).
type SeedDoc struct {
	Name    string
	Content string
}

// ReviseInput is everything the revise prompt interpolates.
type ReviseInput struct {
	Reason     string    // operator's note on why the plan needs revising
	PlanDir    string    // absolute plan/ dir being revised
	ScratchDir string    // where proposed files must be written
	PlanTitle  string    // the workspace task's title
	Evidence   string    // rendered phase table: seq, doc, checkboxes, run_state, outcome, run_error
	DoneDocs   []string  // phase docs that are fully ticked — immutable
	Readme     string    // full current README.md
	Docs       []SeedDoc // every current phase doc, whole
}

// revisePromptTemplate is the revise-mode wizard prompt: the SAME PHASE A
// interview protocol, but a PHASE B that stages a revision proposal into a
// scratch dir instead of writing a new plan — the plan dir on disk is never
// touched by the agent; the daemon validates and stores the proposal.
var revisePromptTemplate = template.Must(template.New("reviser").Parse(
	`You are the swarmery planning assistant, running headlessly to REVISE an existing plan of THIS project (your current working directory is the project repo).

` + phaseAProtocol + `

PHASE B — STAGE THE REVISION (only after the operator sends the PROCEED instruction):
- You are revising an EXISTING plan at {{.PlanDir}}. Do NOT create a new task dir, do NOT write
  anywhere under {{.PlanDir}}, and never emit a "PLAN SAVED:" line — this is a revision, not a new plan.
- Write the FULL proposed content of every doc you are changing or creating into the scratch dir
  {{.ScratchDir}}, using the doc's plan-dir-relative name (e.g. {{.ScratchDir}}/phase-3-api.md,
  {{.ScratchDir}}/README.md). Partial fragments are not accepted — each file is written whole.
- These phase docs are already complete and MUST NOT be changed, renamed, or deleted:{{if .DoneDocs}}{{range .DoneDocs}}
  - {{.}}{{end}}{{else}} (none){{end}}
- Every phase doc you write must keep its ` + "`## Completion Report`" + ` section as the LAST section (empty for a
  phase not yet executed, preserved verbatim for one that has run).
- If you change the phase set, update the README phase-sequencing table in the same revision: every Doc cell
  must name a file that will exist after the revision, and every "Depends on" entry must name a phase number
  present in the table.
- Also write {{.ScratchDir}}/revision.json:
  {"reason":"…","summary":{…the final runningPlan…},"files":[{"path":"phase-3-api.md","action":"update"},
   {"path":"phase-6-rollback.md","action":"create"},{"path":"phase-4-new.md","action":"rename","renameFrom":"phase-4-old.md"},
   {"path":"phase-7-dropped.md","action":"delete"}]}
  Actions: create | update | rename | delete. A file with action delete has no content file in the scratch dir.
- Do NOT implement anything, do NOT create git branches, do NOT edit the code repo.
- Finish your FINAL message with this exact line on its own:
  REVISION STAGED: {{.ScratchDir}}

The plan being revised: {{.PlanTitle}}

The operator's reason for revising:
{{.Reason}}

Current README.md of the plan:

--- README.md ---
{{.Readme}}
{{range .Docs}}
--- {{.Name}} ---
{{.Content}}
{{end}}
Execution evidence (what each phase actually achieved so far):

{{.Evidence}}`))

// rootPlaceholder is what the prompt says when the caller passes no workspace
// root: the shape only, which is what the planner has to guess from. Guessing
// is how a plan lands in ~/swarmery-workspace on a machine whose root is
// elsewhere — written successfully, and invisible to the scanner forever.
const rootPlaceholder = "<workspace root>"

// BuildPrompt renders the planner prompt for one idea. The idea is trimmed and
// interpolated verbatim; template execution on a fixed template with string
// data cannot fail, so the (unreachable) error is ignored.
//
// workspaceRoot is the root the SCANNER walks (cmd/swarmery hands the service
// the same value it gives wsingest). Passing it is what stops the planner
// guessing: the documented default is ~/swarmery-workspace, but an operator who
// moved the workspace has every plan written to a tree nothing reads. "" keeps
// the placeholder, for bare unit tests.
func BuildPrompt(idea, workspaceRoot string) string {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		root = rootPlaceholder
	}
	var b strings.Builder
	_ = promptTemplate.Execute(&b, struct{ Idea, WorkspaceRoot string }{
		Idea:          strings.TrimSpace(idea),
		WorkspaceRoot: root,
	})
	return b.String()
}

// BuildRevisePrompt renders the revise-mode planner prompt.  Same
// cannot-fail reasoning as BuildPrompt: fixed template, string/slice data.
func BuildRevisePrompt(in ReviseInput) string {
	in.Reason = strings.TrimSpace(in.Reason)
	var b strings.Builder
	_ = revisePromptTemplate.Execute(&b, in)
	return b.String()
}

// BuildAnswerMessage renders the resume message sent to the agent when the
// operator selects an answer for question q.
//
// selected contains the chosen option IDs.  Labels are resolved from
// q.Options; unknown IDs are printed as-is.  If otherText is non-empty it is
// appended as an "Other: …" line.  The message closes with the standard
// instruction to rebuild the running plan and continue the interview.
func BuildAnswerMessage(q PlanningQuestion, selected []string, otherText string) string {
	// Build a label lookup from the question options.
	labelFor := make(map[string]string, len(q.Options))
	for _, o := range q.Options {
		labelFor[o.ID] = o.Label
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Answer to question %q:\n", q.ID)
	for _, id := range selected {
		label := labelFor[id]
		if label == "" {
			label = id
		}
		fmt.Fprintf(&b, "- %s — %s\n", id, label)
	}
	if otherText != "" {
		fmt.Fprintf(&b, "Other: %s\n", otherText)
	}
	b.WriteString("\nRebuild the running plan around this decision and continue the interview per the protocol (one question, fenced json block).")
	return b.String()
}

// BuildRefineMessage renders the resume message for operator refinement
// instructions.  The instructions are quoted verbatim inside a fenced block
// and the agent is told to rebuild affected plan fields and ask exactly one
// next question.
func BuildRefineMessage(instructions string) string {
	return "Refinement instructions:\n\n```\n" + instructions + "\n```\n\n" +
		"Rebuild affected plan fields around all accumulated decisions and these instructions, then ask exactly one next question in that direction, per the protocol."
}

// BuildProceedMessage renders the terminal PROCEED instruction that ends the
// interview and triggers PHASE B (plan writing).
func BuildProceedMessage() string {
	return "PROCEED — write the plan now.\nExecute PHASE B of your instructions."
}
