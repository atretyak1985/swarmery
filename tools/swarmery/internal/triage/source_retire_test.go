package triage

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
)

// seedProposal inserts one lesson_retirements row and returns its id.
func seedProposal(t *testing.T, db *sql.DB, lessonID int64, state string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO lesson_retirements(lesson_id, reason, detail, evidence_json, state, proposed_at)
		VALUES(?, 'unused_60d', 'not injected in 60 days', '{"lastUsed":"2026-07-01"}', ?, '2026-10-02T09:00:00.000Z')`,
		lessonID, state)
	if err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func proposalState(t *testing.T, db *sql.DB, id int64) (state string, decided sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT state, decided_at FROM lesson_retirements WHERE id=?`, id).Scan(&state, &decided); err != nil {
		t.Fatal(err)
	}
	return state, decided
}

func TestRetireRunSuggestsOnceThenSweepsStale(t *testing.T) {
	j := &stubJudge{value: "stop"}
	s := newTestService(t, j)
	lid := seedLesson(t, s.DB, "Old lesson", "active", "")
	pid := seedProposal(t, s.DB, lid, "proposed")
	other := seedLesson(t, s.DB, "Kept lesson", "active", "")
	seedProposal(t, s.DB, other, "kept")
	spy := &applySpy{Source: RetireSource{DB: s.DB, Cfg: lessons.DefaultVerifyConfig()}}
	s.Register(spy)

	r := mustRun(t, s, StartReq{Kinds: []string{"retire"}})
	if r.Status != StatusOK || r.Suggested != 1 || r.Applied != 0 {
		t.Fatalf("run = %+v, want ok with 1 suggested, 0 applied", r)
	}
	v := onlyVerdict(t, s, r.ID)
	if v.State != StateSuggested || v.Value != "stop" || v.Reason != "because" || v.Ref != strconv.FormatInt(pid, 10) {
		t.Fatalf("verdict = %+v", v)
	}
	if v.Title != "Stop using “Old lesson”?" || v.ProjectID != nil {
		t.Fatalf("verdict title %q project %v", v.Title, v.ProjectID)
	}
	if st, dec := proposalState(t, s.DB, pid); st != "proposed" || dec.Valid {
		t.Fatalf("proposal changed: state %q decided %v", st, dec)
	}
	if st, _ := lessonRow(t, s.DB, lid); st != "active" {
		t.Fatalf("lesson status = %q, want active", st)
	}
	if spy.applies != 0 || spy.undos != 0 {
		t.Fatalf("Apply called %d, Undo %d times, want 0", spy.applies, spy.undos)
	}

	mustRun(t, s, StartReq{Kinds: []string{"retire"}})
	if j.calls != 1 {
		t.Fatalf("judge calls = %d, want 1 (open suggestion re-judged)", j.calls)
	}

	ctx := context.Background()
	if open, err := spy.Open(ctx, v.Ref); err != nil || !open {
		t.Fatalf("Open before keep = %v, %v; want true", open, err)
	}
	if _, err := s.DB.Exec(`UPDATE lesson_retirements SET state='kept' WHERE id=?`, pid); err != nil {
		t.Fatal(err)
	}
	if open, err := spy.Open(ctx, v.Ref); err != nil || open {
		t.Fatalf("Open after keep = %v, %v; want false", open, err)
	}
	if err := s.SweepOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetVerdict(v.ID); got.State != StateStale {
		t.Fatalf("verdict after sweep = %q, want stale", got.State)
	}
}

func TestRetireSourceCollectFields(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	lid := seedLesson(t, s.DB, "Some lesson", "active", "")
	pid := seedProposal(t, s.DB, lid, "proposed")
	src := RetireSource{DB: s.DB, Cfg: lessons.DefaultVerifyConfig()}
	ctx := context.Background()

	items, err := src.Collect(ctx, Scope{ProjectID: 4}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("collected %d items, want 1", len(items))
	}
	it := items[0]
	ref := strconv.FormatInt(pid, 10)
	if it.Kind != "retire" || it.Key != ref || it.ProjectID != 0 || it.WaitingSince != "2026-10-02T09:00:00.000Z" {
		t.Fatalf("item = %+v", it)
	}
	if len(it.Parts) != 1 || it.Parts[0].Ref != ref || strings.Join(it.Parts[0].Allowed, ",") != "stop,keep" {
		t.Fatalf("parts = %+v", it.Parts)
	}
	if it.Instruction != retireInstruction {
		t.Fatalf("instruction = %q", it.Instruction)
	}
	for _, want := range []string{"Guidance: Guide: Some lesson", "Reason: unused_60d",
		"Detail: not injected in 60 days", `Reason numbers: {"lastUsed":"2026-07-01"}`} {
		if !strings.Contains(it.Evidence, want) {
			t.Errorf("evidence lacks %q:\n%s", want, it.Evidence)
		}
	}
	if capped, _ := src.Collect(ctx, Scope{}, 0); len(capped) != 1 {
		t.Fatalf("limit 0 collected %d items, want all (1)", len(capped))
	}
	seedProposal(t, s.DB, seedLesson(t, s.DB, "Second", "active", ""), "proposed")
	if capped, _ := src.Collect(ctx, Scope{}, 1); len(capped) != 1 {
		t.Fatalf("limit 1 collected %d items", len(capped))
	}
	for _, r := range []string{"999", "-", ""} {
		if open, err := src.Open(ctx, r); err != nil || open {
			t.Errorf("Open(%q) = %v, %v; want false, nil", r, open, err)
		}
	}
	if _, err := src.Apply(ctx, it, it.Parts[0], "stop", "r", nil); err == nil || !strings.Contains(err.Error(), "never applied") {
		t.Errorf("Apply err = %v, want the suggest-only error", err)
	}
	if err := src.Undo(ctx, Verdict{}); err == nil {
		t.Error("Undo returned nil, want the suggest-only error")
	}
}

func TestEffectivenessText(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	got := effectivenessText(lessons.EffectivenessRow{
		WindowN: 10, MinRuns: 3, BeforeN: 8, AfterN: 5,
		MedianBefore: f(0.5), MedianAfter: f(0.4), MedianDrop: f(0.1),
		Uses: 7, Relied: 2, ReliedRate: f(0.25),
	})
	for _, want := range []string{"runs before 8, after 5 (window 10, min 3)", "median off-plan index before 0.5",
		"after 0.4", "drop 0.1", "used 7 times, relied on 2", "relied rate 0.25"} {
		if !strings.Contains(got, want) {
			t.Errorf("effectiveness text lacks %q: %s", want, got)
		}
	}
	bare := effectivenessText(lessons.EffectivenessRow{})
	if strings.Contains(bare, "median") || strings.Contains(bare, "rate") {
		t.Errorf("nil numbers rendered: %s", bare)
	}
	ev := retireEvidence(lessons.Proposal{Guidance: "g", Effectiveness: &lessons.EffectivenessRow{Uses: 1}})
	if !strings.Contains(ev, "Effectiveness: ") {
		t.Errorf("proposal evidence lacks the effectiveness line: %s", ev)
	}
}
