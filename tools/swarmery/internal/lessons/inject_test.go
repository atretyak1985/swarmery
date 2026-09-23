package lessons

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedActive inserts an ACTIVE surprise lesson directly (tests only — the
// production path to active is Accept; activeguard_test.go skips test files).
func seedActive(t *testing.T, db *sql.DB, phaseID int64, seq int, guidance, globs, activatedAt string) int64 {
	t.Helper()
	return mustExec(t, db, `INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, norm_title,
		guidance, area_globs, evidence_json, status, created_at, updated_at, activated_at)
		VALUES ('src-run', ?, ?, ?, ?, ?, ?, '[]', 'active', 'now', 'now', ?)`,
		phaseID, seq, "t"+guidance, "n"+guidance, guidance, globs, activatedAt)
}

func TestMatchesByGlobOverlap(t *testing.T) {
	sc := Scope{
		Areas: []string{"tools/app/internal/store", "internal/cost"},
		Files: []string{"web/src/pages/Lessons.tsx"},
	}
	cases := []struct {
		globs []string
		want  bool
	}{
		{[]string{"internal/store/**"}, true},       // sub-module prefix stripped off the forecast area
		{[]string{"internal/cost"}, true},           // exact
		{[]string{"internal"}, true},                // the forecast area lies inside the glob
		{[]string{"internal/cost/pricing/x"}, true}, // the glob lies inside the forecast area
		{[]string{"web/src/pages/*.tsx"}, true},     // path.Match against a forecast file
		{[]string{"internal/restore"}, false},       // segments, never substrings
		{[]string{"internal/ingest/**"}, false},
		{[]string{"docs", "cmd/*"}, false},
		{[]string{"*"}, true}, // everywhere, for a scope that exists
		{[]string{""}, false},
	}
	for _, c := range cases {
		if got := Matches(c.globs, sc); got != c.want {
			t.Errorf("Matches(%v) = %v, want %v", c.globs, got, c.want)
		}
	}
	if Matches([]string{"*"}, Scope{}) {
		t.Error("a run with no forecast areas must get no lessons, not every '*' lesson")
	}
}

func TestSelectRanksByRecencyAndHonoursEffectivenessHook(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "sel", 0.2, "")
	old := seedActive(t, db, phaseID, 1, "Old lesson.", "internal/cost", "2026-09-01T00:00:00Z")
	newer := seedActive(t, db, phaseID, 2, "Newer lesson.", "internal/**", "2026-09-20T00:00:00Z")
	seedActive(t, db, phaseID, 3, "Elsewhere.", "web/**", "2026-09-22T00:00:00Z")
	cand := seedCandidate(t, db, "cand", "Not accepted yet") // candidates never inject
	_ = cand

	sc, err := PhaseScope(db, phaseID)
	if err != nil || len(sc.Areas) != 1 {
		t.Fatalf("PhaseScope = %+v, %v; want the prior's one area", sc, err)
	}
	sel, err := Select(db, sc, DefaultBudgetTokens)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Lessons) != 2 || sel.Lessons[0].ID != newer || sel.Lessons[1].ID != old {
		t.Fatalf("selected %+v, want [newer, old]", sel.Lessons)
	}
	want := "\n\n" + blockHeader + "\n- [" + Ref(newer) + "] Newer lesson.\n- [" + Ref(old) + "] Old lesson."
	if sel.Text != want {
		t.Errorf("text = %q, want %q", sel.Text, want)
	}
	if strings.Contains(sel.Text, "MUST") {
		t.Error("the block is informational; it must not order the executor around")
	}

	// Phase 16's hook: a scored lesson outranks an unscored one.
	defer func(prev func(*sql.DB, []int64) (map[int64]float64, error)) { Effectiveness = prev }(Effectiveness)
	Effectiveness = func(_ *sql.DB, _ []int64) (map[int64]float64, error) {
		return map[int64]float64{old: 0.9}, nil
	}
	sel, _ = Select(db, sc, DefaultBudgetTokens)
	if len(sel.Lessons) != 2 || sel.Lessons[0].ID != old {
		t.Errorf("with effectiveness, selected %+v, want old first", sel.Lessons)
	}
}

func TestSelectCutsAtTheBudget(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "budget", 0.2, "")
	long := strings.Repeat("word ", 60) // ~300 bytes ≈ 75 tokens per line
	for i := 1; i <= 12; i++ {
		seedActive(t, db, phaseID, i, long+itoa(int64(i)), "internal/cost", "2026-09-"+strings.Repeat("0", 2-len(itoa(int64(i))))+itoa(int64(i))+"T00:00:00Z")
	}
	sc, _ := PhaseScope(db, phaseID)
	for _, budget := range []int{0, 10, 100, 250, 600} {
		sel, err := Select(db, sc, budget)
		if err != nil {
			t.Fatal(err)
		}
		if sel.Tokens > budget {
			t.Errorf("budget %d: injected %d tokens", budget, sel.Tokens)
		}
		if got := EstimateTokens(sel.Text); got != sel.Tokens {
			t.Errorf("budget %d: Tokens %d != estimate of the text %d", budget, sel.Tokens, got)
		}
		if budget >= 600 && (len(sel.Lessons) == 0 || len(sel.Lessons) == 12) {
			t.Errorf("budget %d: %d lessons — the cut should keep some and drop some", budget, len(sel.Lessons))
		}
		if budget <= 10 && sel.Text != "" {
			t.Errorf("budget %d: text %q, want none", budget, sel.Text)
		}
	}
}

func TestBudgetFromEnv(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	for in, want := range map[string]int{"": 600, "off": 0, "0": 0, "250": 250, "-3": 600, "lots": 600} {
		if got, _ := BudgetFromEnv(env(in)); got != want {
			t.Errorf("BudgetFromEnv(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestInjectorRecordsEveryInjectedLessonAndCitations(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, "inj", 0.2, "")
	a := seedActive(t, db, phaseID, 1, "Alpha.", "internal/cost", "2026-09-10T00:00:00Z")
	b := seedActive(t, db, phaseID, 2, "Beta.", "internal/**", "2026-09-11T00:00:00Z")
	inj := NewInjector(db, DefaultBudgetTokens)
	inj.Now = func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) }

	text := inj.ForPhase(phaseID, "run-1")
	if !strings.Contains(text, "["+Ref(a)+"]") || !strings.Contains(text, "["+Ref(b)+"]") {
		t.Fatalf("text %q misses a lesson", text)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM lesson_uses WHERE session_uuid = 'run-1' AND run_kind = 'phaserun'`).Scan(&n)
	if n != 2 {
		t.Fatalf("lesson_uses rows = %d, want one per injected lesson (2)", n)
	}

	// Citations: a in the transcript, b in the report, and an id never injected.
	sid := mustExec(t, db, `INSERT INTO sessions (project_id, session_uuid, status, started_at) VALUES (1, 'run-1', 'done', 'now')`)
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (?, 1, 'assistant', 'now', ?)`, sid, "Following ["+Ref(a)+"] here.")
	doc := filepath.Join(t.TempDir(), "phase.md")
	if err := os.WriteFile(doc, []byte("# P\n\n## Completion Report\n\nRelied on ["+Ref(b)+"] and [L-999].\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inj.AfterPhaseRun(phaseID, "run-1", doc)
	for id, where := range map[int64]string{a: "transcript", b: "report"} {
		var relied int
		var got string
		_ = db.QueryRow(`SELECT relied_on, relied_where FROM lesson_uses WHERE session_uuid='run-1' AND lesson_id=?`, id).Scan(&relied, &got)
		if relied != 1 || got != where {
			t.Errorf("lesson %d: relied_on=%d where=%q, want 1 %q", id, relied, got, where)
		}
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM lesson_uses WHERE lesson_id = 999`).Scan(&n)
	if n != 0 {
		t.Error("a cited id the run was never handed must not create a use")
	}

	// Plan runs read the priors of the plan's phases.
	var taskID int64
	_ = db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, phaseID).Scan(&taskID)
	if text := inj.ForPlan(taskID, "plan-run-1"); !strings.Contains(text, "["+Ref(a)+"]") {
		t.Errorf("plan run text %q misses the lesson", text)
	}
	// No scope ⇒ nothing, and nothing recorded.
	if text := inj.ForPhase(424242, "run-2"); text != "" {
		t.Errorf("a phase without a prior got %q", text)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM lesson_uses WHERE session_uuid = 'run-2'`).Scan(&n)
	if n != 0 {
		t.Errorf("run-2 recorded %d uses for an empty injection", n)
	}
}

func TestCompletionReportSection(t *testing.T) {
	doc := "# T\n\n## Steps\n[L-1]\n\n## Completion Report\n\nused [L-2]\n### sub\n[L-3]\n## After\n[L-4]\n"
	got := Cited(CompletionReport(doc))
	if !got[2] || !got[3] || got[1] || got[4] {
		t.Errorf("cited in report = %v, want {2,3}", got)
	}
}
