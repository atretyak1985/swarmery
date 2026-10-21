package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/plainsum"
)

// fakePlainRunner records every prompt and answers with a fixed retelling.
type fakePlainRunner struct {
	mu      sync.Mutex
	prompts []string
}

func (f *fakePlainRunner) Run(_ context.Context, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, prompt)
	return "  in plain words  \n", nil
}

func plainServer(t *testing.T) (*httptest.Server, *fakePlainRunner, int64, string) {
	t.Helper()
	_, db, taskID, planDir := epicFixture(t)
	runner := &fakePlainRunner{}
	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db, Plain: &plainsum.Service{DB: db, Runner: runner}})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, runner, taskID, planDir
}

func plainCall(t *testing.T, method, url string) (int, plainDTO) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var dto plainDTO
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, dto
}

// GET never generates; POST generates once and the next GET is served from the
// cache. Editing the doc misses the cache again.
func TestPlainPhaseGenerateThenCached(t *testing.T) {
	srv, runner, taskID, planDir := plainServer(t)
	url := srv.URL + "/api/epics/" + itoa(taskID) + "/plain?kind=phase&path=phase-1-schema.md&lang=uk"

	if code, dto := plainCall(t, http.MethodGet, url); code != 200 || dto.Status != "missing" {
		t.Fatalf("first GET = %d %+v, want 200 missing", code, dto)
	}
	if len(runner.prompts) != 0 {
		t.Fatalf("GET spawned a run")
	}
	if code, dto := plainCall(t, http.MethodPost, url); code != 200 || dto.Status != "ready" || dto.Text != "in plain words" {
		t.Fatalf("POST = %d %+v, want 200 ready 'in plain words'", code, dto)
	}
	if code, dto := plainCall(t, http.MethodGet, url); code != 200 || dto.Status != "ready" {
		t.Fatalf("GET after POST = %d %+v, want ready", code, dto)
	}
	plainCall(t, http.MethodPost, url)
	if len(runner.prompts) != 1 {
		t.Fatalf("runs = %d, want 1 (second POST is a cache hit)", len(runner.prompts))
	}
	p := runner.prompts[0]
	if !strings.Contains(p, "Ukrainian") || !strings.Contains(p, "# Phase 1 — Schema") {
		t.Errorf("prompt lacks language or source:\n%s", p)
	}

	if err := os.WriteFile(filepath.Join(planDir, "phase-1-schema.md"), []byte("# Phase 1 — edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, dto := plainCall(t, http.MethodGet, url); dto.Status != "missing" {
		t.Fatalf("GET after edit = %+v, want missing", dto)
	}
}

// A phase without a Completion Report has nothing to retell: "empty", no run.
func TestPlainPhaseReportEmptyWithoutReport(t *testing.T) {
	srv, runner, taskID, _ := plainServer(t)
	url := srv.URL + "/api/epics/" + itoa(taskID) + "/plain?kind=phase-report&path=phase-1-schema.md"
	if code, dto := plainCall(t, http.MethodPost, url); code != 200 || dto.Status != "empty" {
		t.Fatalf("POST = %d %+v, want 200 empty", code, dto)
	}
	if len(runner.prompts) != 0 {
		t.Fatalf("an empty source spawned a run")
	}
}

// plan-report retells SUMMARY.md plus each phase's Completion Report, in
// numeric phase order (phase-2 before phase-10).
func TestPlainPlanReportSource(t *testing.T) {
	srv, runner, taskID, planDir := plainServer(t)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(planDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# Plan\n")
	write("SUMMARY.md", "overall result")
	write("phase-2-ui.md", "# P2\n\n## Completion Report\n\nui shipped\n")
	write("phase-10-docs.md", "# P10\n\n## Completion Report\n\ndocs shipped\n")
	url := srv.URL + "/api/epics/" + itoa(taskID) + "/plain?kind=plan-report"
	if code, dto := plainCall(t, http.MethodPost, url); code != 200 || dto.Status != "ready" {
		t.Fatalf("POST = %d %+v", code, dto)
	}
	p := runner.prompts[0]
	iSum, iUI, iDocs := strings.Index(p, "overall result"), strings.Index(p, "ui shipped"), strings.Index(p, "docs shipped")
	if iSum < 0 || iUI < 0 || iDocs < 0 || iSum >= iUI || iUI >= iDocs {
		t.Errorf("plan-report order wrong (summary %d, ui %d, docs %d)", iSum, iUI, iDocs)
	}
	if !strings.Contains(p, "English") {
		t.Errorf("default language should be English")
	}
}

func TestPlainRejectsBadInput(t *testing.T) {
	srv, _, taskID, _ := plainServer(t)
	base := srv.URL + "/api/epics/" + itoa(taskID) + "/plain"
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?kind=nope", http.StatusBadRequest},
		{"?kind=phase", http.StatusBadRequest},
		{"?kind=phase&path=../../etc/passwd.md", http.StatusBadRequest},
		{"?kind=phase&path=phase-9-missing.md", http.StatusNotFound},
	} {
		if code, _ := plainCall(t, http.MethodGet, base+tc.query); code != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.query, code, tc.want)
		}
	}
}

func TestPlainUnavailableWithoutService(t *testing.T) {
	_, db, taskID, _ := epicFixture(t)
	srv := epicRoutesServer(t, db)
	code, _ := plainCall(t, http.MethodGet, srv.URL+"/api/epics/"+itoa(taskID)+"/plain?kind=plan")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
}
