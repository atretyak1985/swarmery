package wsingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestArtifactsParse exercises the three fixture artifacts of full-card:
// phases/09-retrospective.md, ORCHESTRATION.md, logs/agents.md.
func TestArtifactsParse(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	pinMtime(t)
	stats := scan(t, db)

	if stats.Retros != 1 || stats.Loops != 2 || stats.Delegations != 3 {
		t.Errorf("stats = retros %d loops %d delegations %d, want 1/2/3",
			stats.Retros, stats.Loops, stats.Delegations)
	}

	var taskID int64
	if err := db.QueryRow(`SELECT id FROM tasks WHERE external_id='2026-07-01-full-card'`).Scan(&taskID); err != nil {
		t.Fatalf("full-card task: %v", err)
	}

	t.Run("retro duration row 6h/8h/+33%", func(t *testing.T) {
		var est, act, variance sql.NullFloat64
		if err := db.QueryRow(
			`SELECT estimated_hours, actual_hours, variance_pct FROM task_retros WHERE task_id = ?`,
			taskID).Scan(&est, &act, &variance); err != nil {
			t.Fatalf("task_retros row: %v", err)
		}
		if !est.Valid || est.Float64 != 6 {
			t.Errorf("estimated_hours = %+v, want 6", est)
		}
		if !act.Valid || act.Float64 != 8 {
			t.Errorf("actual_hours = %+v, want 8", act)
		}
		if !variance.Valid || variance.Float64 != 33 {
			t.Errorf("variance_pct = %+v, want 33 (from '+33%%')", variance)
		}
	})

	t.Run("lessons titles, body, action", func(t *testing.T) {
		rows, err := db.Query(`
			SELECT l.seq, l.title, l.body, l.action
			  FROM retro_lessons l JOIN task_retros r ON r.id = l.retro_id
			 WHERE r.task_id = ? ORDER BY l.seq`, taskID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		type lesson struct {
			seq          int
			title        string
			body, action sql.NullString
		}
		var got []lesson
		for rows.Next() {
			var l lesson
			if err := rows.Scan(&l.seq, &l.title, &l.body, &l.action); err != nil {
				t.Fatal(err)
			}
			got = append(got, l)
		}
		if len(got) != 2 {
			t.Fatalf("lessons = %+v, want 2", got)
		}
		if got[0].title != "Pin fixture mtimes" ||
			got[0].action.String != "add pinMtime helpers to every fixture-driven test" {
			t.Errorf("lesson 1 = %+v, want title 'Pin fixture mtimes' + action", got[0])
		}
		if got[0].body.String == "" || got[0].body.String[0:3] != "Git" {
			t.Errorf("lesson 1 body = %q, want the description WITHOUT the action line", got[0].body.String)
		}
		if got[1].title != "Verify template resolution early" || got[1].action.Valid {
			t.Errorf("lesson 2 = %+v, want title without an action", got[1])
		}
	})

	t.Run("improvements skip placeholder and malformed rows", func(t *testing.T) {
		rows, err := db.Query(`
			SELECT i.text, i.priority FROM retro_improvements i
			  JOIN task_retros r ON r.id = i.retro_id
			 WHERE r.task_id = ? ORDER BY i.id`, taskID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var texts, prios []string
		for rows.Next() {
			var text string
			var prio sql.NullString
			if err := rows.Scan(&text, &prio); err != nil {
				t.Fatal(err)
			}
			texts, prios = append(texts, text), append(prios, prio.String)
		}
		want := []string{"Add a migration checklist to the phase template", "Wire evals into CI"}
		if len(texts) != 2 || texts[0] != want[0] || texts[1] != want[1] {
			t.Errorf("improvements = %v, want %v ({{...}} + malformed rows skipped)", texts, want)
		}
		if len(prios) == 2 && (prios[0] != "high" || prios[1] != "medium") {
			t.Errorf("priorities = %v, want [high medium]", prios)
		}
	})

	t.Run("orchestration loops", func(t *testing.T) {
		var failed, delta string
		if err := db.QueryRow(
			`SELECT failed, brief_delta FROM task_loops WHERE task_id = ? AND loop_n = 1`,
			taskID).Scan(&failed, &delta); err != nil {
			t.Fatalf("loop 1: %v", err)
		}
		if failed != "go test ./internal/api — TestRetroAgents wanted 2 agents, got 1" {
			t.Errorf("loop 1 failed = %q", failed)
		}
		if delta != "added the missing subagent_start fixture row to the brief" {
			t.Errorf("loop 1 brief_delta = %q", delta)
		}
		if got := count(t, db, `SELECT COUNT(*) FROM task_loops WHERE task_id = ?`, taskID); got != 2 {
			t.Errorf("loops = %d, want 2", got)
		}
	})

	t.Run("ledger delegations normalized", func(t *testing.T) {
		rows, err := db.Query(
			`SELECT seq, agent, verdict FROM task_delegations WHERE task_id = ? ORDER BY seq`, taskID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		type del struct {
			seq            int
			agent, verdict string
		}
		var got []del
		for rows.Next() {
			var d del
			if err := rows.Scan(&d.seq, &d.agent, &d.verdict); err != nil {
				t.Fatal(err)
			}
			got = append(got, d)
		}
		want := []del{
			{1, "context-gatherer", "OK"}, // '@core:context-gatherer' folded
			{2, "implementation-agent", "RE-DISPATCH"},
			{3, "implementation-agent", "OK"},
		}
		if len(got) != len(want) {
			t.Fatalf("delegations = %+v, want %+v (malformed + empty-agent rows skipped)", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("delegation[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}

// tempTaskWorkspace builds a minimal one-task workspace in a temp dir so the
// hash-gate test can mutate artifact files freely.
func tempTaskWorkspace(t *testing.T) (root, taskDir string) {
	t.Helper()
	root = t.TempDir()
	taskDir = filepath.Join(root, "tempproj", "workspace", "working", "2026", "07", "10", "gate-task")
	for _, d := range []string{filepath.Join(taskDir, "phases"), filepath.Join(taskDir, "logs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(taskDir, "README.md"), "# Gate task\n\n- **Статус**: active\n")
	writeFile(t, filepath.Join(taskDir, "phases", "09-retrospective.md"),
		"## 📊 Task Metrics\n\n| Metric | Estimated | Actual | Variance |\n|---|---|---|---|\n"+
			"| **Duration** | 2h | 4h | +100% |\n\n## 💡 Lessons Learned\n\n### Lesson 1: First lesson\n\nBody one.\n")
	writeFile(t, filepath.Join(taskDir, "ORCHESTRATION.md"),
		"## Loop 1 — corrected instructions\n- Failed: lint\n- Brief delta: fix imports\n")
	writeFile(t, filepath.Join(taskDir, "logs", "agents.md"),
		"| Agent | Phase | Verdict | Artifact |\n|---|---|---|---|\n| @core:debugger | 3 | OK | — |\n")
	return root, taskDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// artifactState snapshots the row ids + gate hashes that must stay frozen
// across a no-change rescan.
func artifactState(t *testing.T, db *sql.DB) (lessonIDs []int64, hashes map[string]string, parsedAt map[string]string) {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM retro_lessons ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		lessonIDs = append(lessonIDs, id)
	}
	hashes, parsedAt = map[string]string{}, map[string]string{}
	arows, err := db.Query(`SELECT kind, content_hash, parsed_at FROM task_artifacts`)
	if err != nil {
		t.Fatal(err)
	}
	defer arows.Close()
	for arows.Next() {
		var kind, hash, at string
		if err := arows.Scan(&kind, &hash, &at); err != nil {
			t.Fatal(err)
		}
		hashes[kind], parsedAt[kind] = hash, at
	}
	return lessonIDs, hashes, parsedAt
}

// TestArtifactGatePathRefreshOnMove pins the M1 contract: when a task dir
// moves working → archive with unchanged content (agent-work.sh complete), the
// hash gate still short-circuits the parse — same hashes, same parsed_at, same
// child row ids — but the gate rows' PATH must follow the file to its new
// location.
func TestArtifactGatePathRefreshOnMove(t *testing.T) {
	db := testDB(t)
	root, taskDir := tempTaskWorkspace(t)

	scanRoot := func() Stats {
		t.Helper()
		stats, err := New(db, Config{WorkspaceRoot: root}).Scan()
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		return stats
	}

	first := scanRoot()
	if first.Retros != 1 || first.Loops != 1 || first.Delegations != 1 {
		t.Fatalf("first scan = %+v, want retros/loops/delegations 1/1/1", first)
	}
	ids1, hashes1, at1 := artifactState(t, db)

	gatePaths := func() map[string]string {
		t.Helper()
		rows, err := db.Query(`SELECT kind, path FROM task_artifacts`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var kind, path string
			if err := rows.Scan(&kind, &path); err != nil {
				t.Fatal(err)
			}
			out[kind] = path
		}
		return out
	}
	paths1 := gatePaths()

	// Move the whole task dir working → archive, contents untouched.
	archDir := filepath.Join(root, "tempproj", "workspace", "archive", "2026", "07", "10", "gate-task")
	if err := os.MkdirAll(filepath.Dir(archDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(taskDir, archDir); err != nil {
		t.Fatal(err)
	}

	second := scanRoot()
	if second.Retros != 1 || second.Loops != 1 || second.Delegations != 1 {
		t.Errorf("post-move scan = %+v, want same totals", second)
	}
	ids2, hashes2, at2 := artifactState(t, db)
	if len(ids2) != len(ids1) || (len(ids1) == 1 && ids2[0] != ids1[0]) {
		t.Errorf("lesson ids changed on a content-unchanged move: %v → %v", ids1, ids2)
	}
	paths2 := gatePaths()
	for kind, old := range paths1 {
		if hashes2[kind] != hashes1[kind] {
			t.Errorf("gate hash for %s changed on a content-unchanged move", kind)
		}
		if at2[kind] != at1[kind] {
			t.Errorf("parsed_at for %s changed on a content-unchanged move (file was reparsed)", kind)
		}
		got := paths2[kind]
		if got == old {
			t.Errorf("gate path for %s not refreshed after the move: still %q", kind, got)
		}
		if !strings.Contains(got, filepath.Join("workspace", "archive")) {
			t.Errorf("gate path for %s = %q, want the archive location", kind, got)
		}
	}
}

func TestArtifactsHashGateAndReparse(t *testing.T) {
	db := testDB(t)
	root, taskDir := tempTaskWorkspace(t)

	scanRoot := func() Stats {
		t.Helper()
		stats, err := New(db, Config{WorkspaceRoot: root}).Scan()
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		return stats
	}

	first := scanRoot()
	if first.Retros != 1 || first.Loops != 1 || first.Delegations != 1 {
		t.Fatalf("first scan = %+v, want retros/loops/delegations 1/1/1", first)
	}
	ids1, hashes1, at1 := artifactState(t, db)
	if len(ids1) != 1 || len(hashes1) != 3 {
		t.Fatalf("state = ids %v hashes %v, want 1 lesson + 3 gate rows", ids1, hashes1)
	}

	// Second scan with unchanged files: a no-op — same counts, same row ids,
	// same gate hashes AND parsed_at (nothing was reparsed).
	// Warnings converge 1 → 0 (the first pass creates the projects row the
	// second pass then resolves through) — compare everything BUT warnings.
	second := scanRoot()
	first.Warnings, second.Warnings = 0, 0
	if first != second {
		t.Errorf("rescan drifted: first %+v, second %+v", first, second)
	}
	ids2, hashes2, at2 := artifactState(t, db)
	if len(ids2) != 1 || ids2[0] != ids1[0] {
		t.Errorf("lesson ids changed on a no-op rescan: %v → %v", ids1, ids2)
	}
	for kind, h := range hashes1 {
		if hashes2[kind] != h {
			t.Errorf("gate hash for %s changed on a no-op rescan", kind)
		}
		if at2[kind] != at1[kind] {
			t.Errorf("parsed_at for %s changed on a no-op rescan (file was reparsed)", kind)
		}
	}

	// Modify the retro file → ONLY that artifact reparses: new lesson set (new
	// row ids), updated duration, updated gate hash; the other gates stay put.
	writeFile(t, filepath.Join(taskDir, "phases", "09-retrospective.md"),
		"## 📊 Task Metrics\n\n| Metric | Estimated | Actual | Variance |\n|---|---|---|---|\n"+
			"| **Duration** | 3h | 3h | 0% |\n\n## 💡 Lessons Learned\n\n"+
			"### Lesson 1: Rewritten lesson\n\nNew body.\n\n### Lesson 2: Added lesson\n\nMore.\n")
	third := scanRoot()
	if third.Retros != 1 || third.Loops != 1 || third.Delegations != 1 {
		t.Errorf("third scan = %+v, want same totals", third)
	}

	var est, act float64
	var title string
	if err := db.QueryRow(`SELECT estimated_hours, actual_hours FROM task_retros`).Scan(&est, &act); err != nil {
		t.Fatal(err)
	}
	if est != 3 || act != 3 {
		t.Errorf("after modify: est/act = %v/%v, want 3/3 (file reparsed)", est, act)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM retro_lessons`); got != 2 {
		t.Errorf("lessons after modify = %d, want 2 (delete + reinsert)", got)
	}
	if err := db.QueryRow(`SELECT title FROM retro_lessons ORDER BY seq LIMIT 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Rewritten lesson" {
		t.Errorf("lesson title = %q, want 'Rewritten lesson'", title)
	}
	_, hashes3, _ := artifactState(t, db)
	if hashes3["retro"] == hashes1["retro"] {
		t.Errorf("retro gate hash unchanged after the file was modified")
	}
	for _, kind := range []string{"orchestration", "agents_log"} {
		if hashes3[kind] != hashes1[kind] {
			t.Errorf("%s gate hash changed although its file did not", kind)
		}
	}
}
