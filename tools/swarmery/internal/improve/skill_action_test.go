package improve

// F3: where the routing action comes from. R11 already chose one and wrote it
// into the recommendation's evidence JSON as `latest_action` — the same string
// the card's "Latest action: …" line shows. Re-deriving it with a second query
// is what made the file the loop edits and the line the operator approved able
// to disagree.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// seedR11Recommendation writes an accepted R11 row whose evidence carries the
// same shape r11RecurringLesson emits: `latest_action` present when the group's
// newest task recorded one, ABSENT when it did not.
func seedR11Recommendation(t *testing.T, db *sql.DB, id int64, latestAction string) {
	t.Helper()
	ev := map[string]any{
		"norm_title": routeNorm,
		"title":      routeTitle,
		"counts":     map[string]int{"tasks": 3},
	}
	if latestAction != "" {
		ev["latest_action"] = latestAction
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	mustExec(t, db, `INSERT INTO recommendations
		(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (?, 'R11', 'skill', ?, 'Recurring lesson', 'detail', ?, 'accepted', ?, ?, ?)`,
		id, routeNorm, string(raw), "R11:skill:"+routeNorm, day(1), day(1))
}

// TestRouteSkillHonorsRecommendationWithNoLatestAction is the F3 regression.
//
// The newest task recorded NO action, so R11 omitted `latest_action` and the
// card showed no "Latest action:" line. The standalone lesson query skips empty
// actions and reaches back to an OLDER task whose action names a skill — so
// before the fix the loop opened a proposal against docker-build off evidence
// the operator never saw. The routed outcome must now match the card:
// needs_target.
func TestRouteSkillHonorsRecommendationWithNoLatestAction(t *testing.T) {
	db := openDB(t)
	// Older task names a skill; newest task (day(1)) records nothing.
	seedLesson(t, db, 3, "2026-09-10-alpha", routeTitle, routeNorm,
		"add it to the plugins/core/skills/docker-build procedure")
	seedLesson(t, db, 2, "2026-09-14-beta", routeTitle, routeNorm, "")
	seedLesson(t, db, 1, "2026-09-18-gamma", routeTitle, routeNorm, "")

	runner := &mockRunner{out: validOut}
	svc := routeService(t, db, runner, map[string]string{"docker-build": skillBody})

	// Precondition: the fallback query WOULD have resolved a target here. Without
	// this the test could pass for the wrong reason (no action anywhere).
	fallback, err := svc.latestLessonAction(routeNorm)
	if err != nil {
		t.Fatalf("latestLessonAction: %v", err)
	}
	if !strings.Contains(fallback, "skills/docker-build") {
		t.Fatalf("fixture broken: fallback action = %q, want one naming skills/docker-build", fallback)
	}

	recID := int64(43)
	seedR11Recommendation(t, db, recID, "") // R11 omitted latest_action
	id, err := svc.RouteSkill(context.Background(), SkillRouteReq{NormTitle: routeNorm, RecommendationID: &recID})
	if err != nil {
		t.Fatalf("RouteSkill: %v", err)
	}
	p := readSkillProposal(t, db, id)
	if p.status != StatusNeedsTarget {
		t.Fatalf("status = %q (target %q), want %s — routing used an action the R11 card never showed",
			p.status, p.path, StatusNeedsTarget)
	}
	if p.path != "" {
		t.Errorf("target_path = %q, want empty", p.path)
	}
	if len(runner.prompts) != 0 {
		t.Errorf("the model ran %d times for an unresolved target; want 0", len(runner.prompts))
	}
}

// TestLessonActionPrefersStoredEvidence pins the resolution order directly: the
// stored `latest_action` wins over the lesson query, and the query is used only
// when there is no recommendation to read (or its evidence is unreadable).
func TestLessonActionPrefersStoredEvidence(t *testing.T) {
	db := openDB(t)
	seedRecurringLesson(t, db, "put it in skills/from-the-lesson-query")
	svc := routeService(t, db, &mockRunner{out: validOut}, map[string]string{})

	recID := int64(44)
	seedR11Recommendation(t, db, recID, "put it in skills/from-the-evidence")

	got, err := svc.lessonAction(&recID, routeNorm)
	if err != nil {
		t.Fatalf("lessonAction: %v", err)
	}
	if !strings.Contains(got, "from-the-evidence") {
		t.Errorf("lessonAction with a recommendation = %q, want R11's stored action", got)
	}

	// No recommendation → the documented fallback.
	got, err = svc.lessonAction(nil, routeNorm)
	if err != nil {
		t.Fatalf("lessonAction(nil): %v", err)
	}
	if !strings.Contains(got, "from-the-lesson-query") {
		t.Errorf("lessonAction without a recommendation = %q, want the lesson query", got)
	}

	// Unreadable evidence is "no stored answer", not "empty action".
	mustExec(t, db, `UPDATE recommendations SET evidence = 'not json' WHERE id = ?`, recID)
	got, err = svc.lessonAction(&recID, routeNorm)
	if err != nil {
		t.Fatalf("lessonAction(bad evidence): %v", err)
	}
	if !strings.Contains(got, "from-the-lesson-query") {
		t.Errorf("lessonAction with unparseable evidence = %q, want the fallback", got)
	}

	// A recommendation that no longer exists falls back too.
	got, err = svc.lessonAction(ptrInt64(9999), routeNorm)
	if err != nil {
		t.Fatalf("lessonAction(missing rec): %v", err)
	}
	if !strings.Contains(got, "from-the-lesson-query") {
		t.Errorf("lessonAction with a missing recommendation = %q, want the fallback", got)
	}
}

func ptrInt64(v int64) *int64 { return &v }
