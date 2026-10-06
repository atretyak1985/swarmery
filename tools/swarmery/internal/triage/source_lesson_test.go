package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
)

// applySpy wraps a real Source and counts the Apply/Undo calls the engine makes.
type applySpy struct {
	Source
	applies, undos int
}

func (a *applySpy) Apply(ctx context.Context, it Item, p Part, value, reason string, payload json.RawMessage) (Applied, error) {
	a.applies++
	return a.Source.Apply(ctx, it, p, value, reason, payload)
}

func (a *applySpy) Undo(ctx context.Context, v Verdict) error {
	a.undos++
	return a.Source.Undo(ctx, v)
}

// kindJudge answers every part of an item with the value for the item's kind.
type kindJudge map[string]string

func (k kindJudge) Judge(_ context.Context, it Item) (Answer, error) {
	vals := map[string]string{}
	for _, p := range it.Parts {
		vals[p.Ref] = k[it.Kind]
	}
	return Answer{Values: vals, Reason: "by kind"}, nil
}

// seedLesson inserts one surprise_lessons row and returns its id.
func seedLesson(t *testing.T, db *sql.DB, title, status, cause string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO surprise_lessons(source_phase_run, phase_id, seq, title, guidance,
		area_globs, cause, source_paragraph, surprise_index, status, recurrences, created_at, updated_at)
		VALUES(?, 1, 1, ?, ?, 'internal/api/**,web/src/**', ?, 'the paragraph that diverged', 0.42, ?, 3,
		       '2026-10-01T10:00:00.000Z', '2026-10-01T10:00:00.000Z')`,
		"run-"+title, title, "Guide: "+title, cause, status)
	if err != nil {
		t.Fatalf("seed lesson: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func lessonRow(t *testing.T, db *sql.DB, id int64) (status, updated string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status, updated_at FROM surprise_lessons WHERE id=?`, id).Scan(&status, &updated); err != nil {
		t.Fatal(err)
	}
	return status, updated
}

// onlyVerdict returns the single verdict of a run, failing on any other count.
func onlyVerdict(t *testing.T, s *Service, runID int64) Verdict {
	t.Helper()
	vs, err := s.ListVerdicts(VerdictFilter{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("run %d stored %d verdicts, want 1: %+v", runID, len(vs), vs)
	}
	return vs[0]
}

func TestLessonRunSuggestsOnceThenSweepsStale(t *testing.T) {
	j := &stubJudge{value: "accept"}
	s := newTestService(t, j)
	id := seedLesson(t, s.DB, "Run the migration test first", "candidate", "a cause")
	seedLesson(t, s.DB, "Already active", "active", "")
	spy := &applySpy{Source: LessonSource{DB: s.DB}}
	s.Register(spy)
	_, updatedBefore := lessonRow(t, s.DB, id)

	r := mustRun(t, s, StartReq{Kinds: []string{"lesson"}})
	if r.Status != StatusOK || r.Suggested != 1 || r.Applied != 0 {
		t.Fatalf("run = %+v, want ok with 1 suggested, 0 applied", r)
	}
	v := onlyVerdict(t, s, r.ID)
	if v.State != StateSuggested || v.Value != "accept" || v.Reason != "because" || v.Ref != strconv.FormatInt(id, 10) {
		t.Fatalf("verdict = %+v", v)
	}
	if v.ProjectID != nil {
		t.Fatalf("lesson verdict project = %d, want nil", *v.ProjectID)
	}
	if st, upd := lessonRow(t, s.DB, id); st != "candidate" || upd != updatedBefore {
		t.Fatalf("lesson row changed: status %q updated %q", st, upd)
	}
	if spy.applies != 0 || spy.undos != 0 {
		t.Fatalf("Apply called %d, Undo %d times, want 0", spy.applies, spy.undos)
	}

	// An item with an open suggestion is not judged again.
	r2 := mustRun(t, s, StartReq{Kinds: []string{"lesson"}})
	if j.calls != 1 {
		t.Fatalf("judge calls = %d, want 1", j.calls)
	}
	if vs, _ := s.ListVerdicts(VerdictFilter{RunID: r2.ID}); len(vs) != 0 {
		t.Fatalf("second run stored %d verdicts, want 0", len(vs))
	}

	// The operator accepted the lesson elsewhere: Open flips, the sweep marks stale.
	ctx := context.Background()
	if open, err := spy.Open(ctx, v.Ref); err != nil || !open {
		t.Fatalf("Open before accept = %v, %v; want true", open, err)
	}
	if _, err := s.DB.Exec(`UPDATE surprise_lessons SET status='active' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if open, err := spy.Open(ctx, v.Ref); err != nil || open {
		t.Fatalf("Open after accept = %v, %v; want false", open, err)
	}
	if err := s.SweepOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetVerdict(v.ID); got.State != StateStale {
		t.Fatalf("verdict after sweep = %q, want stale", got.State)
	}
}

func TestLessonAndRetireCollectedForEveryScope(t *testing.T) {
	// Lesson and proposal ids can be equal, so the judge answers by kind.
	s := newTestService(t, kindJudge{"lesson": "not-useful", "retire": "keep"})
	lid := seedLesson(t, s.DB, "Fleet-wide lesson", "candidate", "")
	seedProposal(t, s.DB, lid, "proposed")
	s.Register(LessonSource{DB: s.DB})
	s.Register(RetireSource{DB: s.DB, Cfg: lessons.DefaultVerifyConfig()})

	r := mustRun(t, s, StartReq{Scope: Scope{ProjectID: 7}, Kinds: []string{"lesson", "retire"}})
	vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("scoped run stored %d verdicts, want 2: %+v", len(vs), vs)
	}
	for _, v := range vs {
		if v.State != StateSuggested || v.ProjectID != nil {
			t.Fatalf("verdict %s/%s = state %q project %v, want suggested with nil project", v.Kind, v.Ref, v.State, v.ProjectID)
		}
	}
	// A project filter still shows them (project-less verdicts are visible everywhere).
	if got, _ := s.ListVerdicts(VerdictFilter{ProjectID: 3}); len(got) != 2 {
		t.Fatalf("ProjectID 3 filter shows %d verdicts, want 2", len(got))
	}
}

func TestLessonSourceCollectFields(t *testing.T) {
	s := newTestService(t, &stubJudge{})
	withCause := seedLesson(t, s.DB, "With cause", "candidate", "the cited cause")
	noCause := seedLesson(t, s.DB, "Without cause", "candidate", "")
	src := LessonSource{DB: s.DB}
	ctx := context.Background()

	items, err := src.Collect(ctx, Scope{ProjectID: 9}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("collected %d items, want 2", len(items))
	}
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	it := byKey[strconv.FormatInt(withCause, 10)]
	if it.Kind != "lesson" || it.Title != "With cause" || it.ProjectID != 0 || it.WaitingSince != "2026-10-01T10:00:00.000Z" {
		t.Fatalf("item = %+v", it)
	}
	if len(it.Parts) != 1 || it.Parts[0].Ref != it.Key || strings.Join(it.Parts[0].Allowed, ",") != "accept,not-useful" {
		t.Fatalf("parts = %+v", it.Parts)
	}
	if it.Instruction != lessonInstruction {
		t.Fatalf("instruction = %q", it.Instruction)
	}
	for _, want := range []string{"Title: With cause", "Guidance: Guide: With cause", "Cause: the cited cause",
		"Areas: internal/api/**, web/src/**", "Recurrences: 3", "Off-plan (surprise) index: 0.42"} {
		if !strings.Contains(it.Evidence, want) {
			t.Errorf("evidence lacks %q:\n%s", want, it.Evidence)
		}
	}
	if strings.Contains(it.Evidence, "Source paragraph") {
		t.Errorf("evidence shows the source paragraph although a cause exists:\n%s", it.Evidence)
	}
	if ev := byKey[strconv.FormatInt(noCause, 10)].Evidence; !strings.Contains(ev, "Source paragraph: the paragraph that diverged") {
		t.Errorf("evidence without a cause lacks the source paragraph:\n%s", ev)
	}

	if capped, _ := src.Collect(ctx, Scope{}, 1); len(capped) != 1 {
		t.Fatalf("limit 1 collected %d items", len(capped))
	}
	for _, ref := range []string{"999", "x", ""} {
		if open, err := src.Open(ctx, ref); err != nil || open {
			t.Errorf("Open(%q) = %v, %v; want false, nil", ref, open, err)
		}
	}
	if _, err := src.Apply(ctx, it, it.Parts[0], "accept", "r", nil); err == nil || !strings.Contains(err.Error(), "never applied") {
		t.Errorf("Apply err = %v, want the suggest-only error", err)
	}
	if err := src.Undo(ctx, Verdict{}); err == nil {
		t.Error("Undo returned nil, want the suggest-only error")
	}
}
