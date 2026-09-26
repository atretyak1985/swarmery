package dispatch

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// preMigrationDB opens a raw store migrated only through 0065 — the schema in
// which a captured card's opening-prompt quote still lived in its prompt as
// prose after "That session opened with:".
func preMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre0066.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := store.MigrateUpTo(db, 65); err != nil {
		t.Fatalf("migrate through 0065: %v", err)
	}
	// A real .git marker: admit() resolves the project path through
	// repopath.ResolveTrusted before reaching the (stubbed) worktree manager.
	repo := mkRepo(t, filepath.Join(t.TempDir(), "p"))
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,?,'p','2026-01-01T00:00:00Z')`, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions(id, project_id, session_uuid, status, started_at)
		 VALUES(5, 1, 'origin-sess', 'completed', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestDispatchedPromptCarriesBackfilledQuoteOnce is the other half of 0066's
// "move, not copy" guarantee, seen from the dispatcher: after the migration
// lifts the quote out of a pre-0066 card's prompt, the prompt the runner
// receives contains that quote exactly once — from the provenance block, not
// from the body — and is recorded verbatim on the card as dispatched_prompt.
// Without this test the backfill could be reverted to a copy and every unit
// test would still pass, while every dispatched run read the quote twice.
func TestDispatchedPromptCarriesBackfilledQuoteOnce(t *testing.T) {
	db := preMigrationDB(t)
	const quote = "Refactor the retry helper and add tests for the backoff."
	oldPrompt := "Extract the retry helper into internal/retry\n\n---\nCaptured from session origin-sess" +
		"\n\nThat session opened with:\n" + quote
	res, err := db.Exec(`
		INSERT INTO tasks(project_id, title, prompt, priority, status, created_at,
		                  source, external_id, board_column, file_scope, dependencies,
		                  origin, origin_session_id, capture_key)
		VALUES(1, 'Extract the retry helper', ?, 5, 'queued', '2026-07-24T00:00:00.000Z',
		       'queue', 'T-cap001', 'todo', '[]', '[]', 'session', 5, 'todo:origin-sess:abc')`, oldPrompt)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("apply 0066: %v", err)
	}

	wt := &stubWt{}
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "Done.")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, wt)
	s.Schedule()
	if r.count() != 1 {
		t.Fatalf("runner started %d times, want 1", r.count())
	}

	sent := r.spec(0).Prompt
	if n := strings.Count(sent, quote); n != 1 {
		t.Errorf("runner prompt carries the quote %d times, want exactly 1:\n%s", n, sent)
	}
	if !strings.Contains(sent, "--- PROVENANCE (captured card) ---") {
		t.Errorf("runner prompt lacks the provenance block:\n%s", sent)
	}
	if strings.Contains(sent, "That session opened with:") {
		t.Errorf("runner prompt still carries the pre-0066 prose marker:\n%s", sent)
	}
	if !strings.HasPrefix(sent, "Extract the retry helper into internal/retry") {
		t.Errorf("runner prompt should open with the card body:\n%s", sent)
	}

	var recorded sql.NullString
	if err := db.QueryRow(`SELECT dispatched_prompt FROM tasks WHERE id=?`, id).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if !recorded.Valid || recorded.String != sent {
		t.Errorf("dispatched_prompt was not recorded verbatim:\n got %q\nwant %q", recorded.String, sent)
	}
}

// TestManualCardDispatchesWithoutProvenance: a hand-written card has no
// origin_* columns, so its runner prompt is the body plus the contract and
// nothing in between — the pre-0066 shape, byte for byte.
func TestManualCardDispatchesWithoutProvenance(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "Done.")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	insertTask(t, db, "T-man", taskOpts{})
	s.Schedule()
	if r.count() != 1 {
		t.Fatalf("runner started %d times, want 1", r.count())
	}
	if sent := r.spec(0).Prompt; strings.Contains(sent, "PROVENANCE") {
		t.Errorf("manual card grew a provenance block:\n%s", sent)
	}
	if got := provenanceBlock("", nil); got != "" {
		t.Errorf("provenanceBlock(empty) = %q, want empty", got)
	}
	if got := provenanceBlock("  ", []string{"a.go"}); !strings.Contains(got, "Files that session touched: a.go") || strings.Contains(got, "was asked") {
		t.Errorf("files-only block = %q", got)
	}
}
