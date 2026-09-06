package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// planStubRunner implements planning.Runner without spawning a process. block,
// when set, holds the run in flight so a test can observe the active state and
// hit the 409 path before the slot releases.
type planStubRunner struct {
	mu    sync.Mutex
	calls int
	block chan struct{}
}

func (r *planStubRunner) Start(ctx context.Context, spec planning.RunSpec) (*planning.Run, error) {
	r.mu.Lock()
	r.calls++
	block := r.block
	r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	return &planning.Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

func (r *planStubRunner) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.calls }

// serverWithPlanning builds an httptest server with a planning service attached
// (package-var, reset on cleanup) backed by the given stub runner. Its async
// spawn runs on a real goroutine so a blocking runner keeps the run in flight;
// the UUID is deterministic.
func serverWithPlanning(t *testing.T, r planning.Runner) (*httptest.Server, *sql.DB, *planning.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "planning_api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	svc := planning.NewService(db, r)
	svc.UUID = func() string { return "uuid-api" }
	AttachPlanning(svc)
	t.Cleanup(func() {
		// Drain any in-flight run before detaching so a run goroutine does not
		// outlive the test (the Notify closure is service-scoped, so this is
		// hygiene, not a race fix). Cancel forces a prompt exit.
		svc.Cancel(1)
		for i := 0; i < 400 && svc.Snapshot(1).Active; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		AttachPlanning(nil)
	})

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, svc
}

// postPlanningJSON posts a JSON body and returns the raw response (planning
// tests need to decode bodies + assert 202/409). Distinct from improve_test's
// postJSON, which posts a nil body and asserts status.
func postPlanningJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	resp, err := http.Post(url, "application/json", reader)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestGetPlanning503WhenUnattached(t *testing.T) {
	AttachPlanning(nil)
	srv := testServer(t)
	resp, err := http.Get(srv.URL + "/api/projects/1/planning")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET planning (unattached) = %d, want 503", resp.StatusCode)
	}
}

func TestGetPlanning_Idle(t *testing.T) {
	srv, _, _ := serverWithPlanning(t, &planStubRunner{})
	resp, err := http.Get(srv.URL + "/api/projects/1/planning")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var st planning.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Active {
		t.Error("idle project reported active")
	}
}

func TestStartPlanning_202(t *testing.T) {
	srv, _, _ := serverWithPlanning(t, &planStubRunner{})
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning", map[string]string{"idea": "build a thing"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 202; body: %s", resp.StatusCode, body)
	}
	var out struct {
		SessionUUID string `json:"sessionUuid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.SessionUUID != "uuid-api" {
		t.Errorf("sessionUuid = %q, want uuid-api", out.SessionUUID)
	}
}

func TestStartPlanning_EmptyIdea400(t *testing.T) {
	srv, _, _ := serverWithPlanning(t, &planStubRunner{})
	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning", map[string]string{"idea": "   "})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty idea = %d, want 400", resp.StatusCode)
	}
}

func TestStartPlanning_UnknownProject404(t *testing.T) {
	srv, _, _ := serverWithPlanning(t, &planStubRunner{})
	resp := postPlanningJSON(t, srv.URL+"/api/projects/999/planning", map[string]string{"idea": "x"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", resp.StatusCode)
	}
}

func TestStartPlanning_409WhenActive(t *testing.T) {
	r := &planStubRunner{block: make(chan struct{})}
	srv, _, svc := serverWithPlanning(t, r)

	// First start parks the run in flight.
	resp1 := postPlanningJSON(t, srv.URL+"/api/projects/1/planning", map[string]string{"idea": "one"})
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusAccepted {
		t.Fatalf("first start = %d, want 202", resp1.StatusCode)
	}
	// Wait until observably active — and then until the spawn goroutine has
	// actually entered the runner: Active flips before the goroutine runs, so
	// asserting on the call count without this races the scheduler (seen as a
	// CI-only "runner called 0 times" flake).
	waitActive(t, svc)
	waitRunnerCalled(t, r)

	resp2 := postPlanningJSON(t, srv.URL+"/api/projects/1/planning", map[string]string{"idea": "two"})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second start = %d, want 409", resp2.StatusCode)
	}
	var out struct {
		SessionUUID string `json:"sessionUuid"`
	}
	json.NewDecoder(resp2.Body).Decode(&out)
	if out.SessionUUID != "uuid-api" {
		t.Errorf("409 body sessionUuid = %q, want the active uuid", out.SessionUUID)
	}
	if r.count() != 1 {
		t.Errorf("runner called %d times, want 1 (second rejected before spawn)", r.count())
	}

	// GET reflects active with the uuid.
	getResp, _ := http.Get(srv.URL + "/api/projects/1/planning")
	var st planning.Status
	json.NewDecoder(getResp.Body).Decode(&st)
	getResp.Body.Close()
	if !st.Active || st.SessionUUID != "uuid-api" {
		t.Errorf("GET status = %+v, want active uuid-api", st)
	}

	close(r.block)
}

func TestCancelPlanning(t *testing.T) {
	r := &planStubRunner{block: make(chan struct{})}
	srv, _, svc := serverWithPlanning(t, r)

	postPlanningJSON(t, srv.URL+"/api/projects/1/planning", map[string]string{"idea": "one"}).Body.Close()
	waitActive(t, svc)

	resp := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/cancel", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel = %d, want 202", resp.StatusCode)
	}

	// A second cancel with nothing in flight → 409.
	waitIdle(t, svc)
	resp2 := postPlanningJSON(t, srv.URL+"/api/projects/1/planning/cancel", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("cancel-when-idle = %d, want 409", resp2.StatusCode)
	}
}

func TestStartPlanning_CrossOriginRejected(t *testing.T) {
	srv, _, _ := serverWithPlanning(t, &planStubRunner{})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/projects/1/planning",
		bytes.NewReader([]byte(`{"idea":"x"}`)))
	req.Header.Set("Origin", "http://evil.example.com")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin start = %d, want 403", resp.StatusCode)
	}
}

func waitActive(t *testing.T, svc *planning.Service) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if svc.Snapshot(1).Active {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("planner run never became active")
}

// waitRunnerCalled blocks until the stub runner has been entered at least
// once. The service marks the run Active before its spawn goroutine reaches
// the runner, so a test that asserts on call counts must wait for the call
// itself, not just for Active.
func waitRunnerCalled(t *testing.T, r *planStubRunner) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if r.count() >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("runner never entered")
}

func waitIdle(t *testing.T, svc *planning.Service) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if !svc.Snapshot(1).Active {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("planner run never went idle")
}

// An unknown model is a client error, and a known short name is accepted and
// surfaced on the status DTO as its full ID.
func TestStartPlanning_Model(t *testing.T) {
	srv, _, svc := serverWithPlanning(t, &planStubRunner{})
	bad := postPlanningJSON(t, srv.URL+"/api/projects/1/planning", map[string]string{"idea": "x", "model": "gpt-9"})
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown model = %d, want 400", bad.StatusCode)
	}
	ok := postPlanningJSON(t, srv.URL+"/api/projects/1/planning", map[string]string{"idea": "x", "model": "sonnet"})
	ok.Body.Close()
	if ok.StatusCode != http.StatusAccepted {
		t.Fatalf("sonnet = %d, want 202", ok.StatusCode)
	}
	if got := svc.Model("uuid-api"); got != "claude-sonnet-5" {
		t.Errorf("stored model = %q, want claude-sonnet-5", got)
	}
	st, err := svc.WizardSnapshot(1)
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "claude-sonnet-5" {
		t.Errorf("status model = %q, want claude-sonnet-5", st.Model)
	}
}
