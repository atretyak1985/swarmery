package api

// agent-memory phase 4 — GET /api/retro/lessons?group=1.
//
// The flat feed answers "what did we learn"; the grouped view answers "what do
// we keep re-learning", which is the only one of the two a human can act on.
// The fixture is three tasks wording one lesson three different ways, plus a
// second lesson in one task and one unfolded ('' norm_title) row.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func retroLessonGroupsServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	today := retroDay(t, 0)
	day1 := retroDay(t, 1)
	day2 := retroDay(t, 2)
	day30 := retroDay(t, 30)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?)`, day30)
	mustExec(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id) VALUES
		(1, 1, 'Newest task',  'goal', 'done', ?, ?, 'workspace', 'task-new'),
		(2, 1, 'Middle task',  'goal', 'done', ?, ?, 'workspace', 'task-mid'),
		(3, 1, 'Oldest task',  'goal', 'done', ?, ?, 'workspace', 'task-old'),
		(4, 1, 'Ancient task', 'goal', 'done', ?, ?, 'workspace', 'task-ancient')`,
		today, today, day1, day1, day2, day2, day30, day30)
	mustExec(`INSERT INTO task_retros (id, task_id, ingested_at) VALUES
		(1, 1, ?), (2, 2, ?), (3, 3, ?), (4, 4, ?)`, today, day1, day2, day30)

	// One identity, three wordings, three tasks. Task 1 also carries a second,
	// unrelated lesson and an unfolded row that must never be grouped.
	mustExec(`INSERT INTO retro_lessons (retro_id, seq, title, body, action, norm_title) VALUES
		(1, 1, 'Sync-cache before build',            NULL, 'add it to the build skill', 'sync cache before build'),
		(1, 2, 'Pin fixture mtimes',                 NULL, NULL,                        'pin fixture mtimes'),
		(1, 3, 'Unfolded legacy row',                NULL, NULL,                        ''),
		(2, 1, 'sync the cache before the build',    NULL, 'older action',              'sync cache before build'),
		(3, 1, 'SYNC-CACHE, BEFORE BUILD!',          NULL, NULL,                        'sync cache before build'),
		(4, 1, 'Sync-cache before build',            NULL, NULL,                        'sync cache before build')`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestRetroLessonsGrouped(t *testing.T) {
	srv := retroLessonGroupsServer(t)
	var out retroLessonGroupsDTO
	getJSON(t, srv.URL+"/api/retro/lessons?group=1&"+retroRange(7), &out)

	if len(out.Groups) != 2 {
		t.Fatalf("groups = %+v, want 2 (the '' row is never an identity)", out.Groups)
	}

	// Ordered by count desc: the recurring lesson leads.
	g := out.Groups[0]
	if g.NormTitle != "sync cache before build" {
		t.Fatalf("group[0].norm_title = %q, want the folded key", g.NormTitle)
	}
	if g.Count != 3 {
		t.Errorf("group[0].count = %d, want 3 (the ancient task is out of range)", g.Count)
	}
	if g.Title != "Sync-cache before build" {
		t.Errorf("group[0].title = %q, want the most recent wording", g.Title)
	}
	if g.LatestAction == nil || *g.LatestAction != "add it to the build skill" {
		t.Errorf("group[0].latest_action = %v, want the newest occurrence's action", g.LatestAction)
	}
	want := []string{"task-new", "task-mid", "task-old"}
	if len(g.Tasks) != 3 {
		t.Fatalf("group[0].tasks = %+v, want %+v", g.Tasks, want)
	}
	for i, id := range want {
		if g.Tasks[i] != id {
			t.Errorf("group[0].tasks[%d] = %q, want %q (newest first)", i, g.Tasks[i], id)
		}
	}

	// The one-off lesson still appears, with count 1 and a null action.
	if out.Groups[1].NormTitle != "pin fixture mtimes" || out.Groups[1].Count != 1 {
		t.Errorf("group[1] = %+v, want pin fixture mtimes / count 1", out.Groups[1])
	}
	if out.Groups[1].LatestAction != nil {
		t.Errorf("group[1].latest_action = %v, want null", out.Groups[1].LatestAction)
	}
}

// TestRetroLessonsGroupedIsOptIn: the same URL without the flag must still
// return the flat feed — the toggle may not change what an existing caller gets.
func TestRetroLessonsGroupedIsOptIn(t *testing.T) {
	srv := retroLessonGroupsServer(t)

	var flat retroLessonsDTO
	getJSON(t, srv.URL+"/api/retro/lessons?"+retroRange(7), &flat)
	if len(flat.Lessons) != 5 {
		t.Fatalf("flat lessons = %d, want 5 rows (grouping must be opt-in)", len(flat.Lessons))
	}

	// An explicit opt-out reads as the flat feed too.
	var off retroLessonsDTO
	getJSON(t, srv.URL+"/api/retro/lessons?group=0&"+retroRange(7), &off)
	if len(off.Lessons) != 5 {
		t.Errorf("group=0 lessons = %d, want the flat 5", len(off.Lessons))
	}
}

// TestRetroLessonsGroupedEmptyWindow pins the empty shape: `[]`, never null —
// the page maps over it directly.
func TestRetroLessonsGroupedEmptyWindow(t *testing.T) {
	srv := retroLessonGroupsServer(t)
	var out retroLessonGroupsDTO
	getJSON(t, srv.URL+"/api/retro/lessons?group=true&from=1999-01-01&to=1999-01-02", &out)
	if out.Groups == nil {
		t.Fatal("groups = null, want an empty array")
	}
	if len(out.Groups) != 0 {
		t.Errorf("groups = %+v, want empty", out.Groups)
	}
}

// TestRetroLessonsNullExternalID pins the NULLABLE column both feeds join on.
//
// tasks.external_id is nullable (migration 0006_workspaces.sql) — a task that
// did not come from the workspace source has none. Scanning that NULL straight
// into a Go string makes the endpoint 500, while advisor/rules_lesson.go
// COALESCEs it to the row id and counts the same lesson happily: the page would
// go blank over exactly the row R11 is raising a card about. Both queries now
// COALESCE, so the id the operator sees is the one the advisor grouped on.
func TestRetroLessonsNullExternalID(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "nullext.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	today := retroDay(t, 0)
	day1 := retroDay(t, 1)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?)`, day1)
	// Task 41 has no external id at all; task 42 does, and they learned the same
	// lesson — so the group must carry BOTH, not silently drop the anonymous one.
	mustExec(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id) VALUES
		(41, 1, 'Anonymous task', 'goal', 'done', ?, ?, 'queue', NULL),
		(42, 1, 'Named task',     'goal', 'done', ?, ?, 'workspace', 'task-named')`,
		today, today, day1, day1)
	mustExec(`INSERT INTO task_retros (id, task_id, ingested_at) VALUES (41, 41, ?), (42, 42, ?)`, today, day1)
	mustExec(`INSERT INTO retro_lessons (retro_id, seq, title, body, action, norm_title) VALUES
		(41, 1, 'Sync-cache before build', NULL, 'add it to the build skill', 'sync cache before build'),
		(42, 1, 'sync the cache first',    NULL, NULL,                        'sync cache before build')`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Explicit status assertion: the regression is a 500, and getJSON's decode
	// failure would name the symptom rather than the cause.
	resp, err := http.Get(srv.URL + "/api/retro/lessons?group=1&" + retroRange(7))
	if err != nil {
		t.Fatalf("GET grouped: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET grouped with a NULL external_id task: status %d, want 200", resp.StatusCode)
	}

	var out retroLessonGroupsDTO
	getJSON(t, srv.URL+"/api/retro/lessons?group=1&"+retroRange(7), &out)
	if len(out.Groups) != 1 {
		t.Fatalf("groups = %+v, want 1", out.Groups)
	}
	g := out.Groups[0]
	if g.Count != 2 {
		t.Errorf("group count = %d, want 2 (the NULL-external_id task counts too)", g.Count)
	}
	want := []string{"41", "task-named"} // newest first; the anonymous task falls back to its row id
	if len(g.Tasks) != 2 || g.Tasks[0] != want[0] || g.Tasks[1] != want[1] {
		t.Errorf("group tasks = %+v, want %+v", g.Tasks, want)
	}

	// The flat twin shares the column and the defect; it must survive too.
	var flat retroLessonsDTO
	getJSON(t, srv.URL+"/api/retro/lessons?"+retroRange(7), &flat)
	if len(flat.Lessons) != 2 {
		t.Fatalf("flat lessons = %d, want 2", len(flat.Lessons))
	}
	if flat.Lessons[0].TaskExternalID != "41" {
		t.Errorf("flat task_external_id = %q, want the row-id fallback %q", flat.Lessons[0].TaskExternalID, "41")
	}
}
