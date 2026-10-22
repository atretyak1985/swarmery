package staleness

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// ---------------------------------------------------------------- Classify ----

func TestClassifyNotRunningIsLive(t *testing.T) {
	for _, status := range []string{"done", "queued", "archived", ""} {
		got := Classify(Input{Status: status, Source: "workspace", LinkedSessions: 3, OpenSessions: 0})
		if got.Kind != KindLive {
			t.Errorf("status %q → %s, want %s: a task that does not claim to be running cannot be stale",
				status, got.Kind, KindLive)
		}
		if got.Actionable {
			t.Errorf("status %q → Actionable, want false", status)
		}
	}
}

// A dead dispatch process is the strongest evidence available, and it outranks the
// session rules — but the WRITE permission still follows source, never evidence.
func TestClassifyDeadProcessActionableOnlyForQueue(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   bool
	}{
		{"queue", true},
		{"workspace", false},
	} {
		got := Classify(Input{Status: "running", Source: tc.source, DispatchProc: "dead",
			LinkedSessions: 1, OpenSessions: 1})
		if got.Kind != KindDeadProc {
			t.Fatalf("source %q → %s, want %s", tc.source, got.Kind, KindDeadProc)
		}
		if got.Actionable != tc.want {
			t.Errorf("source %q → Actionable=%v, want %v — a workspace row's status is rewritten by wsingest, so writing it is a silent no-op loop",
				tc.source, got.Actionable, tc.want)
		}
	}
}

// The rule this test defends is the whole reason KindUnknown exists: SUM over zero
// rows is zero, so without the no-links rule an unlinked task would satisfy the
// all-ended test and we would manufacture evidence out of its absence.
func TestClassifyNoLinkedSessionsIsUnknownNotStale(t *testing.T) {
	got := Classify(Input{Status: "running", Source: "queue", LinkedSessions: 0, OpenSessions: 0})
	if got.Kind != KindUnknown {
		t.Fatalf("Kind = %s, want %s — absence of a link is not evidence of death", got.Kind, KindUnknown)
	}
	if got.Actionable {
		t.Error("Actionable = true on no evidence; nothing may act on this verdict")
	}
	if got.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %s, want %s", got.Confidence, ConfidenceNone)
	}
}

func TestClassifyAllSessionsEndedIsStale(t *testing.T) {
	got := Classify(Input{Status: "running", Source: "workspace", LinkedSessions: 4, OpenSessions: 0,
		HeuristicLinks: 4, AgeDays: 35})
	if got.Kind != KindStale {
		t.Fatalf("Kind = %s, want %s", got.Kind, KindStale)
	}
	if got.Confidence != ConfidenceHeuristic {
		t.Errorf("Confidence = %s, want %s — a verdict resting on heuristic links may be shown, not acted on",
			got.Confidence, ConfidenceHeuristic)
	}
	// The reason must name the evidence, not restate the verdict: an operator sees
	// this string and nothing else.
	for _, want := range []string{"35", "4"} {
		if !contains(got.Reason, want) {
			t.Errorf("Reason %q does not name %q", got.Reason, want)
		}
	}
}

func TestClassifyOpenSessionIsLive(t *testing.T) {
	got := Classify(Input{Status: "running", Source: "queue", LinkedSessions: 3, OpenSessions: 1})
	if got.Kind != KindLive {
		t.Fatalf("Kind = %s, want %s", got.Kind, KindLive)
	}
	if got.Actionable {
		t.Error("a live task must never be actionable")
	}
}

func TestConfidenceProvenance(t *testing.T) {
	for _, tc := range []struct {
		name         string
		linked, heur int
		want         Confidence
	}{
		{"no links at all", 0, 0, ConfidenceNone},
		{"all explicit", 3, 0, ConfidenceExplicit},
		{"one heuristic taints the set", 3, 1, ConfidenceHeuristic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := confidenceOf(Input{LinkedSessions: tc.linked, HeuristicLinks: tc.heur})
			if got != tc.want {
				t.Errorf("= %s, want %s", got, tc.want)
			}
		})
	}
}

// ------------------------------------------------------------------- Load -----

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "staleness.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertTask(t *testing.T, db *sql.DB, id int64, source, status string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO tasks(id, project_id, title, prompt, status, created_at, started_at,
		                  source, external_id, board_column)
		VALUES(?, 1, ?, 'do it', ?, '2026-07-01T00:00:00Z', '2026-07-01T00:00:00Z', ?, ?, 'triage')`,
		id, "task-"+status, status, source, "ext-"+status+"-"+itoa(id)); err != nil {
		t.Fatal(err)
	}
}

// TestLoadKeepsTasksWithNoSessionLinks is the regression guard for the LEFT JOIN.
// An inner join drops an unlinked task from the result, and it would then be
// invisible in the very surface built to make invisible tasks visible.
func TestLoadKeepsTasksWithNoSessionLinks(t *testing.T) {
	db := testDB(t)
	insertTask(t, db, 1, "workspace", "running")

	rows, err := Load(db, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 — a task with no task_sessions row must still be returned", len(rows))
	}
	if rows[0].Verdict.Kind != KindUnknown {
		t.Errorf("Kind = %s, want %s", rows[0].Verdict.Kind, KindUnknown)
	}
	if rows[0].Input.LinkedSessions != 0 {
		t.Errorf("LinkedSessions = %d, want 0", rows[0].Input.LinkedSessions)
	}
}

func TestLoadDerivesStaleFromEndedSessions(t *testing.T) {
	db := testDB(t)
	insertTask(t, db, 1, "workspace", "running")
	// Two sessions, both ended → stale. link_source heuristic → Confidence heuristic.
	for i, uuid := range []string{"u-1", "u-2"} {
		if _, err := db.Exec(`
			INSERT INTO sessions(id, project_id, session_uuid, status, started_at, ended_at)
			VALUES(?, 1, ?, 'completed', '2026-07-01T00:00:00Z', '2026-07-02T00:00:00Z')`,
			i+1, uuid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO task_sessions(task_id, session_id, link_source) VALUES(1, ?, 'heuristic')`,
			i+1); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := Load(db, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.Verdict.Kind != KindStale {
		t.Errorf("Kind = %s, want %s (reason: %q)", got.Verdict.Kind, KindStale, got.Verdict.Reason)
	}
	if got.Input.LinkedSessions != 2 || got.Input.OpenSessions != 0 {
		t.Errorf("linked=%d open=%d, want 2 and 0", got.Input.LinkedSessions, got.Input.OpenSessions)
	}
	if got.Verdict.Actionable {
		t.Error("a workspace task must never be actionable, however strong the evidence")
	}
}

func TestLoadFiltersByProject(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2,'/repo/q','q','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	insertTask(t, db, 1, "workspace", "running")
	if _, err := db.Exec(`
		INSERT INTO tasks(id, project_id, title, prompt, status, created_at, started_at,
		                  source, external_id, board_column)
		VALUES(2, 2, 'other', 'do it', 'running', '2026-07-01T00:00:00Z', '2026-07-01T00:00:00Z',
		       'workspace', 'ext-other', 'triage')`); err != nil {
		t.Fatal(err)
	}

	all, err := Load(db, 0)
	if err != nil {
		t.Fatalf("Load(all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Load(0) = %d rows, want both projects", len(all))
	}
	one, err := Load(db, 1)
	if err != nil {
		t.Fatalf("Load(1): %v", err)
	}
	if len(one) != 1 || one[0].TaskID != 1 {
		t.Fatalf("Load(1) = %+v, want only task 1", one)
	}
}

// ---- tiny helpers (no new dependency for two string ops) --------------------

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// SessionEnded reads the status and the process, never ended_at (ingest stamps
// that with the last record's time on a session that is still writing).
func TestSessionEnded(t *testing.T) {
	for _, tc := range []struct {
		status, proc string
		want         bool
	}{
		{"active", "running", false},
		{"idle", "", false},
		{"waiting_approval", "running", false},
		{"completed", "", true},
		{"killed", "running", true},
		{"idle", procwatch.StateDead, true},
	} {
		if got := SessionEnded(tc.status, tc.proc); got != tc.want {
			t.Errorf("SessionEnded(%q, %q) = %v, want %v", tc.status, tc.proc, got, tc.want)
		}
	}
}
