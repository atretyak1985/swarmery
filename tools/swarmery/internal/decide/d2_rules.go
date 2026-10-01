package decide

// D2's deterministic rules: the cases a finished session's own record already
// decides, answered before any model is asked.
//
//	R0  the operator's verdict is set                      → outcome = the mapped verdict
//	R1  the last assistant turn is a synthetic failure line → outcome = failed, failure_cause = its kind
//	R2  the last tool call is an auto mode no-verdict error → failure_cause = api-error
//	R3  a phase run ended done with every criterion ticked  → outcome = shipped, failure_cause = none
//	R4  a one-shot session ended on a final answer          → outcome = shipped, failure_cause = none
//	R5  a phase or plan run (OFF unless switched on)        → task_type = feature
//
// First match wins per question. A rule answer goes through Question.RuleAnswer
// (backend=rules, confidence 1) and carries its id in Question.RuleID, so the
// eval (eval.go) reports each rule's own coverage and precision.
//
// A "phase or plan run" is a session a plan engine launched: one a row links
// (epic_phases / plan_runs .run_session_uuid) or one that opens with the
// engine's own prompt. Such a session is never task type `planning`
// (runTaskTypes) and never a one-shot session (R4).
//
// Everything here only reads.

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
)

// Rule ids, as the eval's per-rule table lists them.
const (
	RuleOperatorVerdict  = "R0"
	RuleSyntheticFailure = "R1"
	RuleAutoModeStall    = "R2"
	RulePhaseAllTicked   = "R3"
	RuleOneShotShipped   = "R4"
	RulePhaseRunFeature  = "R5"
)

// syntheticModel is turns.model of a line the CLI wrote in place of a model
// reply (an account or API failure, `No response requested.`, an empty filler).
const syntheticModel = "<synthetic>"

// stopEndTurn is the stop_reason of a reply the model finished on its own.
const stopEndTurn = "end_turn"

// Run kinds of a session a plan engine launched.
const (
	runPhase = "phase"
	runPlan  = "plan"
)

// The opening words of the prompts the two plan engines send (phaserun and
// planrun, BuildPrompt). A session whose FIRST user turn opens with one of them
// is that engine's run even when no row links it: epic_phases.run_session_uuid
// keeps only a phase's latest run, so every earlier run of a re-run phase has
// lost its link. The engines import this package, so the words are spelled here
// and pinned against the real prompts by an external test
// (TestRunPromptHeadsMatchEngines).
const (
	PhaseRunPromptHead = "You are executing ONE phase of an approved implementation plan"
	PlanRunPromptHead  = "You are the controller for an ENTIRE approved implementation plan"
)

// The line each engine prompt puts ahead of the document it embeds.
const (
	phaseDocMarker = "PHASE DOCUMENT ("
	planDocMarker  = "PLAN README:"
)

// d2GoalBytes caps the phase goal quoted into the digest.
const d2GoalBytes = 300

// d2DocReadBytes bounds how much of a plan doc — or of the prompt that embeds
// one — is read to find its goal.
const d2DocReadBytes = 64 << 10

// runTaskTypes is TaskTypes without `planning`: a session that EXECUTES a plan
// phase does the phase's kind of work, it does not plan.
var runTaskTypes = slices.DeleteFunc(slices.Clone(TaskTypes), func(t string) bool { return t == "planning" })

// d2Ending is how a session's transcript ends.
type d2Ending struct {
	// turns counts the session's turns, user and assistant.
	turns int
	// hasAssistant is false when the session has no assistant turn at all.
	hasAssistant bool
	// model, stopReason and text are the NEWEST assistant turn's. stopReason is
	// "" when unknown: every turn ingested before migration 0078 has none.
	model, stopReason, text string
	// autoModeStall: the session's last tool call is an error carrying
	// claudeprobe.AutoModeNoVerdictMarker.
	autoModeStall bool
	// lastToolError: the session's last tool call ended in an error.
	lastToolError bool
}

// synthetic reports a last turn the CLI wrote itself.
func (e d2Ending) synthetic() bool { return e.model == syntheticModel }

// failureKind is the account/API failure a synthetic last turn names, or "".
// A synthetic turn with an empty text or `No response requested.` names none.
func (e d2Ending) failureKind() string {
	if !e.synthetic() {
		return ""
	}
	kind, _ := claudeprobe.FailureKind(e.text)
	return kind
}

// finalAnswer reports a last assistant turn that is the model's own prose.
func (e d2Ending) finalAnswer() bool {
	return e.hasAssistant && !e.synthetic() && strings.TrimSpace(e.text) != ""
}

// asksOperator reports a final reply whose last line is a question: the
// session ended waiting on the operator, not on delivered work.
func (e d2Ending) asksOperator() bool {
	lines := strings.Split(strings.TrimSpace(e.text), "\n")
	return strings.Contains(lines[len(lines)-1], "?")
}

// apiError reports an ending the account or the API caused.
func (e d2Ending) apiError() bool { return e.failureKind() != "" || e.autoModeStall }

// d2Run is the plan engine run a session was, if any: linked by
// epic_phases.run_session_uuid or plan_runs.run_session_uuid, or recognised by
// the engine's own prompt.
type d2Run struct {
	kind string // "", runPhase or runPlan
	// name is the phase's name (or the plan's title) and goal the head of the
	// doc's `## Goal` / `## Objective` section; either may be "".
	name, goal string
	// state, ticked and total describe a phase run. tickedKnown is false when the
	// run recorded no end-of-run count.
	state         string
	ticked, total int
	tickedKnown   bool
}

// allTicked reports a phase run that ended done with every criterion ticked.
func (r d2Run) allTicked() bool {
	return r.kind == runPhase && r.state == "done" && r.tickedKnown && r.total > 0 && r.ticked == r.total
}

// d2Facts is what the rules and the evidence block read about one finished
// session, loaded ONCE per session and shared by its three questions.
type d2Facts struct {
	// ok is false when any part failed to load; no rule past R0 fires then.
	ok bool
	// Each part carries its own flag: the evidence block drops the lines of a
	// part that failed and still emits the rest.
	ending   d2Ending
	endingOK bool
	git      gitTally
	gitOK    bool
	// filesEdited counts distinct paths; additions and deletions sum their lines.
	filesEdited, additions, deletions int
	filesOK                           bool
	run                               d2Run
	// opening is the head of the first user turn on one line (at most
	// d2OpeningBytes), "" when the session has no user turn. No rule reads it.
	opening string
}

// oneShot reports a session that is not a plan engine run and ended on the
// model's own final answer with nothing edited, nothing committed and no
// account or API failure. It never holds without an end_turn stop reason: a
// session whose stop reason was not recorded is left to the model. Nor when
// the answer ends on a question to the operator, or the last tool call failed
// (tests failed and the reply explains why): neither ending is delivered work,
// and an R4 answer is never put in front of the operator to correct.
func (f d2Facts) oneShot() bool {
	return f.run.kind == "" &&
		strings.EqualFold(strings.TrimSpace(f.ending.stopReason), stopEndTurn) &&
		f.filesEdited == 0 && f.git.commits == 0 &&
		f.ending.finalAnswer() && !f.ending.apiError() &&
		!f.ending.asksOperator() && !f.ending.lastToolError
}

// loadD2Ending reads how the session's transcript ends.
func loadD2Ending(db *sql.DB, uuid string) (d2Ending, error) {
	var e d2Ending
	if db == nil {
		return e, errors.New("no database")
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM turns WHERE session_id = (SELECT id FROM sessions WHERE session_uuid = ?)`, uuid).
		Scan(&e.turns); err != nil {
		return d2Ending{}, fmt.Errorf("turn count: %w", err)
	}
	err := db.QueryRow(`
		SELECT COALESCE(tr.model, ''), COALESCE(tr.stop_reason, ''), COALESCE(tr.text, '')
		  FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid = ? AND tr.role = 'assistant'
		 ORDER BY tr.seq DESC LIMIT 1`, uuid).Scan(&e.model, &e.stopReason, &e.text)
	switch {
	case err == nil:
		e.hasAssistant = true
	case !errors.Is(err, sql.ErrNoRows):
		return d2Ending{}, fmt.Errorf("last assistant turn: %w", err)
	}
	// The result is extracted only from an errored call: a denial is a short
	// string, while an ok call's result can be a whole file.
	var status, result string
	err = db.QueryRow(`
		SELECT COALESCE(ev.status, ''),
		       CASE WHEN ev.status = 'error' AND json_valid(ev.payload)
		            THEN COALESCE(json_extract(ev.payload, '$.result'), '') ELSE '' END
		  FROM events ev JOIN sessions se ON se.id = ev.session_id
		 WHERE se.session_uuid = ? AND ev.type = 'tool_call'
		 ORDER BY ev.ts DESC, ev.id DESC LIMIT 1`, uuid).Scan(&status, &result)
	switch {
	case err == nil:
		e.lastToolError = status == "error"
		e.autoModeStall = status == "error" && strings.Contains(result, claudeprobe.AutoModeNoVerdictMarker)
	case !errors.Is(err, sql.ErrNoRows):
		return d2Ending{}, fmt.Errorf("last tool call: %w", err)
	}
	return e, nil
}

// loadD2Run reads the plan engine run the session was: the phase run linked to
// it, else the plan run linked to it, else the run its own first prompt says
// it is (enginePrompt), else none. A session linked as both is a phase run.
//
// The goal comes from the doc on disk and, when that is gone (a plan moved to
// the archive) or was never linked, from the copy of the doc the engine put in
// the session's prompt.
//
// The same read of the first prompt yields its opening (d2Facts.opening),
// returned for every session, run or not.
func loadD2Run(db *sql.DB, uuid string) (r d2Run, opening string, err error) {
	r, docPath, err := linkedRun(db, uuid)
	if err != nil {
		return d2Run{}, "", err
	}
	var prompt string
	err = db.QueryRow(`
		SELECT COALESCE(substr(tr.text, 1, ?), '')
		  FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid = ? AND tr.role = 'user'
		 ORDER BY tr.seq LIMIT 1`, d2DocReadBytes, uuid).Scan(&prompt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return d2Run{}, "", fmt.Errorf("first prompt: %w", err)
	}
	// Whitespace is collapsed over a bounded head, not the whole prompt.
	opening = truncate(strings.Join(strings.Fields(truncate(prompt, 4*d2OpeningBytes)), " "), d2OpeningBytes)
	promptKind, doc := enginePrompt(prompt)
	if r.kind == "" {
		r.kind = promptKind
	}
	if r.kind == "" {
		return d2Run{}, opening, nil
	}
	if r.goal = fileGoal(docPath); r.goal == "" && promptKind == r.kind {
		r.goal = goalOf(doc)
	}
	if r.name == "" && promptKind == r.kind {
		r.name = titleOf(doc)
	}
	r.name = strings.Join(strings.Fields(r.name), " ")
	return r, opening, nil
}

// linkedRun reads the run a row links to the session, with the doc that
// describes it (the phase doc, or the plan's README.md); kind "" when no row
// links one.
func linkedRun(db *sql.DB, uuid string) (r d2Run, docPath string, err error) {
	var after sql.NullInt64
	err = db.QueryRow(`
		SELECT name, doc_path, COALESCE(run_state, ''), checkboxes_total, run_checkboxes_after
		  FROM epic_phases WHERE run_session_uuid = ? ORDER BY id DESC LIMIT 1`, uuid).
		Scan(&r.name, &docPath, &r.state, &r.total, &after)
	switch {
	case err == nil:
		r.kind = runPhase
		r.ticked, r.tickedKnown = int(after.Int64), after.Valid
		return r, docPath, nil
	case !errors.Is(err, sql.ErrNoRows):
		return d2Run{}, "", fmt.Errorf("phase run: %w", err)
	}
	var firstDoc string
	err = db.QueryRow(`
		SELECT COALESCE(t.title, ''),
		       COALESCE((SELECT p.doc_path FROM epic_phases p
		                  WHERE p.workspace_task_id = r.workspace_task_id ORDER BY p.seq, p.id LIMIT 1), '')
		  FROM plan_runs r LEFT JOIN tasks t ON t.id = r.workspace_task_id
		 WHERE r.run_session_uuid = ? LIMIT 1`, uuid).Scan(&r.name, &firstDoc)
	switch {
	case err == nil:
		r.kind = runPlan
		if firstDoc != "" {
			docPath = filepath.Join(filepath.Dir(firstDoc), "README.md")
		}
		return r, docPath, nil
	case !errors.Is(err, sql.ErrNoRows):
		return d2Run{}, "", fmt.Errorf("plan run: %w", err)
	}
	return d2Run{}, "", nil
}

// enginePrompt recognises a plan engine's own prompt: the run kind it opens
// (PhaseRunPromptHead / PlanRunPromptHead; "" for any other text) and the
// document it embeds after its marker line ("" when the marker is missing).
func enginePrompt(text string) (kind, doc string) {
	text = strings.TrimSpace(text)
	var marker string
	switch {
	case strings.HasPrefix(text, PhaseRunPromptHead):
		kind, marker = runPhase, phaseDocMarker
	case strings.HasPrefix(text, PlanRunPromptHead):
		kind, marker = runPlan, planDocMarker
	default:
		return "", ""
	}
	if i := strings.Index(text, "\n"+marker); i >= 0 {
		doc = text[i+1+len(marker):]
	}
	return kind, doc
}

// loadD2Facts loads the facts for one session, each part once. A part that
// fails is left zero with its flag down, the others still load, and the joined
// error says which failed. ok is true only when every part loaded, so no rule
// fires on a half-read session — a missed rule costs one model call, a wrong
// rule answer is recorded at confidence 1.
func loadD2Facts(db *sql.DB, uuid string) (d2Facts, error) {
	if db == nil {
		return d2Facts{}, errors.New("no database")
	}
	var f d2Facts
	var errs []error
	if ending, err := loadD2Ending(db, uuid); err != nil {
		errs = append(errs, err)
	} else {
		f.ending, f.endingOK = ending, true
	}
	if err := db.QueryRow(`
		SELECT COUNT(DISTINCT file_path), COALESCE(SUM(additions), 0), COALESCE(SUM(deletions), 0)
		  FROM file_changes
		 WHERE session_id = (SELECT id FROM sessions WHERE session_uuid = ?)`, uuid).
		Scan(&f.filesEdited, &f.additions, &f.deletions); err != nil {
		errs = append(errs, fmt.Errorf("files edited: %w", err))
	} else {
		f.filesOK = true
	}
	if git, err := gitActivity(db, uuid); err != nil {
		errs = append(errs, fmt.Errorf("git activity: %w", err))
	} else {
		f.git, f.gitOK = git, true
	}
	run, opening, err := loadD2Run(db, uuid)
	if err != nil {
		errs = append(errs, err)
	} else {
		f.run, f.opening = run, opening
	}
	f.ok = len(errs) == 0
	return f, errors.Join(errs...)
}

// d2RuleAnswer is one rule's answer to one question; the zero value is "no
// rule applies".
type d2RuleAnswer struct {
	value, rule string
}

// d2RuleSet is the rules' answer to each D2 question.
type d2RuleSet struct {
	taskType, outcome, failure d2RuleAnswer
}

// d2Rules applies the rule table to one session's facts, first match wins per
// question. verdict is the operator's own sessions.outcome (R0, always first).
// phaseRunFeature switches R5 on.
//
// The two answers of a session never contradict each other: a cause other than
// `none` is not given beside a rule-answered `shipped`, nor `none` beside any
// other rule-answered outcome — such a question falls through to the next rule
// and then to the model.
func d2Rules(f d2Facts, verdict string, phaseRunFeature bool) d2RuleSet {
	var out d2RuleSet
	if v := operatorOutcome[verdict]; v != "" {
		out.outcome = d2RuleAnswer{v, RuleOperatorVerdict}
	}
	if !f.ok {
		return out
	}
	kind, allTicked, oneShot := f.ending.failureKind(), f.run.allTicked(), f.oneShot()

	if out.outcome.value == "" {
		switch {
		case kind != "":
			out.outcome = d2RuleAnswer{"failed", RuleSyntheticFailure}
		case allTicked:
			out.outcome = d2RuleAnswer{"shipped", RulePhaseAllTicked}
		case oneShot:
			out.outcome = d2RuleAnswer{"shipped", RuleOneShotShipped}
		}
	}

	causes := []d2RuleAnswer{{kind, RuleSyntheticFailure}}
	if f.ending.autoModeStall {
		causes = append(causes, d2RuleAnswer{claudeprobe.FailureAPIError, RuleAutoModeStall})
	}
	if allTicked {
		causes = append(causes, d2RuleAnswer{"none", RulePhaseAllTicked})
	}
	if oneShot {
		causes = append(causes, d2RuleAnswer{"none", RuleOneShotShipped})
	}
	for _, c := range causes {
		if c.value != "" && causeFitsOutcome(out.outcome.value, c.value) {
			out.failure = c
			break
		}
	}

	if phaseRunFeature && f.run.kind != "" {
		out.taskType = d2RuleAnswer{"feature", RulePhaseRunFeature}
	}
	return out
}

// causeFitsOutcome reports whether a failure cause can stand beside a
// rule-answered outcome ("" ⇒ the rules gave none, anything fits).
func causeFitsOutcome(outcome, cause string) bool {
	switch outcome {
	case "":
		return true
	case "shipped":
		return cause == "none"
	}
	return cause != "none"
}

// runContext is the digest block naming the plan phase (or plan) a session
// executed and quoting the head of its goal, or "" for any other session. A
// goal that could not be read leaves the name alone; a run with neither says
// only that it was one.
func runContext(r d2Run) string {
	if r.kind == "" {
		return ""
	}
	name := r.name
	if name == "" {
		name = "(name not recorded)"
	}
	out := fmt.Sprintf("%s: %s\n", r.kind, name)
	if r.goal != "" {
		out += fmt.Sprintf("%s goal: %s\n", r.kind, r.goal)
	}
	return out
}

// fileGoal is goalOf over a plan doc on disk, or "" when the doc is missing,
// unreadable or not an absolute markdown path.
func fileGoal(path string) string {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".md") {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, d2DocReadBytes))
	if err != nil {
		return ""
	}
	return goalOf(string(raw))
}

// titleOf is a markdown doc's first `# ` heading, on one line, or "".
func titleOf(doc string) string {
	for _, line := range strings.Split(doc, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok {
			return truncate(strings.Join(strings.Fields(h), " "), d2GoalBytes)
		}
	}
	return ""
}

// goalOf returns the first d2GoalBytes bytes of a markdown doc's `## Goal` (or
// `## Objective`) section on one line, or "" when it has no such section.
func goalOf(doc string) string {
	var body []string
	inGoal := false
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if inGoal {
				break
			}
			heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			inGoal = strings.HasPrefix(trimmed, "## ") && (heading == "goal" || heading == "objective")
			continue
		}
		if !inGoal {
			continue
		}
		// A rule line ends the section too: the engine prompts close the doc
		// they embed with one.
		if len(trimmed) >= 3 && strings.Trim(trimmed, "-") == "" {
			break
		}
		body = append(body, trimmed)
	}
	return truncate(strings.Join(strings.Fields(strings.Join(body, " ")), " "), d2GoalBytes)
}

// d2FactsFor is loadD2Facts with the labeler's error policy: a failed load is
// logged and the session is labelled without rules, never skipped.
func d2FactsFor(db *sql.DB, uuid string) d2Facts {
	f, err := loadD2Facts(db, uuid)
	if err != nil {
		log.Printf("warning: decide: d2 facts for %s: %v", uuid, err)
	}
	return f
}
