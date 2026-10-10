package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// PATCH /api/epics/{taskId}/docs {line, done} — a Criteria-tab tick that closes
// a phase doc's last open criterion starts the plan branch review, as a run's
// `done` stamp does (phase-run follow-ups, phase 1, SC-1).

// planReviewCall is one MaybePlanReview call the fake hook saw.
type planReviewCall struct {
	phaseID int64
	docPath string
}

// fakePlanReview records every MaybePlanReview call.
type fakePlanReview struct {
	mu    sync.Mutex
	calls []planReviewCall
}

func (f *fakePlanReview) MaybePlanReview(phaseID int64, docPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, planReviewCall{phaseID, docPath})
}

func (f *fakePlanReview) seen() []planReviewCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]planReviewCall(nil), f.calls...)
}

// planReviewFixture is epicFixture behind a Handler carrying a fake plan review
// hook. It returns the server, the hook, the epic's task id, its plan dir and
// phase 1's id.
func planReviewFixture(t *testing.T) (*httptest.Server, *fakePlanReview, int64, string, int64) {
	t.Helper()
	_, db, taskID, planDir := epicFixture(t)
	hook := &fakePlanReview{}
	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db, PlanReview: hook})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var phase1 int64
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`, taskID).
		Scan(&phase1); err != nil {
		t.Fatal(err)
	}
	return srv, hook, taskID, planDir, phase1
}

func writePlanFile(t *testing.T, planDir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(planDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPatchPlanDocPlanReviewOnClosingTick(t *testing.T) {
	srv, hook, taskID, planDir, phase1 := planReviewFixture(t)
	// Lines 5–7 are the criteria; one ticked, two open.
	writePlanFile(t, planDir, "phase-1-schema.md",
		"# Phase 1 — Schema\n\n**Files:** x\n\n## Acceptance criteria\n- [x] a\n- [ ] b\n- [ ] c\n")
	url := srv.URL + "/api/epics/" + itoa(taskID) + "/docs?path=phase-1-schema.md"
	tick := func(body string, wantCalls int) {
		t.Helper()
		if status, _ := patchDoc(t, url, body); status != http.StatusOK {
			t.Fatalf("PATCH %s = %d", body, status)
		}
		if got := len(hook.seen()); got != wantCalls {
			t.Fatalf("after PATCH %s: %d plan review call(s), want %d: %+v", body, got, wantCalls, hook.seen())
		}
	}

	tick(`{"line":6,"done":true}`, 0)  // "c" is still open
	tick(`{"line":7,"done":true}`, 1)  // the last open criterion closes
	tick(`{"line":7,"done":true}`, 1)  // a re-tick closes nothing
	tick(`{"line":7,"done":false}`, 1) // an untick opens one
	tick(`{"line":7,"done":true}`, 2)  // closing it again is a new trigger

	// The hook gets phase 1's row and the doc_path wsingest stored — the path a
	// run's stamp hands it — even though resolvePlanDoc resolved the plan dir's
	// symlinks (t.TempDir sits under /var → /private/var on macOS).
	want := planReviewCall{phase1, filepath.Join(planDir, "phase-1-schema.md")}
	for i, c := range hook.seen() {
		if c != want {
			t.Errorf("call %d = %+v, want %+v", i, c, want)
		}
	}
}

func TestPatchPlanDocPlanReviewNotTriggered(t *testing.T) {
	cases := []struct {
		name, file, doc, body string
	}{
		{
			// An open [LAND] criterion is open until landpoll (or a hand) ticks it.
			name: "open LAND criterion",
			file: "phase-1-schema.md",
			doc:  "# Phase 1\n\n## Acceptance criteria\n- [ ] a\n- [ ] [LAND] merge the PR\n",
			body: `{"line":3,"done":true}`,
		},
		{
			name: "open MANUAL criterion",
			file: "phase-1-schema.md",
			doc:  "# Phase 1\n\n## Acceptance criteria\n- [ ] a\n- [ ] [MANUAL] check the console\n",
			body: `{"line":3,"done":true}`,
		},
		{
			// A fully ticked doc that is no phase of the plan triggers nothing.
			name: "not a phase doc",
			file: "README.md",
			doc:  "# Plan\n\n- [x] a\n- [ ] b\n",
			body: `{"line":3,"done":true}`,
		},
		{
			name: "untick",
			file: "phase-1-schema.md",
			doc:  "# Phase 1\n\n## Acceptance criteria\n- [x] a\n",
			body: `{"line":3,"done":false}`,
		},
		{
			// A class mark ticks nothing, whatever the doc's counts.
			name: "class mark",
			file: "phase-1-schema.md",
			doc:  "# Phase 1\n\n## Acceptance criteria\n- [x] a\n",
			body: `{"line":3,"class":"LAND"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, hook, taskID, planDir, _ := planReviewFixture(t)
			writePlanFile(t, planDir, c.file, c.doc)
			url := srv.URL + "/api/epics/" + itoa(taskID) + "/docs?path=" + c.file
			if status, _ := patchDoc(t, url, c.body); status != http.StatusOK {
				t.Fatalf("PATCH = %d", status)
			}
			if calls := hook.seen(); len(calls) != 0 {
				t.Fatalf("plan review called: %+v", calls)
			}
		})
	}
}
