package store

import "testing"

// TestMigrate0120FreshDB: phase_decisions and epic_phases.decision_deps land on a
// fresh database, and the CHECKs refuse a value outside each closed set.
func TestMigrate0120FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 120`).Scan(&name); err != nil {
		t.Fatalf("migration 120 not recorded: %v", err)
	}
	if name != "0120_phase_decisions.sql" {
		t.Errorf("migration 120 name: want 0120_phase_decisions.sql, got %s", name)
	}
	mustHaveColumns(t, db, "phase_decisions",
		"id", "workspace_task_id", "planning_session_id", "decision_id", "seq", "source",
		"question_id", "question", "cost", "cost_reason", "status", "answer",
		"option_ids_json", "phase_id", "created_at", "decided_at")
	mustHaveColumns(t, db, "epic_phases", "decision_deps")

	ins := func(cost, status, source string) error {
		_, err := db.Exec(`INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, source, cost, status, created_at)
			VALUES (1, 'D-' || abs(random() % 100000), 1, ?, ?, ?, '2026-10-22T00:00:00Z')`, source, cost, status)
		return err
	}
	if err := ins("cheap", "decided", "interview"); err != nil {
		t.Fatalf("valid row refused: %v", err)
	}
	for _, bad := range [][3]string{
		{"pricey", "decided", "interview"},
		{"cheap", "maybe", "interview"},
		{"cheap", "decided", "chat"},
	} {
		if err := ins(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("row %v accepted — CHECK missing", bad)
		}
	}
}

// TestMigrate0120OnPopulatedDB: a database holding phases and a planning session
// from before 0120 (the shape of a copied live store) migrates; existing phases
// read decision_deps='[]', and both natural keys refuse a duplicate.
func TestMigrate0120OnPopulatedDB(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 119)
	if cols := columnSet(t, db, "epic_phases"); cols["decision_deps"] {
		t.Fatal("epic_phases.decision_deps exists before 0120 — migrateUpTo applied too much")
	}
	for _, q := range []string{
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-01T00:00:00Z')`,
		`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, source, external_id)
		 VALUES (7, 1, 'plan', 'p', 'queued', '2026-10-01T00:00:00Z', 'workspace', 'ws-7')`,
		`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path) VALUES (7, 1, 'Phase one', '/p/phase-1.md')`,
		`INSERT INTO planning_sessions (id, project_id, session_uuid, idea, created_at, updated_at)
		 VALUES (3, 1, 'u-1', 'idea', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate populated db: %v", err)
	}
	var deps string
	if err := db.QueryRow(`SELECT decision_deps FROM epic_phases WHERE workspace_task_id = 7`).Scan(&deps); err != nil {
		t.Fatal(err)
	}
	if deps != "[]" {
		t.Errorf("pre-existing phase decision_deps = %q, want []", deps)
	}

	sid := int64(3)
	d := Decision{PlanningSessionID: &sid, DecisionID: "D-1", Seq: 1, Question: "q", Cost: "expensive",
		Status: DecisionUndecided, CreatedAt: "2026-10-22T00:00:00Z"}
	if err := UpsertSessionDecision(db, d); err != nil {
		t.Fatal(err)
	}
	// Idempotent on (planning_session_id, decision_id).
	d.Question = "q2"
	if err := UpsertSessionDecision(db, d); err != nil {
		t.Fatal(err)
	}
	got, err := ListSessionDecisions(db, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Question != "q2" || !got[0].HoldsWork() {
		t.Fatalf("session decisions = %+v, want one undecided+expensive row with question q2", got)
	}

	// Flipping the cost re-derives an unanswered decision's status.
	upd, err := SetSessionDecisionCost(db, 3, "D-1", DecisionCostCheap, "a rename later is cheap")
	if err != nil {
		t.Fatal(err)
	}
	if upd == nil || upd.Status != DecisionRecommended || upd.CostReason != "a rename later is cheap" || upd.HoldsWork() {
		t.Fatalf("after cheap flip = %+v, want recommended, not holding", upd)
	}
	if miss, err := SetSessionDecisionCost(db, 3, "D-9", DecisionCostCheap, ""); err != nil || miss != nil {
		t.Fatalf("flip of an unrecorded decision = %+v, %v — want nil, nil", miss, err)
	}

	if _, err := db.Exec(`INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, status, created_at)
		VALUES (7, 'D-1', 1, 'decided', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, status, created_at)
		VALUES (7, 'D-1', 1, 'decided', 'x')`); err == nil {
		t.Error("duplicate (workspace_task_id, decision_id) accepted")
	}
}
