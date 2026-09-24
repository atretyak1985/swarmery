package dispatch

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// contractTemplate is the EXECUTION CONTRACT appended verbatim to every
// dispatched task prompt (phase-3 spec — normative). It injects the honest-exit
// sentinel vocabulary (DESIGN.md §4.1), the Swarm-Task-Id commit trailer
// (§4.3), the declared file scope, and the "work only in this worktree" fence.
// text/template so {branch}/{taskID}/{fileScope} interpolate without any risk
// of prompt-side format bugs; the literal wording below is fixed.
var contractTemplate = template.Must(template.New("contract").Parse(
	`--- EXECUTION CONTRACT (swarmery dispatcher) ---
You are running unattended inside a dedicated git worktree on branch {{.Branch}}. This worktree is your ONE root: every path you read or write must be inside it. An absolute path pointing outside this root will be refused by the sandbox, so reaching for one costs you the turn — everything you need has been placed inside.
- Commit your work in logical increments. Every commit message MUST end with the trailer line:
  Swarm-Task-Id: {{.TaskID}}
- Stay within this file scope if declared: {{.FileScope}}. If a required change falls outside it, stop and end with: BLOCKED: <what and why>.
- If you discover the requested work is already done on HEAD, do NOT redo it. End your reply with: PREMISE STALE: <evidence>.
- If no changes are needed, end with: NO-OP: <reason>. If this duplicates other tracked work, end with: DUPLICATE: <task-id>.
- If you are genuinely blocked, end with: BLOCKED: <reason>. Never fake completion by skipping the remaining work.
- Installed dependencies (node_modules, .venv, …) are LENT from the project's main checkout as symlinks, because git only materializes committed files in a worktree: build and test commands work as-is. Do NOT run a package-install command (npm ci / npm install / pip install) — it would mutate the main checkout's shared tree.
- Do not push, do not create PRs, do not switch branches.
{{if .TaskDoc}}- THIS CARD HAS A PLAN DOCUMENT, lent into this worktree at {{.TaskDoc}} (relative to the worktree root) — edit it there. Tick its acceptance checkboxes (- [ ] → - [x]) as you satisfy them, and fill its ` + "`## Completion Report`" + ` section before you finish: what shipped, the files and commits, the verification output, and every deviation or deferral. That section is the ONLY summary the operator's dashboard shows for this card — a report left in your reply or in a scratchpad file is invisible there. Your edits to this file are copied back to the operator's workspace when the run ends, so editing it here IS how the report reaches them. Write it on the blocked path too, describing how far you got and what stopped you.
{{else}}- THIS CARD HAS NO PLAN DOCUMENT, so write your report to {{.ReportPath}} (relative to the worktree root) — create the file — before you finish: what shipped, the files and commits, the verification output, and every deviation or deferral. That file is the ONLY summary the operator's dashboard shows for this card — a report left in your reply or in a scratchpad file is invisible there. It is read back into the card when the run ends, so writing it here IS how the report reaches the operator. Write it on the blocked path too, describing how far you got and what stopped you.
{{end}}
{{.TurnContract}}
--- END CONTRACT ---`))

// contractData is the template payload.
type contractData struct {
	Branch    string
	TaskID    string
	FileScope string
	// TaskDoc is the WORKTREE-RELATIVE path of this card's micro-plan phase doc
	// (worktree.LendPlanDoc lends it in), or "" when it has none. Relative, not
	// absolute: the contract's first line says this worktree is the agent's one
	// root, and an instruction to edit a file outside it contradicts that and is
	// refused by the sandbox. Empty omits the whole paragraph rather than
	// rendering an instruction about a file that does not exist — which is what
	// makes a mint failure genuinely non-fatal instead of merely non-crashing.
	TaskDoc string
	// TurnContract is runcore's standing instruction on how an unattended turn
	// ends, rendered here so the dispatcher's contract states it in the same
	// words phaserun and planrun do. Dispatch carries NO budget line: unlike a
	// phase or plan run, a dispatched stage's deadline lives with the dispatcher
	// (runcore.Spec.Timeout is 0 for this engine — the stage's ctx owns
	// cancellation), so there is no wall clock here that could honestly be stated.
	TurnContract string
	// ReportPath is the WORKTREE-RELATIVE destination the no-doc branch names.
	// It is never blank when TaskDoc is: a card without a plan doc still owes
	// the operator a summary, and the doc branch's text rendered with an empty
	// path would be an instruction about a file that does not exist — the exact
	// failure TaskDoc's comment warns about, moved one branch over.
	ReportPath string
}

// scopeText renders a file-scope list for the contract line. Empty ⇒ the
// literal "(none declared — the whole worktree)" so the model gets an explicit
// signal rather than a bare blank.
func scopeText(scope []string) string {
	clean := cleanScope(scope)
	if len(clean) == 0 {
		return "(none declared — the whole worktree)"
	}
	return strings.Join(clean, ", ")
}

// BuildStagePrompt assembles the full dispatched prompt for ONE stage: the
// stage's (already template-rendered) body followed by a blank line and the
// execution contract block, which is appended to EVERY stage regardless of
// playbook (fusion phase 13). taskID is the external card id (T-xxxxxx); branch
// is the worktree branch (swarm/<id>). For the default single-stage recipe the
// stage body IS the task's own prompt, so this reproduces the pre-playbook
// prompt exactly.
func BuildStagePrompt(stageBody, branch, taskID string, fileScope []string) string {
	return BuildStagePromptDoc(stageBody, branch, taskID, "", fileScope)
}

// BuildStagePromptDoc is BuildStagePrompt with the card's micro-plan phase doc.
// taskDoc is WORKTREE-RELATIVE (see contractData.TaskDoc).
// A non-empty taskDoc adds the tick-and-report paragraph — the same contract
// internal/phaserun states for a plan phase, in the same words, because it is the
// same promise: the doc is where the record of the work lives, and a summary left
// anywhere else is invisible to the dashboard.
func BuildStagePromptDoc(stageBody, branch, taskID, taskDoc string, fileScope []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(stageBody, "\n"))
	b.WriteString("\n\n")
	// template execution on a fixed template with string data cannot fail; the
	// error is ignored deliberately (belt-and-braces: a failure would leave the
	// contract absent, which the caller would still spawn — acceptable, and
	// unreachable).
	_ = contractTemplate.Execute(&b, contractData{
		Branch:       branch,
		TaskID:       taskID,
		FileScope:    scopeText(fileScope),
		TaskDoc:      taskDoc,
		TurnContract: runcore.TurnContract(runcore.Budget{}),
		ReportPath:   worktree.ReportPath,
	})
	return b.String()
}

// repoNote renders the multi-repo orientation block plus, when the project
// declares extra reachable paths, repopath.AdditionalDirsNote — "" when
// neither applies. Mirrors phaserun's/planrun's repoNote (same parameter
// order, repoRoot before projectPath — multiRepoNote's SameDir guard is
// symmetric, so a swap would compile and silently name the umbrella as the
// checkout): dispatch gained resolved-repo-root support (runRoot) without also
// telling the agent what changed, so a card that lands in a sub-repo of a
// multi-repo project ran with no orientation at all (a phase/plan run in the
// same situation has carried this note since 2026-07-30/2026-09-17).
func repoNote(repoRoot, projectPath, worktreePath string) string {
	return multiRepoNote(repoRoot, projectPath) + repopath.AdditionalDirsNote(projectPath, worktreePath)
}

// multiRepoNote is the orientation block, or "" when the run's repository IS
// the project root (where the note would only add noise).
func multiRepoNote(repoRoot, projectPath string) string {
	// repopath.SameDir, not a Clean comparison: the resolved root has been
	// through EvalSymlinks and projects.path has not, so on a symlinked path a
	// single-repo run would otherwise be handed a note telling it it is
	// somewhere it is not.
	if repoRoot == "" || projectPath == "" || repopath.SameDir(repoRoot, projectPath) {
		return ""
	}
	name := filepath.Base(repoRoot)
	return fmt.Sprintf(
		"REPOSITORY: your worktree is a checkout of `%s` (%s), ONE repository inside the project root %s.\n"+
			"Paths in the task or this card's plan document may be written from the project root (e.g. `%s/src/...`); inside your worktree that same file is `src/...`. "+
			"Do NOT create a `%s/` directory to make such a path resolve.\n\n",
		name, repoRoot, projectPath, name, name)
}

// BuildPrompt is the single-stage convenience wrapper (task body + contract),
// retained for the pre-playbook call shape and its contract-verbatim tests. It
// is BuildStagePrompt with the task prompt as the sole stage body.
func BuildPrompt(taskPrompt, branch, taskID string, fileScope []string) string {
	return BuildStagePrompt(taskPrompt, branch, taskID, fileScope)
}

// Sentinel classification of a dispatched session's final assistant text.
// Order matters only in that the terminal-done sentinels are distinct from
// BLOCKED. Prefixes are matched case-insensitively on the trimmed first
// non-empty line's leading token (Fusion parses from the reply tail; our
// contract instructs the model to END with the sentinel, so the last assistant
// turn's leading sentinel is authoritative).

// Sentinel is the parsed outcome of a dispatched run's final assistant text.
type Sentinel struct {
	Kind string // "" (none) | "done" | "blocked"
	Line string // the full sentinel line, for result_note / dispatch_error
}

// doneSentinels move a task straight to done with the line as result_note.
var doneSentinels = []string{"PREMISE STALE:", "NO-OP:", "NOOP:", "DUPLICATE:", "REDUNDANT:"}

// blockedSentinel routes to todo + paused with the line as dispatch_error.
const blockedSentinel = "BLOCKED:"

// ClassifySentinel scans the final assistant text for a leading sentinel. It
// checks every non-empty line (not just the first) because a model may append
// the sentinel as the last line after prose; the LAST matching sentinel wins
// (closest to the reply's end, matching the contract's "end with" instruction).
func ClassifySentinel(text string) Sentinel {
	var found Sentinel
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if hasSentinelPrefix(upper, blockedSentinel) {
			found = Sentinel{Kind: "blocked", Line: line}
			continue
		}
		for _, s := range doneSentinels {
			if hasSentinelPrefix(upper, s) {
				found = Sentinel{Kind: "done", Line: line}
				break
			}
		}
	}
	return found
}

// hasSentinelPrefix reports whether an upper-cased line begins with a sentinel
// token. It tolerates common markdown emphasis wrappers the model might add
// (leading '*', '-', '#', '>' and backticks) before the token.
func hasSentinelPrefix(upperLine, token string) bool {
	trimmed := strings.TrimLeft(upperLine, "*-#> `")
	return strings.HasPrefix(trimmed, token)
}
