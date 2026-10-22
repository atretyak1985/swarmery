package phaserun

import (
	"encoding/json"
	"errors"
	"flag"
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

var updateReviewGolden = flag.Bool("update-review-golden", false, "rewrite testdata/review_prompt_guard.golden")

// guardedDoc is a phase doc with a guard over tests and one criterion.
const guardedDoc = "# Phase 1 — Schema\nStatus: Pending\n\n## Acceptance Criteria\n\n- [ ] the schema lands\n\n## Guard\n\n" +
	"```yaml\nkind: guard\nprotected_paths:\n  - \"**/*_test.go\"\nstop_if:\n  - protected path\n```\n\n## Completion Report\n"

// guardedRun runs phase p1 of the fixture in a REAL git worktree whose executor
// ticks its criterion, edits a protected test (untracked) and — like the hook did
// for it — leaves one refusal in guard.log, then exits 0: a run that would report
// PHASE DONE.
func guardedRun(t *testing.T) (s *Service, taskID, p1 int64, docPath, wt string) {
	t.Helper()
	db, taskID, p1, _ := fixture(t)
	if err := db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, p1).Scan(&docPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docPath, []byte(guardedDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE epic_phases SET checkboxes_total = 1 WHERE id = ?`, p1)
	wt, base := gitRepo(t)
	r := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		lent := filepath.Join(wt, worktree.LentPlanDocRel(docPath))
		b, _ := os.ReadFile(lent)
		_ = os.WriteFile(lent, []byte(strings.ReplaceAll(string(b), "- [ ]", "- [x]")), 0o644)
		_ = os.WriteFile(filepath.Join(wt, "schema_test.go"), []byte("package main\n"), 0o644)
		_ = os.WriteFile(filepath.Join(wt, GuardLogRel),
			[]byte("2026-10-22T18:00:00Z\tEdit\tmain_test.go\tprotected_paths \"**/*_test.go\"\n"), 0o644)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s = newTestService(db, r, &stubWt{pathOverride: wt, startPoint: base})
	s.Git = worktree.ExecGit{}
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var state string
	waitFor(t, func() bool {
		db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&state)
		return state == "done"
	})
	return s, taskID, p1, docPath, wt
}

// A run that touched protected paths raises a source=stop decision even though it
// ended done — the needs-you phase_decision item is read from this row.
func TestGuardDecisionRaisedEvenWhenTheRunEndedDone(t *testing.T) {
	s, taskID, p1, _, _ := guardedRun(t)
	ds, err := store.ListTaskDecisions(s.DB, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("decisions = %+v, want one", ds)
	}
	d := ds[0]
	if d.Source != store.DecisionSourceStop || d.Status != store.DecisionUndecided || d.PhaseID == nil || *d.PhaseID != p1 {
		t.Fatalf("decision = %+v", d)
	}
	if d.DecisionID != "D-1" || !reflect.DeepEqual(d.OptionIDs, GuardOptions) {
		t.Fatalf("decision id/options = %s %q", d.DecisionID, d.OptionIDs)
	}
	const q = "The run touched protected paths: schema_test.go, main_test.go. Allow (add to scope) or reject?"
	if d.Question != q {
		t.Fatalf("question = %q, want %q", d.Question, q)
	}
	var g GuardResult
	if err := json.Unmarshal([]byte(d.GuardJSON), &g); err != nil {
		t.Fatalf("guard_json %q: %v", d.GuardJSON, err)
	}
	if !reflect.DeepEqual(g.ProtectedChanged, []string{"schema_test.go"}) || g.GuardBlocksTotal != 1 || g.DocCopyMismatch {
		t.Fatalf("guard = %+v", g)
	}

	// The same guard hit again refreshes the open decision instead of minting D-2.
	if _, created, err := store.UpsertStopDecision(s.DB, taskID, p1, q, GuardOptions, d.GuardJSON, "2026-10-22T19:00:00Z"); err != nil || created {
		t.Fatalf("second upsert created=%v err=%v", created, err)
	}
	if ds, _ := store.ListTaskDecisions(s.DB, taskID); len(ds) != 1 {
		t.Fatalf("decisions after a repeat = %d, want 1", len(ds))
	}
}

// A clean run raises nothing.
func TestGuardDecisionNotRaisedForACleanRun(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	var docPath string
	_ = db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, p1).Scan(&docPath)
	_ = os.WriteFile(docPath, []byte(guardedDoc), 0o644)
	wt, base := gitRepo(t)
	s := newTestService(db, &stubRunner{}, &stubWt{pathOverride: wt, startPoint: base})
	s.Git = worktree.ExecGit{}
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !s.InFlight(p1) })
	if ds, _ := store.ListTaskDecisions(db, taskID); len(ds) != 0 {
		t.Fatalf("a clean run raised %+v", ds)
	}
}

func TestAnswerGuardDecisionAllowWidensTheGuard(t *testing.T) {
	s, taskID, p1, docPath, _ := guardedRun(t)
	before := readFileT(t, docPath)

	d, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: GuardOptionAllow, Note: "the schema\ntest is new"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != store.DecisionDecided || d.DecidedAt == nil ||
		d.Answer != "allow (add to scope): schema_test.go, main_test.go — the schema test is new" {
		t.Fatalf("decided row = %+v", d)
	}
	after := readFileT(t, docPath)
	wantLine := "- **D-1** (stop) — The run touched protected paths: schema_test.go, main_test.go. Allow (add to scope) or reject? → " + d.Answer
	if !strings.Contains(after, "## Decisions\n\n"+wantLine+"\n") {
		t.Fatalf("doc lacks the decision line:\n%s", after)
	}
	g, perr := mdfence.ParseGuard(after)
	if perr != nil {
		t.Fatal(perr)
	}
	if !reflect.DeepEqual(g.Allow, []string{"schema_test.go", "main_test.go"}) {
		t.Fatalf("allow = %q", g.Allow)
	}
	// The blocked re-run guard sees the answer as a change.
	if blockedFingerprint("b", 1, nil, before) == blockedFingerprint("b", 1, nil, after) {
		t.Fatal("an answered guard decision left the blocked fingerprint unchanged")
	}
	// Answered once is answered.
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: GuardOptionReject}, time.Now()); !errors.Is(err, ErrDecisionNotOpen) {
		t.Fatalf("second answer: %v, want ErrDecisionNotOpen", err)
	}
}

func TestAnswerGuardDecisionReject(t *testing.T) {
	s, taskID, p1, docPath, _ := guardedRun(t)
	before := readFileT(t, docPath)
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: "maybe"}, time.Now()); !errors.Is(err, ErrDecisionBadOption) {
		t.Fatalf("unknown option: %v", err)
	}
	if _, err := AnswerGuardDecision(s.DB, taskID, p1+1, "D-1", GuardAnswer{Option: GuardOptionReject}, time.Now()); !errors.Is(err, ErrDecisionNotFound) {
		t.Fatalf("another phase's decision: %v", err)
	}
	if _, err := AnswerGuardDecision(s.DB, taskID, p1, "D-9", GuardAnswer{Option: GuardOptionReject}, time.Now()); !errors.Is(err, ErrDecisionNotFound) {
		t.Fatalf("unknown decision: %v", err)
	}
	d, err := AnswerGuardDecision(s.DB, taskID, p1, "D-1", GuardAnswer{Option: GuardOptionReject}, time.Now())
	if err != nil || d.Answer != "reject" {
		t.Fatalf("reject = %+v, %v", d, err)
	}
	after := readFileT(t, docPath)
	if !strings.Contains(after, "→ reject\n") || strings.Contains(after, "allow:") {
		t.Fatalf("reject doc:\n%s", after)
	}
	if blockedFingerprint("b", 1, nil, before) == blockedFingerprint("b", 1, nil, after) {
		t.Fatal("a rejected guard decision left the blocked fingerprint unchanged")
	}
}

// The reviewer reads the guard check FIRST, and the review row carries it.
func TestReviewPromptStartsWithTheGuardSummary(t *testing.T) {
	s, db, p1, info := reviewFixture(t)
	dir, base := gitRepo(t)
	rv := &fakeReviewer{outputs: []string{"- fine\nVERDICT: PASS"}}
	s.Review = rv
	guard := GuardResult{HasGuard: true, ProtectedPaths: []string{"**/*_test.go"}, Allow: []string{},
		ProtectedChanged: []string{"a_test.go"}, GuardBlocks: []GuardLogLine{}, Base: base}
	if v, _ := s.reviewRun(p1, info, worktree.Acquired{Path: dir, StartPoint: base}, "done", guard); v != "pass" {
		t.Fatalf("verdict = %q", v)
	}
	prompt := rv.calls()[0].Prompt
	if !strings.HasPrefix(prompt, "GUARD CHECK (independent of the run's own report — verify each item against the diff):\n"+
		"The run changed protected paths: a_test.go.\n\n"+reviewerRole) {
		t.Fatalf("prompt does not open with the guard summary:\n%.400s", prompt)
	}
	var stored string
	if err := db.QueryRow(`SELECT guard_json FROM phase_reviews WHERE phase_id = ?`, p1).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var back GuardResult
	if err := json.Unmarshal([]byte(stored), &back); err != nil || !reflect.DeepEqual(back.ProtectedChanged, []string{"a_test.go"}) {
		t.Fatalf("guard_json = %s (%v)", stored, err)
	}

	// No findings: the prompt opens with the role, unchanged.
	if v, _ := s.reviewRun(p1, info, worktree.Acquired{Path: dir, StartPoint: base}, "done", GuardResult{}); v != "pass" {
		t.Fatalf("verdict = %q", v)
	}
	if p := rv.calls()[1].Prompt; !strings.HasPrefix(p, reviewerRole) {
		t.Fatalf("a clean guard changed the prompt head:\n%.200s", p)
	}
}

// The prompt's head, recorded: what the reviewer reads before anything else.
// Regenerate with `go test ./internal/phaserun -run TestReviewPromptGuardGolden -update-review-golden`.
func TestReviewPromptGuardGolden(t *testing.T) {
	const file = "testdata/review_prompt_guard.golden"
	guard := GuardResult{
		ProtectedChanged: []string{"pkg/a_test.go", "trap_test.go"},
		GuardBlocks:      []GuardLogLine{{At: "t", Tool: "Edit", Path: "trap_test.go", Reason: "r"}},
		GuardBlocksTotal: 1,
		DocCopyMismatch:  true,
	}
	got := reviewPrompt(guard.Summary(), "Phase 1 — Trap", "# Phase 1 — Trap\n", "DIFF: none", 7777, 1)
	got = got[:strings.Index(got, reviewerRole)+len("You are the fleet's independent code reviewer")]
	if *updateReviewGolden {
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read golden: %v — regenerate with -update-review-golden", err)
	}
	if string(want) != got {
		t.Fatalf("review prompt head changed:\n--- golden\n%s\n--- got\n%s", want, got)
	}
}

// A plan revision rewrote a doc: a worktree still holding its copy gets the new
// text, and a phase without a lent copy gets nothing planted.
func TestRelendDocsRefreshesALeftoverCopy(t *testing.T) {
	db, _, p1, p2 := fixture(t)
	var doc1, doc2 string
	_ = db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, p1).Scan(&doc1)
	_ = db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, p2).Scan(&doc2)
	root := t.TempDir()
	wt := &stubWt{root: root}
	s := newTestService(db, &stubRunner{}, wt)
	wt1, _ := wt.Path("p", runcore.PhaseTaskName(p1))
	if _, err := worktree.LendPlanDoc(wt1, doc1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc1, []byte("# Phase 1 — Schema (revised)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := s.RelendDocs([]string{doc1, doc2}); n != 1 {
		t.Fatalf("RelendDocs = %d, want 1", n)
	}
	copyHash, err := worktree.DocCopyHash(wt1, doc1)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := worktree.FileHash(doc1); copyHash != want {
		t.Fatal("the lent copy still holds the pre-revision doc")
	}
	wt2, _ := wt.Path("p", runcore.PhaseTaskName(p2))
	if _, err := os.Stat(filepath.Join(wt2, worktree.PlanDocDir)); !os.IsNotExist(err) {
		t.Fatalf("a copy was planted for a phase that had none: %v", err)
	}
}
