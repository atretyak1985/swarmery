package api

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	// The old events.turn_id → turns.agent_name join counts NONE of these
	// (turn_id is NULL on every row) — the per-agent error assertions below
	// pin the parent-event attribution against that regression. e3/e4 pair
	// with the mirrored status='error' on their parent starts a2/a5 (see
	// above): if subagent_start errors were counted too, main would report 2
	// errors today and the assertions below would fail.
	mustExec(`INSERT INTO events (session_id, parent_event_id, ts, type, tool_name, status, payload, dedup_key) VALUES
		(1, 1,    ?, 'tool_call',     'Bash',  'error', '{"result":"boom"}', 'e1'),
		(2, NULL, ?, 'error',         NULL,    'error', '{"error":"api"}',   'e2'),
		(1, 2,    ?, 'subagent_stop', 'Agent', 'error', '{"agentType":"core:tech-lead","status":"failed"}', 'e3'),
		(3, 5,    ?, 'subagent_stop', 'Agent', 'error', '{"status":"failed"}', 'e4')`,
		today, today, today, day10)

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
		// e1 (sidechain tool error via parent a1) + e3 (subagent_stop naming
		// its own agentType) — a turn_id join would report 0 here.
		if tl.Errors != 2 || !almostEq(tl.ErrorRate, 2.0/3.0) {
			t.Errorf("tech-lead = %+v, want errors 2 rate 2/3", tl)
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

	t.Run("prev window math", func(t *testing.T) {
		// 7-day range → prev is days -13…-7; day10 sits inside it. The prev
		// error is e4 — a subagent_stop with an empty own agentType that must
		// fall back to its parent a5's subagent_type.
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
}
