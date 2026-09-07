package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// Every column that parks a daemon-minted session uuid must be a pending source or
// an explicit, still-valid exemption. Scans the LIVE schema (after all migrations),
// so a renamed table or a new engine's column trips it without anyone remembering
// to update a list by hand.
func TestPendingSessionSources_CoverEverySessionUUIDColumn(t *testing.T) {
	_, db := testServerWithDB(t)
	rows, err := db.Query(`
		SELECT m.name || '.' || p.name
		  FROM sqlite_master m, pragma_table_info(m.name) p
		 WHERE m.type = 'table' AND p.name LIKE '%session_uuid%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	schema := map[string]bool{}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		schema[col] = true
	}
	if len(schema) < 3 {
		t.Fatalf("schema scan found only %d uuid columns — the scan itself is broken", len(schema))
	}

	covered := map[string]bool{}
	for _, src := range pendingSessionSources {
		key := src.Table + "." + src.Column
		if !schema[key] {
			t.Errorf("pending source %s names a column that is not in the schema", key)
		}
		covered[key] = true
	}
	for key, why := range pendingSessionExempt {
		if !schema[key] {
			t.Errorf("stale exemption %s (%s): the column no longer exists", key, why)
		}
		if covered[key] {
			t.Errorf("%s is both a source and an exemption", key)
		}
		covered[key] = true
	}
	for key := range schema {
		if !covered[key] {
			t.Errorf("%s parks a session uuid but GET /api/sessions/{uuid} does not know it — add a pendingSessionSource, or an exemption with the reason", key)
		}
	}

	// Every source query must be valid SQL against the live schema: a typo here
	// would turn the pending path into a 500 the first time that engine ran.
	for _, src := range pendingSessionSources {
		var a, b, c, e, f any
		var d sql.NullInt64
		err := db.QueryRow(src.query, "no-such-uuid").Scan(&a, &b, &c, &d, &e, &f)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("source %s: query against an empty table = %v, want ErrNoRows", src.Kind, err)
		}
	}
}

func getStatus(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("GET %s: decode: %v", url, err)
		}
	}
	return resp.StatusCode
}

// The reported defect: a phase run is admitted, the Plans page links to its
// session, and the detail page 404s until ingest catches up. Now: 202 with the
// phase's context while running; 200 the moment the row exists; 404 only for a
// uuid nobody minted.
func TestGetSession_PendingPhaseRun(t *testing.T) {
	srv, db := testServerWithDB(t)
	mustExecT(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id)
		VALUES (1, 'My Epic', 'goal', 'running', '2026-09-07T15:00:00Z', 'workspace', '2026-09-07-my-epic')`)
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM tasks WHERE external_id='2026-09-07-my-epic'`).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	mustExecT(t, db, `INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, depends_on, run_state, run_session_uuid, run_started_at)
		VALUES (?, 5, 'Phase 5 — Lesson', '/ws/plan/phase-5.md', '[]', 'running', 'uuid-pending-1', '2026-09-07T15:00:43Z')`, taskID)

	if got := getStatus(t, srv.URL+"/api/sessions/uuid-nobody-minted", nil); got != http.StatusNotFound {
		t.Fatalf("unknown uuid: status = %d, want 404", got)
	}
	if got := getStatus(t, srv.URL+"/api/sessions/999999", nil); got != http.StatusNotFound {
		t.Fatalf("unknown integer id: status = %d, want 404 (never a pending lookup)", got)
	}

	var p pendingSessionDTO
	if got := getStatus(t, srv.URL+"/api/sessions/uuid-pending-1", &p); got != http.StatusAccepted {
		t.Fatalf("minted uuid before ingest: status = %d, want 202", got)
	}
	if !p.Pending || p.Source != "phase" || !p.Running || p.Label != "Phase 5 — Lesson" || p.RefID == 0 {
		t.Fatalf("pending body = %+v, want pending phase run, running, labelled", p)
	}
	if p.TaskID == nil || *p.TaskID != taskID {
		t.Fatalf("pending taskId = %v, want %d", p.TaskID, taskID)
	}
	if p.StartedAt == nil || *p.StartedAt != "2026-09-07T15:00:43Z" {
		t.Fatalf("pending startedAt = %v", p.StartedAt)
	}

	// The run ended (state stamped) but no transcript ever landed: still known,
	// but no longer running — the page must stop waiting.
	mustExecT(t, db, `UPDATE epic_phases SET run_state='failed' WHERE run_session_uuid='uuid-pending-1'`)
	if got := getStatus(t, srv.URL+"/api/sessions/uuid-pending-1", &p); got != http.StatusAccepted || p.Running {
		t.Fatalf("ended without transcript: status = %d running = %v, want 202 and not running", got, p.Running)
	}

	// Ingest lands the row: the same URL is now the ordinary 200 detail.
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, status, started_at, source)
		VALUES (1, 'uuid-pending-1', 'active', '2026-09-07T15:00:45Z', 'jsonl')`)
	var detail struct {
		SessionUUID string `json:"sessionUuid"`
	}
	if got := getStatus(t, srv.URL+"/api/sessions/uuid-pending-1", &detail); got != http.StatusOK || detail.SessionUUID != "uuid-pending-1" {
		t.Fatalf("after ingest: status = %d body = %+v, want 200 detail", got, detail)
	}
}

// Each source answers for its own column — the dispatch and verify shapes have
// nullable task ids and derived labels, which is where a scan would break.
func TestGetSession_PendingOtherSources(t *testing.T) {
	srv, db := testServerWithDB(t)
	mustExecT(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at, source, board_column, dispatch_session_uuid, started_at)
		VALUES (1, 'Fix flaky test', 'p', 'running', '2026-09-07T15:00:00Z', 'queue', 'in_progress', 'uuid-dispatch', '2026-09-07T15:01:00Z')`)
	mustExecT(t, db, `INSERT INTO verification_runs (target_key, task_id, verify_session_uuid, status, started_at)
		VALUES ('phase:7', NULL, 'uuid-verify', 'running', '2026-09-07T15:02:00Z')`)
	mustExecT(t, db, `INSERT INTO planning_sessions (project_id, session_uuid, status, idea, created_at, updated_at)
		VALUES (1, 'uuid-planning', 'generating', 'a notch widget', '2026-09-07T15:03:00Z', '2026-09-07T15:03:00Z')`)

	cases := []struct {
		uuid, source, label string
		taskNil             bool
	}{
		{"uuid-dispatch", "dispatch", "Fix flaky test", false},
		{"uuid-verify", "verify", "verification of phase:7", true},
		{"uuid-planning", "planning", "a notch widget", true},
	}
	for _, c := range cases {
		var p pendingSessionDTO
		if got := getStatus(t, srv.URL+"/api/sessions/"+c.uuid, &p); got != http.StatusAccepted {
			t.Errorf("%s: status = %d, want 202", c.uuid, got)
			continue
		}
		if p.Source != c.source || p.Label != c.label || !p.Running {
			t.Errorf("%s: body = %+v, want source %q label %q running", c.uuid, p, c.source, c.label)
		}
		if (p.TaskID == nil) != c.taskNil {
			t.Errorf("%s: taskId = %v, want nil=%v", c.uuid, p.TaskID, c.taskNil)
		}
	}
}

func mustExecT(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
