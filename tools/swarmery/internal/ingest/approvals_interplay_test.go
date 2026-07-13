package ingest

// Phase 2 (approvals) ↔ ingest interplay:
//   - a session first minted by the hooks channel (source='hook') is promoted
//     to 'both' when its JSONL transcript shows up — never overwritten to
//     'jsonl';
//   - status='waiting_approval' is owned by the approvals layer and must
//     survive a JSONL tail re-upsert.

import (
	"path/filepath"
	"testing"
)

func TestUpsertPromotesHookSourceToBoth(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(fixtures, "simple-session.jsonl")

	// First contact came through the hooks channel (as internal/approvals
	// creates it): project by cwd + session with source='hook'.
	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen) VALUES ('/tmp/demo', '-tmp-demo', 'demo', '2026-07-13T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, source)
		 VALUES (1, '0f1e2d3c-4b5a-4968-8776-655443322110', '/tmp/demo', 'waiting_approval',
		         '2026-07-13T00:00:00.000Z', 'hook')`); err != nil {
		t.Fatal(err)
	}

	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	var source, status string
	if err := db.QueryRow(
		`SELECT source, status FROM sessions WHERE session_uuid = '0f1e2d3c-4b5a-4968-8776-655443322110'`).
		Scan(&source, &status); err != nil {
		t.Fatal(err)
	}
	if source != "both" {
		t.Errorf("source = %q, want 'both' (hook + jsonl)", source)
	}
	if status != "waiting_approval" {
		t.Errorf("status = %q — ingest must not steal waiting_approval from the approvals layer", status)
	}

	// Plain jsonl sessions stay 'jsonl' on re-ingest.
	if _, err := File(db, path); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(
		`SELECT source FROM sessions WHERE session_uuid = '0f1e2d3c-4b5a-4968-8776-655443322110'`).
		Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "both" {
		t.Errorf("source after second ingest = %q, want 'both' (stable)", source)
	}
}

// TestTailHealsHookStubWithUnknownCwd: hook-first ordering where the hook
// stdin carried NO cwd — the approvals layer parks the session on the
// '(unknown)' placeholder; the first JSONL tail batch must re-attribute it
// to the real project and backfill cwd, keeping the hook-side started_at.
func TestTailHealsHookStubWithUnknownCwd(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(fixtures, "simple-session.jsonl")

	// As internal/approvals mints it for an empty hook cwd.
	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen, last_activity)
		 VALUES ('(unknown)', '(unknown)', '(unknown)', '2026-07-13T00:00:00.000Z', '2026-07-13T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, source)
		 VALUES (1, '0f1e2d3c-4b5a-4968-8776-655443322110', '(unknown)', 'waiting_approval',
		         '2026-07-13T00:00:00.000Z', 'hook')`); err != nil {
		t.Fatal(err)
	}

	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	var projectPath, cwd, startedAt, source string
	if err := db.QueryRow(
		`SELECT p.path, s.cwd, s.started_at, s.source
		 FROM sessions s JOIN projects p ON p.id = s.project_id
		 WHERE s.session_uuid = '0f1e2d3c-4b5a-4968-8776-655443322110'`).
		Scan(&projectPath, &cwd, &startedAt, &source); err != nil {
		t.Fatal(err)
	}
	if projectPath != "/Users/user/work/example-app" || cwd != "/Users/user/work/example-app" {
		t.Errorf("attribution = %q/%q, want /Users/user/work/example-app", projectPath, cwd)
	}
	if startedAt != "2026-07-13T00:00:00.000Z" {
		t.Errorf("started_at = %q — the hook-side value was good and must survive", startedAt)
	}
	if source != "both" {
		t.Errorf("source = %q, want 'both'", source)
	}
}
