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

func TestActivateEpicPhaseCreatesBoardTask(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)

	// Activate phase seq=1 (its DB id).
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("activate status = %d, want 201", resp.StatusCode)
	}
	var task boardTaskDTO
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	// Title = the doc H1; prompt = full doc; lands in todo.
	if task.Title != "Phase 1 — Schema" {
		t.Errorf("title = %q, want the doc H1", task.Title)
	}
	if task.BoardColumn != "todo" {
		t.Errorf("boardColumn = %q, want todo", task.BoardColumn)
	}
	// file_scope parsed from **Files:**.
	if !reflect.DeepEqual(task.FileScope, []string{"internal/store/x.sql", "internal/api/x.go"}) {
		t.Errorf("fileScope = %v", task.FileScope)
	}
	// The phase row is now stamped.
	var at sql.NullString
	var boardID sql.NullInt64
	if err := db.QueryRow(`SELECT activated_at, activated_board_task_id FROM epic_phases WHERE id=?`, phase1ID).
		Scan(&at, &boardID); err != nil {
		t.Fatal(err)
	}
	if !at.Valid || !boardID.Valid || boardID.Int64 != task.ID {
		t.Errorf("phase not stamped: at=%v board=%v", at, boardID)
	}
}

func TestActivateEpicPhaseIdempotent409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	var phase1ID int64
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id=? AND seq=1`, taskID).
		Scan(&phase1ID); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/epics/" + itoa(taskID) + "/phases/" + itoa(phase1ID) + "/activate"

	// First activation → 201.
	resp1, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first activate = %d, want 201", resp1.StatusCode)
	}
	// Second → 409 carrying the existing task.
	resp2, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second activate = %d, want 409", resp2.StatusCode)
	}
	var body struct {
		Error string       `json:"error"`
		Task  boardTaskDTO `json:"task"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Task.Title != "Phase 1 — Schema" {
		t.Errorf("409 task = %+v", body.Task)
	}
	// Exactly ONE board task was created despite two calls.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE source='queue'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("board tasks = %d, want 1 (idempotent)", n)
	}
}

func TestActivateDependentPhaseWiresDependency(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	id := func(seq int) int64 {
		var v int64
		if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id=? AND seq=?`, taskID, seq).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Activate phase 1, then phase 2 (which depends on 1).
	post := func(phaseID int64) boardTaskDTO {
		resp, err := http.Post(srv.URL+"/api/epics/"+itoa(taskID)+"/phases/"+itoa(phaseID)+"/activate", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("activate = %d, want 201", resp.StatusCode)
		}
		var d boardTaskDTO
		if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	task1 := post(id(1))
	task2 := post(id(2))
	// Phase 2's board task depends on phase 1's board task external_id.
	if !reflect.DeepEqual(task2.Dependencies, []string{task1.ExternalID}) {
		t.Errorf("task2.dependencies = %v, want [%s]", task2.Dependencies, task1.ExternalID)
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

func TestActivateEpicPhaseNotFound(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	// A phase id that doesn't belong to this task → 404.
	got := doReq(t, http.MethodPost, srv.URL+"/api/epics/"+itoa(taskID)+"/phases/999999/activate", "")
	if got != http.StatusNotFound {
		t.Errorf("activate unknown phase = %d, want 404", got)
	}
	// Bad ids → 400.
	if got := doReq(t, http.MethodPost, srv.URL+"/api/epics/abc/phases/1/activate", ""); got != http.StatusBadRequest {
		t.Errorf("activate bad task id = %d, want 400", got)
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
