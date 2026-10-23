package phaserun

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// stopDoc is a phase doc whose guard protects the test runner script and lists
// the planner's default stop triggers.
const stopDoc = "# Phase 1 — Schema\nStatus: Pending\n\n## Acceptance Criteria\n\n- [ ] a\n- [ ] b\n\n## Guard\n\n" +
	"```yaml\nkind: guard\nprotected_paths:\n  - \"scripts/tests/*.sh\"\nstop_if:\n  - out-of-scope change\n  - protected path\n" +
	"  - access or login error\n  - criterion impossible\n```\n\n## Completion Report\n"

// stopFinal is what the blocked executor's last message says.
const stopFinal = "I wired the schema but criterion b needs the runner script.\n\n" +
	"```yaml\nkind: stop\ntrigger: protected-path\nreason: criterion b needs scripts/tests/run.sh to call the new suite\n" +
	"paths:\n  - \"scripts/tests/run.sh\"\noptions:\n" +
	"  - id: allow\n    label: Allow editing scripts/tests/run.sh\n    consequence: the re-run wires the suite and ticks b\n" +
	"  - id: skip\n    label: Keep the script protected\n    consequence: b stays open\n```\n\nPHASE BLOCKED: protected-path"

// TestStopDecisionEndToEnd — real git, real worktree.Manager, the stub executor.
// Run 1 commits part of the work and stops on a protected path with a stop block:
// the run settles blocked and a source=stop decision is raised with the block's
// two options plus `other`. A plain re-run is refused (nothing changed). The
// operator allows: the doc gains the D-n line and the guard's allow: list. The
// re-run is admitted, continues on the phase's own branch (run 1's commit is in
// its worktree) and its prompt states the decision.
func TestStopDecisionEndToEnd(t *testing.T) {
	repo := newTempRepo(t)
	db, taskID, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)
	doc := phaseDocPath(t, db, p1)
	mustWriteDoc(t, doc, stopDoc)
	branch := runcore.PhaseBranch(p1)

	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	s.Wt = &worktree.Manager{Git: repo.git, Root: filepath.Join(t.TempDir(), "wts")}
	s.Git = repo.git
	// A fresh session per run, as in production: the re-run must not read run 1's
	// transcript.
	runs := 0
	s.UUID = func() string { runs++; return "uuid-run-" + string(rune('0'+runs)) }

	var firstCommit, headAtSpawn string
	r.runFn = func(spec RunSpec) (*Run, error) {
		firstCommit = repo.commitIn(spec.Cwd, "phase 1: schema, partial")
		seedTranscript(t, db, spec.SessionUUID, stopFinal)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if state, _, _, runErr := phaseRow(t, db, p1); state != "blocked" || runErr.String != "protected-path" {
		t.Fatalf("after run 1: %q / %q, want blocked / protected-path", state, runErr.String)
	}

	// The decision row: source stop, the block's reason as the question, its two
	// options plus the free-text one, and the block itself on the row.
	stops, err := store.ListPhaseAgentStops(db, taskID, p1)
	if err != nil || len(stops) != 1 {
		t.Fatalf("agent stops = %v, %v; want exactly one", stops, err)
	}
	d := stops[0]
	if d.Source != store.DecisionSourceStop || d.Status != store.DecisionUndecided {
		t.Errorf("decision = %s/%s, want stop/undecided", d.Source, d.Status)
	}
	if d.Question != "criterion b needs scripts/tests/run.sh to call the new suite" {
		t.Errorf("question = %q", d.Question)
	}
	if !reflect.DeepEqual(d.OptionIDs, []string{"allow", "skip", StopOptionOther}) {
		t.Errorf("options = %v", d.OptionIDs)
	}
	st := AgentStop(d)
	if st == nil || st.Trigger != mdfence.StopProtectedPath || len(st.Options) != 2 || len(st.Lints) != 0 {
		t.Fatalf("stored stop block = %+v", st)
	}
	// The guard decision of the same run (the guard check found nothing) is not
	// raised beside it.
	all, _ := store.ListTaskDecisions(db, taskID)
	if len(all) != 1 {
		t.Errorf("decisions = %d, want just the stop", len(all))
	}

	// Nothing has changed yet: the blocked guard refuses the re-run.
	if _, err := s.StartWith(p1, StartOptions{}); !errors.Is(err, ErrBlockedUnchanged) {
		t.Fatalf("unanswered re-run: err = %v, want ErrBlockedUnchanged", err)
	}

	// A bad option and an empty free text are refused and leave it open.
	if _, err := AnswerGuardDecision(db, taskID, p1, d.DecisionID, GuardAnswer{Option: "reject"}, time.Now()); !errors.Is(err, ErrDecisionBadOption) {
		t.Errorf("answer reject: err = %v, want ErrDecisionBadOption (a stop offers its own options)", err)
	}
	if _, err := AnswerGuardDecision(db, taskID, p1, d.DecisionID, GuardAnswer{Option: StopOptionOther}, time.Now()); !errors.Is(err, ErrDecisionBadOption) {
		t.Errorf("answer other without text: err = %v, want ErrDecisionBadOption", err)
	}

	answered, err := AnswerGuardDecision(db, taskID, p1, d.DecisionID,
		GuardAnswer{Option: "allow"}, time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if answered.Status != store.DecisionDecided || answered.Answer != "allow: Allow editing scripts/tests/run.sh" {
		t.Errorf("answered row = %s %q", answered.Status, answered.Answer)
	}
	body, _ := os.ReadFile(doc)
	wantLine := "- **" + d.DecisionID + "** (stop: protected-path) — criterion b needs scripts/tests/run.sh to call the new suite → allow: Allow editing scripts/tests/run.sh"
	if !strings.Contains(string(body), "## Decisions\n\n"+wantLine) {
		t.Errorf("doc lacks the decision line %q:\n%s", wantLine, body)
	}
	g, perr := mdfence.ParseGuard(string(body))
	if perr != nil || !reflect.DeepEqual(g.Allow, []string{"scripts/tests/run.sh"}) {
		t.Errorf("guard allow = %v (%v), want the stop's path", g.Allow, perr)
	}

	// The re-run: admitted, on its own branch, and told the decision.
	r.mu.Lock()
	r.runFn = func(spec RunSpec) (*Run, error) {
		headAtSpawn = strings.TrimSpace(repo.runIn(spec.Cwd, "rev-parse", "HEAD"))
		mustWriteDoc(t, filepath.Join(spec.Cwd, worktree.LentPlanDocRel(doc)), strings.ReplaceAll(string(body), "- [ ]", "- [x]"))
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	r.mu.Unlock()
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatalf("re-run after the answer: %v", err)
	}
	if headAtSpawn != firstCommit {
		t.Errorf("re-run HEAD at spawn = %s, want run 1's commit %s (own-branch continuation)", headAtSpawn, firstCommit)
	}
	if got := repo.tip("refs/heads/" + branch); got != firstCommit {
		t.Errorf("branch tip = %s, want %s kept", got, firstCommit)
	}
	last := r.lastSpec().Prompt
	if want := "- Decision " + d.DecisionID + ": allow: Allow editing scripts/tests/run.sh (stop: protected-path — "; !strings.Contains(last, want) {
		t.Errorf("re-run prompt lacks %q", want)
	}
	if !strings.Contains(last, "STOP TRIGGERS:") {
		t.Error("re-run prompt lacks the stop contract")
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
}

// Free text is recorded as option `other`, and a non-allow option never widens
// the guard.
func TestStopDecisionFreeTextAndNoAllow(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)
	mustWriteDoc(t, doc, stopDoc)
	r := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		seedTranscript(t, db, spec.SessionUUID, stopFinal)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatal(err)
	}
	stops, _ := store.ListPhaseAgentStops(db, taskID, p1)
	if len(stops) != 1 {
		t.Fatalf("agent stops = %d, want 1", len(stops))
	}
	d, err := AnswerGuardDecision(db, taskID, p1, stops[0].DecisionID,
		GuardAnswer{Option: StopOptionOther, Note: "  move the suite\ninto  Makefile  "}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.Answer != "other: move the suite into Makefile" {
		t.Errorf("answer = %q", d.Answer)
	}
	body, _ := os.ReadFile(doc)
	if !strings.Contains(string(body), "(stop: protected-path) — criterion b needs scripts/tests/run.sh to call the new suite → other: move the suite into Makefile") {
		t.Errorf("doc lacks the free-text line:\n%s", body)
	}
	if g, _ := mdfence.ParseGuard(string(body)); len(g.Allow) != 0 {
		t.Errorf("allow = %v, want none for a free-text answer", g.Allow)
	}
	// Answered once: a second answer is refused.
	if _, err := AnswerGuardDecision(db, taskID, p1, d.DecisionID, GuardAnswer{Option: "skip"}, time.Now()); !errors.Is(err, ErrDecisionNotOpen) {
		t.Errorf("second answer: err = %v, want ErrDecisionNotOpen", err)
	}
}

// A blocked run without a stop block raises nothing; a done run's stop block is
// ignored (only a blocked run asks); an agent stop and a guard decision on one
// phase are two rows that never refresh each other.
func TestStopDecisionOnlyFromABlockedRunAndApartFromGuard(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	s := newTestService(db, blockingRunner(t, db), &stubWt{})
	if _, err := s.StartWith(p1, StartOptions{}); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListTaskDecisions(db, taskID); len(all) != 0 {
		t.Errorf("a plain PHASE BLOCKED raised %d decisions, want 0", len(all))
	}

	gd, _, err := store.UpsertStopDecision(db, taskID, p1, "The run touched protected paths: x. Allow or reject?",
		GuardOptions, `{"protectedChanged":["x"]}`, "2026-07-28T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := mdfence.ParseStop(stopFinal)
	s.raiseStopDecision(p1, phaseInfo{WorkspaceTaskID: taskID}, st, nil)
	all, _ := store.ListTaskDecisions(db, taskID)
	if len(all) != 2 {
		t.Fatalf("decisions = %d, want the guard's and the stop's apart", len(all))
	}
	if got, _ := store.GetDecision(db, gd.ID); got.Question != gd.Question {
		t.Errorf("the agent stop overwrote the guard decision: %q", got.Question)
	}
	if AgentStop(all[0]) != nil || AgentStop(all[1]) == nil {
		t.Error("AgentStop does not tell the guard decision from the agent stop")
	}
	// Refreshing the guard decision leaves the agent stop alone.
	if _, created, _ := store.UpsertStopDecision(db, taskID, p1, "again", GuardOptions, `{}`, "2026-07-28T12:00:01Z"); created {
		t.Error("the guard upsert minted a new row instead of refreshing its own")
	}
	if got := AgentStop(all[1]); got == nil || got.Reason != st.Reason {
		t.Error("the guard refresh touched the agent stop")
	}
}
