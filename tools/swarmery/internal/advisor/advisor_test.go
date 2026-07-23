package advisor

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// testNow is the fixed evaluation instant every fixture is seeded around.
var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// ago renders a stored timestamp `days` days before testNow.
func ago(days int) string { return fmtTS(testNow.AddDate(0, 0, -days)) }

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "advisor.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?)`, ago(30))
	mustExec(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(1, 1, 'uuid-one', 'completed', ?),
		(2, 1, 'uuid-two', 'completed', ?)`, ago(2), ago(1))
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v\n%s", err, q)
	}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func evalWindow() window {
	return window{From: ago(WindowDays), To: fmtTS(testNow)}
}

// seedDenied inserts n denied + m ok tool_call events for a tool in-window.
func seedDenied(t *testing.T, db *sql.DB, tool string, denied, ok int) {
	t.Helper()
	for i := 0; i < denied; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
			VALUES (1, ?, 'tool_call', ?, 'denied', '{}', ?)`,
			ago(1+i%7), tool, fmt.Sprintf("den-%s-%d", tool, i))
	}
	for i := 0; i < ok; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
			VALUES (1, ?, 'tool_call', ?, 'ok', '{}', ?)`,
			ago(1), tool, fmt.Sprintf("ok-%s-%d", tool, i))
	}
}

// ── R1 ────────────────────────────────────────────────────────────────────

func TestR1DeniedTools(t *testing.T) {
	db := testDB(t)
	seedDenied(t, db, "Bash", R1MinDenied, 3)   // triggers
	seedDenied(t, db, "Read", R1MinDenied, 0)   // covered by an enabled rule
	seedDenied(t, db, "Grep", R1MinDenied-1, 0) // below threshold
	// Real ingest leaves status NULL on open tool_call events — the scan must
	// tolerate it (regression: R1 crashed on production data with a NULL-to-
	// string Scan error). Counts as a call, not a denial.
	mustExec(t, db, `INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
		VALUES (1, ?, 'tool_call', 'Bash', NULL, '{}', 'null-status-bash')`, ago(1))
	mustExec(t, db, `INSERT INTO approval_rules (project_id, tool_pattern, action, enabled, created_at)
		VALUES (NULL, 'Read(/tmp/*)', 'approve', 1, ?)`, ago(1))

	fs, err := r1DeniedTools(db, evalWindow())
	if err != nil {
		t.Fatalf("r1: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want exactly the Bash one", fs)
	}
	f := fs[0]
	if f.target != "Bash" || f.targetKind != "tool" || f.title != "Add auto-approve rule for Bash" {
		t.Errorf("finding = %+v, want tool Bash", f)
	}
	if !strings.Contains(f.detail, "denied 5 times across 9 calls") {
		t.Errorf("detail %q must bake the counts in", f.detail)
	}
	counts := f.evidence["counts"].(map[string]int64)
	if counts["denied"] != 5 || counts["calls"] != 9 {
		t.Errorf("evidence counts = %+v, want denied 5 calls 9", counts)
	}
	if ids := f.evidence["session_ids"].([]string); len(ids) == 0 {
		t.Errorf("evidence must carry sample session ids")
	}
}

// ── R2 ────────────────────────────────────────────────────────────────────

// seedAgentRuns inserts `runs` subagent_start events and `errs` failed
// subagent_stop events (own-payload agentType — the same classification leg
// retroAgentWindow uses) for an agent, all on ≤2 distinct days so the same
// fixture never trips R3. The stops are UNPARENTED, so each one counts as
// exactly one failed run — the failed-run share stays errs/runs here.
func seedAgentRuns(t *testing.T, db *sql.DB, agent string, runs, errs int) {
	t.Helper()
	for i := 0; i < runs; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
			VALUES (1, ?, 'subagent_start', 'ok', ?, ?)`,
			ago(1), `{"subagent_type":"`+agent+`"}`, fmt.Sprintf("run-%s-%d", agent, i))
	}
	for i := 0; i < errs; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
			VALUES (1, ?, 'subagent_stop', 'error', ?, ?)`,
			ago(1), `{"agentType":"`+agent+`","result":"agent `+agent+` boom"}`,
			fmt.Sprintf("err-%s-%d", agent, i))
	}
}

func TestR2AgentErrorRate(t *testing.T) {
	db := testDB(t)
	seedAgentRuns(t, db, "flaky", 10, 5)  // rate 0.5 — triggers vs median 0.1
	seedAgentRuns(t, db, "steady", 10, 1) // rate 0.1
	seedAgentRuns(t, db, "calm", 10, 1)   // rate 0.1
	seedAgentRuns(t, db, "rare", 2, 2)    // rate 1.0 but < R2MinRuns — excluded

	fs, err := r2AgentErrorRate(db, evalWindow())
	if err != nil {
		t.Fatalf("r2: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want only flaky (rare is under the run floor)", fs)
	}
	f := fs[0]
	if f.target != "flaky" || f.targetKind != "agent" {
		t.Errorf("finding = %+v, want agent flaky", f)
	}
	if !strings.Contains(f.detail, "failed on 5 of 10 runs (50%; 5 behavior-fixable of 5 error events)") {
		t.Errorf("detail %q must bake behavior-fixable failed runs + rate + event count in", f.detail)
	}
	// Top error group cited: "agent flaky boom" folds digitless → itself.
	if top := f.evidence["top_error_group"].(string); !strings.Contains(top, "boom") {
		t.Errorf("top_error_group = %q, want the boom group", top)
	}
	// Evidence carries the per-class error breakdown.
	byClass, ok := f.evidence["errors_by_class"].(map[ErrClass]int64)
	if !ok || byClass[BehaviorFixable] != 5 {
		t.Errorf("errors_by_class = %v, want behavior_fixable 5", f.evidence["errors_by_class"])
	}
}

// seedAgentInfraErrors inserts `errs` failed unparented subagent_stop events
// whose message classifies as infra_noise ("Connection error." — not the
// agent's fault) for an agent that already has runs seeded.
func seedAgentInfraErrors(t *testing.T, db *sql.DB, agent string, errs int) {
	t.Helper()
	for i := 0; i < errs; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
			VALUES (1, ?, 'subagent_stop', 'error', ?, ?)`,
			ago(1), `{"agentType":"`+agent+`","result":"Connection error. Please retry."}`,
			fmt.Sprintf("infra-%s-%d", agent, i))
	}
}

// TestR2IgnoresInfraNoise pins the phase-1 de-noising: an agent whose ONLY
// errors are connection errors (infra_noise) has a behavior-failed-run share
// of 0 — R2 must stay silent even though its raw failed-run share (0.5) beats
// 2× the median, while a behavior-fixable twin with the same shape fires.
func TestR2IgnoresInfraNoise(t *testing.T) {
	db := testDB(t)
	seedAgentRuns(t, db, "netty", 10, 0) // 10 clean runs …
	seedAgentInfraErrors(t, db, "netty", 5)
	seedAgentRuns(t, db, "flaky", 10, 5)  // behavior-fixable ("boom") — fires
	seedAgentRuns(t, db, "steady", 10, 1) // share 0.1
	seedAgentRuns(t, db, "calm", 10, 1)   // share 0.1 — median anchors

	acc, err := agentErrorWindow(db, evalWindow())
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	n := acc["netty"]
	if n == nil || n.errors != 5 || n.byClass[InfraNoise] != 5 || n.byClass[BehaviorFixable] != 0 {
		t.Fatalf("netty = %+v, want 5 errors all infra_noise", n)
	}
	if n.failedRuns() != 5 || n.behaviorFailedRuns() != 0 {
		t.Fatalf("netty failed runs = %d/%d (raw/behavior), want 5/0", n.failedRuns(), n.behaviorFailedRuns())
	}

	fs, err := r2AgentErrorRate(db, evalWindow())
	if err != nil {
		t.Fatalf("r2: %v", err)
	}
	if len(fs) != 1 || fs[0].target != "flaky" {
		t.Fatalf("findings = %+v, want only flaky (netty's errors are all infra noise)", fs)
	}

	// The verification metric follows the same behavior-only share.
	name, v, ok, err := metricValue(db, "R2", "netty", evalWindow())
	if err != nil || !ok {
		t.Fatalf("metricValue: ok=%v err=%v", ok, err)
	}
	if name != "behavior_failed_run_share" || v != 0 {
		t.Errorf("metric = %q %g, want behavior_failed_run_share 0", name, v)
	}
}

func TestR2AbsoluteErrorFloor(t *testing.T) {
	db := testDB(t)
	seedAgentRuns(t, db, "calm-a", 40, 2) // rate 0.05
	seedAgentRuns(t, db, "calm-b", 40, 2) // rate 0.05 — median 0.05
	seedAgentRuns(t, db, "noisy", 10, 2)  // rate 0.2 > 2× median, but only 2 errors
	fs, err := r2AgentErrorRate(db, evalWindow())
	if err != nil {
		t.Fatalf("r2: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none under the %d-error absolute floor", fs, R2MinErrors)
	}
}

// seedRunWithErrors inserts ONE failed subagent_start run for an agent plus
// `errs` sidechain tool errors parented to it — however many error events the
// run sprays, it is exactly one failed run.
func seedRunWithErrors(t *testing.T, db *sql.DB, agent string, errs int) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
		VALUES (1, ?, 'subagent_start', 'error', ?, ?)`,
		ago(1), `{"subagent_type":"`+agent+`"}`, "runerr-"+agent)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	startID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	for i := 0; i < errs; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, parent_event_id, ts, type, tool_name, status, payload, dedup_key)
			VALUES (1, ?, ?, 'tool_call', 'Bash', 'error', '{"result":"burst boom"}', ?)`,
			startID, ago(1), fmt.Sprintf("perr-%s-%d", agent, i))
	}
}

// TestR2FailedRunShare pins the failed-run-share semantics: one run carrying
// 3 parented errors is ONE failed run (rate 1/runs), not 3 — under the old
// errors/runs math burst's 0.3 would beat 2× the 0.1 median and misfire.
func TestR2FailedRunShare(t *testing.T) {
	db := testDB(t)
	seedAgentRuns(t, db, "burst", 9, 0)   // 9 clean runs …
	seedRunWithErrors(t, db, "burst", 3)  // … + 1 run with 3 errors → share 0.1
	seedAgentRuns(t, db, "steady", 10, 1) // share 0.1
	seedAgentRuns(t, db, "calm", 10, 1)   // share 0.1 — median 0.1

	acc, err := agentErrorWindow(db, evalWindow())
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	b := acc["burst"]
	if b == nil || b.runs != 10 || b.errors != 3 || b.failedRuns() != 1 {
		t.Fatalf("burst = %+v, want runs 10 errors 3 failed_runs 1", b)
	}

	fs, err := r2AgentErrorRate(db, evalWindow())
	if err != nil {
		t.Fatalf("r2: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none — burst's failed-run share is 0.1, at the median", fs)
	}

	// The verification metric (BaselineFor / verify recompute) must use the
	// same failed-run share.
	name, v, ok, err := metricValue(db, "R2", "burst", evalWindow())
	if err != nil || !ok {
		t.Fatalf("metricValue: ok=%v err=%v", ok, err)
	}
	if name != "behavior_failed_run_share" || v != 0.1 {
		t.Errorf("metric = %q %g, want behavior_failed_run_share 0.1 (1 failed run of 10)", name, v)
	}
}

// TestR2FailedRunDedupStopPlusToolErrors pins the failed-run dedupe across
// BOTH error legs: a run whose subagent_stop reports status='error' AND whose
// two child tool errors are parented to the same subagent_start folds to ONE
// failed run — the stop's runKey resolves to its parent start id, the same
// key the tool errors carry.
func TestR2FailedRunDedupStopPlusToolErrors(t *testing.T) {
	db := testDB(t)
	res, err := db.Exec(`INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
		VALUES (1, ?, 'subagent_start', 'ok', '{"subagent_type":"dedup"}', 'dedup-start')`, ago(1))
	if err != nil {
		t.Fatalf("seed start: %v", err)
	}
	startID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	mustExec(t, db, `INSERT INTO events (session_id, parent_event_id, ts, type, status, payload, dedup_key)
		VALUES (1, ?, ?, 'subagent_stop', 'error', '{"agentType":"dedup","result":"dedup boom"}', 'dedup-stop')`,
		startID, ago(1))
	for i := 0; i < 2; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, parent_event_id, ts, type, tool_name, status, payload, dedup_key)
			VALUES (1, ?, ?, 'tool_call', 'Bash', 'error', '{"result":"dedup boom"}', ?)`,
			startID, ago(1), fmt.Sprintf("dedup-toolerr-%d", i))
	}

	acc, err := agentErrorWindow(db, evalWindow())
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	a := acc["dedup"]
	if a == nil || a.runs != 1 || a.errors != 3 || a.failedRuns() != 1 {
		t.Fatalf("dedup = %+v, want runs 1 errors 3 failed_runs 1 (stop + tool errors fold to one run)", a)
	}
}

func TestR2NoTriggerWhenUniform(t *testing.T) {
	db := testDB(t)
	seedAgentRuns(t, db, "one", 10, 2) // rate 0.2
	seedAgentRuns(t, db, "two", 10, 2) // rate 0.2 — nobody beats 2× median
	fs, err := r2AgentErrorRate(db, evalWindow())
	if err != nil {
		t.Fatalf("r2: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none for uniform rates", fs)
	}
}

// ── R3 ────────────────────────────────────────────────────────────────────

func TestR3RecurringErrors(t *testing.T) {
	db := testDB(t)
	// Same 529 error on 3 distinct days (request ids differ — must fold).
	for i := 0; i < 3; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
			VALUES (1, ?, 'error', 'error', ?, ?)`,
			ago(1+i), fmt.Sprintf(`{"error":{"message":"API Error 529 overloaded (req_%03d)"}}`, i),
			fmt.Sprintf("recur-%d", i))
	}
	// A different error on only 2 days — below the floor.
	for i := 0; i < 2; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
			VALUES (1, ?, 'error', 'error', '{"error":"connection reset by peer"}', ?)`,
			ago(1+i), fmt.Sprintf("rare-%d", i))
	}

	fs, err := r3RecurringErrors(db, evalWindow())
	if err != nil {
		t.Fatalf("r3: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want only the 3-day group", fs)
	}
	f := fs[0]
	if f.targetKind != "error_group" || !strings.Contains(f.target, "api error #") {
		t.Errorf("finding = %+v, want the folded 529 group", f)
	}
	if days := f.evidence["days"].([]string); len(days) != 3 {
		t.Errorf("days = %v, want 3 distinct days", days)
	}
	if !strings.Contains(f.detail, "3 times on 3 distinct days") {
		t.Errorf("detail %q must bake counts in", f.detail)
	}
}

// ── R4 ────────────────────────────────────────────────────────────────────

func seedDelegations(t *testing.T, db *sql.DB, taskID int64, agent string, redis, ok int) {
	t.Helper()
	seq := count(t, db, `SELECT COUNT(*) FROM task_delegations WHERE task_id = ?`, taskID)
	for i := 0; i < redis; i++ {
		seq++
		mustExec(t, db, `INSERT INTO task_delegations (task_id, seq, agent, verdict)
			VALUES (?, ?, ?, 'RE-DISPATCH')`, taskID, seq, agent)
	}
	for i := 0; i < ok; i++ {
		seq++
		mustExec(t, db, `INSERT INTO task_delegations (task_id, seq, agent, verdict)
			VALUES (?, ?, ?, 'OK')`, taskID, seq, agent)
	}
}

func TestR4Redispatch(t *testing.T) {
	db := testDB(t)
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (1, 1, 'In-range', 'goal', 'done', ?, ?, 'workspace', 'task-a'),
		       (2, 1, 'Ancient',  'goal', 'done', ?, ?, 'workspace', 'task-b')`,
		ago(3), ago(3), ago(60), ago(60))
	seedDelegations(t, db, 1, "impl", 2, 2)     // share 0.5 over 4 rows — triggers
	seedDelegations(t, db, 1, "gatherer", 1, 3) // share 0.25 — not > threshold
	seedDelegations(t, db, 1, "rare", 2, 0)     // 2 rows — under the floor
	seedDelegations(t, db, 2, "impl", 5, 0)     // out-of-window task must not count

	fs, err := r4Redispatch(db, evalWindow())
	if err != nil {
		t.Fatalf("r4: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want only impl", fs)
	}
	f := fs[0]
	if f.target != "impl" || f.targetKind != "agent" {
		t.Errorf("finding = %+v, want agent impl", f)
	}
	if !strings.Contains(f.detail, "re-dispatched on 2 of 4 delegations (50%)") {
		t.Errorf("detail %q must bake the in-window share in (ancient task excluded)", f.detail)
	}
}

// ── R5 ────────────────────────────────────────────────────────────────────

func seedImprovement(t *testing.T, db *sql.DB, taskID int64, extID string, ingestedDaysAgo int, text, priority, status string) int64 {
	t.Helper()
	mustExec(t, db, `INSERT OR IGNORE INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (?, 1, ?, 'goal', 'done', ?, ?, 'workspace', ?)`,
		taskID, extID, ago(ingestedDaysAgo), ago(ingestedDaysAgo), extID)
	mustExec(t, db, `INSERT OR IGNORE INTO task_retros (id, task_id, ingested_at)
		VALUES (?, ?, ?)`, taskID, taskID, ago(ingestedDaysAgo))
	res, err := db.Exec(`INSERT INTO retro_improvements (retro_id, text, priority, status)
		VALUES (?, ?, ?, ?)`, taskID, text, priority, status)
	if err != nil {
		t.Fatalf("insert improvement: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestR5StaleImprovements(t *testing.T) {
	db := testDB(t)
	staleID := seedImprovement(t, db, 1, "task-a", 20, "Pin golden fixtures", "High", "open")
	seedImprovement(t, db, 1, "task-a", 20, "Already done", "P0", "Done")
	seedImprovement(t, db, 1, "task-a", 20, "Low prio", "low", "open")
	seedImprovement(t, db, 2, "task-b", 5, "Too fresh", "P1", "open")

	fs, err := r5StaleImprovements(db, evalWindow(), testNow)
	if err != nil {
		t.Fatalf("r5: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want only the stale open High one", fs)
	}
	f := fs[0]
	wantTarget := fmt.Sprintf("task-a#%d", staleID)
	if f.target != wantTarget || f.targetKind != "process" {
		t.Errorf("target = %q, want %q", f.target, wantTarget)
	}
	if !strings.Contains(f.title, "Pin golden fixtures") {
		t.Errorf("title %q must cite the improvement text", f.title)
	}
	if !strings.Contains(f.detail, "still open 20 days after") {
		t.Errorf("detail %q must bake the age in", f.detail)
	}
}

// ── R6 ────────────────────────────────────────────────────────────────────

// seedTurns inserts one turn carrying the given token totals `daysAgo`.
func seedTurns(t *testing.T, db *sql.DB, seq int, daysAgo int, cacheRead, tokensIn int64) {
	t.Helper()
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_cache_read)
		VALUES (1, ?, 'assistant', ?, ?, ?)`, seq, ago(daysAgo), tokensIn, cacheRead)
}

func TestR6CacheRegression(t *testing.T) {
	db := testDB(t)
	seedTurns(t, db, 1, 20, 800, 200) // prev window: 80% hit rate
	seedTurns(t, db, 2, 3, 500, 500)  // current: 50% — a 30pp drop

	fs, err := r6CacheRegression(db, evalWindow(), testNow)
	if err != nil {
		t.Fatalf("r6: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want the regression", fs)
	}
	f := fs[0]
	if f.target != "cache-hit-rate" || f.targetKind != "config" {
		t.Errorf("finding = %+v, want config/cache-hit-rate", f)
	}
	if !strings.Contains(f.detail, "dropped 30.0 percentage points") {
		t.Errorf("detail %q must bake the drop in", f.detail)
	}
}

func TestR6NoTriggerOnSmallDrop(t *testing.T) {
	db := testDB(t)
	seedTurns(t, db, 1, 20, 800, 200) // prev: 80%
	seedTurns(t, db, 2, 3, 750, 250)  // current: 75% — only 5pp
	fs, err := r6CacheRegression(db, evalWindow(), testNow)
	if err != nil {
		t.Fatalf("r6: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %+v, want none for a 5pp drop", fs)
	}
}

// ── dedup contract ────────────────────────────────────────────────────────

func recRow(t *testing.T, db *sql.DB, target string) (id int64, status, updatedAt, dedupKey string) {
	t.Helper()
	err := db.QueryRow(`SELECT id, status, updated_at, dedup_key FROM recommendations
		WHERE target = ? ORDER BY id DESC LIMIT 1`, target).
		Scan(&id, &status, &updatedAt, &dedupKey)
	if err != nil {
		t.Fatalf("rec row for %q: %v", target, err)
	}
	return
}

func TestDedupUpdateInPlace(t *testing.T) {
	db := testDB(t)
	seedDenied(t, db, "Bash", R1MinDenied+1, 0)

	s1, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if s1.Proposed != 1 || s1.Updated != 0 {
		t.Fatalf("run 1 stats = %+v, want 1 proposed", s1)
	}
	_, _, upd1, key := recRow(t, db, "Bash")
	if key != "R1:Bash" {
		t.Errorf("dedup_key = %q, want R1:Bash", key)
	}

	s2, err := Run(db, testNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if s2.Proposed != 0 || s2.Updated != 1 {
		t.Fatalf("run 2 stats = %+v, want 1 updated", s2)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations`); n != 1 {
		t.Fatalf("rows = %d, want 1 (update in place)", n)
	}
	_, status, upd2, _ := recRow(t, db, "Bash")
	if status != "proposed" {
		t.Errorf("status = %q, want proposed untouched", status)
	}
	if upd2 <= upd1 {
		t.Errorf("updated_at = %q, want bumped past %q", upd2, upd1)
	}
}

func TestDismissedSuppression(t *testing.T) {
	db := testDB(t)
	seedDenied(t, db, "Bash", R1MinDenied+1, 0)
	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Freshly dismissed → suppressed: no new row, status stays dismissed.
	mustExec(t, db, `UPDATE recommendations SET status = 'dismissed', updated_at = ?`, ago(10))
	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run suppressed: %v", err)
	}
	if s.Proposed != 0 || s.Updated != 0 {
		t.Fatalf("stats = %+v, want full suppression", s)
	}
	if _, status, _, _ := recRow(t, db, "Bash"); status != "dismissed" {
		t.Errorf("status = %q, want dismissed kept", status)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations`); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}

	// Dismissed > DismissSuppressDays ago → flipped back to proposed in place.
	mustExec(t, db, `UPDATE recommendations SET updated_at = ?`, ago(DismissSuppressDays+1))
	s, err = Run(db, testNow)
	if err != nil {
		t.Fatalf("run re-propose: %v", err)
	}
	if s.Proposed != 1 {
		t.Fatalf("stats = %+v, want 1 re-proposed", s)
	}
	_, status, upd, _ := recRow(t, db, "Bash")
	if status != "proposed" || upd != fmtTS(testNow) {
		t.Errorf("status/updated_at = %q/%q, want proposed with fresh updated_at", status, upd)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations`); n != 1 {
		t.Fatalf("rows = %d, want 1 (re-proposal reuses the row)", n)
	}
}

func TestVerifiedReRaiseGetsSuffix(t *testing.T) {
	db := testDB(t)
	seedDenied(t, db, "Bash", R1MinDenied+1, 0)
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES ('R1', 'tool', 'Bash', 't', 'd', '{}', 'verified', 'R1:Bash', ?, ?)`,
		ago(40), ago(40))

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Proposed != 1 {
		t.Fatalf("stats = %+v, want a fresh proposal", s)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations WHERE target = 'Bash'`); n != 2 {
		t.Fatalf("rows = %d, want the verified row + a fresh one", n)
	}
	_, status, _, key := recRow(t, db, "Bash")
	if status != "proposed" || key != "R1:Bash:2" {
		t.Errorf("latest = %q/%q, want proposed with dedup_key R1:Bash:2", status, key)
	}
}

// ── adoption detection ────────────────────────────────────────────────────

func seedAcceptedAgentRec(t *testing.T, db *sql.DB, agent string, acceptedDaysAgo int) {
	t.Helper()
	b := fmt.Sprintf(`{"metric":"behavior_failed_run_share","value":0.5,"window":{"from":%q,"to":%q},"accepted_at":%q}`,
		ago(acceptedDaysAgo+WindowDays), ago(acceptedDaysAgo), ago(acceptedDaysAgo))
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
		VALUES ('R2', 'agent', ?, 't', 'd', '{}', 'accepted', ?, ?, ?, ?)`,
		agent, "R2:"+agent, b, ago(acceptedDaysAgo), ago(acceptedDaysAgo))
}

func seedAgentVersion(t *testing.T, db *sql.DB, agentID int64, name string, versionDaysAgo int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO agents (id, name, scope, file_path) VALUES (?, ?, 'global', ?)`,
		agentID, name, "/agents/"+name+".md")
	mustExec(t, db, `INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at)
		VALUES (?, ?, 'h', 'v', ?)`, agentID*100, agentID, ago(versionDaysAgo))
	mustExec(t, db, `UPDATE agents SET current_version_id = ? WHERE id = ?`, agentID*100, agentID)
}

func TestAdoptionDetection(t *testing.T) {
	db := testDB(t)
	// flaky: accepted 10 days ago, current version created 4 days ago → adopted.
	seedAcceptedAgentRec(t, db, "flaky", 10)
	seedAgentVersion(t, db, 1, "core:flaky", 4) // registry notation must fold
	// steady: accepted 10 days ago, version predates acceptance → stays accepted.
	seedAcceptedAgentRec(t, db, "steady", 10)
	seedAgentVersion(t, db, 2, "steady", 20)

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Adopted != 1 {
		t.Fatalf("stats = %+v, want 1 adopted", s)
	}
	_, status, _, _ := recRow(t, db, "flaky")
	if status != "adopted" {
		t.Errorf("flaky status = %q, want adopted", status)
	}
	var base string
	if err := db.QueryRow(`SELECT baseline FROM recommendations WHERE target = 'flaky'`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	var b baseline
	if err := json.Unmarshal([]byte(base), &b); err != nil || b.AdoptedAt != fmtTS(testNow) {
		t.Errorf("baseline = %q, want adopted_at stamped %q", base, fmtTS(testNow))
	}
	if _, status, _, _ := recRow(t, db, "steady"); status != "accepted" {
		t.Errorf("steady status = %q, want accepted (no newer version)", status)
	}
}

// ── verification ──────────────────────────────────────────────────────────

// seedAdoptedRec seeds an adopted R1-style recommendation whose baseline is a
// per-day denied rate (the I4 normalization), adopted `adoptedDaysAgo` days
// before testNow.
func seedAdoptedRec(t *testing.T, db *sql.DB, rule, kind, target string, baselinePerDay float64, adoptedDaysAgo int) {
	t.Helper()
	b := fmt.Sprintf(`{"metric":"denied_per_day","value":%g,"per_day":true,"window_days":14,"window":{"from":%q,"to":%q},"accepted_at":%q,"adopted_at":%q}`,
		baselinePerDay, ago(adoptedDaysAgo+WindowDays), ago(adoptedDaysAgo),
		ago(adoptedDaysAgo+1), ago(adoptedDaysAgo))
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
		VALUES (?, ?, ?, 't', 'd', '{}', 'adopted', ?, ?, ?, ?)`,
		rule, kind, target, rule+":"+target, b, ago(adoptedDaysAgo), ago(adoptedDaysAgo))
}

func TestVerificationImproved(t *testing.T) {
	db := testDB(t)
	// Baseline 10 denied over 14 days ≈ 0.714/day; the 8-day post-adoption
	// window has 2 denied → 0.25/day, a 65% improvement.
	seedAdoptedRec(t, db, "R1", "tool", "Bash", 10.0/14, 8)
	seedDenied(t, db, "Bash", 2, 0)

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Verified != 1 {
		t.Fatalf("stats = %+v, want 1 verified", s)
	}
	if _, status, _, _ := recRow(t, db, "Bash"); status != "verified" {
		t.Errorf("status = %q, want verified", status)
	}
}

func TestVerificationNotYet(t *testing.T) {
	db := testDB(t)
	// Baseline 1.25 denied/day; the 8-day post window still has 9 denied →
	// 1.125/day, only 10% better < 20%: stays adopted with an evidence note.
	// (9 denied also re-fires R1, whose upsert must leave the adopted status
	// untouched.)
	seedAdoptedRec(t, db, "R1", "tool", "Bash", 1.25, 8)
	seedDenied(t, db, "Bash", 9, 0)

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Verified != 0 || s.Updated != 1 {
		t.Fatalf("stats = %+v, want 0 verified / 1 updated (in-place refresh)", s)
	}
	_, status, _, _ := recRow(t, db, "Bash")
	if status != "adopted" {
		t.Errorf("status = %q, want still adopted", status)
	}
	var ev string
	if err := db.QueryRow(`SELECT evidence FROM recommendations WHERE target = 'Bash'`).Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ev, "no measurable improvement yet") {
		t.Errorf("evidence = %q, want the not-yet note", ev)
	}
}

func TestVerificationWaitsSevenDays(t *testing.T) {
	db := testDB(t)
	seedAdoptedRec(t, db, "R1", "tool", "Bash", 10, VerifyAfterDays-1) // adopted 6 days ago
	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Verified != 0 {
		t.Fatalf("stats = %+v, want no verification before %d days", s, VerifyAfterDays)
	}
	if _, status, _, _ := recRow(t, db, "Bash"); status != "adopted" {
		t.Errorf("status = %q, want adopted untouched", status)
	}
}

// TestVerificationRebaselinesOnMetricRename pins the metric-version
// self-healing: an adopted R2 rec still carrying an old-style baseline
// (metric "error_rate", snapshotted before the behavior_failed_run_share
// renames) must
// NOT be compared against the recomputed metric. verify() re-snapshots the
// baseline under the current definition (preserving accepted_at/adopted_at),
// skips the comparison that cycle, and a later Run with genuinely improved
// data still verifies against the fresh baseline.
func TestVerificationRebaselinesOnMetricRename(t *testing.T) {
	db := testDB(t)
	// Trailing-window truth at testNow: 10 runs, 5 failed → share 0.5.
	seedAgentRuns(t, db, "flaky", 10, 5)
	oldBase := fmt.Sprintf(`{"metric":"error_rate","value":0.9,"per_day":false,"window_days":14,"window":{"from":%q,"to":%q},"accepted_at":%q,"adopted_at":%q}`,
		ago(8+WindowDays), ago(8), ago(9), ago(8))
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
		VALUES ('R2', 'agent', 'flaky', 't', 'd', '{}', 'adopted', 'R2:flaky', ?, ?, ?)`,
		oldBase, ago(8), ago(8))

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Past the 7-day gate with a mismatched metric name → re-baseline, no flip.
	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if s.Verified != 0 {
		t.Fatalf("stats = %+v, want no verification on a metric-name mismatch", s)
	}
	if _, status, _, _ := recRow(t, db, "flaky"); status != "adopted" {
		t.Errorf("status = %q, want still adopted", status)
	}
	if !strings.Contains(buf.String(), "baseline metric changed (error_rate -> behavior_failed_run_share), re-baselined") {
		t.Errorf("log = %q, want the re-baseline line", buf.String())
	}
	var base string
	if err := db.QueryRow(`SELECT baseline FROM recommendations WHERE target = 'flaky'`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	var b baseline
	if err := json.Unmarshal([]byte(base), &b); err != nil {
		t.Fatalf("unmarshal %q: %v", base, err)
	}
	if b.Metric != "behavior_failed_run_share" || b.Value != 0.5 {
		t.Errorf("baseline = %+v, want behavior_failed_run_share 0.5 (fresh trailing-window snapshot)", b)
	}
	if b.AcceptedAt != ago(9) || b.AdoptedAt != ago(8) {
		t.Errorf("baseline anchors = %q/%q, want accepted_at %q / adopted_at %q preserved",
			b.AcceptedAt, b.AdoptedAt, ago(9), ago(8))
	}

	// Genuinely improved traffic after the re-baseline: 40 clean runs → the
	// post window (anchored on the preserved adopted_at) folds to 5 failed of
	// 50 runs = 0.1, an 80% improvement over the re-baselined 0.5 → verified.
	for i := 0; i < 40; i++ {
		mustExec(t, db, `INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
			VALUES (1, ?, 'subagent_start', 'ok', '{"subagent_type":"flaky"}', ?)`,
			fmtTS(testNow.AddDate(0, 0, 2)), fmt.Sprintf("post-run-flaky-%d", i))
	}
	s, err = Run(db, testNow.AddDate(0, 0, 8))
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if s.Verified != 1 {
		t.Fatalf("stats = %+v, want 1 verified against the fresh baseline", s)
	}
	if _, status, _, _ := recRow(t, db, "flaky"); status != "verified" {
		t.Errorf("status = %q, want verified", status)
	}
}

// ── BaselineFor ───────────────────────────────────────────────────────────

func TestBaselineFor(t *testing.T) {
	db := testDB(t)
	seedDenied(t, db, "Bash", 6, 0)

	j, err := BaselineFor(db, "R1", "Bash", testNow)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	var b baseline
	if err := json.Unmarshal([]byte(j), &b); err != nil {
		t.Fatalf("unmarshal %q: %v", j, err)
	}
	// I4: count metrics are per-day rates with the window length made explicit.
	if b.Metric != "denied_per_day" || b.Value != 6.0/WindowDays {
		t.Errorf("baseline = %+v, want denied_per_day %g", b, 6.0/WindowDays)
	}
	if !b.PerDay || b.WindowDays != WindowDays {
		t.Errorf("baseline = %+v, want per_day true with window_days %d", b, WindowDays)
	}
	if b.AcceptedAt != fmtTS(testNow) {
		t.Errorf("accepted_at = %q, want %q", b.AcceptedAt, fmtTS(testNow))
	}
	if b.Window.From != fmtTS(testNow.AddDate(0, 0, -WindowDays)) || b.Window.To != fmtTS(testNow) {
		t.Errorf("window = %+v, want the trailing %d days", b.Window, WindowDays)
	}
}

// ── lifecycle hardening (activity floors, per-kind adoption, guards) ──────

func TestVerificationZeroTrafficStaysAdopted(t *testing.T) {
	db := testDB(t)
	// Adopted 8 days ago; the post window has NO Bash calls at all — the R1
	// activity floor fails and absence of data must never verify.
	seedAdoptedRec(t, db, "R1", "tool", "Bash", 10.0/14, 8)

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Verified != 0 {
		t.Fatalf("stats = %+v, want no verification on zero traffic", s)
	}
	if _, status, _, _ := recRow(t, db, "Bash"); status != "adopted" {
		t.Errorf("status = %q, want still adopted", status)
	}
	var ev string
	if err := db.QueryRow(`SELECT evidence FROM recommendations WHERE target = 'Bash'`).Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ev, "insufficient post-adoption traffic") {
		t.Errorf("evidence = %q, want the insufficient-traffic note", ev)
	}
}

func TestMalformedBaselineLogged(t *testing.T) {
	db := testDB(t)
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
		VALUES ('R1', 'tool', 'Bash', 't', 'd', '{}', 'adopted', 'R1:Bash', 'not-json', ?, ?)`,
		ago(10), ago(10))

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	if _, err := Run(db, testNow); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(buf.String(), "malformed baseline json") {
		t.Errorf("log = %q, want the malformed-baseline warning", buf.String())
	}
	if _, status, _, _ := recRow(t, db, "Bash"); status != "adopted" {
		t.Errorf("status = %q, want the rec left untouched", status)
	}
}

func TestSecondReRaiseGetsThirdSuffix(t *testing.T) {
	db := testDB(t)
	seedDenied(t, db, "Bash", R1MinDenied+1, 0)
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES ('R1', 'tool', 'Bash', 't', 'd', '{}', 'verified', 'R1:Bash', ?, ?),
		       ('R1', 'tool', 'Bash', 't', 'd', '{}', 'verified', 'R1:Bash:2', ?, ?)`,
		ago(60), ago(60), ago(40), ago(40))

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Proposed != 1 {
		t.Fatalf("stats = %+v, want a fresh proposal", s)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM recommendations WHERE target = 'Bash'`); n != 3 {
		t.Fatalf("rows = %d, want the two verified rows + a fresh one", n)
	}
	_, status, _, key := recRow(t, db, "Bash")
	if status != "proposed" || key != "R1:Bash:3" {
		t.Errorf("latest = %q/%q, want proposed with dedup_key R1:Bash:3", status, key)
	}
}

// seedAcceptedToolRec seeds an accepted R1 (tool-kind) recommendation with a
// per-day baseline accepted `acceptedDaysAgo` days before testNow.
func seedAcceptedToolRec(t *testing.T, db *sql.DB, tool string, acceptedDaysAgo int) {
	t.Helper()
	b := fmt.Sprintf(`{"metric":"denied_per_day","value":0.7,"per_day":true,"window_days":14,"window":{"from":%q,"to":%q},"accepted_at":%q}`,
		ago(acceptedDaysAgo+WindowDays), ago(acceptedDaysAgo), ago(acceptedDaysAgo))
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
		VALUES ('R1', 'tool', ?, 't', 'd', '{}', 'accepted', ?, ?, ?, ?)`,
		tool, "R1:"+tool, b, ago(acceptedDaysAgo), ago(acceptedDaysAgo))
}

func TestToolAdoptionViaApprovalRule(t *testing.T) {
	db := testDB(t)
	// Bash: enabled covering rule created AFTER acceptance → adopted.
	seedAcceptedToolRec(t, db, "Bash", 10)
	mustExec(t, db, `INSERT INTO approval_rules (project_id, tool_pattern, action, enabled, created_at)
		VALUES (NULL, 'Bash', 'approve', 1, ?)`, ago(4))
	// Grep: covering rule PREDATES acceptance — not adoption evidence.
	seedAcceptedToolRec(t, db, "Grep", 10)
	mustExec(t, db, `INSERT INTO approval_rules (project_id, tool_pattern, action, enabled, created_at)
		VALUES (NULL, 'Grep', 'approve', 1, ?)`, ago(20))

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Adopted != 1 {
		t.Fatalf("stats = %+v, want exactly the Bash adoption", s)
	}
	if _, status, _, _ := recRow(t, db, "Bash"); status != "adopted" {
		t.Errorf("Bash status = %q, want adopted", status)
	}
	if _, status, _, _ := recRow(t, db, "Grep"); status != "accepted" {
		t.Errorf("Grep status = %q, want accepted (rule predates acceptance)", status)
	}
	var base string
	if err := db.QueryRow(`SELECT baseline FROM recommendations WHERE target = 'Bash'`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	var b baseline
	if err := json.Unmarshal([]byte(base), &b); err != nil || b.AdoptedAt != fmtTS(testNow) {
		t.Errorf("baseline = %q, want adopted_at stamped %q", base, fmtTS(testNow))
	}
}

func TestProcessAdoptionAndVerification(t *testing.T) {
	db := testDB(t)
	impID := seedImprovement(t, db, 1, "task-a", 20, "Pin golden fixtures", "High", "open")
	target := fmt.Sprintf("task-a#%d", impID)
	b := fmt.Sprintf(`{"metric":"open_stale_improvements","value":1,"per_day":false,"window_days":14,"window":{"from":%q,"to":%q},"accepted_at":%q}`,
		ago(10+WindowDays), ago(10), ago(10))
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
		VALUES ('R5', 'process', ?, 't', 'd', '{}', 'accepted', ?, ?, ?, ?)`,
		target, "R5:"+target, b, ago(10), ago(10))

	// Still open → no adoption.
	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if s.Adopted != 0 {
		t.Fatalf("stats = %+v, want no adoption while the improvement is open", s)
	}

	// The improvement flips to done → adopted on the next run.
	mustExec(t, db, `UPDATE retro_improvements SET status = 'Done' WHERE id = ?`, impID)
	s, err = Run(db, testNow)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if s.Adopted != 1 {
		t.Fatalf("stats = %+v, want 1 adopted after the status flip", s)
	}
	if _, status, _, _ := recRow(t, db, target); status != "adopted" {
		t.Errorf("status = %q, want adopted", status)
	}

	// ≥ VerifyAfterDays later with the status STILL done → verified, no
	// metric math, with the evidence note.
	s, err = Run(db, testNow.AddDate(0, 0, VerifyAfterDays+1))
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if s.Verified != 1 {
		t.Fatalf("stats = %+v, want 1 verified", s)
	}
	if _, status, _, _ := recRow(t, db, target); status != "verified" {
		t.Errorf("status = %q, want verified", status)
	}
	var ev string
	if err := db.QueryRow(`SELECT evidence FROM recommendations WHERE target = ?`, target).Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ev, "improvement marked done") {
		t.Errorf("evidence = %q, want the improvement-marked-done note", ev)
	}
}

func TestErrorGroupVerifiesDirectlyFromAccepted(t *testing.T) {
	db := testDB(t)
	// R3/error_group has no adoption signal: verification runs straight from
	// accepted, anchored on accepted_at.
	key := "connection reset by peer"
	b := fmt.Sprintf(`{"metric":"error_days_per_day","value":0.5,"per_day":true,"window_days":14,"window":{"from":%q,"to":%q},"accepted_at":%q}`,
		ago(8+WindowDays), ago(8), ago(8))
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
		VALUES ('R3', 'error_group', ?, 't', 'd', '{}', 'accepted', ?, ?, ?, ?)`,
		key, "R3:"+key, b, ago(8), ago(8))
	// One error day in the 8-day post window → 0.125/day vs the 0.5/day
	// baseline: 75% better.
	mustExec(t, db, `INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
		VALUES (1, ?, 'error', 'error', '{"error":"connection reset by peer"}', 'e-post-1')`, ago(2))

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Adopted != 0 || s.Verified != 1 {
		t.Fatalf("stats = %+v, want verified straight from accepted (no adopted hop)", s)
	}
	if _, status, _, _ := recRow(t, db, key); status != "verified" {
		t.Errorf("status = %q, want verified", status)
	}
}

// ── R7 ────────────────────────────────────────────────────────────────────

// makeProjectDir creates a temporary project directory with a fake .git
// (loose ref) and an architecture-out/architecture-map.json whose mtime is
// forced to `age` before testNow. Returns the directory path.
func makeProjectDir(t *testing.T, analyzedCommit string, age time.Duration) string {
	t.Helper()
	root := t.TempDir()

	// Fake .git with HEAD → refs/heads/main → shaA.
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGit := func(p, content string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeGit(filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
	writeGit(filepath.Join(gitDir, "refs", "heads", "main"), shaA+"\n")

	// architecture-out/architecture-map.json with the requested analyzedAtCommit.
	mapDir := filepath.Join(root, "architecture-out")
	if err := os.MkdirAll(mapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mapPath := filepath.Join(mapDir, "architecture-map.json")
	content := fmt.Sprintf(`{"analyzedAtCommit":%q}`, analyzedCommit)
	if err := os.WriteFile(mapPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force mtime to testNow - age.
	mtime := testNow.Add(-age)
	if err := os.Chtimes(mapPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return root
}

// Two distinct fake commit shas for R7 fixture.
const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // current HEAD
const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // stale analyzed commit

// insertProjectAt inserts a non-archived project row with the given path and
// returns its id.
func insertProjectAt(t *testing.T, db *sql.DB, id int64, path, slug string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen, archived)
		VALUES (?, ?, ?, ?, ?, 0)`, id, path, slug, slug, ago(30))
}

func TestR7StaleArchitectureMap(t *testing.T) {
	// ── trigger: mismatch + old enough ───────────────────────────────────────
	t.Run("triggers on mismatch and old map", func(t *testing.T) {
		db := testDB(t)
		staleAge := time.Duration(R7StaleDays+1) * 24 * time.Hour
		root := makeProjectDir(t, shaB, staleAge)
		insertProjectAt(t, db, 99, root, "proj-stale")

		fs, err := r7StaleArchitectureMap(db, evalWindow(), testNow)
		if err != nil {
			t.Fatalf("r7: %v", err)
		}
		if len(fs) != 1 {
			t.Fatalf("findings = %+v, want exactly 1", fs)
		}
		f := fs[0]
		if f.rule != "R7" || f.targetKind != "project" || f.target != "proj-stale" {
			t.Errorf("finding = %+v, want R7/project/proj-stale", f)
		}
		if !strings.Contains(f.detail, shaA[:7]) {
			t.Errorf("detail %q must mention HEAD short sha %s", f.detail, shaA[:7])
		}
		if !strings.Contains(f.detail, shaB[:7]) {
			t.Errorf("detail %q must mention analyzed short sha %s", f.detail, shaB[:7])
		}
	})

	// ── counter-case (a): analyzed == HEAD → no finding ──────────────────────
	t.Run("no finding when analyzed matches HEAD", func(t *testing.T) {
		db := testDB(t)
		staleAge := time.Duration(R7StaleDays+1) * 24 * time.Hour
		root := makeProjectDir(t, shaA, staleAge) // shaA == HEAD
		insertProjectAt(t, db, 99, root, "proj-match")

		fs, err := r7StaleArchitectureMap(db, evalWindow(), testNow)
		if err != nil {
			t.Fatalf("r7: %v", err)
		}
		if len(fs) != 0 {
			t.Fatalf("findings = %+v, want none when analyzed == HEAD", fs)
		}
	})

	// ── counter-case (b): mismatch but fresh mtime < R7StaleDays → no finding ─
	t.Run("no finding when map is too fresh", func(t *testing.T) {
		db := testDB(t)
		freshAge := time.Duration(R7StaleDays-1) * 24 * time.Hour
		root := makeProjectDir(t, shaB, freshAge) // mismatch but recent
		insertProjectAt(t, db, 99, root, "proj-fresh")

		fs, err := r7StaleArchitectureMap(db, evalWindow(), testNow)
		if err != nil {
			t.Fatalf("r7: %v", err)
		}
		if len(fs) != 0 {
			t.Fatalf("findings = %+v, want none when map is fresher than %d days", fs, R7StaleDays)
		}
	})

	// ── counter-case (c): no map file → no finding ────────────────────────────
	t.Run("no finding when map file absent", func(t *testing.T) {
		db := testDB(t)
		root := t.TempDir()
		// Only create the fake .git, no architecture-out.
		gitDir := filepath.Join(root, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte(shaA+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		insertProjectAt(t, db, 99, root, "proj-nomap")

		fs, err := r7StaleArchitectureMap(db, evalWindow(), testNow)
		if err != nil {
			t.Fatalf("r7: %v", err)
		}
		if len(fs) != 0 {
			t.Fatalf("findings = %+v, want none when map file is absent", fs)
		}
	})

	// ── counter-case (d): unreadable .git → no finding (never guess) ─────────
	t.Run("no finding when .git is unreadable", func(t *testing.T) {
		db := testDB(t)
		staleAge := time.Duration(R7StaleDays+1) * 24 * time.Hour
		root := makeProjectDir(t, shaB, staleAge)
		// Remove .git entirely so githead.Resolve returns ok=false.
		if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
			t.Fatal(err)
		}
		insertProjectAt(t, db, 99, root, "proj-nogit")

		fs, err := r7StaleArchitectureMap(db, evalWindow(), testNow)
		if err != nil {
			t.Fatalf("r7: %v", err)
		}
		if len(fs) != 0 {
			t.Fatalf("findings = %+v, want none when .git is unreadable", fs)
		}
	})

	// ── counter-case (e): archived project → no finding ──────────────────────
	t.Run("no finding for archived project", func(t *testing.T) {
		db := testDB(t)
		staleAge := time.Duration(R7StaleDays+1) * 24 * time.Hour
		root := makeProjectDir(t, shaB, staleAge)
		// Insert as archived.
		mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen, archived)
			VALUES (99, ?, 'proj-archived', 'proj-archived', ?, 1)`, root, ago(30))

		fs, err := r7StaleArchitectureMap(db, evalWindow(), testNow)
		if err != nil {
			t.Fatalf("r7: %v", err)
		}
		if len(fs) != 0 {
			t.Fatalf("findings = %+v, want none for archived project", fs)
		}
	})
}

func TestGuardedTransitionsRespectDismissed(t *testing.T) {
	db := testDB(t)
	res, err := db.Exec(`INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES ('R1', 'tool', 'Bash', 't', 'd', '{}', 'dismissed', 'R1:Bash', ?, ?)`,
		ago(1), ago(1))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	if flipped, err := markAdopted(db, id, `{}`, fmtTS(testNow)); err != nil || flipped {
		t.Errorf("markAdopted = %v/%v, want a 0-row no-op on a dismissed rec", flipped, err)
	}
	if flipped, err := markVerified(db, id, fmtTS(testNow)); err != nil || flipped {
		t.Errorf("markVerified = %v/%v, want a 0-row no-op on a dismissed rec", flipped, err)
	}
	if _, status, _, _ := recRow(t, db, "Bash"); status != "dismissed" {
		t.Errorf("status = %q, want dismissed untouched", status)
	}
}

// A rec targeting an agent ABSENT from the registry (an ad-hoc delegation
// ledger label, e.g. a hand-named reviewer) has no adoption signal — it must
// verify directly from accepted instead of waiting forever, while a
// registry-known agent in the same state keeps waiting for its version bump.
func TestUnknownRegistryAgentVerifiesFromAccepted(t *testing.T) {
	db := testDB(t)
	// Post-acceptance window truth: one in-window task, 4 clean ledger rows
	// each → redispatch_share 0 (≥ R4MinRows, so the activity floor passes).
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (1, 1, 'In-range', 'goal', 'done', ?, ?, 'workspace', 'task-a')`, ago(3), ago(3))
	seedDelegations(t, db, 1, "ghost-reviewer", 0, 4)
	seedDelegations(t, db, 1, "known-reviewer", 0, 4)
	// known-reviewer exists in the registry; ghost-reviewer does not.
	mustExec(t, db, `INSERT INTO agents (id, name, scope, file_path) VALUES (1, 'known-reviewer', 'global', '/x/known-reviewer.md')`)

	base := func(target string) string {
		return fmt.Sprintf(`{"metric":"redispatch_share","value":0.67,"per_day":false,"window_days":14,"accepted_at":%q}`, ago(8))
	}
	for i, target := range []string{"ghost-reviewer", "known-reviewer"} {
		mustExec(t, db, `INSERT INTO recommendations
			(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
			VALUES (?, 'R4', 'agent', ?, 't', 'd', '{}', 'accepted', ?, ?, ?, ?)`,
			i+1, target, "R4:"+target, base(target), ago(8), ago(8))
	}

	s, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if s.Verified != 1 {
		t.Fatalf("stats = %+v, want exactly the ghost verified", s)
	}
	if _, status, _, _ := recRow(t, db, "ghost-reviewer"); status != "verified" {
		t.Errorf("ghost status = %q, want verified (0.67 -> 0 redispatch share)", status)
	}
	if _, status, _, _ := recRow(t, db, "known-reviewer"); status != "accepted" {
		t.Errorf("known status = %q, want still accepted (waits for a registry version bump)", status)
	}
}
