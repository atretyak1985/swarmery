package evals

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const fixture = "../store/testdata/promptfoo-results.json"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "evals.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedAgent(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO agents (id, name, scope, file_path) VALUES
		(1, 'tech-lead', 'global', '/plugins/core/agents/tech-lead.md'),
		(2, 'debugger',  'global', '/plugins/core/agents/debugger.md')`)
	mustExec(`INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at) VALUES
		(10, 1, 'aaa', '# tech-lead v1', '2026-07-01T00:00:00Z'),
		(11, 1, 'bbb', '# tech-lead v2', '2026-07-10T00:00:00Z')`)
	mustExec(`UPDATE agents SET current_version_id = 11 WHERE id = 1`)
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func TestImport(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db)

	res, err := Import(db, "tech-lead", fixture)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Skipped {
		t.Fatalf("first import reported skipped")
	}
	if res.Suite != "tech-lead-routing" || res.Passed != 1 || res.Failed != 2 || res.Cases != 3 {
		t.Errorf("res = %+v, want suite tech-lead-routing passed 1 failed 2 cases 3", res)
	}
	if res.StartedAt != "2026-07-20T10:00:00.000Z" {
		t.Errorf("started_at = %q, want the file-level results.timestamp", res.StartedAt)
	}

	// Run row: stats-derived counts + the agent's CURRENT version.
	var passed, failed, versionID int64
	var started, finished string
	if err := db.QueryRow(
		`SELECT passed, failed, agent_version_id, started_at, finished_at FROM eval_runs`).
		Scan(&passed, &failed, &versionID, &started, &finished); err != nil {
		t.Fatalf("run row: %v", err)
	}
	if passed != 1 || failed != 2 || versionID != 11 {
		t.Errorf("run = passed %d failed %d version %d, want 1/2/11", passed, failed, versionID)
	}

	// Cases keyed by prompt text; results carry pass/fail/error + notes.
	if got := count(t, db, `SELECT COUNT(*) FROM eval_cases`); got != 3 {
		t.Errorf("eval_cases = %d, want 3", got)
	}
	statusOf := func(promptFrag string) (status string, notes sql.NullString) {
		t.Helper()
		if err := db.QueryRow(`
			SELECT r.status, r.notes FROM eval_results r
			  JOIN eval_cases c ON c.id = r.case_id
			 WHERE c.prompt LIKE '%' || ? || '%'`, promptFrag).Scan(&status, &notes); err != nil {
			t.Fatalf("result for %q: %v", promptFrag, err)
		}
		return status, notes
	}
	if s, n := statusOf("flaky unit test"); s != "pass" || n.Valid {
		t.Errorf("pass case = %q/%+v, want pass with NULL notes", s, n)
	}
	if s, n := statusOf("deploy the release"); s != "fail" || !strings.Contains(n.String, "guardrail-checker") {
		t.Errorf("fail case = %q/%q, want fail + grading reason", s, n.String)
	}
	if s, n := statusOf("summarize the sprint"); s != "error" || !strings.Contains(n.String, "529") {
		t.Errorf("error case = %q/%q, want error + raw error text", s, n.String)
	}

	// Idempotent re-import: same (suite, started_at) → skip, no new rows.
	again, err := Import(db, "tech-lead", fixture)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if !again.Skipped || again.RunID != res.RunID {
		t.Errorf("re-import = %+v, want skipped with the original run id %d", again, res.RunID)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM eval_runs`); got != 1 {
		t.Errorf("eval_runs after re-import = %d, want 1", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM eval_results`); got != 3 {
		t.Errorf("eval_results after re-import = %d, want 3", got)
	}
}

func TestImportUnknownAgent(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db)

	_, err := Import(db, "nope", fixture)
	if err == nil {
		t.Fatalf("unknown agent must be a hard error")
	}
	for _, want := range []string{"nope", "tech-lead", "debugger"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must mention %q", err, want)
		}
	}
}

func TestImportVersionFallback(t *testing.T) {
	db := testDB(t)
	seedAgent(t, db)
	// debugger has no current_version_id and no versions at all → hard error.
	if _, err := Import(db, "debugger", fixture); err == nil ||
		!strings.Contains(err.Error(), "no recorded version") {
		t.Errorf("versionless agent: err = %v, want 'no recorded version'", err)
	}
	// Give it one version (still no current_version_id) → newest version wins.
	if _, err := db.Exec(`INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at)
		VALUES (20, 2, 'ccc', '# debugger', '2026-07-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	res, err := Import(db, "debugger", fixture)
	if err != nil {
		t.Fatalf("import with fallback version: %v", err)
	}
	var versionID int64
	if err := db.QueryRow(`SELECT agent_version_id FROM eval_runs WHERE id = ?`, res.RunID).
		Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if versionID != 20 {
		t.Errorf("agent_version_id = %d, want the fallback version 20", versionID)
	}
}
