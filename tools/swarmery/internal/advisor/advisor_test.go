package advisor

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	if !strings.Contains(f.detail, "denied 5 times across 8 calls") {
		t.Errorf("detail %q must bake the counts in", f.detail)
	}
	counts := f.evidence["counts"].(map[string]int64)
	if counts["denied"] != 5 || counts["calls"] != 8 {
		t.Errorf("evidence counts = %+v, want denied 5 calls 8", counts)
	}
	if ids := f.evidence["session_ids"].([]string); len(ids) == 0 {
		t.Errorf("evidence must carry sample session ids")
	}
}

// ── R2 ────────────────────────────────────────────────────────────────────

// seedAgentRuns inserts `runs` subagent_start events and `errs` failed
// subagent_stop events (own-payload agentType — the same classification leg
// retroAgentWindow uses) for an agent, all on ≤2 distinct days so the same
// fixture never trips R3.
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
	if !strings.Contains(f.detail, "50% error rate (5 errors over 10 runs)") {
		t.Errorf("detail %q must bake rate + counts in", f.detail)
	}
	// Top error group cited: "agent flaky boom" folds digitless → itself.
	if top := f.evidence["top_error_group"].(string); !strings.Contains(top, "boom") {
		t.Errorf("top_error_group = %q, want the boom group", top)
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
	b := fmt.Sprintf(`{"metric":"error_rate","value":0.5,"window":{"from":%q,"to":%q},"accepted_at":%q}`,
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

func seedAdoptedRec(t *testing.T, db *sql.DB, rule, kind, target string, baselineValue float64, adoptedDaysAgo int) {
	t.Helper()
	b := fmt.Sprintf(`{"metric":"denied_count","value":%g,"window":{"from":%q,"to":%q},"accepted_at":%q,"adopted_at":%q}`,
		baselineValue, ago(adoptedDaysAgo+WindowDays), ago(adoptedDaysAgo),
		ago(adoptedDaysAgo+1), ago(adoptedDaysAgo))
	mustExec(t, db, `INSERT INTO recommendations
		(rule, target_kind, target, title, detail, evidence, status, dedup_key, baseline, created_at, updated_at)
		VALUES (?, ?, ?, 't', 'd', '{}', 'adopted', ?, ?, ?, ?)`,
		rule, kind, target, rule+":"+target, b, ago(adoptedDaysAgo), ago(adoptedDaysAgo))
}

func TestVerificationImproved(t *testing.T) {
	db := testDB(t)
	// Baseline 10 denied; post-adoption window (8 days) has only 2 → 80% better.
	seedAdoptedRec(t, db, "R1", "tool", "Bash", 10, 8)
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
	// Baseline 10, post-adoption still 9 denied → 10% < 20%: stays adopted
	// with an evidence note. (9 denied also re-fires R1, whose upsert must
	// leave the adopted status untouched.)
	seedAdoptedRec(t, db, "R1", "tool", "Bash", 10, 8)
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
	if b.Metric != "denied_count" || b.Value != 6 {
		t.Errorf("baseline = %+v, want denied_count 6", b)
	}
	if b.AcceptedAt != fmtTS(testNow) {
		t.Errorf("accepted_at = %q, want %q", b.AcceptedAt, fmtTS(testNow))
	}
	if b.Window.From != fmtTS(testNow.AddDate(0, 0, -WindowDays)) || b.Window.To != fmtTS(testNow) {
		t.Errorf("window = %+v, want the trailing %d days", b.Window, WindowDays)
	}
}
