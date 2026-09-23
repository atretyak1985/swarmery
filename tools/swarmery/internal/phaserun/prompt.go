package phaserun

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// promptTemplate is the phase-run execution contract (interactive planning v2
// phase 5, spec Part 2 §2.2). The phase DOC is the contract — the executor
// works in an isolated worktree, ticks the doc's acceptance checkboxes in
// place (progress then flows through the existing wsingest checkbox pipeline),
// commits locally, and never pushes.
//
// The doc is also where the phase's SUMMARY has to land. wsingest parses the
// doc's `## Completion Report` section (parseCompletionReport) and the Plans UI
// renders exactly that as the phase summary; nothing else is read. Executors
// that write their account into a reports/ file or only into their final reply
// leave the operator staring at "no summary of the work written" over a phase
// that in fact shipped — so the prompt demands the section explicitly, on the
// blocked path too.
//
// THE FORECAST BULLET is the predictive half of the learning loop (phase 11):
// the executor writes a `kind: posterior` block after reading the code and
// before its first edit, so the prediction is made while it is still a
// prediction. The sentence "It is a prediction, not a limit: do whatever the
// phase actually needs." is load-bearing and appears EXACTLY ONCE here — the
// measured risk of asking an agent for a forecast is that the agent then treats
// its own forecast as a scope fence and under-delivers to stay inside it. No
// code path in this daemon gates on a forecast, and no second sentence in this
// prompt may suggest one does. TestPromptForecastContract pins the count.
//
// text/template so the doc path/content interpolate without any prompt-side
// format bug (idiom of planning/prompt.go).
var promptTemplate = template.Must(template.New("phaserun").Parse(
	`You are executing ONE phase of an approved implementation plan, headlessly, in an isolated git worktree of the project repo (your cwd). This worktree is your ONE root: every path you read or write must be inside it. An absolute path pointing outside this root will be refused by the sandbox, so reaching for one costs you the turn — everything you need has been placed inside.

The phase document below is your complete contract. Follow it exactly:
- Complete the numbered tasks / acceptance criteria of THIS phase only — do not start other phases.
- As you complete each acceptance criterion, EDIT the phase document itself and tick its checkbox (- [ ] → - [x]). The document has been lent into this worktree at: {{.DocPath}} (relative to the worktree root) — edit it there. Your edits are copied back to the operator's workspace when the run ends.
- FORECAST: after you have read the code this phase touches and before your first edit, add a ` + "`kind: posterior`" + ` yaml block to the document's ` + "`## Forecast`" + ` section, mirroring the shape of the ` + "`kind: prior`" + ` block already there (areas, size_band, duration_band, outcome, risks, confidence). It is a prediction, not a limit: do whatever the phase actually needs. If the work turns out different, add a short "Where reality diverged" paragraph to the Completion Report saying how and why.
- Run the verification commands the document specifies before declaring done.
- Installed dependencies (node_modules, .venv, …) are LENT from the project's main checkout as symlinks, because git only materializes committed files in a worktree: build and test commands work as-is. Do NOT run a package-install command (npm ci / npm install / pip install) — it would mutate the main checkout's shared tree.
- Commit your work in the worktree with conventional commits. Do NOT push, do NOT open PRs, do NOT merge.
- WRITE THE PHASE SUMMARY INTO THE PHASE DOCUMENT before you finish: fill the doc's ` + "`## Completion Report`" + ` section, or append it at the end of the doc when the section does not exist yet. Cover what shipped, the files and commits, the verification output, and every deviation or deferral. That section is the ONLY summary the operator's dashboard shows for this phase — a report left in your reply, in a scratchpad, or in a reports/ file is invisible there — editing the lent document IS how it reaches them. Write it on the blocked path too, describing how far you got and what stopped you.
- ENDING YOUR TURN ENDS THIS PROCESS, and any subagent still running dies with it — while the exit code stays 0, so the run is recorded as a clean success that landed nothing. Never dispatch helpers and then reply that you are waiting on them: that reply IS the kill. Await anything you dispatch inside the same turn, or do the work yourself.
- If the document's premises don't match the code you find, STOP and end your reply with: PHASE BLOCKED: <one-line reason>. Otherwise end with: PHASE DONE.

{{.TurnContract}}

{{.RepoNote}}PHASE DOCUMENT ({{.DocRelPath}}):
----------------------------------------
{{.DocContent}}
----------------------------------------`))

// BuildPrompt renders the phase-run prompt for one phase doc. Template
// execution on a fixed template with string data cannot fail, so the
// (unreachable) error is ignored (same posture as planning.BuildPrompt).
func BuildPrompt(docPath, docRelPath, docContent string) string {
	return BuildPromptIn(docPath, docRelPath, docContent, "", "", runcore.Budget{})
}

// BuildPromptIn is BuildPrompt with the run's repository context: repoRoot is the
// resolved checkout the worktree was cut from, projectPath the project root.
//
// When they differ — a multi-repo project, where the run lives in ONE checkout
// inside the umbrella — the prompt says so. Phase docs for such projects write
// their paths from the project root ("sk-next/src/components/x.tsx"), and inside
// the worktree that same file is "src/components/x.tsx"; without the note an
// agent "fixes" the mismatch by creating a nested directory and writes the whole
// phase into a tree nobody reads.
// docPath is WORKTREE-RELATIVE (worktree.LendPlanDoc lends the doc in). Relative,
// not absolute: the contract's first line says this worktree is the agent's one
// root, and an instruction to edit a file outside it contradicts that and is
// refused by the sandbox — which is what one retro window measured as 56
// isolation errors and 4 plan-read refusals.
//
// budget states this run's wall clock and start instant (step 3.5). It is a
// VALUE, not a knob read here, because the same budget has to reach three places
// — this prompt, every continuation message, and settle's loop deadline — and
// three independent reads of SWARMERY_PHASERUN_TIMEOUT plus three calls to
// time.Now() would leave the executor reading one clock while the harness enforces
// another. A zero Budget renders no budget line at all (the BuildPrompt shape and
// the prompt tests that predate the clock).
func BuildPromptIn(docPath, docRelPath, docContent, repoRoot, projectPath string, budget runcore.Budget) string {
	var b strings.Builder
	_ = promptTemplate.Execute(&b, struct {
		DocPath      string
		DocRelPath   string
		DocContent   string
		RepoNote     string
		TurnContract string
	}{docPath, docRelPath, docContent, repoNote(repoRoot, projectPath), runcore.TurnContract(budget)})
	return b.String()
}

// repoNote renders the multi-repo orientation block, or "" when the run's
// repository IS the project root (where the note would only add noise).
func repoNote(repoRoot, projectPath string) string {
	// repopath.SameDir, not a Clean comparison: the resolved root has been through
	// EvalSymlinks and projects.path has not, so on a symlinked path a single-repo
	// run would otherwise be handed a note telling it it is somewhere it is not.
	if repoRoot == "" || projectPath == "" || repopath.SameDir(repoRoot, projectPath) {
		return ""
	}
	name := filepath.Base(repoRoot)
	return fmt.Sprintf(
		"REPOSITORY: your worktree is a checkout of `%s` (%s), ONE repository inside the project root %s.\n"+
			"Paths in this document may be written from the project root (e.g. `%s/src/...`); inside your worktree that same file is `src/...`. "+
			"Do NOT create a `%s/` directory to make such a path resolve.\n\n",
		name, repoRoot, projectPath, name, name)
}
