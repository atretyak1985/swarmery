package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/probe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const (
	probeTarget = "plugins/core/agents/demo.md"
	probeBase   = "# demo\n\nAnswer in one line.\n"
	probeDiff   = "--- a/plugins/core/agents/demo.md\n+++ b/plugins/core/agents/demo.md\n" +
		"@@ -1,3 +1,4 @@\n # demo\n \n Answer in one line.\n+Report the exit code.\n"
	probeSuite = "- description: one line\n  vars:\n    system: file://../../plugins/core/agents/demo.md\n    task: hi\n" +
		"- description: exit code\n  vars:\n    system: file://../../plugins/core/agents/demo.md\n    task: run\n"
)

type probeTreeSource struct{}

func (probeTreeSource) Export(_ context.Context, dst string, _ []string) error {
	for rel, body := range map[string]string{
		probeTarget: probeBase, "evals/agents/demo.yaml": probeSuite,
		"plugins/core/.claude-plugin/plugin.json": `{"name":"core"}`,
		"evals/bench/tasks/b01-fix/prompt.md":     "fix it",
		"evals/bench/tasks/b01-fix/meta.json":     `{"agents":["demo"]}`,
		"evals/bench/tasks/b02-noop/prompt.md":    "already fixed",
	} {
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type probeSuiteFake struct{}

func (probeSuiteFake) RunCase(_ context.Context, tree string, c probe.Case) (probe.CaseRun, error) {
	variant := filepath.Base(tree) == "variant"
	// The variant fails the exit-code case: a split the judge must not see.
	pass := !(variant && c.ID == "exit code")
	return probe.CaseRun{HardPass: pass, Task: "t", Output: filepath.Base(tree), USD: 0.01}, nil
}

// probeBenchFake: the variant fails b01-fix, both sides pass b02-noop.
type probeBenchFake struct{ agents []string }

func (f *probeBenchFake) RunTask(_ context.Context, tree string, side probe.BenchSide, t probe.BenchTask) (probe.BenchRun, error) {
	f.agents = append(f.agents, side.Agent)
	pass := !(filepath.Base(tree) == "variant" && t.ID == "b01-fix")
	return probe.BenchRun{Pass: pass, USD: 0.1, Reply: "done"}, nil
}

type probeJudgeFake struct{ calls int }

func (j *probeJudgeFake) Judge(context.Context, string) (string, float64, error) {
	j.calls++
	return "same\nCHOICE: TIE", 0.005, nil
}

func probeServer(t *testing.T) (*httptest.Server, *sql.DB, *probeJudgeFake) {
	srv, db, judge, _ := probeServerWith(t, &probeBenchFake{})
	return srv, db, judge
}

// probeServerWith wires bench as the bench runner (nil = not attached).
func probeServerWith(t *testing.T, bench probe.BenchRunner) (*httptest.Server, *sql.DB, *probeJudgeFake, *probe.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "probe-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	judge := &probeJudgeFake{}
	svc := &probe.Service{
		DB: db, Source: probeTreeSource{}, Suite: probeSuiteFake{}, Judge: judge, Bench: bench,
		MaxUSD: 1, BenchMaxUSD: 3, Go: func(fn func()) { fn() }, Flip: func() bool { return true },
	}
	h := &Handler{DB: db, ProposalProbe: svc}
	mux := http.NewServeMux()
	Routes(mux, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, db, judge, svc
}

func seedProbeProposal(t *testing.T, db *sql.DB, agent, kind, agentPath, targetPath, status string) int64 {
	t.Helper()
	sum := sha256.Sum256([]byte(probeBase))
	res, err := db.Exec(`INSERT INTO agent_change_proposals
		(agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'why', ?, '2026-11-03T00:00:00.000Z')`,
		agent, agentPath, kind, targetPath, hex.EncodeToString(sum[:]), probeDiff, status)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func probePost(t *testing.T, url string, want int) map[string]any {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != want {
		t.Fatalf("POST %s: status %d, want %d (%v)", url, resp.StatusCode, want, body)
	}
	return body
}

func TestProposalProbeRunsAsAJobAndListsRows(t *testing.T) {
	srv, db, judge := probeServer(t)
	id := seedProbeProposal(t, db, "demo", "agent", probeTarget, "", "proposed")
	url := srv.URL + "/api/improve/proposals/" + itoa(id)

	body := probePost(t, url+"/probe", http.StatusAccepted)
	job, _ := body["job"].(map[string]any)
	if job == nil || job["id"] == "" || job["proposal_id"].(float64) != float64(id) {
		t.Fatalf("202 body = %v", body)
	}

	var out struct {
		ProposalID int64       `json:"proposal_id"`
		MaxUSD     float64     `json:"max_usd"`
		Jobs       []probe.Job `json:"jobs"`
		Probes     []probe.Row `json:"probes"`
	}
	getJSON(t, url+"/probes", &out)
	if out.ProposalID != id || out.MaxUSD != 1 || len(out.Jobs) != 1 || out.Jobs[0].Status != probe.JobDone {
		t.Fatalf("envelope = %+v", out)
	}
	if len(out.Probes) != 2 {
		t.Fatalf("probes = %+v", out.Probes)
	}
	tie, split := out.Probes[0], out.Probes[1]
	if tie.BlindChoice != probe.ChoiceTie || split.BlindChoice != probe.ChoiceSkipped ||
		*split.BasePass != true || *split.VariantPass != false || tie.JobID != out.Jobs[0].ID {
		t.Errorf("rows = %+v / %+v", tie, split)
	}
	if judge.calls != 1 {
		t.Errorf("judge ran %d times, want only the tie", judge.calls)
	}

	var status string
	var decided sql.NullString
	if err := db.QueryRow(`SELECT status, decided_at FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&status, &decided); err != nil {
		t.Fatal(err)
	}
	if status != "proposed" || decided.Valid {
		t.Errorf("a probe changed the proposal: status=%s decided=%v", status, decided)
	}
}

func TestProposalProbeRefusals(t *testing.T) {
	srv, db, _ := probeServer(t)
	applied := seedProbeProposal(t, db, "a1", "agent", probeTarget, "", "applied")
	probePost(t, srv.URL+"/api/improve/proposals/"+itoa(applied)+"/probe", http.StatusUnprocessableEntity)
	probePost(t, srv.URL+"/api/improve/proposals/99999/probe", http.StatusNotFound)
	probePost(t, srv.URL+"/api/improve/proposals/abc/probe", http.StatusBadRequest)

	var none struct {
		Probes []probe.Row `json:"probes"`
	}
	getJSON(t, srv.URL+"/api/improve/proposals/"+itoa(applied)+"/probes", &none)
	if len(none.Probes) != 0 {
		t.Errorf("a refused probe wrote rows: %+v", none.Probes)
	}
	resp, err := http.Get(srv.URL + "/api/improve/proposals/99999/probes")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown = %d", resp.StatusCode)
	}
}

func TestProposalProbeSkillIsNoSuite(t *testing.T) {
	srv, db, _ := probeServer(t)
	id := seedProbeProposal(t, db, "lesson", "skill", "", "plugins/core/skills/testing/SKILL.md", "approved")
	probePost(t, srv.URL+"/api/improve/proposals/"+itoa(id)+"/probe", http.StatusAccepted)
	var out struct {
		Probes []probe.Row `json:"probes"`
	}
	getJSON(t, srv.URL+"/api/improve/proposals/"+itoa(id)+"/probes", &out)
	if len(out.Probes) != 1 || out.Probes[0].BlindChoice != probe.ChoiceNoSuite {
		t.Errorf("probes = %+v", out.Probes)
	}
}

func TestProposalProbeConflictWhileRunning(t *testing.T) {
	_, db, _ := probeServer(t)
	id := seedProbeProposal(t, db, "demo", "agent", probeTarget, "", "proposed")
	// Hold the job: the seam parks the closure instead of running it.
	var parked func()
	h := &Handler{DB: db, ProposalProbe: &probe.Service{
		DB: db, Source: probeTreeSource{}, Suite: probeSuiteFake{}, Judge: &probeJudgeFake{},
		Go: func(fn func()) { parked = fn },
	}}
	mux := http.NewServeMux()
	Routes(mux, h)
	held := httptest.NewServer(mux)
	defer held.Close()

	probePost(t, held.URL+"/api/improve/proposals/"+itoa(id)+"/probe", http.StatusAccepted)
	body := probePost(t, held.URL+"/api/improve/proposals/"+itoa(id)+"/probe", http.StatusConflict)
	if body["job"] == nil {
		t.Errorf("409 must name the running job: %v", body)
	}
	parked()
}

func TestProposalProbeBenchSource(t *testing.T) {
	bench := &probeBenchFake{}
	srv, db, judge, _ := probeServerWith(t, bench)
	id := seedProbeProposal(t, db, "demo", "agent", probeTarget, "", "approved")
	url := srv.URL + "/api/improve/proposals/" + itoa(id)

	body := probePost(t, url+"/probe?source=bench", http.StatusAccepted)
	job, _ := body["job"].(map[string]any)
	if job == nil || job["source"] != probe.SourceBench || job["max_usd"].(float64) != 3 {
		t.Fatalf("202 body = %v", body)
	}
	var out struct {
		MaxUSD      float64     `json:"max_usd"`
		BenchMaxUSD float64     `json:"bench_max_usd"`
		Jobs        []probe.Job `json:"jobs"`
		Probes      []probe.Row `json:"probes"`
	}
	getJSON(t, url+"/probes", &out)
	if out.MaxUSD != 1 || out.BenchMaxUSD != 3 || len(out.Jobs) != 1 || out.Jobs[0].Source != probe.SourceBench {
		t.Fatalf("envelope = %+v", out)
	}
	if len(out.Probes) != 2 {
		t.Fatalf("one row per tagged task: %+v", out.Probes)
	}
	split, tie := out.Probes[0], out.Probes[1]
	if split.Source != probe.SourceBench || split.TaskID != "b01-fix" || split.BlindChoice != probe.ChoiceSkipped ||
		tie.TaskID != "b02-noop" || tie.BlindChoice != probe.ChoiceTie {
		t.Errorf("rows = %+v / %+v", split, tie)
	}
	if judge.calls != 1 || len(bench.agents) != 4 || bench.agents[0] != "core:demo" {
		t.Errorf("judge %d, sessions %v", judge.calls, bench.agents)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM agent_change_proposals WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "approved" {
		t.Errorf("a bench probe changed the proposal: %s", status)
	}

	probePost(t, url+"/probe?source=vibes", http.StatusBadRequest)
	unattached, db2, _, _ := probeServerWith(t, nil)
	id2 := seedProbeProposal(t, db2, "demo", "agent", probeTarget, "", "proposed")
	probePost(t, unattached.URL+"/api/improve/proposals/"+itoa(id2)+"/probe?source=bench", http.StatusServiceUnavailable)
}

func TestProposalProbeUnattached(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	probePost(t, srv.URL+"/api/improve/proposals/1/probe", http.StatusServiceUnavailable)
}
