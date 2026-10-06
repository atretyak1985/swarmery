package api

import (
	"slices"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// The labelling queue leaves out decisions about System-project sessions — the
// daemon's own utility runs — and returns them only when ?system=1 asks.
func TestDecisionsQueueSystemParam(t *testing.T) {
	sysPath := ingest.SystemDir()
	if sysPath == "" {
		t.Skip("no home dir: the System project path is unknown")
	}
	srv, db := testServerWithDB(t)
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (902, ?, 'system-test', '2026-01-01T00:00:00Z')`,
		sysPath); err != nil {
		t.Fatalf("insert System project: %v", err)
	}
	for _, s := range []string{
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (901, '/work/repo', 'work-repo', '2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions (project_id, session_uuid, title, started_at) VALUES (901, 's-work', 'work', '2026-09-20T10:00:00.000Z')`,
		`INSERT INTO sessions (project_id, session_uuid, title, started_at) VALUES (902, 's-sys', 'sys', '2026-09-20T10:00:00.000Z')`,
		`INSERT INTO turns (session_id, seq, role, started_at, text) VALUES ((SELECT id FROM sessions WHERE session_uuid = 's-work'), 1, 'assistant', '2026-09-20T10:30:00.000Z', 'x')`,
		`INSERT INTO turns (session_id, seq, role, started_at, text) VALUES ((SELECT id FROM sessions WHERE session_uuid = 's-sys'), 1, 'assistant', '2026-09-20T10:30:00.000Z', 'x')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for _, sess := range []string{"s-work", "s-sys", ""} { // ids 1, 2, 3
		if _, err := db.Exec(`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, calibrated, error, backend, created_at)
			VALUES ('d2.task_type', 'x', ?, 'h', 'docs', 1, 1, '', 'local', '2026-09-24T18:00:00Z')`, sess); err != nil {
			t.Fatal(err)
		}
	}
	var q struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	for _, c := range []struct {
		query string
		want  []int64
	}{
		{"", []int64{3, 1}},
		{"?system=0", []int64{3, 1}},
		{"?system=1", []int64{3, 2, 1}},
		{"?system=true&limit=all", []int64{3, 2, 1}},
	} {
		getJSON(t, srv.URL+"/api/decisions/queue"+c.query, &q)
		got := []int64{}
		for _, it := range q.Items {
			got = append(got, it.ID)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("queue%s = %v, want %v", c.query, got, c.want)
		}
	}
}
