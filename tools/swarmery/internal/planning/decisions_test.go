package planning

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// ── protocol: costToChange ──

func costTurn(cost string) string {
	c := ""
	if cost != "-" {
		c = `"costToChange":"` + cost + `","costReason":"a migration to undo",`
	}
	return "```json\n" + `{"type":"question","data":{"id":"q","type":"single_select","question":"Q?",` + c +
		`"options":[{"id":"a","label":"A"},{"id":"other","label":"Other","isOther":true}]}}` + "\n```"
}

func TestParseTurnValidatesCostToChange(t *testing.T) {
	cases := []struct {
		in, want, lint string
	}{
		{"expensive", CostExpensive, ""},
		{"Cheap", CostCheap, ""},
		{"-", CostCheap, LintQuestionCostMissing},
		{"medium", CostCheap, LintQuestionCostInvalid},
	}
	for _, c := range cases {
		pt := ParseTurn(costTurn(c.in))
		if pt.Question == nil {
			t.Fatalf("%s: question dropped — a bad cost must degrade, never reject", c.in)
		}
		if pt.Question.CostToChange != c.want {
			t.Errorf("%s: costToChange = %q, want %q", c.in, pt.Question.CostToChange, c.want)
		}
		gotLint := strings.Join(pt.Question.Lints, ",")
		if gotLint != c.lint {
			t.Errorf("%s: lints = %q, want %q", c.in, gotLint, c.lint)
		}
	}
	if pt := ParseTurn(costTurn("expensive")); pt.Question.CostReason != "a migration to undo" {
		t.Errorf("costReason = %q", pt.Question.CostReason)
	}
}

// ── prompt: the PHASE A / PHASE B rules ──

func TestPromptCarriesDecisionRules(t *testing.T) {
	golden, err := os.ReadFile(goldenPlanPrompt)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"rendered": BuildPrompt("idea", "/ws"), "golden": string(golden)} {
		for _, want := range []string{
			`"costToChange": "cheap" or "expensive"`, // PHASE A rule 3
			`"costReason"`,
			`"costToChange":"expensive","costReason":"…"`, // the JSON example
			"`## Decisions`",                              // PHASE B
			"`- **D-n** (<cheap|expensive>, <decided|recommended|undecided>) — <question> → <answer> (reason: <costReason>)`",
			"`(без відповіді, рекомендований)`",
			"`**Depends on decisions:** D-n, …`",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt missing %q", name, want)
			}
		}
	}
	if !strings.Contains(BuildRevisePrompt(ReviseInput{}), `"costToChange"`) {
		t.Error("revise prompt lost the shared PHASE A cost rule")
	}
}

func TestBuildProceedMessageCarriesDecisionLines(t *testing.T) {
	msg := BuildProceedMessage("- **D-1** (cheap, decided) — Q → A")
	if !strings.Contains(msg, "PROCEED") || !strings.Contains(msg, "- **D-1** (cheap, decided) — Q → A") ||
		!strings.Contains(msg, "VERBATIM") {
		t.Errorf("proceed message = %q", msg)
	}
}

// ── service: PROCEED persists one row per turn ──

func insertTurn(t *testing.T, s *Service, sessionID int64, seq int, q PlanningQuestion, ans *wizardAnswer) {
	t.Helper()
	qb, _ := json.Marshal(q)
	var a any
	if ans != nil {
		b, _ := json.Marshal(ans)
		a = string(b)
	}
	if _, err := s.DB.Exec(`INSERT INTO planning_turns(planning_session_id, seq, question, answer, created_at)
		VALUES(?, ?, ?, ?, 'x')`, sessionID, seq, string(qb), a); err != nil {
		t.Fatal(err)
	}
}

func q3(id, cost string) PlanningQuestion {
	return PlanningQuestion{ID: id, Type: "single_select", Question: "Question " + id + "?", CostToChange: cost,
		CostReason: "why " + id,
		Options: []PlanningOption{
			{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta", Recommended: true},
			{ID: "other", Label: "Other", IsOther: true},
		}}
}

func proceedFixture(t *testing.T) (*Service, int64) {
	t.Helper()
	db := testDB(t)
	s := newInlineService(t, db, &stubRunner{})
	id := insertWizardRow(t, db, "uuid-dec", StatusAwaiting)
	insertTurn(t, s, id, 1, q3("answered", CostExpensive), &wizardAnswer{Kind: "answer", SelectedOptionIDs: []string{"a", "other"}, OtherText: "with a twist"})
	insertTurn(t, s, id, 2, q3("skipped-cheap", CostCheap), &wizardAnswer{Kind: "refine", Instructions: "go deeper"})
	insertTurn(t, s, id, 3, q3("skipped-expensive", CostExpensive), nil)
	cur, _ := json.Marshal(q3("skipped-expensive", CostExpensive))
	if _, err := db.Exec(`UPDATE planning_sessions SET current_question=? WHERE id=?`, string(cur), id); err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestProceedPersistsOneDecisionPerTurn(t *testing.T) {
	s, id := proceedFixture(t)
	text, _, err := s.Proceed(1)
	if err != nil {
		t.Fatalf("Proceed: %v", err)
	}
	ds, err := store.ListSessionDecisions(s.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 {
		t.Fatalf("decisions = %d, want one per turn (3)", len(ds))
	}
	want := []struct{ id, status, cost, answer string }{
		{"D-1", store.DecisionDecided, "expensive", "Alpha; with a twist"},
		{"D-2", store.DecisionRecommended, "cheap", "Beta (без відповіді, рекомендований)"},
		{"D-3", store.DecisionUndecided, "expensive", "undecided"},
	}
	for i, w := range want {
		d := ds[i]
		if d.DecisionID != w.id || d.Status != w.status || d.Cost != w.cost || d.Answer != w.answer || d.Source != "interview" {
			t.Errorf("%s = %+v, want %+v", w.id, d, w)
		}
	}
	if ds[0].DecidedAt == nil || ds[1].DecidedAt != nil {
		t.Errorf("decided_at: answered %v, skipped %v", ds[0].DecidedAt, ds[1].DecidedAt)
	}
	if !ds[2].HoldsWork() || ds[1].HoldsWork() {
		t.Error("only the skipped expensive decision may hold work")
	}
	for _, line := range []string{
		"- **D-1** (expensive, decided) — Question answered? → Alpha; with a twist (reason: why answered)",
		"- **D-3** (expensive, undecided) — Question skipped-expensive? → undecided (reason: why skipped-expensive)",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("PROCEED message lacks %q:\n%s", line, text)
		}
	}
}

func TestProceedReviseRecordsNothing(t *testing.T) {
	s, id := proceedFixture(t)
	if _, err := s.DB.Exec(`UPDATE planning_sessions SET mode='revise' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Proceed(1); err != nil {
		t.Fatal(err)
	}
	if ds, _ := store.ListSessionDecisions(s.DB, id); len(ds) != 0 {
		t.Errorf("revise PROCEED recorded %d decisions", len(ds))
	}
}

// ── service: the cost toggle ──

func TestSetTurnCostBeforeAndAfterProceed(t *testing.T) {
	s, id := proceedFixture(t)
	// Before PROCEED: the stored turn and the current question flip.
	q, err := s.SetTurnCost(id, 3, "cheap", "")
	if err != nil {
		t.Fatal(err)
	}
	if q.CostToChange != CostCheap || q.CostReason != "why skipped-expensive" {
		t.Errorf("flipped question = %+v", q)
	}
	st, _ := s.WizardSnapshot(1)
	if st.CurrentQuestion == nil || st.CurrentQuestion.CostToChange != CostCheap || st.PlanningSessionID != id {
		t.Errorf("snapshot current question = %+v (session %d)", st.CurrentQuestion, st.PlanningSessionID)
	}
	// PROCEED then records the operator's cost, not the planner's.
	if _, _, err := s.Proceed(1); err != nil {
		t.Fatal(err)
	}
	ds, _ := store.ListSessionDecisions(s.DB, id)
	if ds[2].Cost != "cheap" || ds[2].Status != store.DecisionRecommended {
		t.Errorf("D-3 after flip+proceed = %+v, want cheap/recommended", ds[2])
	}

	// After the plan exists: the row flips, and so does the README line.
	planDir := filepath.Join(t.TempDir(), "plan")
	os.MkdirAll(planDir, 0o755)
	readme := "# Plan\n\n## Decisions\n\n- **D-3** (cheap, recommended) — Question skipped-expensive? → Beta (без відповіді, рекомендований)\n\n## Risks\n"
	os.WriteFile(filepath.Join(planDir, "README.md"), []byte(readme), 0o644)
	mustExecP(t, s, `INSERT INTO tasks (id, project_id, title, prompt, status, created_at, source, external_id)
		VALUES (9, 1, 'p', 'p', 'running', 'x', 'workspace', 'ws-9')`)
	mustExecP(t, s, `INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at) VALUES (9, 'plan', ?, 'h', 'x')`, planDir)
	mustExecP(t, s, `UPDATE phase_decisions SET workspace_task_id = 9 WHERE planning_session_id = ?`, id)

	if _, err := s.SetTurnCost(id, 3, "expensive", "now it is a contract"); err != nil {
		t.Fatal(err)
	}
	ds, _ = store.ListTaskDecisions(s.DB, 9)
	if ds[2].Cost != "expensive" || ds[2].Status != store.DecisionUndecided || ds[2].CostReason != "now it is a contract" {
		t.Errorf("D-3 after the second flip = %+v", ds[2])
	}
	b, _ := os.ReadFile(filepath.Join(planDir, "README.md"))
	if !strings.Contains(string(b), "- **D-3** (expensive, undecided) — Question skipped-expensive? → undecided (reason: now it is a contract)") ||
		!strings.Contains(string(b), "## Risks") {
		t.Errorf("README after the flip:\n%s", b)
	}

	if _, err := s.SetTurnCost(id, 3, "pricey", ""); !errors.Is(err, ErrBadCost) {
		t.Errorf("bad cost err = %v", err)
	}
	if _, err := s.SetTurnCost(id, 99, "cheap", ""); !errors.Is(err, ErrTurnNotFound) {
		t.Errorf("missing turn err = %v", err)
	}
}

func mustExecP(t *testing.T, s *Service, q string, args ...any) {
	t.Helper()
	if _, err := s.DB.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
