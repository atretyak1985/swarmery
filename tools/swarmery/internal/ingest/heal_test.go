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

// TestTailDefersHeaderOnlyBatch: the first tail batch contains only header
// records (last-prompt / mode / permission-mode — no cwd, no timestamp).
// Such a batch is NOT evidence that a session ran, so it mints nothing and
// leaves the offset where it was; the next batch carries envelope records and
// the session is created once, fully attributed.
//
// This used to mint an '(unknown)' stub that a later batch healed in place.
// Minting was dropped because the same code path turned every timestamp-less
// transcript (Claude Code writes ai-title-only files) into a session frozen
// in 'active' forever — see ErrNoSessionEvidence and phantom_session_test.go.
// Stub healing itself still matters for hook-created rows and is covered by
// TestHealStubSessions.
func TestTailDefersHeaderOnlyBatch(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path, header, rest := splitFixture(t, dir)

	if err := os.WriteFile(path, []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := TailFile(db, path, "", DefaultThresholds())
	if err != nil {
		t.Fatalf("tail header batch: %v", err)
	}
	if res.NextOffset != 0 {
		t.Errorf("NextOffset = %d, want 0 — the header batch must stay re-readable", res.NextOffset)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM sessions`); got != 0 {
		t.Fatalf("sessions after header-only batch = %d, want 0", got)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rest); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := TailFile(db, path, "", DefaultThresholds()); err != nil {
		t.Fatalf("tail rest batch: %v", err)
	}

	projectPath, cwd, startedAt, _, _ := sessionRow(t, db, simpleUUID)
	if projectPath != simpleCWD {
		t.Errorf("project = %q, want %q (from envelope cwd)", projectPath, simpleCWD)
	}
	if cwd != simpleCWD {
		t.Errorf("cwd = %q, want %q", cwd, simpleCWD)
	}
	if startedAt == "" {
		t.Error("started_at empty — the deferred header batch must be re-read with the rest")
	}
	var slug, name string
	if err := db.QueryRow(
		`SELECT slug, name FROM projects WHERE path = ?`, simpleCWD).Scan(&slug, &name); err != nil {
		t.Fatal(err)
	}
	if slug != SlugForPath(simpleCWD) || name != "example-app" {
		t.Errorf("project slug/name = %q/%q, want %q/example-app", slug, name, SlugForPath(simpleCWD))
	}
	// The re-point orphaned the '(unknown)' placeholder — it must be dropped
	// in the same batch, not linger as a ghost row until the next restart.
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE path = ?`, UnknownProjectPath); got != 0 {
		t.Errorf("placeholder projects = %d, want 0 (orphaned by in-batch heal)", got)
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

	healed, err := HealStubSessions(db, []string{root}, nil)
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
	healed, err = HealStubSessions(db, []string{root}, nil)
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

// TestHealCreateDiffs: file_changes rows written before Write-create diffs
// were synthesised from `content` sit with diff=” / +0 forever (unchanged
// transcripts are offset no-ops). The startup pass re-reads the transcript,
// matches each row's tool_use_id (the dedup_key suffix) against the
// tool_result records and fills diff + additions. Idempotent.
func TestHealCreateDiffs(t *testing.T) {
	const (
		heavyUUID = "a9b8c7d6-e5f4-4321-9876-fedcba098765"
		heavyCWD  = "/Users/user/work/example-app"
	)
	db := testDB(t)
	root := t.TempDir()
	projDir := filepath.Join(root, SlugForPath(heavyCWD))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(fixtures, "tool-heavy-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, heavyUUID+".jsonl")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Legacy shape: the create row exists but carries no diff.
	if _, err := db.Exec(
		`UPDATE file_changes SET diff = '', additions = 0 WHERE change_type = 'create'`); err != nil {
		t.Fatal(err)
	}

	healed, err := HealCreateDiffs(db, []string{root})
	if err != nil {
		t.Fatalf("heal: %v", err)
	}
	if healed != 1 {
		t.Fatalf("healed = %d, want 1", healed)
	}
	var adds int
	var diff string
	if err := db.QueryRow(
		`SELECT additions, diff FROM file_changes WHERE change_type = 'create'`).Scan(&adds, &diff); err != nil {
		t.Fatal(err)
	}
	if adds != 4 || !strings.HasPrefix(diff, "@@ -0,0 +1,4 @@\n+# Changelog\n") {
		t.Errorf("after heal: +%d diff=%q, want +4 and the synthesised hunk", adds, diff)
	}

	healed, err = HealCreateDiffs(db, []string{root})
	if err != nil {
		t.Fatalf("heal (2nd pass): %v", err)
	}
	if healed != 0 {
		t.Errorf("second pass healed %d, want 0 (idempotent)", healed)
	}
}
