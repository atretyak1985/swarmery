package planrun

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// Phase is one entry of the plan manifest handed to the executor: enough for it
// to see the shape and skip finished work, WITHOUT inlining the docs. Phase docs
// are long (plans routinely run tens of KB per phase); pasting all of them would
// spend the context window before the first line of work — and the run-plan
// skill deliberately passes phase content to its executors as file references,
// not pasted text.
type Phase struct {
	Seq       int
	Name      string
	DocPath   string // absolute path — the doc lives outside the repo worktree
	Done      int    // ticked acceptance checkboxes
	Total     int    // acceptance checkboxes in the doc
	DependsOn []int  // seqs this phase waits on
	// Repo is the RAW declared Repo cell from epic_phases.repo (migration 0046) —
	// "`sk-next` (`/abs/sk-next`)", "sk-next (+ helm)", or "" when the plan declares
	// nothing. Resolved to a run root by internal/repopath at Start.
	Repo string
}

// complete reports whether the phase needs no further work.
func (p Phase) complete() bool { return p.Total > 0 && p.Done >= p.Total }

// Mode is how the controller executes the phases — the one execution call the
// run-plan skill cannot derive from the phase DAG, and the same fork an
// interactive session asks about before running a plan.
type Mode string

const (
	// ModeAuto leaves the skill's own route triage authoritative (default).
	ModeAuto Mode = "auto"
	// ModeSubagents forces per-phase executor+reviewer dispatch.
	ModeSubagents Mode = "subagents"
	// ModeInline forces the controller to do the work itself in this session.
	ModeInline Mode = "inline"
)

// ValidMode normalizes a caller-supplied mode; anything unknown degrades to
// ModeAuto rather than smuggling a typo into the prompt.
func ValidMode(m string) Mode {
	switch Mode(strings.TrimSpace(m)) {
	case ModeSubagents:
		return ModeSubagents
	case ModeInline:
		return ModeInline
	default:
		return ModeAuto
	}
}

// modeDirective is the extra instruction layered on the skill for a forced
// mode. ModeAuto adds nothing — the skill's triage table stands.
func modeDirective(m Mode) string {
	switch m {
	case ModeSubagents:
		return "\nEXECUTION MODE — subagent-driven (overrides the skill's route choice): dispatch every phase to an implementation subagent, then review each result with a FRESH reviewer subagent before moving on, exactly as the skill's route S/P describe. Do not implement phase work yourself in this session; your job is to control, dispatch, review and integrate.\n"
	case ModeInline:
		return "\nEXECUTION MODE — inline (overrides the skill's route choice): implement the phases YOURSELF in this session. Do not dispatch implementation subagents. Everything else from the skill still holds — the phase order, the per-phase verification commands, the acceptance-criteria ticks and the run ledger.\n"
	default:
		return ""
	}
}

// manifest renders the phase table: one line per phase, finished ones marked so
// the controller skips them instead of re-doing landed work.
func manifest(phases []Phase) string {
	var b strings.Builder
	for _, p := range phases {
		state := "TODO"
		if p.complete() {
			state = "DONE — skip"
		}
		fmt.Fprintf(&b, "- Phase %d [%s] %s (%d/%d criteria", p.Seq, state, p.Name, p.Done, p.Total)
		if len(p.DependsOn) > 0 {
			deps := make([]string, len(p.DependsOn))
			for i, d := range p.DependsOn {
				deps[i] = fmt.Sprintf("%d", d)
			}
			fmt.Fprintf(&b, ", depends on phase %s", strings.Join(deps, ", "))
		}
		fmt.Fprintf(&b, ")\n  doc: %s\n", p.DocPath)
	}
	return b.String()
}

// promptTemplate hands a whole plan to ONE controller session.
//
// It does NOT restate how to execute a plan: core already ships that contract as
// the `run-plan` skill (plugins/core/skills/run-plan/SKILL.md — phase-DAG
// triage, per-phase implement+review loops, worktree reconciliation, the
// run-ledger, checkbox ticking). Re-specifying it here would fork a maintained
// contract into a prompt string that silently rots. So the prompt's whole job is
// to (a) point the session at the plan, (b) invoke that skill, and (c) state the
// constraints the skill cannot know because they come from the RUN's context:
// this session is headless, so nothing may wait on a human.
//
// The headless caveat is the sharp edge. The skill ASK-gates base-branch pulls,
// pushes and commits, and routes `manual_legs` steps to the main session — all
// of which assume a human is watching. Under `-p` there is nobody to ask, so the
// prompt converts those into explicit deferrals plus a stop-and-report rule,
// rather than letting the run silently hang or improvise past a gate.
//
// The second run-context fact the skill cannot know: ENDING THE TURN ENDS THE
// PROCESS. Interactively, dispatching background executors and replying "waiting
// on them" is correct — a task notification re-invokes the controller when a child
// finishes. Under `-p` there is no re-invocation: the reply terminates `claude`,
// every child dies with it, and the exit code is 0, so the daemon records a clean
// `done` over work that never landed. That is not hypothetical — plan 70 burned
// 13m24s exactly this way on 2026-07-30 (both executor transcripts stop at the
// parent's final second), and the green chip claimed success. Hence the
// await-your-children rule below.
//
// The third: the skill's per-phase report contract points executors at
// `<task-dir>/reports/phase-<N>-report.md`, a path the dashboard never reads.
// The Plans UI renders a phase's summary from the doc's own `## Completion
// Report` section (wsingest.parseCompletionReport) and from nothing else, so a
// run whose phases landed can still show "no summary of the work written". The
// prompt therefore makes the in-doc section part of finishing a phase.
//
// text/template so paths and content interpolate without any prompt-side format
// bug (idiom of planning/prompt.go, phaserun/prompt.go).
var promptTemplate = template.Must(template.New("planrun").Parse(
	`You are the controller for an ENTIRE approved implementation plan, running HEADLESSLY (claude -p) in an isolated git worktree of the project repo (your cwd).

Execute this plan using the ` + "`run-plan`" + ` skill — invoke it with the Skill tool (skill: run-plan) and this plan directory as its argument. That skill IS your procedure: it parses the phase DAG, picks the execution route, dispatches per-phase implement+review loops, ticks acceptance criteria in the phase docs, and keeps the run ledger. Follow it; do not invent a different procedure.

PLAN DIRECTORY: {{.PlanDir}}
{{.RepoNote}}{{.ModeDirective}}
Constraints this run adds on top of the skill, because THERE IS NO HUMAN in this session — nothing can be ASK-gated:
- Work only in your cwd worktree. Do NOT touch the operator's main checkout.
- Commit per phase, in the worktree, with conventional commits. Do NOT push, do NOT open PRs, do NOT merge into the default branch, do NOT pull the base branch.
- Installed dependencies (node_modules, .venv, …) are LENT from the project's main checkout as symlinks, because git only materializes committed files in a worktree: build and test commands work as-is. Do NOT run a package-install command (npm ci / npm install / pip install) — it would mutate the main checkout's shared tree.
- Any step the skill routes to the main session as a manual leg (browser checks, live-environment probes, anything interactive) is DEFERRED: note it in the ledger as DEFERRED with the reason and carry on with the automated part.
- If you reach a decision that genuinely needs a human — an ASK gate you cannot defer, a destructive operation, or a phase whose premises contradict the code you find — STOP there and end your reply with: PLAN BLOCKED at phase <n>: <one-line reason>. Do not improvise a different design to get past it.
- ENDING YOUR TURN ENDS THIS PROCESS, and every subagent still running dies with it — while the exit code stays 0, so the run is recorded as a clean success that landed nothing. Never dispatch executors and then reply that you are waiting on them: that reply IS the kill. Await every subagent you dispatch inside the same turn (poll it to completion), and only finish your reply once no dispatched work is still in flight and its results are written to disk. If you cannot await them, do the phase work inline instead.
- Tick each acceptance criterion in its phase doc as you satisfy it, not in one batch at the end. Those ticks are the ONLY progress signal the operator can see while you run.
- When a phase's last criterion is ticked, WRITE THAT PHASE'S SUMMARY INTO ITS OWN PHASE DOC: fill the doc's ` + "`## Completion Report`" + ` section, or append it at the end of the doc when the section does not exist yet — what shipped, files and commits, verification output, deviations, and anything DEFERRED. That section is the ONLY per-phase summary the operator's dashboard shows; the run ledger and any reports/ file are invisible there, so this is in addition to them, never instead. A phase left with no Completion Report is not finished.
- When every phase is finished, end with: PLAN DONE.

{{.TurnContract}}

PHASE MANIFEST (current state — phases marked DONE are already landed; skip them):
{{.Manifest}}
PLAN README:
----------------------------------------
{{.Readme}}
----------------------------------------`))

// BuildPrompt renders the plan-run prompt. Template execution on a fixed
// template with string data cannot fail, so the (unreachable) error is ignored
// (same posture as planning.BuildPrompt / phaserun.BuildPrompt).
func BuildPrompt(planDir, readme string, phases []Phase, mode Mode) string {
	return BuildPromptIn(planDir, readme, phases, mode, "", "", runcore.Budget{})
}

// BuildPromptIn is BuildPrompt with the run's repository context: repoRoot is the
// resolved checkout the worktree was cut from, projectPath the project root.
//
// When they differ — a multi-repo project, where the run lives in ONE checkout
// inside the umbrella — the prompt has to say so. Plans for such projects write
// their paths from the project root ("sk-next/src/components/x.tsx"), and inside
// the worktree that same file is "src/components/x.tsx". Without this note an
// agent "fixes" the mismatch by creating a nested sk-next/ directory and writes
// the whole phase into a tree nobody reads.
//
// budget states this run's wall clock and start instant (step 3.5), resolved
// once by the service so the prompt, every continuation message and the
// completion loop's deadline all read the same clock. A zero Budget renders no
// budget line (BuildPrompt's shape).
func BuildPromptIn(planDir, readme string, phases []Phase, mode Mode, repoRoot, projectPath string, budget runcore.Budget) string {
	var b strings.Builder
	_ = promptTemplate.Execute(&b, struct {
		PlanDir       string
		RepoNote      string
		ModeDirective string
		Manifest      string
		Readme        string
		TurnContract  string
	}{planDir, repoNote(repoRoot, projectPath), modeDirective(mode), manifest(phases), readme, runcore.TurnContract(budget)})
	return b.String()
}

// repoNote renders the multi-repo orientation block, or "" when the run's
// repository IS the project root (the single-repo case, where the note would only
// add noise).
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
			"Paths in this plan may be written from the project root (e.g. `%s/src/...`); inside your worktree that same file is `src/...`. "+
			"Do NOT create a `%s/` directory to make such a path resolve.\n",
		name, repoRoot, projectPath, name, name)
}
