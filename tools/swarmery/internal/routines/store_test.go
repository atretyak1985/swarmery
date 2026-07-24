package routines

import (
	"database/sql"
	"testing"
	"time"
)

func TestCreateComputesNextRun(t *testing.T) {
	s, clk := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{
		Name:     "every minute",
		CronExpr: "* * * * *",
		Enabled:  true,
		Steps:    mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"}),
	})
	if !r.NextRunAt.Valid {
		t.Fatal("enabled cron routine has no next_run_at")
	}
	next := parseTS(r.NextRunAt.String)
	// next slot strictly after 12:00:00 is 12:01:00.
	want := clk.now().Add(time.Minute).Truncate(time.Minute)
	if !next.Equal(want) {
		t.Errorf("next_run_at = %s, want %s", next, want)
	}
	// ID + defaults.
	if len(r.ID) != 8 || r.ID[:2] != "R-" {
		t.Errorf("bad id %q", r.ID)
	}
}

func TestCreateManualNoNextRun(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{
		Name:    "manual",
		Enabled: true, // enabled but no cron ⇒ still no schedule
		Steps:   mkSteps(Step{Type: StepAIPrompt, Name: "a", Prompt: "hi"}),
	})
	if r.NextRunAt.Valid {
		t.Errorf("manual routine should have no next_run_at, got %s", r.NextRunAt.String)
	}
}

func TestCreateDisabledNoNextRun(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{
		Name:     "disabled",
		CronExpr: "* * * * *",
		Enabled:  false,
		Steps:    mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"}),
	})
	if r.NextRunAt.Valid {
		t.Errorf("disabled routine should have no next_run_at, got %s", r.NextRunAt.String)
	}
}

func TestGetUpdateDelete(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	seedProject(t, s.DB, 1, "/tmp/p1")
	r := mustCreate(t, s, CreateParams{
		ProjectID: sql.NullInt64{Int64: 1, Valid: true},
		Name:      "orig",
		CronExpr:  "0 3 * * *",
		Enabled:   true,
		Steps:     mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"}),
	})

	// Get round-trips project scope + steps.
	got, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ProjectID.Valid || got.ProjectID.Int64 != 1 {
		t.Errorf("project scope lost: %+v", got.ProjectID)
	}
	if len(got.Steps) != 1 || got.Steps[0].Command != "true" {
		t.Errorf("steps lost: %+v", got.Steps)
	}

	// Update name + cron; next_run_at recomputed.
	newName := "renamed"
	newCron := "*/5 * * * *"
	up, err := s.Update(r.ID, UpdateParams{Name: &newName, CronExpr: &newCron})
	if err != nil {
		t.Fatal(err)
	}
	if up.Name != "renamed" || up.CronExpr != "*/5 * * * *" {
		t.Errorf("update did not apply: %+v", up)
	}
	if !up.NextRunAt.Valid {
		t.Error("next_run_at not recomputed after cron change")
	}

	// Disable clears next_run_at.
	dis := false
	up2, err := s.Update(r.ID, UpdateParams{Enabled: &dis})
	if err != nil {
		t.Fatal(err)
	}
	if up2.NextRunAt.Valid {
		t.Error("disabling should clear next_run_at")
	}

	// Delete.
	if err := s.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(r.ID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if err := s.Delete(r.ID); err != ErrNotFound {
		t.Errorf("delete of missing routine: want ErrNotFound, got %v", err)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	name := "x"
	if _, err := s.Update("R-nope00", UpdateParams{Name: &name}); err != ErrNotFound {
		t.Errorf("update missing: want ErrNotFound, got %v", err)
	}
}

func TestListScoping(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	seedProject(t, s.DB, 1, "/tmp/p1")
	seedProject(t, s.DB, 2, "/tmp/p2")
	_ = mustCreate(t, s, CreateParams{Name: "global", Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	_ = mustCreate(t, s, CreateParams{ProjectID: sql.NullInt64{Int64: 1, Valid: true}, Name: "p1a", Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	_ = mustCreate(t, s, CreateParams{ProjectID: sql.NullInt64{Int64: 1, Valid: true}, Name: "p1b", Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})

	all, err := s.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("List(0) = %d, want 3", len(all))
	}
	p1, err := s.List(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1) != 2 {
		t.Errorf("List(1) = %d, want 2", len(p1))
	}
}

func TestDueQuery(t *testing.T) {
	s, clk := newTestSvc(t, &stubRunner{}, &stubTasks{})
	// A routine due in the past.
	due := mustCreate(t, s, CreateParams{Name: "due", CronExpr: "* * * * *", Enabled: true,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	// Force its next_run_at into the past.
	past := clk.now().Add(-2 * time.Minute).Format(tsFormat)
	if _, err := s.DB.Exec(`UPDATE routines SET next_run_at=? WHERE id=?`, past, due.ID); err != nil {
		t.Fatal(err)
	}
	// A disabled routine (never due) + a manual routine (no cron).
	_ = mustCreate(t, s, CreateParams{Name: "disabled", CronExpr: "* * * * *", Enabled: false,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	_ = mustCreate(t, s, CreateParams{Name: "manual", Enabled: true,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})

	got, err := s.due(clk.now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != due.ID {
		t.Errorf("due() = %+v, want just %s", got, due.ID)
	}
}

func TestRunHistoryPruneTo50(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "hist", Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	// Insert 55 runs.
	for i := 0; i < 55; i++ {
		id, err := s.startRun(r.ID, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.finishRun(id, "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != MaxRunHistory {
		t.Errorf("run history = %d, want %d (pruned)", len(runs), MaxRunHistory)
	}
	// Newest first.
	if runs[0].ID < runs[len(runs)-1].ID {
		t.Error("runs not ordered newest-first")
	}
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM routine_runs WHERE routine_id=?`, r.ID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != MaxRunHistory {
		t.Errorf("stored rows = %d, want %d", total, MaxRunHistory)
	}
}

func TestHealStale(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	r := mustCreate(t, s, CreateParams{Name: "heal", Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	// A running row left behind by a "crash".
	id, err := s.startRun(r.ID, "cron")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.DB.QueryRow(`SELECT status FROM routine_runs WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Errorf("healed run status = %q, want failed", status)
	}
}

func TestWebhookTokenRotation(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	r := mustCreate(t, s, CreateParams{Name: "hook", WebhookToken: tok,
		Steps: mkSteps(Step{Type: StepCommand, Name: "c", Command: "true"})})
	if r.WebhookToken != tok {
		t.Errorf("token not stored: %q != %q", r.WebhookToken, tok)
	}
	// Clear the token.
	empty := ""
	up, err := s.Update(r.ID, UpdateParams{WebhookToken: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if up.WebhookToken != "" {
		t.Errorf("token not cleared: %q", up.WebhookToken)
	}
}

func TestNextRunHelper(t *testing.T) {
	from := time.Date(2026, 7, 24, 12, 30, 0, 0, time.UTC)
	next, ok := NextRun("0 * * * *", from) // top of every hour
	if !ok {
		t.Fatal("expected ok")
	}
	if next.Hour() != 13 || next.Minute() != 0 {
		t.Errorf("next = %s, want 13:00", next)
	}
	if _, ok := NextRun("", from); ok {
		t.Error("blank cron should yield ok=false")
	}
	if _, ok := NextRun("not a cron", from); ok {
		t.Error("invalid cron should yield ok=false")
	}
}
