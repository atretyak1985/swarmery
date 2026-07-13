package ingest

// Stub-session attribution regressions (live bug: sessions minted before
// their cwd was known stayed on the '(unknown)' project with empty
// started_at/cwd forever):
//   - header-only first tail batch → stub, healed by the next tail batch;
//   - hook-first stub (source='hook', real cwd) → correct project from the
//     start, tail promotes to 'both' without touching attribution;
//   - HealStubSessions: startup pass re-attributes pre-existing stub rows
//     from their transcript files and drops the orphaned '(unknown)' project.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	simpleUUID = "0f1e2d3c-4b5a-4968-8776-655443322110"
	simpleCWD  = "/Users/user/work/example-app"
)

// splitFixture copies the simple-session fixture into dir, returning the
// path plus the fixture body split at the first envelope record: the header
// prefix (last-prompt / mode / permission-mode — no cwd, no timestamps) and
// the rest.
func splitFixture(t *testing.T, dir string) (path, header, rest string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtures, "simple-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if len(lines) < 4 {
		t.Fatalf("fixture too short: %d lines", len(lines))
	}
	return filepath.Join(dir, simpleUUID+".jsonl"),
		strings.Join(lines[:3], ""), strings.Join(lines[3:], "")
}

func sessionRow(t *testing.T, db *sql.DB, uuid string) (projectPath, cwd, startedAt, branch, source string) {
	t.Helper()
	var b sql.NullString
	if err := db.QueryRow(
		`SELECT p.path, s.cwd, s.started_at, s.git_branch, s.source
		 FROM sessions s JOIN projects p ON p.id = s.project_id
		 WHERE s.session_uuid = ?`, uuid).
		Scan(&projectPath, &cwd, &startedAt, &b, &source); err != nil {
		t.Fatal(err)
	}
	return projectPath, cwd, startedAt, b.String, source
}

// TestTailHealsHeaderOnlyStub reproduces the live bug end-to-end: the first
// tail batch contains only header records (no cwd/timestamp) and mints an
// '(unknown)' stub; the next batch carries envelope records and must heal
// project/cwd/started_at/git_branch in place.
func TestTailHealsHeaderOnlyStub(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path, header, rest := splitFixture(t, dir)

	if err := os.WriteFile(path, []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TailFile(db, path, DefaultThresholds()); err != nil {
		t.Fatalf("tail header batch: %v", err)
	}
	projectPath, cwd, startedAt, _, _ := sessionRow(t, db, simpleUUID)
	if projectPath != UnknownProjectPath || cwd != UnknownProjectPath || startedAt != "" {
		t.Fatalf("stub precondition failed: project=%q cwd=%q started_at=%q",
			projectPath, cwd, startedAt)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rest); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := TailFile(db, path, DefaultThresholds()); err != nil {
		t.Fatalf("tail rest batch: %v", err)
	}

	projectPath, cwd, startedAt, _, _ = sessionRow(t, db, simpleUUID)
	if projectPath != simpleCWD {
		t.Errorf("project = %q, want %q (healed from envelope cwd)", projectPath, simpleCWD)
	}
	if cwd != simpleCWD {
		t.Errorf("cwd = %q, want %q", cwd, simpleCWD)
	}
	if startedAt == "" {
		t.Error("started_at still empty after heal")
	}
	var slug, name string
	if err := db.QueryRow(
		`SELECT slug, name FROM projects WHERE path = ?`, simpleCWD).Scan(&slug, &name); err != nil {
		t.Fatal(err)
	}
	if slug != SlugForPath(simpleCWD) || name != "example-app" {
		t.Errorf("project slug/name = %q/%q, want %q/example-app", slug, name, SlugForPath(simpleCWD))
	}
}

// TestUpsertNeverOverwritesGoodAttribution: a mid-file tail batch must not
// clobber existing project/cwd/started_at with batch-local values.
func TestUpsertNeverOverwritesGoodAttribution(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(fixtures, "simple-session.jsonl")
	if _, err := File(db, path); err != nil {
		t.Fatal(err)
	}
	before := [5]string{}
	before[0], before[1], before[2], before[3], before[4] = sessionRow(t, db, simpleUUID)

	if _, err := File(db, path); err != nil { // full re-ingest = worst case
		t.Fatal(err)
	}
	after := [5]string{}
	after[0], after[1], after[2], after[3], after[4] = sessionRow(t, db, simpleUUID)
	if before != after {
		t.Errorf("attribution drifted on re-ingest: before=%v after=%v", before, after)
	}
}

// TestHealStubSessions: the startup pass re-attributes pre-existing stub rows
// (hook-first or header-batch origin) from their transcript files, backfills
// empty fields, and deletes the '(unknown)' project once orphaned.
func TestHealStubSessions(t *testing.T) {
	db := testDB(t)
	root := t.TempDir()
	projDir := filepath.Join(root, SlugForPath(simpleCWD))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(fixtures, "simple-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, simpleUUID+".jsonl"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// Live-DB shape: '(unknown)' project with empty first_seen, stub session
	// with empty started_at/placeholder cwd, plus one stub whose transcript
	// does not exist (must stay, and must keep the placeholder project alive).
	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen) VALUES (?, ?, ?, '')`,
		UnknownProjectPath, UnknownProjectPath, UnknownProjectPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, source)
		 VALUES (1, ?, ?, 'completed', '', 'jsonl'),
		        (1, 'aaaaaaaa-0000-4000-8000-000000000001', ?, 'completed', '', 'jsonl')`,
		simpleUUID, UnknownProjectPath, UnknownProjectPath); err != nil {
		t.Fatal(err)
	}

	healed, err := HealStubSessions(db, root)
	if err != nil {
		t.Fatalf("heal: %v", err)
	}
	if len(healed) != 1 {
		t.Fatalf("healed = %v, want exactly the recoverable session", healed)
	}

	projectPath, cwd, startedAt, branch, _ := sessionRow(t, db, simpleUUID)
	if projectPath != simpleCWD || cwd != simpleCWD {
		t.Errorf("attribution = %q/%q, want %q", projectPath, cwd, simpleCWD)
	}
	if startedAt == "" {
		t.Error("started_at not backfilled")
	}
	if branch == "" {
		t.Error("git_branch not backfilled")
	}
	// Unrecoverable stub keeps the placeholder project alive.
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE path = ?`, UnknownProjectPath); got != 1 {
		t.Errorf("placeholder projects = %d, want 1 (still referenced)", got)
	}

	// Second pass: idempotent, and once the last stub is deleted the
	// placeholder project goes too.
	if _, err := db.Exec(
		`DELETE FROM sessions WHERE session_uuid = 'aaaaaaaa-0000-4000-8000-000000000001'`); err != nil {
		t.Fatal(err)
	}
	healed, err = HealStubSessions(db, root)
	if err != nil {
		t.Fatalf("heal (2nd pass): %v", err)
	}
	if len(healed) != 0 {
		t.Errorf("second pass healed %v, want none (idempotent)", healed)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE path = ?`, UnknownProjectPath); got != 0 {
		t.Errorf("placeholder projects = %d, want 0 (orphaned → deleted)", got)
	}
}
