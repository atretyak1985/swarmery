package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// apiTriageSource is a friction Source that never touches real tables: it
// offers one project-less item unless items is set.
type apiTriageSource struct {
	undone int
	items  []triage.Item
}

// A fleet-wide run over items of projects 1 and 2 plus a project-less one:
// ?project=p (id 1) lists project 1's verdict and the project-less one only,
// and the verdict JSON carries projectId (null when absent).
func TestTriageAPIVerdictsFilterByItemProject(t *testing.T) {
	srv, svc, src := serverWithTriage(t)
	if _, err := svc.DB.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(2,'/repo/q','q','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	mk := func(key, ref string, project int64) triage.Item {
		return triage.Item{Kind: "friction", Key: key, Title: key, ProjectID: project,
			Parts: []triage.Part{{Ref: ref, Allowed: []string{"noise", "fixable"}}}}
	}
	src.items = []triage.Item{mk("k1", "f1", 1), mk("k2", "f2", 2), mk("k0", "f0", 0)}
	if resp, body := doRoutineReq(t, http.MethodPost, srv+"/api/triage/runs", map[string]any{}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start = %d %s", resp.StatusCode, body)
	}
	resp, body := doRoutineReq(t, http.MethodGet, srv+"/api/triage/verdicts?project=p", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verdicts = %d %s", resp.StatusCode, body)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	decodeInto(t, body, &out)
	got := map[string]any{}
	for _, v := range out.Items {
		pid, present := v["projectId"]
		if !present {
			t.Fatalf("verdict JSON has no projectId key: %v", v)
		}
		got[v["ref"].(string)] = pid
	}
	if len(got) != 2 || got["f1"] != float64(1) || got["f0"] != nil {
		t.Fatalf("project p verdicts = %v", got)
	}
}

func (*apiTriageSource) Kind() string { return "friction" }
func (s *apiTriageSource) Collect(context.Context, triage.Scope, int) ([]triage.Item, error) {
	if s.items != nil {
		return s.items, nil
	}
	return []triage.Item{{Kind: "friction", Key: "k1", Title: "t",
		Parts: []triage.Part{{Ref: "f1", Allowed: []string{"noise", "fixable"}}}}}, nil
}
func (*apiTriageSource) Apply(context.Context, triage.Item, triage.Part, string, string, json.RawMessage) (triage.Applied, error) {
	return triage.Applied{Prior: json.RawMessage(`{"state":"open"}`)}, nil
}
func (s *apiTriageSource) Undo(context.Context, triage.Verdict) error { s.undone++; return nil }
func (*apiTriageSource) Open(context.Context, string) (bool, error)   { return true, nil }

type apiTriageJudge struct{}

func (apiTriageJudge) Judge(_ context.Context, it triage.Item) (triage.Answer, error) {
	vals := map[string]string{}
	for _, p := range it.Parts {
		vals[p.Ref] = "noise"
	}
	return triage.Answer{Values: vals, Reason: "r", SessionUUID: "s1", CostUSD: 0.5}, nil
}

func serverWithTriage(t *testing.T) (string, *triage.Service, *apiTriageSource) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "triage_api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	svc := triage.NewService(db, apiTriageJudge{})
	svc.Go = func(fn func()) { fn() }
	AttachTriage(svc)
	t.Cleanup(func() { AttachTriage(nil) })
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	// Registered AFTER NewServer: the fake shares the "friction" kind with the
	// real friction Source NewServer registers, and replaces it here.
	src := &apiTriageSource{}
	svc.Register(src)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, svc, src
}

func decodeInto(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func TestTriageAPIUnattached(t *testing.T) {
	srv, _, _ := serverWithTriage(t)
	AttachTriage(nil)
	resp, _ := doRoutineReq(t, http.MethodGet, srv+"/api/triage/runs", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestTriageAuditUnattached(t *testing.T) {
	srv, _, _ := serverWithTriage(t)
	AttachTriage(nil)
	resp, _ := doRoutineReq(t, http.MethodGet, srv+"/api/triage/audit", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestTriageAuditEmpty(t *testing.T) {
	srv, _, _ := serverWithTriage(t)
	resp, body := doRoutineReq(t, http.MethodGet, srv+"/api/triage/audit", nil)
	if resp.StatusCode != http.StatusOK || string(body) != `{"answered":0,"agree":0,"byQuestion":[]}`+"\n" {
		t.Fatalf("audit = %d %q", resp.StatusCode, body)
	}
}

// One sampled answer the operator confirmed and one they overruled: 2 answered,
// 1 agreeing, both questions listed in question-id order.
func TestTriageAuditCounts(t *testing.T) {
	srv, svc, _ := serverWithTriage(t)
	res, err := svc.DB.Exec(`INSERT INTO triage_runs(trigger, status, started_at) VALUES('operator','ok','2026-10-06T12:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := res.LastInsertId()
	seed := func(question, truth, agentValue, state string) {
		t.Helper()
		res, err := svc.DB.Exec(`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence,
			calibrated, backend, created_at, ground_truth, ground_truth_source)
			VALUES (?, 's1', 's1', 'h', 'x', 0.6, 1, 'local', '2026-10-02T10:00:00Z', ?, 'operator')`, question, truth)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := svc.DB.Exec(`INSERT INTO triage_verdicts(run_id, kind, ref, value, state, created_at)
			VALUES(?, 'classifier', ?, ?, ?, '2026-10-06T12:00:00Z')`, run, fmt.Sprint(id), agentValue, state); err != nil {
			t.Fatal(err)
		}
	}
	seed("d2.outcome", "shipped", "shipped", triage.StateAudited)
	seed("d2.task_type", "bugfix", "feature", triage.StateSample)

	resp, body := doRoutineReq(t, http.MethodGet, srv+"/api/triage/audit", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d %s", resp.StatusCode, body)
	}
	want := `{"answered":2,"agree":1,"byQuestion":[` +
		`{"questionId":"d2.outcome","answered":1,"agree":1},` +
		`{"questionId":"d2.task_type","answered":1,"agree":0}]}` + "\n"
	if string(body) != want {
		t.Fatalf("audit = %s, want %s", body, want)
	}
}

func TestTriageAPIRunLifecycle(t *testing.T) {
	srv, svc, src := serverWithTriage(t)

	// Idle: active is JSON null.
	resp, body := doRoutineReq(t, http.MethodGet, srv+"/api/triage/runs/active", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "null\n" {
		t.Fatalf("active idle = %d %q", resp.StatusCode, body)
	}

	// Unknown project → 404; bad trigger → 400.
	if resp, _ := doRoutineReq(t, http.MethodPost, srv+"/api/triage/runs", map[string]any{"project": "nope"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project = %d", resp.StatusCode)
	}
	if resp, _ := doRoutineReq(t, http.MethodPost, srv+"/api/triage/runs", map[string]any{"trigger": "cron"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad trigger = %d", resp.StatusCode)
	}

	// Start → 202 {id}; the synchronous seam finishes the run inline.
	resp, body = doRoutineReq(t, http.MethodPost, srv+"/api/triage/runs", map[string]any{"project": "p", "kinds": []string{"friction"}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start = %d %s", resp.StatusCode, body)
	}
	var started struct{ ID int64 }
	decodeInto(t, body, &started)

	resp, body = doRoutineReq(t, http.MethodGet, fmt.Sprintf("%s/api/triage/runs/%d", srv, started.ID), nil)
	var run triage.Run
	decodeInto(t, body, &run)
	if resp.StatusCode != http.StatusOK || run.Status != "ok" || run.Applied != 1 || run.ScopeProjectID == nil || *run.ScopeProjectID != 1 {
		t.Fatalf("run = %d %+v", resp.StatusCode, run)
	}
	if resp, _ := doRoutineReq(t, http.MethodGet, srv+"/api/triage/runs/999", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing run = %d", resp.StatusCode)
	}
	if resp, _ := doRoutineReq(t, http.MethodGet, srv+"/api/triage/runs/x", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad run id = %d", resp.StatusCode)
	}
	resp, body = doRoutineReq(t, http.MethodGet, srv+"/api/triage/runs?limit=5", nil)
	var runs struct{ Items []triage.Run }
	decodeInto(t, body, &runs)
	if resp.StatusCode != http.StatusOK || len(runs.Items) != 1 {
		t.Fatalf("runs = %d %+v", resp.StatusCode, runs)
	}

	// Busy → 409 with the active run id.
	var pending func()
	svc.Go = func(fn func()) { pending = fn }
	resp, body = doRoutineReq(t, http.MethodPost, srv+"/api/triage/runs", map[string]any{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("held start = %d", resp.StatusCode)
	}
	resp, body = doRoutineReq(t, http.MethodPost, srv+"/api/triage/runs", map[string]any{})
	var busy struct {
		Error       string
		ActiveRunID int64 `json:"activeRunId"`
	}
	decodeInto(t, body, &busy)
	if resp.StatusCode != http.StatusConflict || busy.ActiveRunID == 0 {
		t.Fatalf("busy = %d %s", resp.StatusCode, body)
	}
	resp, body = doRoutineReq(t, http.MethodGet, srv+"/api/triage/runs/active", nil)
	if resp.StatusCode != http.StatusOK || string(body) == "null\n" {
		t.Fatalf("active running = %q", body)
	}
	pending()

	// Verdict filters.
	listV := func(q string) []triage.Verdict {
		t.Helper()
		resp, body := doRoutineReq(t, http.MethodGet, srv+"/api/triage/verdicts"+q, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("verdicts%s = %d %s", q, resp.StatusCode, body)
		}
		var out struct{ Items []triage.Verdict }
		decodeInto(t, body, &out)
		return out.Items
	}
	// Two runs each applied f1 (the fake source keeps offering it). The filter
	// keys on the ITEM's project, not the run's scope: this fake's item carries
	// none, so both verdicts are project-less and match every project filter
	// (item-project filtering: TestTriageAPIVerdictsFilterByItemProject).
	if got := listV(""); len(got) != 2 {
		t.Fatalf("all verdicts = %d", len(got))
	}
	if got := listV("?state=applied,suggested&kind=friction&project=p"); len(got) != 2 {
		t.Fatalf("filtered = %d", len(got))
	}
	if got := listV(fmt.Sprintf("?run=%d", started.ID)); len(got) != 1 {
		t.Fatalf("by run = %d", len(got))
	}
	if got := listV("?state=rejected"); len(got) != 0 {
		t.Fatalf("rejected = %d", len(got))
	}
	if got := listV("?project=nope"); len(got) != 0 {
		t.Fatalf("unknown project = %d", len(got))
	}
	if got := listV("?kind=lesson&limit=5"); len(got) != 0 {
		t.Fatalf("other kind = %d", len(got))
	}
	vid := listV(fmt.Sprintf("?run=%d", started.ID))[0].ID

	// Undo → 200, then 409; bad/missing id → 400/404.
	resp, body = doRoutineReq(t, http.MethodPost, fmt.Sprintf("%s/api/triage/verdicts/%d/undo", srv, vid), nil)
	var v triage.Verdict
	decodeInto(t, body, &v)
	if resp.StatusCode != http.StatusOK || v.State != "undone" || src.undone != 1 {
		t.Fatalf("undo = %d %+v", resp.StatusCode, v)
	}
	if resp, _ := doRoutineReq(t, http.MethodPost, fmt.Sprintf("%s/api/triage/verdicts/%d/undo", srv, vid), nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second undo = %d", resp.StatusCode)
	}
	if resp, _ := doRoutineReq(t, http.MethodPost, srv+"/api/triage/verdicts/999/undo", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing undo = %d", resp.StatusCode)
	}
	if resp, _ := doRoutineReq(t, http.MethodPost, srv+"/api/triage/verdicts/x/undo", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad undo id = %d", resp.StatusCode)
	}
	if src.undone != 1 {
		t.Fatalf("Source.Undo calls = %d", src.undone)
	}
}

func TestTriageRawScalar(t *testing.T) {
	for in, want := range map[string]string{`"p"`: "p", `7`: "7", `null`: "", ``: ""} {
		if got := rawScalar(json.RawMessage(in)); got != want {
			t.Errorf("rawScalar(%q) = %q, want %q", in, got, want)
		}
	}
}

// apiCountSource is a Source of one Inbox kind that offers n distinct items.
type apiCountSource struct {
	kind string
	n    int
}

func (s *apiCountSource) Kind() string { return s.kind }
func (s *apiCountSource) Collect(context.Context, triage.Scope, int) ([]triage.Item, error) {
	items := make([]triage.Item, s.n)
	for i := range items {
		ref := fmt.Sprintf("%s-%d", s.kind, i)
		items[i] = triage.Item{Kind: s.kind, Key: ref, Title: ref,
			Parts: []triage.Part{{Ref: ref, Allowed: []string{"noise", "fixable"}}}}
	}
	return items, nil
}
func (*apiCountSource) Apply(context.Context, triage.Item, triage.Part, string, string, json.RawMessage) (triage.Applied, error) {
	return triage.Applied{Prior: json.RawMessage(`{}`)}, nil
}
func (*apiCountSource) Undo(context.Context, triage.Verdict) error { return nil }
func (*apiCountSource) Open(context.Context, string) (bool, error) { return true, nil }

// The Inbox banner's request body reaches StartReq: its kinds are recorded on
// the run and its cap bounds the run; a cap above MaxCap is clamped, and a body
// without a cap takes DefaultCap.
func TestTriageAPIStartBodyReachesStartReq(t *testing.T) {
	startRun := func(t *testing.T, srv string, body map[string]any) triage.Run {
		t.Helper()
		resp, raw := doRoutineReq(t, http.MethodPost, srv+"/api/triage/runs", body)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("start %v = %d %s", body, resp.StatusCode, raw)
		}
		var started struct{ ID int64 }
		decodeInto(t, raw, &started)
		resp, raw = doRoutineReq(t, http.MethodGet, fmt.Sprintf("%s/api/triage/runs/%d", srv, started.ID), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("run = %d %s", resp.StatusCode, raw)
		}
		var run triage.Run
		decodeInto(t, raw, &run)
		return run
	}

	t.Run("kinds recorded, cap honoured", func(t *testing.T) {
		srv, svc, _ := serverWithTriage(t)
		for _, k := range []string{"classifier", "advisor", "lesson"} {
			svc.Register(&apiCountSource{kind: k, n: 4})
		}
		run := startRun(t, srv, map[string]any{"kinds": []string{"classifier", "advisor"}, "cap": 3})
		if !slices.Equal(run.Kinds, []string{"classifier", "advisor"}) {
			t.Fatalf("kinds = %v", run.Kinds)
		}
		if run.Total != 3 {
			t.Fatalf("total = %d, want the cap 3 (12 items offered)", run.Total)
		}
	})

	t.Run("cap above MaxCap is clamped", func(t *testing.T) {
		srv, svc, _ := serverWithTriage(t)
		svc.Register(&apiCountSource{kind: "classifier", n: triage.MaxCap + 1})
		run := startRun(t, srv, map[string]any{"kinds": []string{"classifier"}, "cap": 5000})
		if run.Total != triage.MaxCap {
			t.Fatalf("total = %d, want MaxCap %d", run.Total, triage.MaxCap)
		}
	})

	t.Run("no cap takes DefaultCap", func(t *testing.T) {
		srv, svc, _ := serverWithTriage(t)
		svc.Register(&apiCountSource{kind: "classifier", n: triage.DefaultCap + 10})
		run := startRun(t, srv, map[string]any{})
		if run.Total != triage.DefaultCap {
			t.Fatalf("total = %d, want DefaultCap %d", run.Total, triage.DefaultCap)
		}
	})
}
