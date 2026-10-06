package api

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// agentFixture: project p (1), session 1, an improve registry that ships
// tech-lead (debugger is not in it), and a real engine with the agent Source.
func agentFixture(t *testing.T, j *advisorJudge) (*sql.DB, *triage.Service, *agentSource) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "triage_agents.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions(id, project_id, session_uuid, status, started_at) VALUES(1,1,'u1','completed','2026-01-02T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{DB: db, Improve: &improve.Service{DB: db, Repo: "/repo",
		Exec: &repoAgentsExec{agents: []string{"tech-lead"}}}}
	src := &agentSource{h: h}
	svc := triage.NewService(db, j)
	svc.Go = func(fn func()) { fn() }
	svc.Register(src)
	return db, svc, src
}

// seedRuns inserts runs subagent_start events for agent, the first failed of
// them with a behaviour error (a sidechain tool error parented to the run).
func seedRuns(t *testing.T, db *sql.DB, agent string, runs, failed int) {
	t.Helper()
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for i := 0; i < runs; i++ {
		res, err := db.Exec(`INSERT INTO events (session_id, ts, type, status, payload, dedup_key)
			VALUES (1, ?, 'subagent_start', 'ok', ?, ?)`, ts, `{"subagent_type":"`+agent+`"}`, fmt.Sprintf("%s-run-%d", agent, i))
		if err != nil {
			t.Fatal(err)
		}
		if i >= failed {
			continue
		}
		parent, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO events (session_id, parent_event_id, ts, type, tool_name, status, payload, dedup_key)
			VALUES (1, ?, ?, 'tool_call', 'Bash', 'error', '{"result":"boom"}', ?)`,
			parent, ts, fmt.Sprintf("%s-err-%d", agent, i)); err != nil {
			t.Fatal(err)
		}
	}
}

func runAgents(t *testing.T, svc *triage.Service) []triage.Verdict {
	t.Helper()
	id, err := svc.Start(triage.StartReq{Kinds: []string{agentTriageKind}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	vs, err := svc.ListVerdicts(triage.VerdictFilter{RunID: id, Kind: agentTriageKind})
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

func collectAgents(t *testing.T, src *agentSource) []string {
	t.Helper()
	items, err := src.Collect(context.Background(), triage.Scope{}, 0)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	var out []string
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

// A failing improvable agent gets ONE T2 recommendation and ONE suggested
// advisor improve verdict, with no judge call; a re-run files nothing; Undo
// dismisses the recommendation (twice is nil).
func TestTriageAgentSourceFilesAndUndo(t *testing.T) {
	j := &advisorJudge{value: "file"}
	db, svc, src := agentFixture(t, j)
	seedRuns(t, db, "tech-lead", 6, 4)
	vs := runAgents(t, svc)
	if len(vs) != 1 || vs[0].State != triage.StateApplied || vs[0].Ref != "tech-lead" || vs[0].Value != "file" {
		t.Fatalf("verdicts = %+v", vs)
	}
	if want := "tech-lead failed in 4 of 6 runs in 14 days (67% behaviour-fixable)"; vs[0].Reason != want {
		t.Fatalf("reason = %q, want %q", vs[0].Reason, want)
	}
	if j.calls != 0 {
		t.Fatalf("judge calls = %d, want 0", j.calls)
	}
	if n := countRecs(t, db, `rule='T2' AND target_kind='agent' AND target='tech-lead' AND status='proposed' AND dedup_key='T2:tech-lead'`); n != 1 {
		t.Fatalf("T2 recs = %d, want 1", n)
	}
	var recID int64
	if err := db.QueryRow(`SELECT id FROM recommendations WHERE rule='T2'`).Scan(&recID); err != nil {
		t.Fatal(err)
	}
	sug, err := svc.ListVerdicts(triage.VerdictFilter{Kind: advisorKind})
	if err != nil {
		t.Fatal(err)
	}
	if len(sug) != 1 || sug[0].State != triage.StateSuggested || sug[0].Value != "improve" ||
		sug[0].Class != triage.ClassImprove || sug[0].Ref != strconv.FormatInt(recID, 10) {
		t.Fatalf("suggestions = %+v", sug)
	}
	if vs2 := runAgents(t, svc); len(vs2) != 0 || countRecs(t, db, `rule='T2'`) != 1 {
		t.Fatalf("second run verdicts = %+v", vs2)
	}
	if err := src.Undo(context.Background(), vs[0]); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if s := recStatus(t, db, recID); s != "dismissed" {
		t.Fatalf("rec after undo = %q", s)
	}
	if err := src.Undo(context.Background(), vs[0]); err != nil {
		t.Fatalf("second undo: %v", err)
	}
}

// Each exclusion keeps the agent out of the listing.
func TestTriageAgentSourceExclusions(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, db *sql.DB)
	}{
		{"below threshold", func(t *testing.T, db *sql.DB) { seedRuns(t, db, "tech-lead", 6, 2) }},
		{"under 5 runs", func(t *testing.T, db *sql.DB) { seedRuns(t, db, "tech-lead", 4, 4) }},
		{"not improvable", func(t *testing.T, db *sql.DB) { seedRuns(t, db, "debugger", 6, 6) }},
		{"open rec of another rule", func(t *testing.T, db *sql.DB) {
			seedRuns(t, db, "tech-lead", 6, 6)
			seedRec(t, db, 40, "R2", "agent", "tech-lead", `{}`)
		}},
		{"rec of another rule dismissed inside the suppression window", func(t *testing.T, db *sql.DB) {
			seedRuns(t, db, "tech-lead", 6, 6)
			seedRec(t, db, 41, "R2", "agent", "tech-lead", `{}`)
			dismissRec(t, db, 41, time.Now().Add(-24*time.Hour))
		}},
		{"open proposal", func(t *testing.T, db *sql.DB) {
			seedRuns(t, db, "tech-lead", 6, 6)
			if _, err := db.Exec(`INSERT INTO agent_change_proposals (agent, agent_path, base_sha256, diff, rationale, status, created_at)
				VALUES ('tech-lead', '/x/tech-lead.md', 's', 'd', 'r', 'proposed', '2026-07-20T00:00:00.000Z')`); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, _, src := agentFixture(t, &advisorJudge{})
			c.seed(t, db)
			if got := collectAgents(t, src); len(got) != 0 {
				t.Fatalf("collected %v, want none", got)
			}
		})
	}
}

// A dismissal older than the suppression window no longer holds the agent back.
func TestTriageAgentSourceOldDismissalDoesNotSuppress(t *testing.T) {
	db, _, src := agentFixture(t, &advisorJudge{})
	seedRuns(t, db, "tech-lead", 6, 6)
	seedRec(t, db, 41, "R2", "agent", "tech-lead", `{}`)
	dismissRec(t, db, 41, time.Now().AddDate(0, 0, -(advisor.DismissSuppressDays+1)))
	if got := collectAgents(t, src); len(got) != 1 || got[0] != "tech-lead" {
		t.Fatalf("collected %v, want tech-lead", got)
	}
}

// The env override moves the threshold; a bad value falls back to 0.5.
func TestTriageAgentSourceThresholdEnv(t *testing.T) {
	db, _, src := agentFixture(t, &advisorJudge{})
	seedRuns(t, db, "tech-lead", 6, 2) // 33%
	t.Setenv(agentErrorRateEnv, "0.3")
	if got := collectAgents(t, src); len(got) != 1 || got[0] != "tech-lead" {
		t.Fatalf("threshold 0.3: collected %v", got)
	}
	for _, bad := range []string{"abc", "0", "1.5", "-0.2"} {
		t.Setenv(agentErrorRateEnv, bad)
		if agentErrorRate() != agentErrorRateDefault {
			t.Fatalf("%q did not fall back", bad)
		}
		if got := collectAgents(t, src); len(got) != 0 {
			t.Fatalf("bad value %q: collected %v", bad, got)
		}
	}
}

// NewServer registers the agent Source: the engine's Undo of an applied agent
// verdict reaches agentSource.Undo, which dismisses the filed recommendation.
// (A NewServer handler has no improve registry in tests, so nothing collects.)
func TestTriageAgentSourceRegisteredByNewServer(t *testing.T) {
	_, svc := serverWithRealSources(t)
	runID, err := svc.Start(triage.StartReq{Kinds: []string{agentTriageKind}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	seedRec(t, svc.DB, 60, "T2", "agent", "tech-lead", `{}`)
	res, err := svc.DB.Exec(`INSERT INTO triage_verdicts
		(run_id, kind, class, ref, item_key, title, value, reason, payload, prior, state, created_at)
		VALUES (?, 'agent', '', 'tech-lead', 'tech-lead', 't', 'file', 'r', '{}', '{"recommendationId":60}', 'applied', ?)`,
		runID, advisorNow())
	if err != nil {
		t.Fatal(err)
	}
	vid, _ := res.LastInsertId()
	if _, err := svc.Undo(context.Background(), vid); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if s := recStatus(t, svc.DB, 60); s != "dismissed" {
		t.Fatalf("rec after undo = %q, want dismissed", s)
	}
}
