package store

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func boardDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-08-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, session_uuid, status, started_at)
		 VALUES (7, 1, 'sess-uuid', 'completed', '2026-08-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestInsertBoardTaskDefaults(t *testing.T) {
	db := boardDB(t)
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	id, inserted, err := InsertBoardTask(db, BoardTaskInput{
		ProjectID: 1, Title: "  hello  ", Prompt: " do it ", Now: now,
	})
	if err != nil || !inserted || id == 0 {
		t.Fatalf("insert = (%d, %v, %v)", id, inserted, err)
	}
	var (
		title, prompt, status, source, column, origin, extID, scope, labels, deps, created string
		priority                                                                           int
		movedAt, model, key, quote, files                                                  sql.NullString
	)
	if err := db.QueryRow(`
		SELECT title, prompt, status, source, board_column, origin, external_id, file_scope, labels,
		       dependencies, created_at, priority, column_moved_at, model, capture_key, origin_quote, origin_files
		  FROM tasks WHERE id = ?`, id).Scan(&title, &prompt, &status, &source, &column, &origin, &extID,
		&scope, &labels, &deps, &created, &priority, &movedAt, &model, &key, &quote, &files); err != nil {
		t.Fatal(err)
	}
	if title != "hello" || prompt != "do it" {
		t.Errorf("title/prompt = %q/%q, want trimmed", title, prompt)
	}
	if status != "queued" || source != "queue" || column != "triage" || origin != "manual" {
		t.Errorf("row = %s/%s/%s/%s, want queued/queue/triage/manual", status, source, column, origin)
	}
	if !strings.HasPrefix(extID, "T-") || len(extID) != 8 {
		t.Errorf("external_id = %q, want minted T-xxxxxx", extID)
	}
	if scope != "[]" || labels != "[]" || deps != "[]" {
		t.Errorf("lists = %q %q %q, want [] each", scope, labels, deps)
	}
	if created != "2026-08-02T10:00:00.000Z" {
		t.Errorf("created_at = %q", created)
	}
	if priority != NormalPriority {
		t.Errorf("priority = %d, want %d", priority, NormalPriority)
	}
	if movedAt.Valid || model.Valid || key.Valid || quote.Valid || files.Valid {
		t.Errorf("triage/manual card has non-NULL optionals: moved=%v model=%v key=%v quote=%v files=%v",
			movedAt, model, key, quote, files)
	}
}

func TestInsertBoardTaskExplicitFields(t *testing.T) {
	db := boardDB(t)
	sess := int64(7)
	model := "opus"
	id, _, err := InsertBoardTask(db, BoardTaskInput{
		ProjectID: 1, Title: "t", Prompt: "p", Priority: 3, Column: "todo", Origin: "session",
		OriginSessionID: &sess, OriginTurnUUID: "turn-1", OriginQuote: "opened with",
		OriginFiles: []string{"a.go", "b.go"}, CaptureKey: "todo:x:1", ExternalID: "T-fixed1",
		Model: &model, FileScope: []string{"internal/"}, Labels: []string{"jira"}, Dependencies: []string{"T-dep"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		column, origin, extID, turn, quote, files, scope, labels, deps, m, key string
		priority                                                               int
		movedAt                                                                sql.NullString
		originSession                                                          sql.NullInt64
	)
	if err := db.QueryRow(`
		SELECT board_column, origin, external_id, origin_turn_uuid, origin_quote, origin_files, file_scope,
		       labels, dependencies, model, capture_key, priority, column_moved_at, origin_session_id
		  FROM tasks WHERE id = ?`, id).Scan(&column, &origin, &extID, &turn, &quote, &files, &scope,
		&labels, &deps, &m, &key, &priority, &movedAt, &originSession); err != nil {
		t.Fatal(err)
	}
	if column != "todo" || origin != "session" || extID != "T-fixed1" || priority != 3 || m != "opus" || key != "todo:x:1" {
		t.Errorf("row = %s/%s/%s/%d/%s/%s", column, origin, extID, priority, m, key)
	}
	if turn != "turn-1" || quote != "opened with" || files != `["a.go","b.go"]` {
		t.Errorf("provenance = %q/%q/%q", turn, quote, files)
	}
	if scope != `["internal/"]` || labels != `["jira"]` || deps != `["T-dep"]` {
		t.Errorf("lists = %q %q %q", scope, labels, deps)
	}
	if !movedAt.Valid {
		t.Error("a card born in todo must carry column_moved_at")
	}
	if !originSession.Valid || originSession.Int64 != sess {
		t.Errorf("origin_session_id = %v, want %d", originSession, sess)
	}

	// Capture-key replay: no new row, the original id, inserted=false.
	id2, inserted, err := InsertBoardTask(db, BoardTaskInput{
		ProjectID: 1, Title: "other", Prompt: "other", Origin: "session", CaptureKey: "todo:x:1",
	})
	if err != nil || inserted || id2 != id {
		t.Errorf("replay = (%d, %v, %v), want (%d, false, nil)", id2, inserted, err, id)
	}
}

func TestInsertBoardTaskValidation(t *testing.T) {
	db := boardDB(t)
	base := BoardTaskInput{ProjectID: 1, Title: "t", Prompt: "p"}
	cases := map[string]func(in *BoardTaskInput){
		"blank title":    func(in *BoardTaskInput) { in.Title = "  " },
		"empty prompt":   func(in *BoardTaskInput) { in.Prompt = "" },
		"unknown column": func(in *BoardTaskInput) { in.Column = "backlog" },
		"unknown origin": func(in *BoardTaskInput) { in.Origin = "telepathy" },
		"capital origin": func(in *BoardTaskInput) { in.Origin = "Manual" },
	}
	for name, mutate := range cases {
		in := base
		mutate(&in)
		if _, _, err := InsertBoardTask(db, in); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, inserted, err := InsertBoardTask(db, base); err != nil || !inserted {
		t.Errorf("baseline = %v, %v", inserted, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&n); err != nil || n != 1 {
		t.Errorf("tasks = %d (%v), want only the baseline", n, err)
	}
}

func TestValidOriginAndColumn(t *testing.T) {
	for _, o := range []string{"manual", "session", "llm", "verify-fix", "github"} {
		if !ValidOrigin(o) {
			t.Errorf("ValidOrigin(%q) = false", o)
		}
	}
	for _, o := range []string{"", "Session", "queue", "fix"} {
		if ValidOrigin(o) {
			t.Errorf("ValidOrigin(%q) = true", o)
		}
	}
	for _, c := range []string{"triage", "todo", "in_progress", "in_review", "done", "archived"} {
		if !ValidColumn(c) {
			t.Errorf("ValidColumn(%q) = false", c)
		}
	}
	if ValidColumn("") || ValidColumn("Todo") {
		t.Error("ValidColumn accepted an unknown column")
	}
}
