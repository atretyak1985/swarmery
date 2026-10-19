package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// jsonStr renders s as a JSON string literal (quotes + escaping) for embedding
// in a request body.
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// epicFixture builds a server + DB with one workspace epic task whose plan/ dir
// lives on disk: 2 phases (phase-1 activatable, phase-2 depends on 1), plus the
// task_artifacts 'plan' gate row pointing at the plan dir. Returns the server,
// db, the workspace task id, and the plan dir path.
func epicFixture(t *testing.T) (*httptest.Server, *sql.DB, int64, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "epics.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	planDir := filepath.Join(t.TempDir(), "ws", "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(planDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("phase-1-schema.md", "# Phase 1 — Schema\n\n**Files:** internal/store/x.sql, internal/api/x.go\n\n## Acceptance criteria\n- [x] a\n- [ ] b\n")
	write("phase-2-ui.md", "# Phase 2 — UI\n\n## Acceptance criteria\n- [ ] c\n")

	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// One workspace-sourced task = the epic.
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1,'My Epic','goal','running',
		'2026-07-24T00:00:00Z','2026-07-24T00:00:00Z','workspace','2026-07-24-my-epic')`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()

	// Two phase rows + the plan gate row.
	if _, err := db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done)
		VALUES (?, 1, 'Phase 1 — Schema', ?, '[]', 2, 1),
		       (?, 2, 'Phase 2 — UI', ?, '[1]', 1, 0)`,
		taskID, filepath.Join(planDir, "phase-1-schema.md"),
		taskID, filepath.Join(planDir, "phase-2-ui.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?, 'plan', ?, 'hash', '2026-07-24T00:00:00Z')`, taskID, planDir); err != nil {
		t.Fatal(err)
	}

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, taskID, planDir
}

func TestListEpics(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	resp, err := http.Get(srv.URL + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var epics []epicDTO
	if err := json.NewDecoder(resp.Body).Decode(&epics); err != nil {
		t.Fatal(err)
	}
	if len(epics) != 1 {
		t.Fatalf("epics = %d, want 1", len(epics))
	}
	e := epics[0]
	if e.TaskID != taskID || e.Title != "My Epic" {
		t.Errorf("epic = %+v", e)
	}
	if len(e.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(e.Phases))
	}
	// Rollup: 1 done / 3 total → 33.33%.
	if e.Rollup.Done != 1 || e.Rollup.Total != 3 {
		t.Errorf("rollup = %+v, want 1/3", e.Rollup)
	}
	if e.Rollup.Pct < 33 || e.Rollup.Pct > 34 {
		t.Errorf("rollup pct = %v, want ~33.3", e.Rollup.Pct)
	}
	// Relative doc paths for the editor.
	if e.Phases[0].DocRelPath != "phase-1-schema.md" {
		t.Errorf("phase[0].docRelPath = %q", e.Phases[0].DocRelPath)
	}
	if !reflect.DeepEqual(e.Phases[1].DependsOn, []int{1}) {
		t.Errorf("phase[1].dependsOn = %v, want [1]", e.Phases[1].DependsOn)
	}
}

// epicRoutesServer builds a routes-only httptest.Server for the epics fixture
// DB — no SPA fallback. This is the preferred harness for negative-routing
// assertions: an unregistered API path returns a genuine 404 from the mux
// (methodNotAllowed or 404), rather than the SPA index.html 200 that
// NewServer's "mux.Handle("/")" catch-all produces.
func epicRoutesServer(t *testing.T, db *sql.DB) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestActivateRouteGone asserts that the activate route no longer exists — the
// route was removed in interactive-planning-v2 phase 4 (Board is exclusively
// for tasks created on the board; plan phases run via the phase-run mechanism
// instead). We use a routes-only mux (no SPA fallback) so an unregistered path
// returns a genuine 404 from the mux rather than the index.html 200 that the
// full NewServer catch-all would produce.
func TestActivateRouteGone(t *testing.T) {
	_, db, taskID, _ := epicFixture(t)
	srv := epicRoutesServer(t, db)

	var phase1ID int64
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id=? AND seq=1`, taskID).
		Scan(&phase1ID); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/epics/" + itoa(taskID) + "/phases/" + itoa(phase1ID) + "/activate"
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("activate route = %d, want 404 (route removed; routes-only mux, no SPA fallback)", resp.StatusCode)
	}
}

func TestGetPlanDoc(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	resp, err := http.Get(srv.URL + "/api/epics/" + itoa(taskID) + "/docs?path=phase-1-schema.md")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var doc planDocResponse
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(doc.Content), []byte("# Phase 1 — Schema")) {
		t.Errorf("content missing H1: %q", doc.Content)
	}
}

func TestPutPlanDocWritesBackup(t *testing.T) {
	srv, _, taskID, planDir := epicFixture(t)
	newBody := "# Phase 1 — Schema (edited)\n\n- [x] a\n- [x] b\n"
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/api/epics/"+itoa(taskID)+"/docs?path=phase-1-schema.md",
		bytes.NewBufferString(`{"content":`+jsonStr(newBody)+`}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// File changed on disk.
	got, err := os.ReadFile(filepath.Join(planDir, "phase-1-schema.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != newBody {
		t.Errorf("file content = %q, want the edit", string(got))
	}
	// A second write backs up the (now-existing) file — the response carries the
	// backup path, and it exists on disk.
	req2, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/api/epics/"+itoa(taskID)+"/docs?path=phase-1-schema.md",
		bytes.NewBufferString(`{"content":`+jsonStr("second edit")+`}`))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var doc planDocResponse
	if err := json.NewDecoder(resp2.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Backup == "" {
		t.Error("expected a backup path on the second write")
	}
	if _, err := os.Stat(doc.Backup); err != nil {
		t.Errorf("backup file missing: %v", err)
	}
}

func TestPatchPlanDocTogglesCheckbox(t *testing.T) {
	srv, _, taskID, planDir := epicFixture(t)
	// phase-1 line index of "- [ ] b": file is
	//   0: # Phase 1 — Schema
	//   1: (blank)
	//   2: **Files:** ...
	//   3: (blank)
	//   4: ## Acceptance criteria
	//   5: - [x] a
	//   6: - [ ] b
	req, _ := http.NewRequest(http.MethodPatch,
		srv.URL+"/api/epics/"+itoa(taskID)+"/docs?path=phase-1-schema.md",
		bytes.NewBufferString(`{"line":6,"done":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, err := os.ReadFile(filepath.Join(planDir, "phase-1-schema.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("- [x] b")) {
		t.Errorf("checkbox not flipped: %q", string(got))
	}

	// A non-checkbox line index → 400.
	req2, _ := http.NewRequest(http.MethodPatch,
		srv.URL+"/api/epics/"+itoa(taskID)+"/docs?path=phase-1-schema.md",
		bytes.NewBufferString(`{"line":0,"done":true}`))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("patch non-checkbox = %d, want 400", resp2.StatusCode)
	}
}

func TestPlanDocPathTraversalRejected(t *testing.T) {
	srv, _, taskID, planDir := epicFixture(t)
	// A secret file OUTSIDE the plan dir (sibling of ws/).
	secret := filepath.Join(filepath.Dir(filepath.Dir(planDir)), "secret.md")
	if err := os.WriteFile(secret, []byte("# top secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"../../secret.md",
		"..%2f..%2fsecret.md",
		"/etc/passwd",
		"../secret.md",
	} {
		resp, err := http.Get(srv.URL + "/api/epics/" + itoa(taskID) + "/docs?path=" + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Errorf("traversal %q = %d, want 400/404", p, resp.StatusCode)
		}
		if resp.StatusCode == http.StatusOK {
			t.Errorf("traversal %q leaked a file", p)
		}
	}
}

func TestPlanDocSymlinkEscapeRejected(t *testing.T) {
	srv, _, taskID, planDir := epicFixture(t)
	// A symlink INSIDE the plan dir pointing OUT to a secret.
	secretDir := filepath.Join(filepath.Dir(filepath.Dir(planDir)), "outside")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(secretDir, "leak.md")
	if err := os.WriteFile(secret, []byte("# leak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(planDir, "escape.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	resp, err := http.Get(srv.URL + "/api/epics/" + itoa(taskID) + "/docs?path=escape.md")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("symlink escape leaked a file (status 200)")
	}
}

// doReq is a tiny helper for the error-branch table below.
func doReq(t *testing.T, method, url, body string) int {
	t.Helper()
	var rdr *bytes.Buffer
	if body != "" {
		rdr = bytes.NewBufferString(body)
	} else {
		rdr = bytes.NewBufferString("")
	}
	req, _ := http.NewRequest(method, url, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPlanDocEndpointErrorBranches(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	base := srv.URL + "/api/epics/"
	tid := itoa(taskID)

	cases := []struct {
		name, method, url, body string
		want                    int
	}{
		{"GET missing path", http.MethodGet, base + tid + "/docs", "", http.StatusBadRequest},
		{"GET unknown doc", http.MethodGet, base + tid + "/docs?path=nope.md", "", http.StatusNotFound},
		{"GET bad task id", http.MethodGet, base + "abc/docs?path=x.md", "", http.StatusBadRequest},
		{"GET no plan dir", http.MethodGet, base + "999999/docs?path=x.md", "", http.StatusNotFound},
		{"PUT missing path", http.MethodPut, base + tid + "/docs", `{"content":"x"}`, http.StatusBadRequest},
		{"PUT bad JSON", http.MethodPut, base + tid + "/docs?path=phase-1-schema.md", `{not json`, http.StatusBadRequest},
		{"PATCH missing fields", http.MethodPatch, base + tid + "/docs?path=phase-1-schema.md", `{}`, http.StatusBadRequest},
		{"PATCH bad JSON", http.MethodPatch, base + tid + "/docs?path=phase-1-schema.md", `{`, http.StatusBadRequest},
		{"PATCH out of range", http.MethodPatch, base + tid + "/docs?path=phase-1-schema.md", `{"line":9999,"done":true}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := doReq(t, c.method, c.url, c.body); got != c.want {
				t.Errorf("%s = %d, want %d", c.name, got, c.want)
			}
		})
	}
}

func TestListEpicsEmptyForUnknownProject(t *testing.T) {
	srv, _, _, _ := epicFixture(t)
	resp, err := http.Get(srv.URL + "/api/epics?projectId=424242")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var epics []epicDTO
	if err := json.NewDecoder(resp.Body).Decode(&epics); err != nil {
		t.Fatal(err)
	}
	if len(epics) != 0 {
		t.Errorf("epics = %d, want 0 for an unknown project", len(epics))
	}
}

// TestPlanStatusDerivation: planStatus precedence is zone > README > rollup.
func TestPlanStatusDerivation(t *testing.T) {
	cases := []struct {
		name        string
		archived    bool
		taskStatus  string
		done, tot   int
		allPhasesOK bool
		want        string
	}{
		{"archived wins over everything", true, "paused", 3, 3, true, "archived"},
		{"archived with running readme", true, "running", 0, 3, true, "archived"},
		{"paused beats a complete rollup", false, "paused", 3, 3, true, "paused"},
		{"paused with open boxes", false, "paused", 1, 3, true, "paused"},
		{"full rollup reads done", false, "running", 3, 3, true, "done"},
		{"readme done but boxes open stays active", false, "done", 1, 3, true, "active"},
		{"running with open boxes", false, "running", 1, 3, true, "active"},
		{"zero checkboxes never done", false, "running", 0, 0, true, "active"},
		// A fully-ticked plan whose phases the completion gate refuses is NOT done.
		// Every box ticked and no grade where the doc asked for one is exactly the
		// state that used to read as finished work.
		{"full rollup but a phase is unverified stays active", false, "running", 3, 3, false, "active"},
		{"archived still wins over an unverified phase", true, "running", 3, 3, false, "archived"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := planStatus(c.archived, c.taskStatus, c.done, c.tot, c.allPhasesOK); got != c.want {
				t.Errorf("planStatus(%v,%q,%d,%d,%v) = %q, want %q",
					c.archived, c.taskStatus, c.done, c.tot, c.allPhasesOK, got, c.want)
			}
		})
	}
}

// TestListEpicsSpecCoverage: an epic with a spec answers hasSpec plus the
// coverage object — criteria with coveredBy phase seqs, covered/total, and an
// unknown ref for a phase covering an id the spec never declared. The fixture
// plants the rows wsingest would have written (spec_criteria + the covers
// column) and the spec.md file the hasSpec stat probes.
func TestListEpicsSpecCoverage(t *testing.T) {
	srv, db, taskID, planDir := epicFixture(t)
	if err := os.WriteFile(filepath.Join(planDir, "spec.md"),
		[]byte("# Spec\n\n- [x] **SC-1** — first\n- [ ] **SC-2** — second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO spec_criteria (workspace_task_id, pos, cid, text, done, line)
		VALUES (?, 0, 'SC-1', 'first', 1, 2), (?, 1, 'SC-2', 'second', 0, 3)`, taskID, taskID); err != nil {
		t.Fatal(err)
	}
	// Phase 1 covers SC-1 and the never-declared SC-9; phase 2 covers nothing.
	if _, err := db.Exec(`UPDATE epic_phases SET covers='["SC-1","SC-9"]'
		WHERE workspace_task_id=? AND seq=1`, taskID); err != nil {
		t.Fatal(err)
	}

	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	if len(epics) != 1 {
		t.Fatalf("epics = %d, want 1", len(epics))
	}
	e := epics[0]
	if !e.HasSpec {
		t.Error("hasSpec = false, want true (plan/spec.md exists)")
	}
	if e.Spec == nil {
		t.Fatal("spec = null, want the coverage object")
	}
	if e.Spec.Covered != 1 || e.Spec.Total != 2 {
		t.Errorf("covered/total = %d/%d, want 1/2", e.Spec.Covered, e.Spec.Total)
	}
	if len(e.Spec.Criteria) != 2 {
		t.Fatalf("criteria = %d, want 2", len(e.Spec.Criteria))
	}
	c1, c2 := e.Spec.Criteria[0], e.Spec.Criteria[1]
	if c1.Cid != "SC-1" || !c1.Done || c1.Text != "first" {
		t.Errorf("criteria[0] = %+v, want SC-1/done/first", c1)
	}
	if !reflect.DeepEqual(c1.CoveredBy, []int{1}) {
		t.Errorf("criteria[0].coveredBy = %v, want [1]", c1.CoveredBy)
	}
	if c2.Cid != "SC-2" || c2.Done || len(c2.CoveredBy) != 0 {
		t.Errorf("criteria[1] = %+v, want SC-2 uncovered", c2)
	}
	if c2.CoveredBy == nil {
		t.Error("criteria[1].coveredBy = null, want [] (uncovered is a value, not an absence)")
	}
	if len(e.Spec.UnknownRefs) != 1 || e.Spec.UnknownRefs[0].Seq != 1 || e.Spec.UnknownRefs[0].Cid != "SC-9" {
		t.Errorf("unknownRefs = %+v, want [{1 SC-9}]", e.Spec.UnknownRefs)
	}
}

// TestListEpicsNoSpec: a spec-less plan is wire-unchanged — hasSpec:false,
// spec:null (asserted on the raw JSON, so a future omitempty cannot silently
// drop the contract).
func TestListEpicsNoSpec(t *testing.T) {
	srv, _, _, _ := epicFixture(t)
	resp, err := http.Get(srv.URL + "/api/epics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw []map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("epics = %d, want 1", len(raw))
	}
	if got := string(raw[0]["hasSpec"]); got != "false" {
		t.Errorf("hasSpec = %s, want false", got)
	}
	if got := string(raw[0]["spec"]); got != "null" {
		t.Errorf("spec = %s, want null for a spec-less plan", got)
	}
}

// TestListEpicsDerivedStatus: the fixture epic (running, rollup 1/3) reads
// "active" — the raw tasks.status value never leaks through the DTO.
func TestListEpicsDerivedStatus(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	if len(epics) != 1 || epics[0].TaskID != taskID {
		t.Fatalf("epics = %+v", epics)
	}
	if epics[0].Status != "active" {
		t.Errorf("status = %q, want active (raw running normalized)", epics[0].Status)
	}
}

// TestListEpics_LinkedSessions pins the DTO the Plans page's sessions panel reads.
// Its absence is what produced the board-redesign run's "no sessions ran this plan"
// screen over work that was visibly landing: the epic DTO carried run_session_uuid
// and nothing else, so an interactive session — the path that did most of the work —
// had nowhere to appear.
func TestListEpics_LinkedSessions(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)

	// Two sessions: a daemon run (explicit) and an interactive one inferred from the
	// plan files it edited (heuristic). The run session is priced; the other is not.
	mustExecEpics(t, db, `INSERT INTO sessions (id, project_id, session_uuid, started_at, ended_at)
		VALUES (1, 1, 'u-run', '2026-07-24T10:00:00Z', '2026-07-24T11:00:00Z'),
		       (2, 1, 'u-interactive', '2026-07-24T12:00:00Z', NULL)`)
	mustExecEpics(t, db, `INSERT INTO turns (session_id, seq, role, started_at, cost_usd)
		VALUES (1, 1, 'assistant', '2026-07-24T10:30:00Z', 0.25),
		       (1, 2, 'assistant', '2026-07-24T10:40:00Z', 0.75)`)
	mustExecEpics(t, db, `INSERT INTO task_sessions (task_id, session_id, link_source, confidence)
		VALUES (?, 1, 'explicit', 1.0), (?, 2, 'heuristic', 0.9)`, taskID, taskID)

	e := firstEpic(t, srv)
	if len(e.LinkedSessions) != 2 {
		t.Fatalf("linkedSessions = %+v, want 2", e.LinkedSessions)
	}
	// Newest first, so the panel's top row is the session an operator is most likely
	// looking for.
	if e.LinkedSessions[0].SessionUUID != "u-interactive" {
		t.Errorf("order = %q first, want the newest session", e.LinkedSessions[0].SessionUUID)
	}

	run := e.LinkedSessions[1]
	if run.LinkSource != "explicit" || run.Confidence == nil || *run.Confidence != 1.0 {
		t.Errorf("run link = %+v, want explicit/1.0", run)
	}
	if run.CostUSD == nil || *run.CostUSD != 1.0 {
		t.Errorf("run costUsd = %v, want 1.0 (the sum of its turns)", run.CostUSD)
	}
	if run.EndedAt == nil {
		t.Error("a finished session has no endedAt")
	}

	inferred := e.LinkedSessions[0]
	if inferred.LinkSource != "heuristic" || inferred.Confidence == nil || *inferred.Confidence != 0.9 {
		t.Errorf("inferred link = %+v, want heuristic/0.9", inferred)
	}
	if inferred.CostUSD != nil {
		t.Errorf("costUsd = %v, want null while no turn is priced", *inferred.CostUSD)
	}
	if inferred.EndedAt != nil {
		t.Errorf("endedAt = %v, want null for a live session", *inferred.EndedAt)
	}
}

// An epic nothing has worked on yet must serialize [] rather than null: "nothing has
// run" and "we don't know" are different claims, and the UI maps over this field.
func TestListEpics_LinkedSessionsEmptyIsAList(t *testing.T) {
	srv, _, _, _ := epicFixture(t)
	resp, err := http.Get(srv.URL + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	got, ok := raw[0]["linkedSessions"]
	if !ok {
		t.Fatal("linkedSessions missing from the DTO")
	}
	if _, isList := got.([]any); !isList {
		t.Errorf("linkedSessions = %#v, want []", got)
	}
}

// A BOARD card's dispatch link must not leak into a plan's panel — the board has its
// own surface for those, and this join would otherwise sweep up every link in the DB.
func TestListEpics_LinkedSessionsExcludesBoardTasks(t *testing.T) {
	srv, db, _, _ := epicFixture(t)
	mustExecEpics(t, db, `INSERT INTO sessions (id, project_id, session_uuid, started_at)
		VALUES (1, 1, 'u-board', '2026-07-24T10:00:00Z')`)
	mustExecEpics(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at,
		source, external_id) VALUES (1,'a card','p','queued','2026-07-24T00:00:00Z','queue','T-1')`)
	var boardID int64
	if err := db.QueryRow(`SELECT id FROM tasks WHERE external_id='T-1'`).Scan(&boardID); err != nil {
		t.Fatal(err)
	}
	mustExecEpics(t, db, `INSERT INTO task_sessions (task_id, session_id, link_source, confidence)
		VALUES (?, 1, 'explicit', 1.0)`, boardID)

	if got := firstEpic(t, srv).LinkedSessions; len(got) != 0 {
		t.Errorf("linkedSessions = %+v, want none — that link belongs to a board card", got)
	}
}

func firstEpic(t *testing.T, srv *httptest.Server) epicDTO {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var epics []epicDTO
	if err := json.NewDecoder(resp.Body).Decode(&epics); err != nil {
		t.Fatal(err)
	}
	if len(epics) == 0 {
		t.Fatal("no epics returned")
	}
	return epics[0]
}

func mustExecEpics(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

// The completion gate on the wire: every phase carries `completionState`, and a
// fully-ticked phase whose verification never landed reads `unverified` — not
// `complete`, and not a failure. Before the gate, that phase presented as done
// while the store held verification rows saying the verifier never started.
func TestListEpics_CompletionGate(t *testing.T) {
	// This test is about the VERIFICATION condition. The closure conditions have
	// their own test below; switching them off here keeps a failure here pointing
	// at the thing it is testing rather than at a missing Completion Report.
	t.Setenv("SWARMERY_CLOSURE_GATE", "0")
	srv, db, taskID, _ := epicFixture(t)

	// Tick both phases fully. Phase 1 opted into verification, was dispatched, and
	// was never graded; phase 2 never asked, so it must be unaffected.
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE epic_phases SET checkboxes_done = checkboxes_total`)
	mustExec(`UPDATE epic_phases SET verify_mode='normal', verify_verdict=NULL,
		run_started_at='2026-10-19T10:00:00Z' WHERE seq=1 AND workspace_task_id=?`, taskID)
	mustExec(`UPDATE epic_phases SET verify_mode='off' WHERE seq=2 AND workspace_task_id=?`, taskID)

	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	if len(epics) != 1 {
		t.Fatalf("epics = %d, want 1", len(epics))
	}
	e := epics[0]
	byseq := map[int]epicPhaseDTO{}
	for _, p := range e.Phases {
		byseq[p.Seq] = p
	}
	if got := byseq[1].CompletionState; got != "unverified" {
		t.Errorf("phase 1 completionState = %q, want unverified", got)
	}
	if len(byseq[1].CompletionBlockers) == 0 {
		t.Error("an unverified phase must say why")
	}
	if got := byseq[2].CompletionState; got != "complete" {
		t.Errorf("phase 2 completionState = %q, want complete — it never asked to be graded", got)
	}
	// Rollup + plan status: all boxes ticked, but the plan is NOT done.
	if e.Rollup.IncompletePhases != 1 {
		t.Errorf("rollup.incompletePhases = %d, want 1", e.Rollup.IncompletePhases)
	}
	if e.Status != "active" {
		t.Errorf("plan status = %q, want active — a plan with an unverified phase is not done", e.Status)
	}

	// The same phase worked through outside the phase runner (by hand, a session,
	// a plan run) can never be graded, so a missing verdict does not hold it.
	mustExec(`UPDATE epic_phases SET run_started_at=NULL WHERE seq=1 AND workspace_task_id=?`, taskID)
	getJSON(t, srv.URL+"/api/epics", &epics)
	for _, p := range epics[0].Phases {
		if p.Seq == 1 && p.CompletionState != "complete" {
			t.Errorf("never-dispatched phase 1 completionState = %q, want complete", p.CompletionState)
		}
	}
	if epics[0].Status != "done" {
		t.Errorf("plan status with a never-dispatched ungraded phase = %q, want done", epics[0].Status)
	}
	mustExec(`UPDATE epic_phases SET run_started_at='2026-10-19T10:00:00Z' WHERE seq=1 AND workspace_task_id=?`, taskID)

	// Grading it closes the gate and the plan reads done.
	mustExec(`UPDATE epic_phases SET verify_verdict='pass' WHERE seq=1 AND workspace_task_id=?`, taskID)
	getJSON(t, srv.URL+"/api/epics", &epics)
	if epics[0].Status != "done" {
		t.Errorf("plan status after grading = %q, want done", epics[0].Status)
	}
	if epics[0].Rollup.IncompletePhases != 0 {
		t.Errorf("rollup.incompletePhases after grading = %d, want 0", epics[0].Rollup.IncompletePhases)
	}
}

// The closure half of the same gate on the wire: a phase the daemon RAN whose
// Completion Report is empty reads `unreported` — not complete — and holds the
// plan back. A phase that never ran is untouched: the contract that demands a
// report is the dispatcher's, and a hand-executed phase was never given it.
//
// That second half is not a nicety. Gating every phase reopened 48 of 49
// finished plans in the live store and emptied the dashboard's Done tab.
func TestListEpics_ClosureGate(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE epic_phases SET checkboxes_done = checkboxes_total, verify_mode='off'`)
	// Phase 1 was executed by the daemon and reported nothing; phase 2 was done by
	// hand and owes nothing.
	mustExec(`UPDATE epic_phases SET run_ended_at='2026-08-24T10:00:00Z', completion_report=NULL
	           WHERE seq=1 AND workspace_task_id=?`, taskID)
	mustExec(`UPDATE epic_phases SET run_ended_at=NULL, completion_report=NULL
	           WHERE seq=2 AND workspace_task_id=?`, taskID)

	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	byseq := map[int]epicPhaseDTO{}
	for _, p := range epics[0].Phases {
		byseq[p.Seq] = p
	}
	if got := byseq[1].CompletionState; got != "unreported" {
		t.Fatalf("a dispatched phase with no report = %q, want unreported", got)
	}
	if len(byseq[1].CompletionBlockers) == 0 {
		t.Error("an unreported phase must say what is missing")
	}
	if got := byseq[2].CompletionState; got != "complete" {
		t.Errorf("a hand-executed phase = %q, want complete — it was never handed the report contract", got)
	}
	if epics[0].Status != "active" {
		t.Errorf("plan status = %q, want active while a dispatched phase is unreported", epics[0].Status)
	}

	// Writing the report closes it — no lesson required, deliberately: a
	// retrospective is written after the work, so requiring one to call the work
	// done can never be satisfied in sequence.
	mustExec(`UPDATE epic_phases SET completion_report =
		'Shipped the schema change in internal/store/migrations/0058_x.sql and the reader in internal/api/x.go; make test green.'
		 WHERE seq=1 AND workspace_task_id=?`, taskID)

	getJSON(t, srv.URL+"/api/epics", &epics)
	for _, p := range epics[0].Phases {
		if p.CompletionState != "complete" {
			t.Errorf("phase %d = %q after the report, want complete (%v)", p.Seq, p.CompletionState, p.CompletionBlockers)
		}
	}
	if epics[0].Status != "done" {
		t.Errorf("plan status = %q, want done", epics[0].Status)
	}
}
