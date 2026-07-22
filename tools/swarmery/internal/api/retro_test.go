package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// retroDay renders a UTC timestamp at local noon, `back` days before today —
// the shared timestamp helper for the retro fixtures.
func retroDay(t *testing.T, back int) string {
	t.Helper()
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return todayStart.AddDate(0, 0, -back).Add(12 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
}

// retroRange returns explicit ?from&to covering the last n local days
// (inclusive of today) so the prev-window math is deterministic in tests.
func retroRange(n int) string {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	from := todayStart.AddDate(0, 0, -(n - 1)).Format(dayFmt)
	return "from=" + from + "&to=" + todayStart.Format(dayFmt)
}

// retroAgentsServer seeds a 7-day current window (today) and a matching prev
// window (10 days back): subagent turns with agent_name in both notations
// ("core:tech-lead" + "tech-lead" must fold), a NULL-agent_name main turn,
// subagent_start events with durations, and error events shaped the way
// ingest actually writes them — sidechain rows with a NULL turn_id +
// parent_event_id → subagent_start, plus subagent_stop failures.
func retroAgentsServer(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "retro.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	today := retroDay(t, 0)
	day10 := retroDay(t, 10) // inside the prev window of a 7-day range

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?)`, day10)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, outcome) VALUES
		(1, 1, 'u1', 'completed', ?, 'success'),
		(2, 1, 'u2', 'completed', ?, 'fail'),
		(3, 1, 'u3', 'completed', ?, NULL)`, today, today, day10)

	// Current window: main 0.5/50out; tech-lead 4.0/200out over both notations;
	// debugger has a run but no turns. Prev window: tech-lead 1.0, main 0.25.
	mustExec(`INSERT INTO turns (id, session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd, agent_name) VALUES
		(1, 1, 0, 'assistant', ?, 10, 50,  0.5,  NULL),
		(2, 1, 1, 'assistant', ?, 10, 100, 2.0,  'core:tech-lead'),
		(3, 1, 2, 'assistant', ?, 10, 80,  1.5,  'tech-lead'),
		(4, 2, 0, 'assistant', ?, 10, 20,  0.5,  'tech-lead'),
		(5, 3, 0, 'assistant', ?, 10, 10,  1.0,  'tech-lead'),
		(6, 3, 1, 'assistant', ?, 10, 5,   0.25, NULL)`,
		today, today, today, today, day10, day10)

	// Runs: tech-lead ×3 today (both notations), debugger ×1 (no duration),
	// tech-lead ×2 in the prev window. Event ids are 1…6 in insert order —
	// the error rows below parent onto them. a2 and a5 carry status='error'
	// exactly like ingest: closeToolCall inserts the failed subagent_stop AND
	// mirrors status='error' onto the parent subagent_start — those mirrored
	// start rows must NOT be counted again (they have no parent and would
	// land on "main").
	mustExec(`INSERT INTO events (session_id, ts, type, status, payload, duration_ms, dedup_key) VALUES
		(1, ?, 'subagent_start', 'ok',    '{"subagent_type":"core:tech-lead"}', 1000, 'a1'),
		(1, ?, 'subagent_start', 'error', '{"subagent_type":"tech-lead"}',      3000, 'a2'),
		(2, ?, 'subagent_start', 'ok',    '{"subagent_type":"tech-lead"}',      2000, 'a3'),
		(2, ?, 'subagent_start', 'ok',    '{"subagent_type":"debugger"}',       NULL, 'a4'),
		(3, ?, 'subagent_start', 'error', '{"subagent_type":"tech-lead"}',      NULL, 'a5'),
		(3, ?, 'subagent_start', 'ok',    '{"subagent_type":"tech-lead"}',      NULL, 'a6')`,
		today, today, today, today, day10, day10)

	// Errors the way ingest writes them — every sidechain row has a NULL
	// turn_id (openToolCall zeroes it) and is attributed ONLY through
	// parent_event_id → subagent_start:
	//   e1  sidechain tool error parented to a1 (core:tech-lead) → tech-lead
	//   e2  orchestrator api_error, no parent                    → main
	//   e3  subagent_stop error naming the agent in its OWN
	//       payload (agentType)                                  → tech-lead
	//   e4  prev-window subagent_stop error with an empty own
	//       agentType, falling back to a5's subagent_type        → tech-lead
	//   e5  SECOND sidechain tool error parented to a1           → tech-lead
	// The old events.turn_id → turns.agent_name join counts NONE of these
	// (turn_id is NULL on every row) — the per-agent error assertions below
	// pin the parent-event attribution against that regression. e3/e4 pair
	// with the mirrored status='error' on their parent starts a2/a5 (see
	// above): if subagent_start errors were counted too, main would report 2
	// errors today and the assertions below would fail. e1+e5 share the run
	// a1, so error_rate (failed-run share) must dedupe them: 3 error events
	// today but only 2 failed runs (a1, a2) out of 3.
	mustExec(`INSERT INTO events (session_id, parent_event_id, ts, type, tool_name, status, payload, dedup_key) VALUES
		(1, 1,    ?, 'tool_call',     'Bash',  'error', '{"result":"boom"}', 'e1'),
		(2, NULL, ?, 'error',         NULL,    'error', '{"error":"api"}',   'e2'),
		(1, 2,    ?, 'subagent_stop', 'Agent', 'error', '{"agentType":"core:tech-lead","status":"failed"}', 'e3'),
		(3, 5,    ?, 'subagent_stop', 'Agent', 'error', '{"status":"failed"}', 'e4'),
		(1, 1,    ?, 'tool_call',     'Bash',  'error', '{"result":"boom again"}', 'e5')`,
		today, today, today, day10, today)

	// Phase-2 chips. Ledger: tech-lead has 1 OK + 1 RE-DISPATCH on an in-range
	// task → rate 0.5; the out-of-range task's redispatch row must NOT count.
	// Evals: two runs for the tech-lead registry agent — the NEWEST (3/1) wins.
	mustExec(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id) VALUES
		(1, 1, 'In-range task',  'goal', 'done', ?, ?, 'workspace', 'task-in-range'),
		(2, 1, 'Ancient task',   'goal', 'done', '1999-06-01T00:00:00Z', '1999-06-01T00:00:00Z', 'workspace', 'task-ancient')`,
		today, today)
	mustExec(`INSERT INTO task_delegations (task_id, seq, agent, verdict) VALUES
		(1, 1, 'tech-lead', 'OK'),
		(1, 2, 'tech-lead', 'RE-DISPATCH'),
		(2, 1, 'tech-lead', 'RE-DISPATCH')`)
	mustExec(`INSERT INTO agents (id, name, scope, file_path, current_version_id) VALUES
		(1, 'tech-lead', 'global', '/agents/tech-lead.md', NULL)`)
	mustExec(`INSERT INTO eval_suites (id, agent_id, name, created_at) VALUES (1, 1, 'routing', ?)`, today)
	mustExec(`INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at) VALUES
		(1, 1, 'h1', 'v1', ?)`, today)
	mustExec(`INSERT INTO eval_runs (suite_id, agent_version_id, started_at, finished_at, passed, failed) VALUES
		(1, 1, '2026-07-01T00:00:00Z', '2026-07-01T00:05:00Z', 1, 3),
		(1, 1, '2026-07-10T00:00:00Z', '2026-07-10T00:05:00Z', 3, 1)`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db
}

func TestRetroAgents(t *testing.T) {
	srv, db := retroAgentsServer(t)
	var out retroAgentsDTO
	getJSON(t, srv.URL+"/api/retro/agents?"+retroRange(7), &out)

	t.Run("main fold is top-level, excluded from agents", func(t *testing.T) {
		if out.Main.CostUSD != 0.5 || out.Main.TokensOut != 50 {
			t.Errorf("main = %+v, want cost 0.5 tokens_out 50", out.Main)
		}
		if out.Main.Errors != 1 {
			t.Errorf("main = %+v, want errors 1 (the unparented api_error)", out.Main)
		}
		for _, a := range out.Agents {
			if a.Agent == "main" {
				t.Errorf("agents[] contains the main fold key")
			}
		}
		if out.Approx {
			t.Errorf("approx = true, want false without rollups")
		}
	})

	t.Run("per-agent aggregation folds notations", func(t *testing.T) {
		if len(out.Agents) != 2 {
			t.Fatalf("agents = %d, want 2 (%+v)", len(out.Agents), out.Agents)
		}
		tl := out.Agents[0] // runs desc → tech-lead first
		if tl.Agent != "tech-lead" || tl.Runs != 3 || tl.Sessions != 2 {
			t.Errorf("tech-lead = %+v, want runs 3 sessions 2", tl)
		}
		if tl.CostUSD != 4.0 || tl.TokensOut != 200 {
			t.Errorf("tech-lead = %+v, want cost 4.0 tokens_out 200", tl)
		}
		// e1+e5 (sidechain tool errors via parent a1) + e3 (subagent_stop
		// naming its own agentType) — a turn_id join would report 0 here.
		// error_rate is the failed-run share: e1 and e5 belong to the SAME run
		// (a1), so 3 error events fold to 2 failed runs of 3 — 2/3, not 3/3.
		if tl.Errors != 3 || !almostEq(tl.ErrorRate, 2.0/3.0) {
			t.Errorf("tech-lead = %+v, want errors 3 rate 2/3 (failed-run share)", tl)
		}
		if tl.AvgMs == nil || *tl.AvgMs != 2000 {
			t.Errorf("tech-lead avg_ms = %v, want 2000", tl.AvgMs)
		}
		if tl.P95Ms == nil || *tl.P95Ms != 3000 {
			t.Errorf("tech-lead p95_ms = %v, want 3000", tl.P95Ms)
		}
		// Judged sessions with tech-lead turns: s1 success, s2 fail → 0.5.
		if tl.SuccessRate == nil || *tl.SuccessRate != 0.5 {
			t.Errorf("tech-lead success_rate = %v, want 0.5", tl.SuccessRate)
		}

		dbg := out.Agents[1]
		if dbg.Agent != "debugger" || dbg.Runs != 1 || dbg.Errors != 0 || dbg.ErrorRate != 0 {
			t.Errorf("debugger = %+v, want runs 1 errors 0", dbg)
		}
		if dbg.CostUSD != 0 || dbg.AvgMs != nil || dbg.P95Ms != nil || dbg.SuccessRate != nil {
			t.Errorf("debugger = %+v, want no cost/durations/rate", dbg)
		}
	})

	t.Run("re-dispatch rate and eval chip", func(t *testing.T) {
		tl := out.Agents[0]
		// 2 in-range ledger rows, 1 redispatch — the ancient task's row is out.
		if tl.ReDispatchRate == nil || !almostEq(*tl.ReDispatchRate, 0.5) {
			t.Errorf("tech-lead re_dispatch_rate = %v, want 0.5", tl.ReDispatchRate)
		}
		// Latest run wins (3/1, started 07-10) over the older 1/3.
		if tl.Eval == nil || tl.Eval.Passed != 3 || tl.Eval.Failed != 1 {
			t.Errorf("tech-lead eval = %+v, want passed 3 failed 1 (latest run)", tl.Eval)
		}
		dbg := out.Agents[1]
		if dbg.ReDispatchRate != nil || dbg.Eval != nil {
			t.Errorf("debugger = rate %v eval %+v, want nil/nil (no ledger rows, no runs)", dbg.ReDispatchRate, dbg.Eval)
		}
	})

	t.Run("prev window math", func(t *testing.T) {
		// 7-day range → prev is days -13…-7; day10 sits inside it. The prev
		// error is e4 — a subagent_stop with an empty own agentType that must
		// fall back to its parent a5's subagent_type. Rate 0.5 = failed-run
		// share: 1 failed run (a5) of 2.
		tl := out.Agents[0]
		if tl.Prev.Runs != 2 || tl.Prev.Errors != 1 || !almostEq(tl.Prev.ErrorRate, 0.5) || tl.Prev.CostUSD != 1.0 {
			t.Errorf("tech-lead prev = %+v, want runs 2 errors 1 rate 0.5 cost 1.0", tl.Prev)
		}
		dbg := out.Agents[1]
		if dbg.Prev.Runs != 0 || dbg.Prev.Errors != 0 || dbg.Prev.ErrorRate != 0 || dbg.Prev.CostUSD != 0 {
			t.Errorf("debugger prev = %+v, want zeroes", dbg.Prev)
		}
	})

	t.Run("narrow range excludes prev-window activity from current", func(t *testing.T) {
		// A 3-day range ends before day10 in BOTH windows → prev is empty.
		var short retroAgentsDTO
		getJSON(t, srv.URL+"/api/retro/agents?"+retroRange(3), &short)
		if len(short.Agents) != 2 {
			t.Fatalf("agents = %d, want 2", len(short.Agents))
		}
		if short.Agents[0].Prev.Runs != 0 || short.Agents[0].Prev.CostUSD != 0 {
			t.Errorf("prev = %+v, want empty (day10 outside a 3-day prev window)", short.Agents[0].Prev)
		}
	})

	t.Run("approx covers the prev window", func(t *testing.T) {
		// A rollup on day -10 sits OUTSIDE the current 7-day range but INSIDE
		// its prev window (days -13…-7) — approx must still flip, because the
		// prev column undercounts there.
		day := time.Now().AddDate(0, 0, -10).Format(dayFmt)
		if _, err := db.Exec(
			`INSERT INTO daily_rollups (day, project_id, agent_id, sessions) VALUES (?, 1, NULL, 1)`,
			day); err != nil {
			t.Fatalf("insert rollup: %v", err)
		}
		var rolled retroAgentsDTO
		getJSON(t, srv.URL+"/api/retro/agents?"+retroRange(7), &rolled)
		if !rolled.Approx {
			t.Errorf("approx = false, want true (rollup overlaps the prev window)")
		}
	})
}

// retroFrictionServer seeds denied tool calls (Bash covered by an exact
// global rule, Read by a paren pattern, WebFetch only by a DISABLED rule,
// Grep only by a rule scoped to ANOTHER project), two same-key error events,
// and one resolved + one out-of-range pending permission request.
func retroFrictionServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "friction.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	today := retroDay(t, 0)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  'Beta',  ?)`, today, today)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(1, 1, 'uuid-one', 'completed', ?),
		(2, 1, 'uuid-two', 'completed', ?)`, today, today)

	// Bash: 3 calls / 2 denied; Read: 2 / 1; WebFetch: 1 / 1; Grep: 1 / 1;
	// Edit: 1 / 0 (never denied → excluded from the board).
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key) VALUES
		(1, ?, 'tool_call', 'Bash',     'denied', '{}', 'd1'),
		(1, ?, 'tool_call', 'Bash',     'denied', '{}', 'd2'),
		(1, ?, 'tool_call', 'Bash',     'ok',     '{}', 'd3'),
		(1, ?, 'tool_call', 'Read',     'denied', '{}', 'd4'),
		(1, ?, 'tool_call', 'Read',     'ok',     '{}', 'd5'),
		(2, ?, 'tool_call', 'WebFetch', 'denied', '{}', 'd6'),
		(2, ?, 'tool_call', 'Edit',     'ok',     '{}', 'd7'),
		(2, ?, 'tool_call', 'Grep',     'denied', '{}', 'd8')`,
		today, today, today, today, today, today, today, today)

	// Two api_errors differing only in the request id → ONE group, 2 sessions.
	mustExec(`INSERT INTO events (session_id, ts, type, status, payload, dedup_key) VALUES
		(1, ?, 'error', 'error', '{"error":{"message":"API Error 529 overloaded (req_011abc)"}}', 'g1'),
		(2, ?, 'error', 'error', '{"error":{"message":"API Error 529 overloaded (req_022xyz)"}}', 'g2')`,
		today, today)

	// has_rule: exact tool pattern, Tool(argGlob) pattern, a disabled rule
	// that must NOT count, and an enabled rule scoped to project 2 — visible
	// unscoped, filtered out under ?project=<project 1>.
	mustExec(`INSERT INTO approval_rules (project_id, tool_pattern, action, enabled, created_at) VALUES
		(NULL, 'Bash',         'approve', 1, ?),
		(NULL, 'Read(/tmp/*)', 'approve', 1, ?),
		(NULL, 'WebFetch(*)',  'approve', 0, ?),
		(2,    'Grep',         'approve', 1, ?)`, today, today, today, today)

	// One resolved (300 s wait) + one pending permission request. The pending
	// one was opened 30 days ago — OUTSIDE the 7-day range — because
	// "pending now" must not be range-filtered.
	resolvedAt := time.Now().UTC().Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	requestedAt := time.Now().UTC().Add(-1*time.Hour - 300*time.Second).Format("2006-01-02T15:04:05.000Z")
	pendingAt := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02T15:04:05.000Z")
	mustExec(`INSERT INTO permission_requests (session_id, tool_name, request_json, status, requested_at, resolved_at) VALUES
		(1, 'Bash', '{}', 'approved', ?, ?),
		(1, 'Read', '{}', 'pending',  ?, NULL)`, requestedAt, resolvedAt, pendingAt)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestRetroFriction(t *testing.T) {
	srv := retroFrictionServer(t)
	var out frictionDTO
	getJSON(t, srv.URL+"/api/retro/friction?"+retroRange(7), &out)

	t.Run("denied tools ranked, never-denied excluded", func(t *testing.T) {
		if len(out.DeniedTools) != 4 {
			t.Fatalf("denied_tools = %+v, want 4 rows", out.DeniedTools)
		}
		bash := out.DeniedTools[0] // denied desc → Bash first
		if bash.Tool != "Bash" || bash.Denied != 2 || bash.Calls != 3 {
			t.Errorf("bash = %+v, want denied 2 calls 3", bash)
		}
		for _, d := range out.DeniedTools {
			if d.Tool == "Edit" {
				t.Errorf("Edit surfaced with zero denials")
			}
		}
	})

	t.Run("has_rule matches exact and paren patterns, enabled only", func(t *testing.T) {
		byTool := map[string]frictionDeniedDTO{}
		for _, d := range out.DeniedTools {
			byTool[d.Tool] = d
		}
		if !byTool["Bash"].HasRule {
			t.Errorf("Bash has_rule = false, want true (exact 'Bash' rule)")
		}
		if !byTool["Read"].HasRule {
			t.Errorf("Read has_rule = false, want true ('Read(/tmp/*)' rule)")
		}
		if byTool["WebFetch"].HasRule {
			t.Errorf("WebFetch has_rule = true, want false (its rule is disabled)")
		}
		// Unscoped requests see rules of EVERY project scope.
		if !byTool["Grep"].HasRule {
			t.Errorf("Grep has_rule = false, want true unscoped (project-2 rule counts)")
		}
	})

	t.Run("has_rule respects ?project scope", func(t *testing.T) {
		var scoped frictionDTO
		getJSON(t, srv.URL+"/api/retro/friction?"+retroRange(7)+"&project=-work-alpha", &scoped)
		byTool := map[string]frictionDeniedDTO{}
		for _, d := range scoped.DeniedTools {
			byTool[d.Tool] = d
		}
		if !byTool["Bash"].HasRule {
			t.Errorf("Bash has_rule = false under scope, want true (global rule)")
		}
		if byTool["Grep"].HasRule {
			t.Errorf("Grep has_rule = true under scope, want false (rule belongs to another project)")
		}
	})

	t.Run("error groups fold ids and carry session uuids", func(t *testing.T) {
		if len(out.ErrorGroups) != 1 {
			t.Fatalf("error_groups = %+v, want 1 folded group", out.ErrorGroups)
		}
		g := out.ErrorGroups[0]
		if g.Count != 2 {
			t.Errorf("count = %d, want 2", g.Count)
		}
		if len(g.Sessions) != 2 || g.Sessions[0] == g.Sessions[1] {
			t.Fatalf("sessions = %v, want two distinct uuids", g.Sessions)
		}
		for _, u := range g.Sessions {
			if u != "uuid-one" && u != "uuid-two" {
				t.Errorf("session %q is not a seeded uuid", u)
			}
		}
	})

	t.Run("approvals waits and pending", func(t *testing.T) {
		a := out.Approvals
		// The pending request was opened 30 days ago — outside the range —
		// and must STILL count: "pending now" is not range-filtered.
		if a.Resolved != 1 || a.Pending != 1 {
			t.Errorf("approvals = %+v, want resolved 1 pending 1", a)
		}
		if a.AvgResolveSec == nil || !almostEq(*a.AvgResolveSec, 300) {
			t.Errorf("avg_resolve_sec = %v, want 300", a.AvgResolveSec)
		}
		if !almostEq(a.WaitTotalMin, 5) {
			t.Errorf("wait_total_min = %v, want 5", a.WaitTotalMin)
		}
	})

	t.Run("approx false without rollups", func(t *testing.T) {
		if out.Approx {
			t.Errorf("approx = true, want false without rolled-up days")
		}
	})
}

// Empty ranges must serialize as [] (never null) so the UI can map without
// guards — asserted on the raw JSON, not the decoded structs.
func TestRetroEmptyRange(t *testing.T) {
	srv, _ := retroAgentsServer(t)
	past := "from=2000-01-01&to=2000-01-07"

	fetchBody := func(path string) string {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, res.StatusCode)
		}
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	agents := fetchBody("/api/retro/agents?" + past)
	if !strings.Contains(agents, `"agents":[]`) {
		t.Errorf("agents body = %s, want \"agents\":[]", agents)
	}

	friction := fetchBody("/api/retro/friction?" + past)
	for _, want := range []string{`"denied_tools":[]`, `"error_groups":[]`} {
		if !strings.Contains(friction, want) {
			t.Errorf("friction body = %s, want %s", friction, want)
		}
	}

	lessons := fetchBody("/api/retro/lessons?" + past)
	if !strings.Contains(lessons, `"lessons":[]`) {
		t.Errorf("lessons body = %s, want \"lessons\":[]", lessons)
	}
	tasks := fetchBody("/api/retro/tasks?" + past)
	if !strings.Contains(tasks, `"tasks":[]`) {
		t.Errorf("tasks body = %s, want \"tasks\":[]", tasks)
	}
}

// retroArtifactsServer seeds the phase-2 artifact tables directly (the parse
// path is covered in internal/wsingest): two in-range tasks — one with a full
// retro + loops + ledger, one with loops only — plus an out-of-range task
// whose lesson/loops must never surface.
func retroArtifactsServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "artifacts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	today := retroDay(t, 0)
	day1 := retroDay(t, 1)
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
		(1, 1, 'Ship retro phase 2', 'goal', 'done',    ?, ?, 'workspace', 'task-new'),
		(2, 1, 'Loops-only task',    'goal', 'running', ?, ?, 'workspace', 'task-loops'),
		(3, 1, 'No artifacts',       'goal', 'running', ?, ?, 'workspace', 'task-bare'),
		(4, 1, 'Ancient task',       'goal', 'done',    ?, ?, 'workspace', 'task-old')`,
		today, today, day1, day1, today, today, day30, day30)

	mustExec(`INSERT INTO task_retros (id, task_id, estimated_hours, actual_hours, variance_pct, ingested_at) VALUES
		(1, 1, 6, 8, 33, ?),
		(2, 4, 1, 1, 0,  ?)`, today, today)
	mustExec(`INSERT INTO retro_lessons (retro_id, seq, title, body, action) VALUES
		(1, 1, 'Pin fixture mtimes', 'git drops mtimes', 'add pinMtime helpers'),
		(1, 2, 'Check templates early', NULL, NULL),
		(2, 1, 'Ancient lesson', NULL, NULL)`)
	mustExec(`INSERT INTO task_loops (task_id, loop_n, failed, brief_delta) VALUES
		(1, 1, 'go test', 'fix fixture'),
		(1, 2, 'tsc',     'extend types'),
		(2, 1, 'lint',    'imports')`)
	mustExec(`INSERT INTO task_delegations (task_id, seq, agent, verdict) VALUES
		(1, 1, 'context-gatherer',     'OK'),
		(1, 2, 'implementation-agent', 'RE-DISPATCH'),
		(1, 3, 'implementation-agent', 'ok'),
		(4, 1, 'implementation-agent', 'RE-DISPATCH')`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestIsRedispatch pins the bilingual verdict classifier: canonical English
// and Ukrainian re-dispatch verdicts hit, accepts don't, and a long prose cell
// that merely mentions a failure is commentary — never a verdict.
func TestIsRedispatch(t *testing.T) {
	cases := []struct {
		verdict string
		want    bool
	}{
		{"RE-DISPATCH", true},
		{"redispatch", true},
		{"redo", true},
		{"FAILED", true},
		{"rejected", true},
		{"відхилено", true},
		{"провалено", true},
		{"повторити", true},
		{"фейл", true},
		{"OK", false},
		{"ok", false},
		{"ACCEPT", false},
		{"прийнято", false},
		// Word anchors: benign substrings must not hit.
		{"failsafe verified", false},
		// > 40 runes → prose commentary, not a verdict cell.
		{"OK, but flaky test failure noted across three independent reruns", false},
	}
	for _, c := range cases {
		if got := isRedispatch(c.verdict); got != c.want {
			t.Errorf("isRedispatch(%q) = %v, want %v", c.verdict, got, c.want)
		}
	}
}

func TestRetroLessons(t *testing.T) {
	srv := retroArtifactsServer(t)
	var out retroLessonsDTO
	getJSON(t, srv.URL+"/api/retro/lessons?"+retroRange(7), &out)

	if len(out.Lessons) != 2 {
		t.Fatalf("lessons = %+v, want 2 (the ancient task is out of range)", out.Lessons)
	}
	l1 := out.Lessons[0]
	if l1.TaskExternalID != "task-new" || l1.Title != "Pin fixture mtimes" || l1.Seq != 1 {
		t.Errorf("lesson[0] = %+v, want task-new / 'Pin fixture mtimes' / seq 1", l1)
	}
	if l1.Action == nil || *l1.Action != "add pinMtime helpers" ||
		l1.Body == nil || *l1.Body != "git drops mtimes" {
		t.Errorf("lesson[0] action/body = %v/%v, want the seeded values", l1.Action, l1.Body)
	}
	if l1.Date == "" || l1.TaskTitle != "Ship retro phase 2" {
		t.Errorf("lesson[0] date/title = %q/%q, want a YYYY-MM-DD date + the task title", l1.Date, l1.TaskTitle)
	}
	l2 := out.Lessons[1]
	if l2.Title != "Check templates early" || l2.Seq != 2 || l2.Action != nil || l2.Body != nil {
		t.Errorf("lesson[1] = %+v, want the seq-2 action-less lesson with nil action/body", l2)
	}
}

func TestRetroTasks(t *testing.T) {
	srv := retroArtifactsServer(t)
	var out retroTasksDTO
	getJSON(t, srv.URL+"/api/retro/tasks?"+retroRange(7), &out)

	// task-bare has no artifacts, task-old is out of range → 2 rows,
	// newest task first.
	if len(out.Tasks) != 2 {
		t.Fatalf("tasks = %+v, want 2", out.Tasks)
	}
	full := out.Tasks[0]
	if full.ExternalID != "task-new" {
		t.Fatalf("tasks[0] = %+v, want task-new (newest first)", full)
	}
	if full.EstimatedHours == nil || *full.EstimatedHours != 6 ||
		full.ActualHours == nil || *full.ActualHours != 8 ||
		full.VariancePct == nil || *full.VariancePct != 33 {
		t.Errorf("task-new hours = %+v, want 6/8/33", full)
	}
	if full.Loops != 2 || full.Delegations != 3 {
		t.Errorf("task-new loops/delegations = %d/%d, want 2/3", full.Loops, full.Delegations)
	}
	// 'OK' + 'ok' accept, 'RE-DISPATCH' redispatches.
	if full.Verdicts.OK != 2 || full.Verdicts.Redispatch != 1 {
		t.Errorf("task-new verdicts = %+v, want ok 2 redispatch 1", full.Verdicts)
	}

	loopsOnly := out.Tasks[1]
	if loopsOnly.ExternalID != "task-loops" {
		t.Fatalf("tasks[1] = %+v, want task-loops", loopsOnly)
	}
	if loopsOnly.EstimatedHours != nil || loopsOnly.ActualHours != nil || loopsOnly.VariancePct != nil {
		t.Errorf("task-loops hours = %+v, want all nil (no retro doc)", loopsOnly)
	}
	if loopsOnly.Loops != 1 || loopsOnly.Delegations != 0 ||
		loopsOnly.Verdicts.OK != 0 || loopsOnly.Verdicts.Redispatch != 0 {
		t.Errorf("task-loops = %+v, want loops 1 and zero delegations/verdicts", loopsOnly)
	}
}

// ── phase 3: advisor recommendations ──────────────────────────────────────

// retroRecServer seeds one recommendation per lifecycle status plus enough
// denied Bash events that POST /api/retro/advise has something to propose
// and PATCH-accept has a metric to snapshot.
func retroRecServer(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "recs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	today := retroDay(t, 0)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?)`, today)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(1, 1, 'uuid-one', 'completed', ?)`, today)
	// Stamp the denied events yesterday, not today: retroDay(0) is noon today,
	// which is in the FUTURE when tests run in the morning — and the advise
	// endpoint's window ends at time.Now(), so future events silently fall out
	// (date-flaky: green after noon, red before).
	yesterday := retroDay(t, 1)
	for i := 0; i < 6; i++ {
		mustExec(`INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
			VALUES (1, ?, 'tool_call', 'Bash', 'denied', '{}', ?)`,
			yesterday, "den-"+string(rune('a'+i)))
	}
	for i, st := range []string{"proposed", "accepted", "dismissed", "adopted", "verified"} {
		mustExec(`INSERT INTO recommendations
			(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
			VALUES (?, 'R1', 'tool', ?, 'Title', 'Detail', '{"counts":{"denied":6}}', ?, ?, ?, ?)`,
			i+1, "Tool"+st, st, "R1:Tool"+st, today, today)
	}

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db
}

type recsResp struct {
	Recommendations []struct {
		ID       int64           `json:"id"`
		Rule     string          `json:"rule"`
		Target   string          `json:"target"`
		Status   string          `json:"status"`
		Evidence json.RawMessage `json:"evidence"`
		Baseline json.RawMessage `json:"baseline"`
	} `json:"recommendations"`
}

func TestRetroRecommendationsList(t *testing.T) {
	srv, _ := retroRecServer(t)

	statuses := func(path string) map[string]bool {
		t.Helper()
		var out recsResp
		getJSON(t, srv.URL+path, &out)
		got := map[string]bool{}
		for _, r := range out.Recommendations {
			got[r.Status] = true
		}
		return got
	}

	t.Run("default filter is the actionable set", func(t *testing.T) {
		got := statuses("/api/retro/recommendations")
		want := map[string]bool{"proposed": true, "accepted": true, "adopted": true}
		if len(got) != len(want) || !got["proposed"] || !got["accepted"] || !got["adopted"] {
			t.Errorf("statuses = %v, want exactly %v", got, want)
		}
	})

	t.Run("explicit CSV filter", func(t *testing.T) {
		got := statuses("/api/retro/recommendations?status=verified,dismissed")
		if len(got) != 2 || !got["verified"] || !got["dismissed"] {
			t.Errorf("statuses = %v, want verified+dismissed", got)
		}
	})

	t.Run("status=all returns everything", func(t *testing.T) {
		var out recsResp
		getJSON(t, srv.URL+"/api/retro/recommendations?status=all", &out)
		if len(out.Recommendations) != 5 {
			t.Errorf("rows = %d, want all 5", len(out.Recommendations))
		}
		// Evidence must be raw JSON passthrough, not a re-encoded string.
		var ev struct {
			Counts map[string]int64 `json:"counts"`
		}
		if err := json.Unmarshal(out.Recommendations[0].Evidence, &ev); err != nil || ev.Counts["denied"] != 6 {
			t.Errorf("evidence = %s, want the raw counts object", out.Recommendations[0].Evidence)
		}
		// Seeded rows carry no baseline snapshot — the DTO folds NULL to null.
		if string(out.Recommendations[0].Baseline) != "null" {
			t.Errorf("baseline = %s, want null for a never-accepted row", out.Recommendations[0].Baseline)
		}
	})

	t.Run("unknown status is 400", func(t *testing.T) {
		res, err := http.Get(srv.URL + "/api/retro/recommendations?status=bogus")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", res.StatusCode)
		}
	})
}

func TestPatchRecommendation(t *testing.T) {
	srv, db := retroRecServer(t)
	url := func(id int) string {
		return srv.URL + "/api/retro/recommendations/" + strconv.Itoa(id)
	}

	t.Run("accept writes the baseline snapshot", func(t *testing.T) {
		out := doJSON(t, http.MethodPatch, url(1), map[string]any{"status": "accepted"}, http.StatusOK)
		if out["status"] != "accepted" {
			t.Errorf("status = %v, want accepted", out["status"])
		}
		// The fresh snapshot must ride back on the PATCH response itself (the
		// UI derives its verification countdown from it without a refetch).
		if bm, ok := out["baseline"].(map[string]any); !ok || bm["accepted_at"] == "" {
			t.Errorf("response baseline = %v, want the snapshot object with accepted_at", out["baseline"])
		}
		var base sql.NullString
		if err := db.QueryRow(`SELECT baseline FROM recommendations WHERE id = 1`).Scan(&base); err != nil {
			t.Fatal(err)
		}
		if !base.Valid {
			t.Fatal("baseline not written on accept")
		}
		var b struct {
			Metric     string  `json:"metric"`
			Value      float64 `json:"value"`
			AcceptedAt string  `json:"accepted_at"`
		}
		if err := json.Unmarshal([]byte(base.String), &b); err != nil {
			t.Fatalf("baseline %q: %v", base.String, err)
		}
		// The seeded rec targets "Toolproposed" — 0 denied events carry that
		// tool name, so the snapshot is denied_per_day 0 with accepted_at set.
		if b.Metric != "denied_per_day" || b.AcceptedAt == "" {
			t.Errorf("baseline = %+v, want a denied_per_day snapshot with accepted_at", b)
		}
	})

	t.Run("accepted can still be dismissed", func(t *testing.T) {
		out := doJSON(t, http.MethodPatch, url(1), map[string]any{"status": "dismissed"}, http.StatusOK)
		if out["status"] != "dismissed" {
			t.Errorf("status = %v, want dismissed", out["status"])
		}
	})

	t.Run("illegal transitions are 422", func(t *testing.T) {
		// dismissed (id 3), adopted (4), verified (5) can't be re-accepted;
		// nothing accepts a dismissal twice either.
		for _, id := range []int{3, 4, 5} {
			doJSON(t, http.MethodPatch, url(id), map[string]any{"status": "accepted"},
				http.StatusUnprocessableEntity)
		}
		doJSON(t, http.MethodPatch, url(3), map[string]any{"status": "dismissed"},
			http.StatusUnprocessableEntity)
		// Lifecycle statuses are never PATCHable, even from proposed.
		doJSON(t, http.MethodPatch, url(2), map[string]any{"status": "verified"},
			http.StatusUnprocessableEntity)
	})

	t.Run("unknown id is 404", func(t *testing.T) {
		doJSON(t, http.MethodPatch, url(424242), map[string]any{"status": "accepted"},
			http.StatusNotFound)
	})
}

// TestPatchRecommendationConflict drives the guarded-write 409 path: the
// recPatchHook test seam mutates the row between the handler's status read
// and its guarded UPDATE (a concurrent dismiss winning the race), so the
// UPDATE affects 0 rows and the handler answers 409 with the current status.
func TestPatchRecommendationConflict(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "conflict.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO recommendations
		(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (1, 'R1', 'tool', 'Bash', 'Title', 'Detail', '{}', 'proposed', 'R1:Bash', ?, ?)`,
		retroDay(t, 0), retroDay(t, 0)); err != nil {
		t.Fatalf("seed rec: %v", err)
	}

	h := &Handler{DB: db}
	h.recPatchHook = func() {
		if _, err := db.Exec(`UPDATE recommendations SET status = 'dismissed' WHERE id = 1`); err != nil {
			t.Fatalf("hook update: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/retro/recommendations/1",
		strings.NewReader(`{"status":"accepted"}`))
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.patchRecommendation(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if body.Status != "dismissed" || body.Error == "" {
		t.Errorf("body = %+v, want the current status (dismissed) and an error message", body)
	}
	var cur string
	if err := db.QueryRow(`SELECT status FROM recommendations WHERE id = 1`).Scan(&cur); err != nil {
		t.Fatal(err)
	}
	if cur != "dismissed" {
		t.Errorf("db status = %q, want the concurrent dismissed kept", cur)
	}
}

func TestRetroAdvise(t *testing.T) {
	srv, db := retroRecServer(t)

	out := doJSON(t, http.MethodPost, srv.URL+"/api/retro/advise", map[string]any{}, http.StatusOK)
	// 6 denied Bash events + no covering rule → R1 proposes a Bash rec.
	if p, ok := out["proposed"].(float64); !ok || p < 1 {
		t.Errorf("advise stats = %v, want proposed >= 1", out)
	}
	var status string
	if err := db.QueryRow(
		`SELECT status FROM recommendations WHERE rule = 'R1' AND target = 'Bash'`).
		Scan(&status); err != nil {
		t.Fatalf("Bash rec after advise: %v", err)
	}
	if status != "proposed" {
		t.Errorf("Bash rec status = %q, want proposed", status)
	}
}
