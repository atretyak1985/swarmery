package decide

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
)

const testModel = "claude-opus-5-5"

// seedTurn adds one turn; an empty model or stop reason is stored as NULL, the
// way a turn ingested before migration 0078 has no stop_reason.
func seedTurn(t *testing.T, db *sql.DB, uuid string, seq int, role, model, stop, text string) {
	t.Helper()
	var m, s any
	if model != "" {
		m = model
	}
	if stop != "" {
		s = stop
	}
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, model, stop_reason, text)
		VALUES ((SELECT id FROM sessions WHERE session_uuid = ?), ?, ?, '2026-09-20T10:30:00.000Z', ?, ?, ?)`,
		uuid, seq, role, m, s, text)
}

// seedEnding is a finished session whose transcript is a user prompt and one
// assistant turn with the given model, stop reason and text.
func seedEnding(t *testing.T, db *sql.DB, uuid, model, stop, text string) {
	t.Helper()
	seedEmptySession(t, db, uuid, "2026-09-20T11:00:00.000Z", "")
	seedTurn(t, db, uuid, 1, "user", "", "", "what does the parser do?")
	seedTurn(t, db, uuid, 2, "assistant", model, stop, text)
}

// seedOneShot is the R4 shape: a question, and the model's own final answer.
func seedOneShot(t *testing.T, db *sql.DB, uuid string) {
	t.Helper()
	seedEnding(t, db, uuid, testModel, "end_turn", "It tokenises the input and builds the tree.")
}

// seedToolCall records one tool call with its status, timestamp and result.
func seedToolCall(t *testing.T, db *sql.DB, uuid, tool, status, ts string, result any) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"input": map[string]any{"command": "true"}, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
		VALUES ((SELECT id FROM sessions WHERE session_uuid = ?), ?, 'tool_call', ?, ?, ?, ?)`,
		uuid, ts, tool, status, string(payload), fmt.Sprintf("%s-%d", uuid, bashSeq.Add(1)))
}

// autoModeDenial is the denial a tool call gets when the auto mode check
// returns no verdict.
var autoModeDenial = "Error: The server-side " + claudeprobe.AutoModeNoVerdictMarker +
	" (error), so auto mode cannot determine the safety of Bash."

var phaseSeq atomic.Int64

// seedPhase attaches the session to a plan phase as its run.
func seedPhase(t *testing.T, db *sql.DB, uuid, name, docPath, state string, total int, after any) {
	t.Helper()
	seq := phaseSeq.Add(1)
	if docPath == "" {
		docPath = fmt.Sprintf("/ws/plan/phase-%d-x.md", seq)
	}
	mustExec(t, db, `INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, checkboxes_done, checkboxes_total,
		run_session_uuid, run_state, run_checkboxes_before, run_checkboxes_after)
		VALUES (1, ?, ?, ?, 0, ?, ?, ?, 0, ?)`, seq, name, docPath, total, uuid, state, after)
}

// seedPlanRun makes the session the run of a whole plan (task 77).
func seedPlanRun(t *testing.T, db *sql.DB, uuid, title string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, created_at, source)
		VALUES (77, 1, ?, '', '2026-09-20T09:00:00Z', 'workspace')`, title)
	mustExec(t, db, `INSERT INTO plan_runs (workspace_task_id, run_state, run_session_uuid) VALUES (77, 'done', ?)`, uuid)
}

// d2QuestionsFor builds the session's three questions the way the labeler and
// the eval do, keyed by question id.
func d2QuestionsFor(t *testing.T, db *sql.DB, uuid string, r5 bool) map[string]Question {
	t.Helper()
	s, reason, err := evalSession(context.Background(), db, uuid)
	if err != nil || reason != "" {
		t.Fatalf("session %s: reason %q err %v", uuid, reason, err)
	}
	out := map[string]Question{}
	for _, q := range d2Questions(db, s, r5) {
		out[q.ID] = q
	}
	if len(out) != 3 {
		t.Fatalf("questions = %v, want the three D2 questions", out)
	}
	return out
}

// wantRule asserts one question's rule answer and the rule that gave it;
// value "" means no rule answers — the question goes to the model.
func wantRule(t *testing.T, qs map[string]Question, id, value, rule string) {
	t.Helper()
	if q := qs[id]; q.RuleAnswer != value || q.RuleID != rule {
		t.Errorf("%s: rule answer %q by %q, want %q by %q", id, q.RuleAnswer, q.RuleID, value, rule)
	}
}

// R1: a session whose last assistant turn is a synthetic account/API failure
// line failed, and the line says why — for every shape FailureKind knows (the
// table mirrors claudeprobe's failureShapes, one text per shape).
func TestRuleSyntheticFailure(t *testing.T) {
	shapes := []struct{ text, kind string }{
		{"Not logged in · Please run /login", claudeprobe.FailureAuth},
		{"Login expired · Please run /login", claudeprobe.FailureAuth},
		{"Failed to authenticate: OAuth session expired and could not be refreshed", claudeprobe.FailureAuth},
		{"Your organization has disabled Claude subscription access for Claude Code", claudeprobe.FailureAuth},
		{`Please run /login · API Error: 401 {"type":"error"}`, claudeprobe.FailureAuth},
		{"You've hit your session limit · resets 1:30am (Europe/Kiev)", claudeprobe.FailureQuota},
		{"You've hit your weekly limit · resets Sep 19 at 5pm (Europe/Kiev)", claudeprobe.FailureQuota},
		{"You've hit your individual spend limit · run /usage-credits to ask for more", claudeprobe.FailureQuota},
		{"You've hit your org's monthly spend limit · run /usage-credits", claudeprobe.FailureQuota},
		{"You've hit your monthly spend limit. /model to switch models.", claudeprobe.FailureQuota},
		{"You've reached your Opus limit. Run /usage-credits to continue", claudeprobe.FailureQuota},
		{"You're out of usage credits", claudeprobe.FailureQuota},
		{"Usage limit reached", claudeprobe.FailureQuota},
		{"API Error: 529 Overloaded. This is a server-side issue, usually temporary", claudeprobe.FailureAPIError},
		{"Request timed out", claudeprobe.FailureAPIError},
	}
	db := openDB(t)
	for i, sh := range shapes {
		if kind, ok := claudeprobe.FailureKind(sh.text); !ok || kind != sh.kind {
			t.Fatalf("FailureKind(%q) = %q %v, want %q — the table no longer mirrors claudeprobe", sh.text, kind, ok, sh.kind)
		}
		uuid := fmt.Sprintf("s-synth-%d", i)
		// The stop reason varies the way the store's synthetic turns do.
		seedEnding(t, db, uuid, syntheticModel, []string{"stop_sequence", ""}[i%2], sh.text)
		qs := d2QuestionsFor(t, db, uuid, false)
		wantRule(t, qs, QD2Outcome, "failed", RuleSyntheticFailure)
		wantRule(t, qs, QD2Failure, sh.kind, RuleSyntheticFailure)
		wantRule(t, qs, QD2TaskType, "", "")
	}

	// End to end: both answers come from the rules backend at confidence 1 and
	// only the task type reaches the model.
	s := &stub{name: BackendLocal, a: Answer{Value: "other", Confidence: 0.9, Calibrated: true}}
	e := &Engine{DB: db, Local: s}
	for _, q := range d2QuestionsFor(t, db, "s-synth-0", false) {
		a, err := e.Decide(context.Background(), q)
		if err != nil {
			t.Fatalf("%s: %v", q.ID, err)
		}
		if q.ID == QD2TaskType {
			continue
		}
		if a.Backend != BackendRules || a.Confidence != 1 || !a.Calibrated || a.Rule != RuleSyntheticFailure {
			t.Errorf("%s = %+v, want a rules answer at confidence 1 by R1", q.ID, a)
		}
	}
	if s.calls != 1 {
		t.Errorf("model calls = %d, want 1 (task type only)", s.calls)
	}

	// The failure line must be the LAST assistant turn: a session that recovered
	// and answered afterwards did not end on it.
	seedEnding(t, db, "s-recovered", syntheticModel, "stop_sequence", "API Error: 529 Overloaded")
	seedTurn(t, db, "s-recovered", 3, "assistant", testModel, "", "Retried; the parser is fixed.")
	qs := d2QuestionsFor(t, db, "s-recovered", false)
	wantRule(t, qs, QD2Outcome, "", "")
	wantRule(t, qs, QD2Failure, "", "")
}

// R1 stays out of a session whose work landed before the failure line: the
// outcome prompt calls committed, pushed or merged work shipped, so "failed" at
// confidence 1 would contradict the evidence — the operator who merged and then
// hit a limit on a follow-up did not fail. Both questions go to the model.
func TestRuleSyntheticFailureAfterLandedWork(t *testing.T) {
	db := openDB(t)
	for i, cmd := range []string{
		`git commit -m "fix: parser"`,
		`git push -u origin fix/parser`,
		`gh pr create --fill`,
		`gh pr merge 12 --squash`,
	} {
		uuid := fmt.Sprintf("s-landed-%d", i)
		seedEnding(t, db, uuid, syntheticModel, "stop_sequence", "You've hit your session limit · resets 1:30am (UTC)")
		seedBash(t, db, uuid, cmd, "ok")
		qs := d2QuestionsFor(t, db, uuid, false)
		wantRule(t, qs, QD2Outcome, "", "")
		wantRule(t, qs, QD2Failure, "", "")
	}

	// Reading git, or a landing command that failed, landed nothing: R1 stands.
	seedEnding(t, db, "s-nothing-landed", syntheticModel, "stop_sequence", "You've hit your session limit · resets 1:30am (UTC)")
	seedBash(t, db, "s-nothing-landed", `git status && git log --oneline -3`, "ok")
	seedBash(t, db, "s-nothing-landed", `git push`, "error")
	qs := d2QuestionsFor(t, db, "s-nothing-landed", false)
	wantRule(t, qs, QD2Outcome, "failed", RuleSyntheticFailure)
	wantRule(t, qs, QD2Failure, claudeprobe.FailureQuota, RuleSyntheticFailure)
}

// A synthetic last turn that names no failure — empty, the CLI's `No response
// requested.` filler, an unrecorded wording — matches no rule, and neither does
// a MODEL turn that merely opens with a failure line. All go to the model.
func TestRuleSyntheticUnknownGoesToModel(t *testing.T) {
	db := openDB(t)
	cases := []struct{ uuid, model, stop, text string }{
		{"s-empty-text", syntheticModel, "", ""},
		{"s-blank-text", syntheticModel, "stop_sequence", "  \n"},
		{"s-filler", syntheticModel, "stop_sequence", "No response requested."},
		{"s-unrecorded", syntheticModel, "stop_sequence", "Something the CLI never said before"},
		{"s-refusal", syntheticModel, "refusal", ""},
		{"s-model-quote", testModel, "", "API Error: 529 Overloaded is what the log shows; I retried and it passed."},
	}
	s := &stub{name: BackendLocal, a: Answer{Value: "other", Confidence: 0.9, Calibrated: true},
		byQ: map[string]Answer{QD2Outcome: {Value: "failed", Confidence: 0.9, Calibrated: true}}}
	e := &Engine{DB: db, Local: s}
	for _, tc := range cases {
		seedEnding(t, db, tc.uuid, tc.model, tc.stop, tc.text)
		qs := d2QuestionsFor(t, db, tc.uuid, false)
		for _, id := range EvalQuestions {
			wantRule(t, qs, id, "", "")
		}
		before := s.calls
		for _, q := range qs {
			if a, err := e.Decide(context.Background(), q); err != nil || a.Backend != BackendLocal {
				t.Errorf("%s %s = %+v (%v), want the model's answer", tc.uuid, q.ID, a, err)
			}
		}
		if s.calls-before != 3 {
			t.Errorf("%s: model calls = %d, want all 3 questions", tc.uuid, s.calls-before)
		}
	}
	// A synthetic turn is never a final answer, so R4 cannot take it either —
	// even with an end_turn stop reason on it.
	seedEnding(t, db, "s-synth-end-turn", syntheticModel, "end_turn", "No response requested.")
	qs := d2QuestionsFor(t, db, "s-synth-end-turn", false)
	wantRule(t, qs, QD2Outcome, "", "")
	wantRule(t, qs, QD2Failure, "", "")
}

// R2: a session whose LAST tool call is an auto mode no-verdict error stalled
// on the API — the cause is api-error, and the outcome is left to the model.
func TestRuleAutoModeStall(t *testing.T) {
	db := openDB(t)
	// An R4-shaped ending after the stall: R2 answers the cause, and R4 must not
	// call the session shipped.
	seedOneShot(t, db, "s-stall")
	seedToolCall(t, db, "s-stall", "Bash", "ok", "2026-09-20T10:20:00.000Z", map[string]any{"stdout": "ok"})
	seedToolCall(t, db, "s-stall", "Bash", "error", "2026-09-20T10:25:00.000Z", autoModeDenial)
	qs := d2QuestionsFor(t, db, "s-stall", false)
	wantRule(t, qs, QD2Failure, claudeprobe.FailureAPIError, RuleAutoModeStall)
	wantRule(t, qs, QD2Outcome, "", "")
	wantRule(t, qs, QD2TaskType, "", "")
	if in := qs[QD2Outcome].Input; !strings.Contains(in, "api error: yes\n") {
		t.Errorf("the digest does not report the API error:\n%s", in)
	}

	// The stall was not the last tool call: the check recovered, no rule.
	seedOneShot(t, db, "s-recovered")
	seedToolCall(t, db, "s-recovered", "Bash", "error", "2026-09-20T10:20:00.000Z", autoModeDenial)
	seedToolCall(t, db, "s-recovered", "Bash", "ok", "2026-09-20T10:25:00.000Z", map[string]any{"stdout": "ok"})
	qs = d2QuestionsFor(t, db, "s-recovered", false)
	wantRule(t, qs, QD2Failure, "none", RuleOneShotShipped)
	wantRule(t, qs, QD2Outcome, "shipped", RuleOneShotShipped)

	// An ordinary tool error is not a stall — and, as the last call, keeps R4
	// out too, so both questions go to the model.
	seedOneShot(t, db, "s-plain-error")
	seedToolCall(t, db, "s-plain-error", "Bash", "error", "2026-09-20T10:25:00.000Z", "Error: exit status 1")
	qs = d2QuestionsFor(t, db, "s-plain-error", false)
	wantRule(t, qs, QD2Failure, "", "")
	wantRule(t, qs, QD2Outcome, "", "")

	// A failed call whose output merely QUOTES the refusal (the automode tests
	// failing) is not a stall either: R2 needs the result to be the refusal.
	seedOneShot(t, db, "s-quoted-error")
	seedToolCall(t, db, "s-quoted-error", "Bash", "error", "2026-09-20T10:25:00.000Z",
		"Error: Exit code 1\n--- FAIL: TestCount\n    want a row quoting \""+autoModeDenial+"\"")
	qs = d2QuestionsFor(t, db, "s-quoted-error", false)
	wantRule(t, qs, QD2Failure, "", "")

	// An OK call whose output merely quotes the marker is not a stall.
	seedOneShot(t, db, "s-quoted")
	seedToolCall(t, db, "s-quoted", "Bash", "ok", "2026-09-20T10:25:00.000Z",
		map[string]any{"stdout": "grep: " + claudeprobe.AutoModeNoVerdictMarker})
	qs = d2QuestionsFor(t, db, "s-quoted", false)
	wantRule(t, qs, QD2Failure, "none", RuleOneShotShipped)
}

// R3: a phase run that ended done with every criterion ticked shipped, with no
// failure cause.
func TestRulePhaseAllTicked(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-phase", "2026-09-20T11:00:00.000Z", "")
	seedPhase(t, db, "s-phase", "Phase 1 — parser", "", "done", 3, 3)
	qs := d2QuestionsFor(t, db, "s-phase", false)
	wantRule(t, qs, QD2Outcome, "shipped", RulePhaseAllTicked)
	wantRule(t, qs, QD2Failure, "none", RulePhaseAllTicked)
	wantRule(t, qs, QD2TaskType, "", "") // R5 is off

	// A stalled last tool call does not turn a shipped phase into an api-error:
	// the two answers of one session never contradict each other.
	seedSession(t, db, "s-phase-stall", "2026-09-20T11:00:00.000Z", "")
	seedPhase(t, db, "s-phase-stall", "Phase 2", "", "done", 2, 2)
	seedToolCall(t, db, "s-phase-stall", "Bash", "error", "2026-09-20T10:25:00.000Z", autoModeDenial)
	qs = d2QuestionsFor(t, db, "s-phase-stall", false)
	wantRule(t, qs, QD2Outcome, "shipped", RulePhaseAllTicked)
	wantRule(t, qs, QD2Failure, "none", RulePhaseAllTicked)

	// First match wins: a run that ended on a failure line failed, ticks or not.
	seedEnding(t, db, "s-phase-quota", syntheticModel, "stop_sequence", "You've hit your session limit · resets 1:30am")
	seedPhase(t, db, "s-phase-quota", "Phase 3", "", "done", 2, 2)
	qs = d2QuestionsFor(t, db, "s-phase-quota", false)
	wantRule(t, qs, QD2Outcome, "failed", RuleSyntheticFailure)
	wantRule(t, qs, QD2Failure, claudeprobe.FailureQuota, RuleSyntheticFailure)
}

// A phase run that is done without every criterion ticked — or not done, or
// with no recorded end-of-run count, or with no criteria at all — matches no
// rule. Nor does R4: a phase run is never a one-shot session.
func TestRulePhaseDoneNotAllTicked(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		uuid, state string
		total       int
		after       any
	}{
		{"s-some", "done", 3, 2},
		{"s-none-ticked", "done", 6, 0},
		{"s-no-count", "done", 3, nil},
		{"s-no-criteria", "done", 0, 0},
		{"s-blocked", "blocked", 3, 3},
		{"s-failed", "failed", 5, 5},
		{"s-running", "running", 3, 3},
	}
	for _, tc := range cases {
		seedOneShot(t, db, tc.uuid) // an R4-shaped transcript, to prove R4 stays out too
		seedPhase(t, db, tc.uuid, "Phase", "", tc.state, tc.total, tc.after)
		qs := d2QuestionsFor(t, db, tc.uuid, false)
		wantRule(t, qs, QD2Outcome, "", "")
		wantRule(t, qs, QD2Failure, "", "")
	}
}

// R4: a session that is no plan run and ended on the model's own final answer
// (end_turn) with nothing edited, nothing committed and no API error shipped.
func TestRuleOneShotShipped(t *testing.T) {
	db := openDB(t)
	seedOneShot(t, db, "s-qa")
	seedBash(t, db, "s-qa", `git commit -m "nothing to commit"`, "error") // a failed commit is not a commit
	seedBash(t, db, "s-qa", `git status && git log --oneline -3`, "ok")   // nor is reading git
	qs := d2QuestionsFor(t, db, "s-qa", false)
	wantRule(t, qs, QD2Outcome, "shipped", RuleOneShotShipped)
	wantRule(t, qs, QD2Failure, "none", RuleOneShotShipped)
	wantRule(t, qs, QD2TaskType, "", "")
	in := qs[QD2Outcome].Input
	for _, want := range []string{"turns: 2\n", "final answer: yes\n", "last stop reason: end_turn\n", "api error: no\n"} {
		if !strings.Contains(in, want) {
			t.Errorf("digest lacks %q:\n%s", want, in)
		}
	}

	// The operator's verdict stays first, and the cause never contradicts it.
	seedOneShot(t, db, "s-verdict-fail")
	mustExec(t, db, `UPDATE sessions SET outcome = 'fail' WHERE session_uuid = 's-verdict-fail'`)
	qs = d2QuestionsFor(t, db, "s-verdict-fail", false)
	wantRule(t, qs, QD2Outcome, "failed", RuleOperatorVerdict)
	wantRule(t, qs, QD2Failure, "", "") // `none` beside a failed verdict: left to the model

	seedOneShot(t, db, "s-verdict-ok")
	mustExec(t, db, `UPDATE sessions SET outcome = 'success' WHERE session_uuid = 's-verdict-ok'`)
	qs = d2QuestionsFor(t, db, "s-verdict-ok", false)
	wantRule(t, qs, QD2Outcome, "shipped", RuleOperatorVerdict)
	wantRule(t, qs, QD2Failure, "none", RuleOneShotShipped)

	seedEnding(t, db, "s-verdict-vs-failure", syntheticModel, "stop_sequence", "Not logged in · Please run /login")
	mustExec(t, db, `UPDATE sessions SET outcome = 'success' WHERE session_uuid = 's-verdict-vs-failure'`)
	qs = d2QuestionsFor(t, db, "s-verdict-vs-failure", false)
	wantRule(t, qs, QD2Outcome, "shipped", RuleOperatorVerdict)
	wantRule(t, qs, QD2Failure, "", "") // `auth` beside a shipped verdict: left to the model
}

// R4 is answered at confidence 1 and hidden from the Inbox queue, so it stays
// out of the two endings a final prose reply does not make shipped: the reply
// asks the operator something (its last line is a question), or the session's
// last tool call failed (the tests failed and the model explains why). Both go
// to the model. A question earlier in the reply is not the ending.
func TestRuleOneShotNotOnQuestionOrFailedCall(t *testing.T) {
	db := openDB(t)
	noRule := func(uuid string) {
		t.Helper()
		qs := d2QuestionsFor(t, db, uuid, false)
		wantRule(t, qs, QD2Outcome, "", "")
		wantRule(t, qs, QD2Failure, "", "")
	}

	seedEnding(t, db, "s-asks", testModel, "end_turn", "I can do this two ways.\n\nShould I use approach A or B?\n")
	noRule("s-asks")

	seedOneShot(t, db, "s-tests-failed")
	seedToolCall(t, db, "s-tests-failed", "Bash", "ok", "2026-09-20T10:20:00.000Z", map[string]any{"stdout": "built"})
	seedToolCall(t, db, "s-tests-failed", "Bash", "error", "2026-09-20T10:25:00.000Z", "FAIL: TestParser (0.01s)")
	noRule("s-tests-failed")

	seedEnding(t, db, "s-asked-earlier", testModel, "end_turn", "Does it handle unicode? Yes.\nIt tokenises the input and builds the tree.")
	qs := d2QuestionsFor(t, db, "s-asked-earlier", false)
	wantRule(t, qs, QD2Outcome, "shipped", RuleOneShotShipped)

	seedOneShot(t, db, "s-failed-then-ok")
	seedToolCall(t, db, "s-failed-then-ok", "Bash", "error", "2026-09-20T10:20:00.000Z", "FAIL: TestParser (0.01s)")
	seedToolCall(t, db, "s-failed-then-ok", "Bash", "ok", "2026-09-20T10:25:00.000Z", map[string]any{"stdout": "ok"})
	qs = d2QuestionsFor(t, db, "s-failed-then-ok", false)
	wantRule(t, qs, QD2Outcome, "shipped", RuleOneShotShipped)
}

// R4 never fires without an end_turn stop reason — a turn ingested before
// migration 0078 has none — nor when anything was edited or committed, when
// the last turn carries no answer, or for a plan engine run.
func TestRuleOneShotNeedsEndTurn(t *testing.T) {
	db := openDB(t)
	noRule := func(uuid string) {
		t.Helper()
		qs := d2QuestionsFor(t, db, uuid, false)
		wantRule(t, qs, QD2Outcome, "", "")
		wantRule(t, qs, QD2Failure, "", "")
	}

	seedEnding(t, db, "s-no-stop", testModel, "", "It tokenises the input.")
	noRule("s-no-stop")
	if in := d2QuestionsFor(t, db, "s-no-stop", false)[QD2Outcome].Input; !strings.Contains(in, "final answer: yes\nlast stop reason: unknown\n") {
		t.Errorf("a missing stop reason must read `unknown`:\n%s", in)
	}

	for _, stop := range []string{"tool_use", "max_tokens", "stop_sequence", "refusal"} {
		seedEnding(t, db, "s-"+stop, testModel, stop, "Working on it.")
		noRule("s-" + stop)
	}

	seedEnding(t, db, "s-no-text", testModel, "end_turn", "")
	noRule("s-no-text")

	seedOneShot(t, db, "s-edited")
	seedFileChange(t, db, "s-edited", "parser.go", 3, 1)
	noRule("s-edited")

	seedOneShot(t, db, "s-committed")
	seedBash(t, db, "s-committed", `git commit -m "fix: parser"`, "ok")
	noRule("s-committed")

	// A commit behind a quoted option value with a space in it is still a commit.
	seedOneShot(t, db, "s-committed-quoted")
	seedBash(t, db, "s-committed-quoted", `git add . && git -c user.name="First Last" -c user.email="a@b.c" commit -q -m "fix: parser"`, "ok")
	noRule("s-committed-quoted")
	if in := d2QuestionsFor(t, db, "s-committed-quoted", false)[QD2Outcome].Input; !strings.Contains(in, "commits: 1\n") {
		t.Errorf("the quoted-identity commit is not counted:\n%s", in)
	}

	seedOneShot(t, db, "s-plan-run")
	seedPlanRun(t, db, "s-plan-run", "Parser rewrite")
	noRule("s-plan-run")

	// A session with no assistant turn at all has nothing to decide from.
	seedEmptySession(t, db, "s-user-only", "2026-09-20T11:00:00.000Z", "")
	seedTurn(t, db, "s-user-only", 1, "user", "", "", "hello")
	noRule("s-user-only")
	if in := d2QuestionsFor(t, db, "s-user-only", false)[QD2Outcome].Input; !strings.Contains(in, "turns: 1\nfinal answer: no\nlast stop reason: unknown\napi error: no\n") {
		t.Errorf("ending lines of a session with no assistant turn:\n%s", in)
	}
}

// A session that ran a plan phase (or a whole plan) cannot be labelled
// `planning`: the option is gone, the prompt says why, and the digest names the
// phase with the head of its goal.
func TestPhaseRunExcludesPlanning(t *testing.T) {
	db := openDB(t)
	dir := t.TempDir()
	doc := filepath.Join(dir, "phase-2-parser.md")
	goal := "Rewrite the tokenizer so that\nnested   quotes parse. " + strings.Repeat("Detail. ", 60)
	if err := os.WriteFile(doc, []byte("# Phase 2 — Parser\n\nStatus: Pending\n\n## Goal\n\n"+goal+"\n\n## Files to Modify\n\n- parser.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Plan\n\n## Objective\n\nShip the new parser.\n\n## Risks\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	seedSession(t, db, "s-phase", "2026-09-20T11:00:00.000Z", "")
	seedPhase(t, db, "s-phase", "Phase 2 —\nParser", doc, "blocked", 3, 1)
	qs := d2QuestionsFor(t, db, "s-phase", false)
	task := qs[QD2TaskType]
	if slices.Contains(task.Opts, "planning") || len(task.Opts) != len(TaskTypes)-1 {
		t.Errorf("phase-run task types = %v, want TaskTypes without planning", task.Opts)
	}
	if !strings.HasSuffix(task.Prompt, "is the kind of work the phase does, never planning.") {
		t.Errorf("phase-run prompt = %q", task.Prompt)
	}
	if !strings.Contains(task.Input, "title: fix the parser\nphase: Phase 2 — Parser\nphase goal: Rewrite the tokenizer so that nested quotes parse. Detail.") {
		t.Errorf("digest does not name the phase and its goal:\n%s", task.Input)
	}
	for _, line := range strings.Split(task.Input, "\n") {
		if g, ok := strings.CutPrefix(line, "phase goal: "); ok && (len(g) > d2GoalBytes || strings.Contains(g, "Files to Modify")) {
			t.Errorf("goal is %d bytes or ran past its section: %q", len(g), g)
		}
	}
	// The other two questions keep their options, and share the digest.
	if !slices.Equal(qs[QD2Outcome].Opts, Outcomes) || !slices.Equal(qs[QD2Failure].Opts, FailureCauses) || qs[QD2Outcome].Input != task.Input {
		t.Errorf("outcome/failure questions changed: %+v", qs)
	}
	// A model that still answers `planning` is a failed call, not a label.
	e := &Engine{DB: db, Local: &stub{name: BackendLocal, a: Answer{Value: "planning", Confidence: 0.99, Calibrated: true}}}
	if a, err := e.Decide(context.Background(), task); err == nil {
		t.Errorf("`planning` was accepted for a phase run: %+v", a)
	}

	// An unreadable doc leaves the name alone.
	seedSession(t, db, "s-phase-gone", "2026-09-20T11:00:00.000Z", "")
	seedPhase(t, db, "s-phase-gone", "Phase 9", filepath.Join(dir, "missing.md"), "done", 3, 1)
	in := d2QuestionsFor(t, db, "s-phase-gone", false)[QD2TaskType].Input
	if !strings.Contains(in, "phase: Phase 9\nsession window:") {
		t.Errorf("an unreadable doc must leave the name only:\n%s", in)
	}

	// A whole-plan run: named by its task, goal from the plan README's Objective.
	seedSession(t, db, "s-plan", "2026-09-20T11:00:00.000Z", "")
	mustExec(t, db, `UPDATE epic_phases SET workspace_task_id = 77 WHERE run_session_uuid = 's-phase'`)
	seedPlanRun(t, db, "s-plan", "Parser rewrite")
	plan := d2QuestionsFor(t, db, "s-plan", false)[QD2TaskType]
	if slices.Contains(plan.Opts, "planning") || !strings.Contains(plan.Input, "plan: Parser rewrite\nplan goal: Ship the new parser.\n") {
		t.Errorf("plan run: opts %v, digest:\n%s", plan.Opts, plan.Input)
	}

	// A run no row links any more — a phase keeps only its LATEST run's session —
	// is still recognised by the engine's own prompt, which also carries the doc.
	phasePrompt := PhaseRunPromptHead + ", headlessly, in an isolated git worktree.\n\n- Follow it exactly.\n\n" +
		"PHASE DOCUMENT (plan/phase-3-cache.md):\n----------------------------------------\n" +
		"# Phase 3 — Cache\n\nStatus: Pending\n\n## Goal\n\nAdd the read-through cache.\n----------------------------------------"
	seedEmptySession(t, db, "s-unlinked", "2026-09-20T11:00:00.000Z", "")
	seedTurn(t, db, "s-unlinked", 1, "user", "", "", phasePrompt)
	seedTurn(t, db, "s-unlinked", 2, "assistant", testModel, "end_turn", "PHASE DONE")
	qs = d2QuestionsFor(t, db, "s-unlinked", false)
	unlinked := qs[QD2TaskType]
	if slices.Contains(unlinked.Opts, "planning") || !strings.HasSuffix(unlinked.Prompt, "never planning.") ||
		!strings.Contains(unlinked.Input, "phase: Phase 3 — Cache\nphase goal: Add the read-through cache.\nsession window:") {
		t.Errorf("unlinked phase run: opts %v prompt %q digest:\n%s", unlinked.Opts, unlinked.Prompt, unlinked.Input)
	}
	// It is a run, so the one-shot rule stays out of it, end_turn or not.
	wantRule(t, qs, QD2Outcome, "", "")
	wantRule(t, qs, QD2Failure, "", "")

	planPrompt := PlanRunPromptHead + ", running HEADLESSLY.\n\nPLAN DIRECTORY: /ws/plan\n\n" +
		"PLAN README:\n----------------------------------------\n# Cache plan\n\n## Objective\n\nShip the cache.\n\n## Risks\n\nNone.\n----------------------------------------"
	seedEmptySession(t, db, "s-unlinked-plan", "2026-09-20T11:00:00.000Z", "")
	seedTurn(t, db, "s-unlinked-plan", 1, "user", "", "", planPrompt)
	seedTurn(t, db, "s-unlinked-plan", 2, "assistant", testModel, "end_turn", "PLAN DONE.")
	unlinkedPlan := d2QuestionsFor(t, db, "s-unlinked-plan", false)[QD2TaskType]
	if slices.Contains(unlinkedPlan.Opts, "planning") || !strings.Contains(unlinkedPlan.Input, "plan: Cache plan\nplan goal: Ship the cache.\nsession window:") {
		t.Errorf("unlinked plan run: opts %v digest:\n%s", unlinkedPlan.Opts, unlinkedPlan.Input)
	}

	// A linked run whose doc has moved (a plan archived since) takes its goal
	// from the prompt's copy; the linked name stays.
	seedEmptySession(t, db, "s-archived", "2026-09-20T11:00:00.000Z", "")
	seedTurn(t, db, "s-archived", 1, "user", "", "", phasePrompt)
	seedTurn(t, db, "s-archived", 2, "assistant", testModel, "", "PHASE DONE")
	seedPhase(t, db, "s-archived", "Phase 3 (linked name)", filepath.Join(dir, "moved-away.md"), "done", 3, 1)
	if in := d2QuestionsFor(t, db, "s-archived", false)[QD2TaskType].Input; !strings.Contains(in, "phase: Phase 3 (linked name)\nphase goal: Add the read-through cache.\n") {
		t.Errorf("an archived doc must fall back to the prompt's copy:\n%s", in)
	}

	// A prompt that merely QUOTES the engine's opening further down is no run.
	seedEmptySession(t, db, "s-quote", "2026-09-20T11:00:00.000Z", "")
	seedTurn(t, db, "s-quote", 1, "user", "", "", "Why does the prompt say \""+PhaseRunPromptHead+"\"?")
	seedTurn(t, db, "s-quote", 2, "assistant", testModel, "", "Because the engine sends it.")
	if q := d2QuestionsFor(t, db, "s-quote", false)[QD2TaskType]; !slices.Contains(q.Opts, "planning") || strings.Contains(q.Input, "phase:") {
		t.Errorf("a session quoting the engine prompt was taken for a run: %v\n%s", q.Opts, q.Input)
	}

	// Any other session keeps `planning` and the plain prompt.
	seedSession(t, db, "s-plain", "2026-09-20T11:00:00.000Z", "")
	plain := d2QuestionsFor(t, db, "s-plain", false)[QD2TaskType]
	if !slices.Equal(plain.Opts, TaskTypes) || plain.Prompt != d2TaskTypePrompt || strings.Contains(plain.Input, "phase:") {
		t.Errorf("a plain session: opts %v prompt %q", plain.Opts, plain.Prompt)
	}
	if !slices.Contains(OptionsFor(QD2TaskType), "planning") {
		t.Error("ground truth must still accept `planning`")
	}
}

// R5 (a phase or plan run is task type feature) is off unless switched on.
func TestRuleR5PhaseRunFeatureIsOffByDefault(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-phase", "2026-09-20T11:00:00.000Z", "")
	seedPhase(t, db, "s-phase", "Phase 1", "", "blocked", 3, 1)
	seedSession(t, db, "s-plan", "2026-09-20T11:00:00.000Z", "")
	seedPlanRun(t, db, "s-plan", "Parser rewrite")
	seedSession(t, db, "s-plain", "2026-09-20T11:00:00.000Z", "")

	for _, uuid := range []string{"s-phase", "s-plan", "s-plain"} {
		wantRule(t, d2QuestionsFor(t, db, uuid, false), QD2TaskType, "", "")
	}
	for _, uuid := range []string{"s-phase", "s-plan"} {
		wantRule(t, d2QuestionsFor(t, db, uuid, true), QD2TaskType, "feature", RulePhaseRunFeature)
	}
	wantRule(t, d2QuestionsFor(t, db, "s-plain", true), QD2TaskType, "", "")

	// The switch is the engine's, read from SWARMERY_DECIDE_R5, and reaches both
	// the labeler and the eval through it.
	cfg, warn := ConfigFromEnv(func(string) string { return "" })
	if cfg.R5PhaseRunFeature || New(nil, cfg).R5PhaseRunFeature || len(warn) != 0 {
		t.Errorf("R5 must default to off: %+v %v", cfg, warn)
	}
	cfg, warn = ConfigFromEnv(func(k string) string { return map[string]string{"SWARMERY_DECIDE_R5": "on"}[k] })
	if !cfg.R5PhaseRunFeature || !New(nil, cfg).R5PhaseRunFeature || len(warn) != 0 || !strings.Contains(cfg.String(), " r5=on") {
		t.Errorf("SWARMERY_DECIDE_R5=on: %+v %v", cfg, warn)
	}
	if cfg, warn = ConfigFromEnv(func(k string) string { return map[string]string{"SWARMERY_DECIDE_R5": "maybe"}[k] }); cfg.R5PhaseRunFeature || len(warn) != 1 {
		t.Errorf("an unknown SWARMERY_DECIDE_R5 value must warn and stay off: %+v %v", cfg, warn)
	}
	seedTruth(t, db, QD2TaskType, "s-phase", "planning", "feature", "2026-09-29T10:00:00Z")
	for on, wantAgree := range map[bool]int{false: 0, true: 1} {
		rep, err := Eval(context.Background(), db, &Engine{R5PhaseRunFeature: on}, EvalOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if q := evalQ(t, rep, QD2TaskType); q.Agree != wantAgree {
			t.Errorf("eval with R5=%v: agree %d, want %d", on, q.Agree, wantAgree)
		}
	}
}

// A facts load that fails leaves only the operator's verdict: no rule fires on
// a half-read session.
func TestRuleFactsErrorFiresNoRule(t *testing.T) {
	db := openDB(t)
	seedOneShot(t, db, "s-qa")
	db.Close()
	f := d2FactsFor(db, "s-qa")
	if f.ok {
		t.Fatal("facts loaded from a closed database")
	}
	if got := d2Rules(f, "", true); got != (d2RuleSet{}) {
		t.Errorf("rules on unloaded facts = %+v, want none", got)
	}
	if got := d2Rules(f, "abandoned", true); got != (d2RuleSet{outcome: d2RuleAnswer{"abandoned", RuleOperatorVerdict}}) {
		t.Errorf("rules on unloaded facts with a verdict = %+v, want R0 only", got)
	}
}

// Per-question thresholds override the family's, a bad entry is ignored with
// one warning, and NeverThreshold switches a question's model leg off while a
// rule's answer still labels.
func TestQuestionThresholds(t *testing.T) {
	e := &Engine{Thresholds: map[string]float64{"d2": 0.6},
		QuestionThresholds: map[string]float64{QD2Outcome: 0.95}}
	if e.Threshold(QD2Outcome) != 0.95 || e.Threshold(QD2TaskType) != 0.6 || e.Threshold(QD1) != DefaultThreshold {
		t.Errorf("thresholds: outcome %.2f task %.2f d1 %.2f", e.Threshold(QD2Outcome), e.Threshold(QD2TaskType), e.Threshold(QD1))
	}

	env := map[string]string{"SWARMERY_DECIDE_THRESHOLDS": " d2.outcome=0.95 , d2.task_type=0.8,d2.failure_cause=1.01,, "}
	cfg, warn := ConfigFromEnv(func(k string) string { return env[k] })
	want := map[string]float64{QD2Outcome: 0.95, QD2TaskType: 0.8, QD2Failure: NeverThreshold}
	if len(warn) != 0 || len(cfg.QuestionThresholds) != len(want) {
		t.Fatalf("thresholds = %v warn = %v, want %v", cfg.QuestionThresholds, warn, want)
	}
	for id, v := range want {
		if cfg.QuestionThresholds[id] != v {
			t.Errorf("%s = %v, want %v", id, cfg.QuestionThresholds[id], v)
		}
	}
	eng := New(nil, cfg)
	if eng.Threshold(QD2Outcome) != 0.95 || eng.Threshold(QD2TaskType) != 0.8 || eng.Threshold(QD1) != 0.85 {
		t.Errorf("engine thresholds: %v %v %v", eng.Threshold(QD2Outcome), eng.Threshold(QD2TaskType), eng.Threshold(QD1))
	}
	if s := cfg.String(); !strings.Contains(s, "thresholds=d2.failure_cause=1.01,d2.outcome=0.95,d2.task_type=0.80") {
		t.Errorf("startup line does not list the thresholds: %q", s)
	}

	// Bad entries: unknown question, no `=`, not a number, out of range. One
	// warning names them all; the good entry still applies.
	env["SWARMERY_DECIDE_THRESHOLDS"] = "d2.nope=0.9,d2.outcome,d2.task_type=high,d2.failure_cause=0,d1.run_end=2,d2.outcome=0.7"
	cfg, warn = ConfigFromEnv(func(k string) string { return env[k] })
	if len(cfg.QuestionThresholds) != 1 || cfg.QuestionThresholds[QD2Outcome] != 0.7 {
		t.Errorf("thresholds = %v, want only d2.outcome=0.7", cfg.QuestionThresholds)
	}
	if len(warn) != 1 {
		t.Fatalf("warnings = %q, want exactly one", warn)
	}
	for _, bad := range []string{`"d2.nope=0.9"`, `"d2.outcome"`, `"d2.task_type=high"`, `"d2.failure_cause=0"`, `"d1.run_end=2"`} {
		if !strings.Contains(warn[0], bad) {
			t.Errorf("warning %q does not name %s", warn[0], bad)
		}
	}
	if cfg, warn = ConfigFromEnv(func(string) string { return "" }); cfg.QuestionThresholds != nil || len(warn) != 0 {
		t.Errorf("unset: %v %v", cfg.QuestionThresholds, warn)
	}

	// Active mode: the model answers every question at confidence 1. Outcome's
	// floor is NeverThreshold so its model answer is `unknown`; task type's own
	// floor (0.8) is cleared; the second session's outcome is a RULE answer and
	// labels whatever the floor.
	db := openDB(t)
	seedSession(t, db, "s-model", "2026-09-20T11:00:00.000Z", "")
	seedOneShot(t, db, "s-rule")
	s := &stub{name: BackendLocal, byQ: map[string]Answer{
		QD2TaskType: {Value: "bugfix", Confidence: 0.85, Calibrated: true},
		QD2Outcome:  {Value: "partial", Confidence: 1, Calibrated: true},
		QD2Failure:  {Value: "other", Confidence: 0.85, Calibrated: true},
	}}
	active := &Engine{DB: db, Local: s, Now: func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) },
		DefaultModes:       map[string]Mode{"d2": ModeActive},
		Thresholds:         map[string]float64{"d2": 0.9},
		QuestionThresholds: map[string]float64{QD2Outcome: NeverThreshold, QD2TaskType: 0.8}}
	if n, err := (&Labeler{E: active}).Run(context.Background()); err != nil || n != 2 {
		t.Fatalf("run: %d %v", n, err)
	}
	if l, err := LabelFor(db, "s-model"); err != nil || l == nil || l.TaskType != "bugfix" || l.Outcome != LabelUnknown || l.FailureCause != LabelUnknown {
		t.Errorf("s-model labels = %+v (%v), want bugfix / unknown / unknown", l, err)
	}
	if l, err := LabelFor(db, "s-rule"); err != nil || l == nil || l.Outcome != "shipped" || l.FailureCause != "none" {
		t.Errorf("s-rule labels = %+v (%v), want the rules' shipped / none past a NeverThreshold floor", l, err)
	}
}

// The labelling queue leaves rule-answered decisions out — nothing in them
// needs the operator — unless the caller asks for them.
func TestLabelQueueHidesRuleAnswers(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-1", "2026-09-20T11:00:00.000Z", "")
	for _, row := range []struct{ q, answer, backend string }{
		{QD2TaskType, "docs", BackendLocal},   // id 1: listed
		{QD2Outcome, "shipped", BackendRules}, // id 2: a rule's answer — hidden
		{QD2Failure, "none", BackendRules},    // id 3: hidden
		{QD2Failure, "other", BackendClaude},  // id 4: listed
	} {
		mustExec(t, db, `INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, calibrated, backend, created_at)
			VALUES (?, 's-1', 's-1', 'h', ?, 1, 1, ?, '2026-09-24T18:00:00Z')`, row.q, row.answer, row.backend)
	}
	ids := func(includeRules bool, projectID int64) []int64 {
		t.Helper()
		items, err := LabelQueue(db, QueueOptions{Limit: -1, ProjectID: projectID, IncludeRules: includeRules})
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, it := range items {
			out = append(out, it.ID)
		}
		return out
	}
	if got := ids(false, 0); !slices.Equal(got, []int64{4, 1}) {
		t.Errorf("queue = %v, want [4 1] (the two rule answers hidden)", got)
	}
	if got := ids(true, 0); !slices.Equal(got, []int64{4, 3, 2, 1}) {
		t.Errorf("queue with rules = %v, want all four", got)
	}
	if got := ids(false, 1); !slices.Equal(got, []int64{4, 1}) {
		t.Errorf("project queue = %v, want [4 1]", got)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&n); err != nil || n != 4 {
		t.Errorf("decisions = %d (%v), want all 4 kept — hidden, not deleted", n, err)
	}

	// What the labeler records for a rule-answered session stays out of the
	// queue end to end.
	seedOneShot(t, db, "s-rule")
	e := &Engine{DB: db, Local: &stub{name: BackendLocal, a: Answer{Value: "docs", Confidence: 0.9, Calibrated: true}},
		Now: func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }}
	mustExec(t, db, `DELETE FROM decisions`)
	if _, err := (&Labeler{E: e}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := LabelQueue(db, QueueOptions{Limit: -1})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.SessionUUID == "s-rule" && it.QuestionID != QD2TaskType {
			t.Errorf("a rule-answered decision is queued for the operator: %+v", it)
		}
	}
}

// The eval reports each rule on its own: how many questions it answered
// (coverage over the replayed) and how many of those agreed (precision).
func TestEvalPerRuleTable(t *testing.T) {
	db := openDB(t)
	seedOneShot(t, db, "s-qa-1")                                                                      // R4, agrees
	seedOneShot(t, db, "s-qa-2")                                                                      // R4, truth says partial
	seedEnding(t, db, "s-auth", syntheticModel, "stop_sequence", "Not logged in · Please run /login") // R1
	seedSession(t, db, "s-verdict", "2026-09-20T11:00:00.000Z", "abandoned")                          // R0
	seedSession(t, db, "s-model", "2026-09-20T11:00:00.000Z", "")                                     // no rule
	truths := map[string][2]string{                                                                   // outcome, failure_cause
		"s-qa-1":    {"shipped", "none"},
		"s-qa-2":    {"partial", "none"},
		"s-auth":    {"failed", "other"}, // auth agrees with other through the parent rule
		"s-verdict": {"abandoned", "blocked-on-operator"},
		"s-model":   {"failed", "timeout"},
	}
	for sess, tr := range truths {
		seedTruth(t, db, QD2Outcome, sess, "partial", tr[0], "2026-09-29T10:00:00Z")
		seedTruth(t, db, QD2Failure, sess, "other", tr[1], "2026-09-29T10:00:00Z")
	}
	rep, err := Eval(context.Background(), db, nil, EvalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rule := func(q EvalQuestion, id string) EvalRule {
		t.Helper()
		for _, r := range q.Rules {
			if r.Rule == id {
				return r
			}
		}
		t.Fatalf("%s has no %s row: %+v", q.QuestionID, id, q.Rules)
		return EvalRule{}
	}
	out := evalQ(t, rep, QD2Outcome)
	if len(out.Rules) != 3 || out.Rules[0].Rule != RuleOperatorVerdict || out.Rules[2].Rule != RuleOneShotShipped {
		t.Fatalf("outcome rules = %+v, want R0, R1, R4 in id order", out.Rules)
	}
	if r := rule(out, RuleOneShotShipped); r.N != 2 || r.Agree != 1 || *r.Precision != 0.5 || *r.Coverage != 0.4 {
		t.Errorf("outcome R4 = %+v, want 2 answered of 5 replayed, 1 agreeing", r)
	}
	if r := rule(out, RuleSyntheticFailure); r.N != 1 || r.Agree != 1 || *r.Precision != 1 {
		t.Errorf("outcome R1 = %+v", r)
	}
	if b := evalBackend(t, out, BackendRules); b.N != 4 || b.Agree != 3 {
		t.Errorf("rules backend = %+v, want the per-rule rows to sum to it (4 answered, 3 agree)", b)
	}
	fail := evalQ(t, rep, QD2Failure)
	if len(fail.Rules) != 2 {
		t.Fatalf("failure_cause rules = %+v, want R1 and R4 (the verdict answers no cause)", fail.Rules)
	}
	if r := rule(fail, RuleSyntheticFailure); r.N != 1 || r.Agree != 1 {
		t.Errorf("failure_cause R1 = %+v, want auth agreeing with the `other` label", r)
	}
	if r := rule(fail, RuleOneShotShipped); r.N != 2 || r.Agree != 2 {
		t.Errorf("failure_cause R4 = %+v", r)
	}
	if task := evalQ(t, rep, QD2TaskType); len(task.Rules) != 0 || task.Rules == nil {
		t.Errorf("task_type rules = %#v, want an empty list", task.Rules)
	}

	var text bytes.Buffer
	if err := RenderEval(&text, rep, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  rule             n  agree precision  coverage",
		"  R0               1      1    100.0%     20.0%",
		"  R4               2      1     50.0%     40.0%",
	} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text report lacks %q:\n%s", want, text.String())
		}
	}
	var js bytes.Buffer
	if err := RenderEval(&js, rep, true); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"rules"`, `"rule": "R4"`, `"coverage"`} {
		if !strings.Contains(js.String(), key) {
			t.Errorf("JSON report lacks %s", key)
		}
	}
}

// The ending lines are part of the ship-evidence block, ahead of the verdict.
func TestShipEvidenceEndingLines(t *testing.T) {
	db := openDB(t)
	seedEnding(t, db, "s-fail", syntheticModel, "stop_sequence", "API Error: 529 Overloaded")
	got := shipEvidence(db, d2FactsFor(db, "s-fail"), "s-fail", "fail")
	want := "turns: 2\nfinal answer: no\nlast stop reason: stop_sequence\napi error: yes\noperator verdict: fail\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("evidence =\n%s\nwant it to end with\n%s", got, want)
	}
}

// A model call that fails must not cost a session the answers its rules
// already have. Task type is asked first; for a phase run `planning` is off the
// option list, so a model that still says it yields an errored call — and the
// all-ticked phase's outcome and cause are recorded by the rules all the same,
// once, however many passes retry the session.
func TestRuleAnswersSurviveModelFailure(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	seed := func(t *testing.T) (*sql.DB, *stub, *Engine) {
		t.Helper()
		db := openDB(t)
		seedSession(t, db, "s-phase", "2026-09-20T11:00:00.000Z", "")
		seedPhase(t, db, "s-phase", "Phase 1 — parser", "", "done", 3, 3)
		s := &stub{name: BackendLocal, a: Answer{Value: "planning", Confidence: 0.99, Calibrated: true}}
		return db, s, &Engine{DB: db, Local: s, Now: now, DefaultModes: map[string]Mode{"d2": ModeActive}}
	}
	// tally counts the session's rows: errored task-type calls, answered
	// task-type calls, and the rules' outcome / cause rows.
	tally := func(t *testing.T, db *sql.DB) (taskErr, taskOK, outcome, cause int) {
		t.Helper()
		for _, r := range decisionRows(t, db) {
			switch {
			case r["q"] == QD2TaskType && r["error"] != "":
				if r["backend"] != BackendLocal || !strings.Contains(r["error"].(string), "is not one of") {
					t.Errorf("task-type failure row = %v, want the local backend's off-list error", r)
				}
				taskErr++
			case r["q"] == QD2TaskType:
				taskOK++
			case r["q"] == QD2Outcome && r["answer"] == "shipped" && r["backend"] == BackendRules && r["error"] == "":
				outcome++
			case r["q"] == QD2Failure && r["answer"] == "none" && r["backend"] == BackendRules && r["error"] == "":
				cause++
			default:
				t.Errorf("unexpected decision row: %v", r)
			}
		}
		return
	}

	t.Run("the model never recovers", func(t *testing.T) {
		db, s, e := seed(t)
		for pass := 1; pass <= d2MaxFailures; pass++ {
			if n, err := (&Labeler{E: e}).Run(context.Background()); err != nil || n != 1 {
				t.Fatalf("pass %d: labelled %d (%v), want the session retried", pass, n, err)
			}
			// The failure is counted as before — one errored row per pass — and the
			// rule-answered pair is there from the first pass, never duplicated.
			if taskErr, taskOK, outcome, cause := tally(t, db); taskErr != pass || taskOK != 0 || outcome != 1 || cause != 1 {
				t.Fatalf("pass %d: %d errored and %d answered task-type rows, %d outcome and %d cause rule rows; want %d/0/1/1",
					pass, taskErr, taskOK, outcome, cause, pass)
			}
		}
		// Given up after d2MaxFailures errored calls, exactly as before — with its
		// rule answers on record.
		if n, _ := (&Labeler{E: e}).Run(context.Background()); n != 0 {
			t.Errorf("after %d failures the session is still asked about (%d)", d2MaxFailures, n)
		}
		if s.calls != d2MaxFailures {
			t.Errorf("model calls = %d, want %d (task type once per pass; the rules need no backend)", s.calls, d2MaxFailures)
		}
		// A failed pass still writes no session label.
		if l, _ := LabelFor(db, "s-phase"); l != nil {
			t.Errorf("a failed pass wrote labels: %+v", l)
		}
	})

	t.Run("the model recovers on the next pass", func(t *testing.T) {
		db, s, e := seed(t)
		if n, err := (&Labeler{E: e}).Run(context.Background()); err != nil || n != 1 {
			t.Fatalf("pass 1: %d %v", n, err)
		}
		s.a = Answer{Value: "feature", Confidence: 0.99, Calibrated: true}
		if n, err := (&Labeler{E: e}).Run(context.Background()); err != nil || n != 1 {
			t.Fatalf("pass 2: %d %v", n, err)
		}
		// Only the task type was asked again; the stored rule answers stand and
		// feed the label.
		if taskErr, taskOK, outcome, cause := tally(t, db); taskErr != 1 || taskOK != 1 || outcome != 1 || cause != 1 {
			t.Errorf("rows: %d errored, %d answered task type, %d outcome, %d cause; want 1/1/1/1", taskErr, taskOK, outcome, cause)
		}
		if s.calls != 2 {
			t.Errorf("model calls = %d, want 2", s.calls)
		}
		if l, err := LabelFor(db, "s-phase"); err != nil || l == nil || l.TaskType != "feature" || l.Outcome != "shipped" || l.FailureCause != "none" {
			t.Errorf("labels = %+v (%v), want feature / shipped / none", l, err)
		}
		if n, _ := (&Labeler{E: e}).Run(context.Background()); n != 0 {
			t.Errorf("a fully answered session was asked again (%d)", n)
		}
	})

	// The same holds for a question in the middle: outcome fails at the model,
	// the cause a rule knows (R2) is still recorded, and nothing model-bound
	// after the failure is asked.
	t.Run("a rule answer after a failed model question", func(t *testing.T) {
		db := openDB(t)
		seedEnding(t, db, "s-stall", testModel, "", "The permission check stopped returning verdicts.")
		seedToolCall(t, db, "s-stall", "Bash", "error", "2026-09-20T10:25:00.000Z", autoModeDenial)
		b := &failOn{bad: map[string]bool{QD2Outcome: true}}
		e := &Engine{DB: db, Local: b, Now: now}
		if n, err := (&Labeler{E: e}).Run(context.Background()); err != nil || n != 1 {
			t.Fatalf("run: %d %v", n, err)
		}
		got := map[string]string{}
		for _, r := range decisionRows(t, db) {
			got[r["q"].(string)] = fmt.Sprintf("%v/%v/err=%v", r["answer"], r["backend"], r["error"] != "")
		}
		want := map[string]string{QD2TaskType: "bugfix/local/err=false", QD2Outcome: "/local/err=true", QD2Failure: "api-error/rules/err=false"}
		for q, w := range want {
			if got[q] != w {
				t.Errorf("%s = %q, want %q (all rows: %v)", q, got[q], w, got)
			}
		}
	})
}
