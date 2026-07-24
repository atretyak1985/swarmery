package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// ── pure-function tests (no DB) ────────────────────────────────────────────

func TestAutonomyRatio(t *testing.T) {
	// Zero interventions → ratio degrades to the raw tool-call count and the
	// window is flagged fullyAutonomous (the phase-doc invariant).
	if r, full := autonomyRatio(42, 0); r != 42 || !full {
		t.Fatalf("zero interventions: got ratio=%v full=%v, want 42,true", r, full)
	}
	// Normal case: 100 calls / 4 interventions = 25.
	if r, full := autonomyRatio(100, 4); r != 25 || full {
		t.Fatalf("100/4: got ratio=%v full=%v, want 25,false", r, full)
	}
	// Negative denominator (defensive) is treated as zero → fully autonomous.
	if r, full := autonomyRatio(5, -1); r != 5 || !full {
		t.Fatalf("negative interventions: got ratio=%v full=%v, want 5,true", r, full)
	}
}

func TestLangExtOf(t *testing.T) {
	cases := map[string]string{
		"src/api/foo.ts":            "ts",
		"src/api/foo.test.ts":       "ts", // compound suffix → outermost real ext
		"web/styles.module.css":     "css",
		"cmd/main.go":               "go",
		"Makefile":                  "other", // no extension
		".gitignore":                "other", // dotfile, not an extension
		"README":                    "other",
		"a/b/c/deep.PY":             "py",       // lowercased
		"archive.tar.gz":            "gz",       // outermost
		"weird.name.with.many.dots": "dots",
		"trailing.":                 "other", // trailing dot → empty ext
	}
	for in, want := range cases {
		if got := langExtOf(in); got != want {
			t.Errorf("langExtOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNearestRankPercentile(t *testing.T) {
	// Known set of 10 sorted values; nearest-rank ranks are ceil(p*n).
	xs := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	check := func(p, want float64) {
		got := nearestRankPercentile(xs, p)
		if got == nil {
			t.Fatalf("p=%v: got nil", p)
		}
		if *got != want {
			t.Errorf("p=%v: got %v, want %v", p, *got, want)
		}
	}
	check(0.5, 50)  // ceil(5.0)=5 → xs[4]=50
	check(0.9, 90)  // ceil(9.0)=9 → xs[8]=90
	check(0.95, 100) // ceil(9.5)=10 → xs[9]=100
	check(0.1, 10)  // ceil(1.0)=1 → xs[0]=10
	check(0.0, 10)  // clamp low
	check(1.0, 100) // clamp high

	// Single element: every percentile is that element.
	one := []float64{7}
	if got := nearestRankPercentile(one, 0.9); got == nil || *got != 7 {
		t.Errorf("single element p90: got %v, want 7", got)
	}
	// Empty → nil.
	if got := nearestRankPercentile(nil, 0.5); got != nil {
		t.Errorf("empty: got %v, want nil", got)
	}
}

func TestCompletionRate(t *testing.T) {
	if r := completionRate(3, 4); r != 0.75 {
		t.Errorf("3/4: got %v, want 0.75", r)
	}
	if r := completionRate(0, 0); r != 0 {
		t.Errorf("0/0: got %v, want 0 (guarded)", r)
	}
	if r := completionRate(5, 0); r != 0 {
		t.Errorf("5/0: got %v, want 0 (guarded)", r)
	}
}

// ── HTTP integration tests ─────────────────────────────────────────────────

const upliftTSFmt = "2006-01-02T15:04:05.000Z"

// upliftServer seeds a project with: 6 tool_call events, 2 human-resolved
// approvals + 1 rule-resolved (excluded) + 1 user_prompt intervention; a commit
// event; three file_changes (foo.ts +10/-2, foo.test.ts +5/-0, main.go +3/-1);
// and three completed board tasks of 100/200/400 s in range plus one out of
// range and one still running.
func upliftServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "uplift.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	base := todayStart.Add(10 * time.Hour)
	at := func(d time.Duration) string { return base.Add(d).UTC().Format(upliftTSFmt) }
	day20 := todayStart.AddDate(0, 0, -20).Add(12 * time.Hour).UTC().Format(upliftTSFmt)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  NULL,    ?)`, day20, day20)

	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at, ended_at) VALUES
		(1, 1, 'u1', 'claude-fable-5', 'completed', ?, ?),
		(2, 2, 'u2', 'claude-fable-5', 'completed', ?, ?)`,
		at(0), at(time.Hour), at(0), at(time.Hour))

	// 6 tool_call events (5 in session 1, 1 in session 2).
	for i := 0; i < 5; i++ {
		mustExec(`INSERT INTO events (session_id, ts, type, dedup_key) VALUES (1, ?, 'tool_call', ?)`,
			at(time.Duration(i)*time.Minute), "tc1-"+time.Duration(i).String())
	}
	mustExec(`INSERT INTO events (session_id, ts, type, dedup_key) VALUES (2, ?, 'tool_call', 'tc2')`, at(2*time.Minute))
	// 1 user_prompt (intervention).
	mustExec(`INSERT INTO events (session_id, ts, type, dedup_key) VALUES (1, ?, 'user_prompt', 'up1')`, at(3*time.Minute))
	// 1 commit event.
	mustExec(`INSERT INTO events (session_id, ts, type, dedup_key) VALUES (1, ?, 'commit', 'cm1')`, at(4*time.Minute))
	// A file_change event to hang file_changes off.
	mustExec(`INSERT INTO events (id, session_id, ts, type, dedup_key) VALUES (100, 1, ?, 'file_change', 'fc-ev')`, at(5*time.Minute))

	// Approvals: 2 human-resolved (dashboard/terminal), 1 rule-resolved (auto —
	// excluded from interventions), 1 pending (excluded).
	mustExec(`INSERT INTO permission_requests (session_id, tool_name, request_json, status, requested_at, resolved_at, resolved_via) VALUES
		(1, 'Bash', '{}', 'approved', ?, ?, 'dashboard'),
		(1, 'Bash', '{}', 'approved', ?, ?, 'terminal'),
		(1, 'Bash', '{}', 'approved', ?, ?, 'rule'),
		(1, 'Bash', '{}', 'pending',  ?, NULL, NULL)`,
		at(time.Minute), at(2*time.Minute),
		at(time.Minute), at(2*time.Minute),
		at(time.Minute), at(2*time.Minute),
		at(time.Minute))

	// file_changes: foo.ts +10/-2, foo.test.ts +5/-0, main.go +3/-1 → LOC 12+5+4=21.
	mustExec(`INSERT INTO file_changes (event_id, session_id, file_path, change_type, additions, deletions) VALUES
		(100, 1, 'src/api/foo.ts', 'edit', 10, 2),
		(100, 1, 'src/api/foo.test.ts', 'edit', 5, 0),
		(100, 1, 'cmd/main.go', 'edit', 3, 1)`)

	// Board tasks: three completed 100/200/400 s in range, one out of range,
	// one running (no finished_at).
	mustExec(`INSERT INTO tasks (id, project_id, title, prompt, status, source, board_column, created_at, started_at, finished_at, column_moved_at) VALUES
		(1, 1, 't1', 'p', 'done', 'queue', 'done', ?, ?, ?, ?),
		(2, 1, 't2', 'p', 'done', 'queue', 'done', ?, ?, ?, ?),
		(3, 1, 't3', 'p', 'done', 'queue', 'done', ?, ?, ?, ?),
		(4, 1, 't4', 'p', 'done', 'queue', 'done', ?, ?, ?, ?),
		(5, 1, 't5', 'p', 'running', 'queue', 'in_progress', ?, ?, NULL, ?)`,
		at(0), at(0), at(100*time.Second), at(100*time.Second),
		at(0), at(0), at(200*time.Second), at(200*time.Second),
		at(0), at(0), at(400*time.Second), at(400*time.Second),
		day20, day20, day20, day20,
		at(0), at(0), at(0))

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

func TestStatsAutonomyHTTP(t *testing.T) {
	srv := upliftServer(t)
	var got autonomyDTO
	getJSON(t, srv.URL+"/api/stats/autonomy", &got)
	if got.ToolCalls != 6 {
		t.Errorf("toolCalls = %d, want 6", got.ToolCalls)
	}
	// 2 human approvals + 1 user_prompt = 3 interventions (rule + pending excluded).
	if got.Interventions.Approvals != 2 {
		t.Errorf("approvals = %d, want 2 (rule + pending excluded)", got.Interventions.Approvals)
	}
	if got.Interventions.UserPrompts != 1 {
		t.Errorf("userPrompts = %d, want 1", got.Interventions.UserPrompts)
	}
	if got.Interventions.Total != 3 {
		t.Errorf("interventions.total = %d, want 3", got.Interventions.Total)
	}
	if math.Abs(got.Ratio-2.0) > 1e-9 { // 6/3
		t.Errorf("ratio = %v, want 2.0", got.Ratio)
	}
	if got.FullyAutonomous {
		t.Errorf("fullyAutonomous = true, want false")
	}
}

func TestStatsAutonomyScopedFullyAutonomous(t *testing.T) {
	srv := upliftServer(t)
	// Scope to project beta (slug -work-beta): 1 tool_call, 0 interventions.
	var got autonomyDTO
	getJSON(t, srv.URL+"/api/stats/autonomy?project=-work-beta", &got)
	if got.ToolCalls != 1 {
		t.Errorf("beta toolCalls = %d, want 1", got.ToolCalls)
	}
	if got.Interventions.Total != 0 {
		t.Errorf("beta interventions = %d, want 0", got.Interventions.Total)
	}
	if !got.FullyAutonomous || got.Ratio != 1 {
		t.Errorf("beta: fullyAutonomous=%v ratio=%v, want true,1", got.FullyAutonomous, got.Ratio)
	}
}

func TestStatsProductivityHTTP(t *testing.T) {
	srv := upliftServer(t)
	var got productivityDTO
	getJSON(t, srv.URL+"/api/stats/productivity", &got)
	if got.Commits != 1 {
		t.Errorf("commits = %d, want 1", got.Commits)
	}
	if got.LOC != 21 {
		t.Errorf("loc = %d, want 21 (12+5+4)", got.LOC)
	}
	if got.FilesModified != 3 {
		t.Errorf("filesModified = %d, want 3", got.FilesModified)
	}
	// Languages: ts (foo.ts + foo.test.ts → 2 files, 17 loc), go (1 file, 4 loc).
	langByExt := map[string]languageStatDTO{}
	for _, l := range got.Languages {
		langByExt[l.Ext] = l
	}
	if ts := langByExt["ts"]; ts.Files != 2 || ts.LOC != 17 {
		t.Errorf("ts lang = %+v, want files=2 loc=17", ts)
	}
	if g := langByExt["go"]; g.Files != 1 || g.LOC != 4 {
		t.Errorf("go lang = %+v, want files=1 loc=4", g)
	}
	// ts (17) should rank before go (4).
	if len(got.Languages) < 2 || got.Languages[0].Ext != "ts" {
		t.Errorf("languages not ranked by loc desc: %+v", got.Languages)
	}
	// Durations: 3 completed tasks (100/200/400 s). median nearest-rank of
	// [100,200,400] at p50 = ceil(1.5)=2 → 200. p90 = ceil(2.7)=3 → 400.
	if got.TaskDurations.Completed != 3 {
		t.Errorf("completed = %d, want 3", got.TaskDurations.Completed)
	}
	if got.TaskDurations.MedianSec == nil || *got.TaskDurations.MedianSec != 200 {
		t.Errorf("medianSec = %v, want 200", got.TaskDurations.MedianSec)
	}
	if got.TaskDurations.P90Sec == nil || *got.TaskDurations.P90Sec != 400 {
		t.Errorf("p90Sec = %v, want 400", got.TaskDurations.P90Sec)
	}
	// Hours-saved is ALWAYS an estimate: 21/15 = 1.4, formula "loc/15".
	if !got.HumanHoursSaved.Estimate || got.HumanHoursSaved.Formula != "loc/15" {
		t.Errorf("hoursSaved not flagged estimate: %+v", got.HumanHoursSaved)
	}
	if math.Abs(got.HumanHoursSaved.Value-1.4) > 1e-9 {
		t.Errorf("hoursSaved value = %v, want 1.4", got.HumanHoursSaved.Value)
	}
}

func TestStatsFunnelHTTP(t *testing.T) {
	srv := upliftServer(t)
	var got funnelDTO
	getJSON(t, srv.URL+"/api/stats/funnel", &got)
	if !got.Snapshot {
		t.Errorf("snapshot flag = false, want true (honesty flag)")
	}
	// Current occupancy: done has tasks 1,2,3 (in range) + 4 (out of range) = 4;
	// in_progress has task 5.
	colByName := map[string]funnelColumnDTO{}
	for _, c := range got.Columns {
		colByName[c.Column] = c
	}
	if colByName["done"].Count != 4 {
		t.Errorf("done count = %d, want 4", colByName["done"].Count)
	}
	if colByName["in_progress"].Count != 1 {
		t.Errorf("in_progress count = %d, want 1", colByName["in_progress"].Count)
	}
	// done.entered = reached done in range (column_moved_at in range) = tasks 1,2,3 = 3.
	if colByName["done"].Entered != 3 {
		t.Errorf("done entered = %d, want 3 (in-range moves only)", colByName["done"].Entered)
	}
	// All 6 board columns present in order.
	if len(got.Columns) != 6 {
		t.Errorf("columns = %d, want 6", len(got.Columns))
	}
	if got.Columns[0].Column != "triage" || got.Columns[5].Column != "archived" {
		t.Errorf("column order wrong: %s..%s", got.Columns[0].Column, got.Columns[5].Column)
	}
	// enteredInRange = tasks created in range: tasks 1,2,3,5 (4 created today; task 4 is day20).
	if got.EnteredInRange != 4 {
		t.Errorf("enteredInRange = %d, want 4", got.EnteredInRange)
	}
	if got.DoneInRange != 3 {
		t.Errorf("doneInRange = %d, want 3", got.DoneInRange)
	}
	// completionRate = 3/4 = 0.75.
	if math.Abs(got.CompletionRate-0.75) > 1e-9 {
		t.Errorf("completionRate = %v, want 0.75", got.CompletionRate)
	}
}

func TestStatsPlaybooksDegradesPrePhase13(t *testing.T) {
	srv := upliftServer(t)
	// The base schema has no `playbook` column → empty list, HTTP 200 (not 500).
	var got []playbookRollupDTO
	getJSON(t, srv.URL+"/api/stats/playbooks", &got)
	if len(got) != 0 {
		t.Errorf("playbooks pre-13 = %d rows, want 0 (graceful degrade)", len(got))
	}
}

// TestStatsPlaybooksWithColumn adds the Phase-13 `playbook` column at runtime to
// prove the rollup query works once the column exists.
func TestStatsPlaybooksWithColumn(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pb.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := func(d time.Duration) string { return todayStart.Add(10*time.Hour + d).UTC().Format(upliftTSFmt) }
	day20 := todayStart.AddDate(0, 0, -20).UTC().Format(upliftTSFmt)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, '/w/a', '-w-a', 'A', ?)`, day20)
	mustExec(`ALTER TABLE tasks ADD COLUMN playbook TEXT`)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at) VALUES (1, 1, 'u1', 'm', 'completed', ?)`, at(0))
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd) VALUES
		(1, 1, 'assistant', ?, 100, 50, 0.5)`, at(0))
	mustExec(`INSERT INTO tasks (id, project_id, title, prompt, status, source, board_column, created_at, column_moved_at, session_id, playbook) VALUES
		(1, 1, 't1', 'p', 'done', 'queue', 'done', ?, ?, 1, 'standard')`, at(0), at(time.Minute))

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var got []playbookRollupDTO
	getJSON(t, srv.URL+"/api/stats/playbooks", &got)
	if len(got) != 1 {
		t.Fatalf("playbooks = %d rows, want 1", len(got))
	}
	if got[0].Playbook != "standard" || got[0].TasksDone != 1 || got[0].Tokens != 150 {
		t.Errorf("rollup = %+v, want standard/1 done/150 tokens", got[0])
	}
	if got[0].CostUSD == nil || *got[0].CostUSD != 0.5 {
		t.Errorf("rollup cost = %v, want 0.5", got[0].CostUSD)
	}
}
