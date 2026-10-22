package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrev"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// seedRevisionTask mints the workspace task + plan artifact StartRevise and
// the revision endpoints resolve, pointing at a real temp plan dir.
func seedRevisionTask(t *testing.T, db *sql.DB, planDir string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1,'My Epic','goal','running',
		'2026-08-11T00:00:00Z','2026-08-11T00:00:00Z','workspace','2026-08-11-my-epic')`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?,'plan',?,'hash','2026-08-11T00:00:00Z')`, taskID, planDir); err != nil {
		t.Fatal(err)
	}
	return taskID
}

func stageRevisionRow(t *testing.T, db *sql.DB, taskID int64, planDir string, files []planrev.File) int64 {
	t.Helper()
	id, err := planrev.Insert(db, planrev.Revision{
		WorkspaceTaskID: taskID,
		PlanDir:         planDir,
		SessionUUID:     "sess-rev",
		Origin:          planrev.OriginOperator,
		Reason:          "tighten the plan",
		CreatedAt:       "2026-08-11T10:00:00Z",
	}, files)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func postRevJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func TestStartRevisionAccepted(t *testing.T) {
	srv, db, svc := serverWithPlanning(t, &planStubRunner{})
	svc.ScratchRoot = t.TempDir()
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "README.md"), []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskID := seedRevisionTask(t, db, planDir)

	resp := postRevJSON(t, srv.URL+"/api/epics/"+itoa(taskID)+"/revisions", `{"reason":"phase 3 drifted"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if body := decodeBody(t, resp); body["sessionUuid"] != "uuid-api" {
		t.Errorf("sessionUuid = %v, want uuid-api", body["sessionUuid"])
	}
}

func TestStartRevisionValidation(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)

	// 400 empty reason.
	resp := postRevJSON(t, srv.URL+"/api/epics/"+itoa(taskID)+"/revisions", `{"reason":"  "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty reason: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// 404 unknown task.
	resp = postRevJSON(t, srv.URL+"/api/epics/99999/revisions", `{"reason":"r"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown task: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStartRevision409PlanBusy(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)
	if _, err := db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, run_state)
		VALUES (?,1,'A',?, 'running')`, taskID, filepath.Join(planDir, "phase-1.md")); err != nil {
		t.Fatal(err)
	}

	resp := postRevJSON(t, srv.URL+"/api/epics/"+itoa(taskID)+"/revisions", `{"reason":"r"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStartRevision409NamesOpenRevision(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)
	openID := stageRevisionRow(t, db, taskID, planDir, []planrev.File{
		{DocPath: "a.md", Action: planrev.ActionCreate, Proposed: "x"},
	})

	resp := postRevJSON(t, srv.URL+"/api/epics/"+itoa(taskID)+"/revisions", `{"reason":"another"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if int64(body["revisionId"].(float64)) != openID {
		t.Errorf("revisionId = %v, want the open revision %d named", body["revisionId"], openID)
	}
}

func TestListRevisions(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)
	first := stageRevisionRow(t, db, taskID, planDir, []planrev.File{
		{DocPath: "a.md", Action: planrev.ActionCreate, Proposed: "secret content"},
	})
	if _, err := planrev.Decide(db, first, planrev.StatusRejected, "operator", "2026-08-11T10:30:00Z"); err != nil {
		t.Fatal(err)
	}
	second := stageRevisionRow(t, db, taskID, planDir, []planrev.File{
		{DocPath: "b.md", Action: planrev.ActionRename, RenameFrom: "old-b.md", Proposed: "moved"},
	})

	resp, err := http.Get(srv.URL + "/api/epics/" + itoa(taskID) + "/revisions")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "secret content") {
		t.Error("list endpoint leaked proposed content")
	}
	var body struct {
		Revisions []struct {
			ID        int64  `json:"id"`
			Status    string `json:"status"`
			Origin    string `json:"origin"`
			Reason    string `json:"reason"`
			CreatedAt string `json:"createdAt"`
			DecidedBy string `json:"decidedBy"`
			Files     []struct {
				DocPath    string `json:"docPath"`
				Action     string `json:"action"`
				RenameFrom string `json:"renameFrom"`
			} `json:"files"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2", len(body.Revisions))
	}
	if body.Revisions[0].ID != second || body.Revisions[1].ID != first {
		t.Errorf("order = [%d %d], want newest first [%d %d]",
			body.Revisions[0].ID, body.Revisions[1].ID, second, first)
	}
	if body.Revisions[1].Status != "rejected" || body.Revisions[1].DecidedBy != "operator" {
		t.Errorf("decided revision fields missing: %+v", body.Revisions[1])
	}
	f := body.Revisions[0].Files[0]
	if f.DocPath != "b.md" || f.Action != "rename" || f.RenameFrom != "old-b.md" {
		t.Errorf("file DTO = %+v, want docPath/action/renameFrom", f)
	}

	// 404 unknown task.
	resp, err = http.Get(srv.URL + "/api/epics/99999/revisions")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown task: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestGetRevisionDiffAndStale(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)
	if err := os.WriteFile(filepath.Join(planDir, "a.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := stageRevisionRow(t, db, taskID, planDir, []planrev.File{
		{DocPath: "a.md", Action: planrev.ActionUpdate,
			BaseHash: planrev.Sha256Hex([]byte("one\n")), Proposed: "two\n"},
	})

	get := func() (int, map[string]any) {
		resp, err := http.Get(srv.URL + "/api/revisions/" + itoa(id))
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, decodeBody(t, resp)
	}

	code, body := get()
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	files := body["files"].([]any)
	f := files[0].(map[string]any)
	if f["stale"] != false {
		t.Errorf("stale = %v, want false while disk matches base_hash", f["stale"])
	}
	diff := f["diff"].(string)
	if !strings.Contains(diff, "a/a.md") || !strings.Contains(diff, "-one") || !strings.Contains(diff, "+two") {
		t.Errorf("diff not computed live:\n%s", diff)
	}

	// Drift the live file — the SAME revision now reports stale.
	if err := os.WriteFile(filepath.Join(planDir, "a.md"), []byte("drifted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, body = get()
	f = body["files"].([]any)[0].(map[string]any)
	if f["stale"] != true {
		t.Errorf("stale = %v, want true after the live file drifted", f["stale"])
	}
	if !strings.Contains(f["diff"].(string), "-drifted") {
		t.Errorf("diff must be recomputed against the live bytes:\n%s", f["diff"])
	}

	// 404 unknown revision.
	resp, err := http.Get(srv.URL + "/api/revisions/99999")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown revision: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestApplyRevisionEndpoint(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	var relent []string
	AttachRevisionRelend(func(docs []string) int { relent = append(relent, docs...); return len(docs) })
	t.Cleanup(func() { AttachRevisionRelend(nil) })
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)
	if err := os.WriteFile(filepath.Join(planDir, "a.md"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := stageRevisionRow(t, db, taskID, planDir, []planrev.File{
		{DocPath: "a.md", Action: planrev.ActionUpdate,
			BaseHash: planrev.Sha256Hex([]byte("v1")), Proposed: "v2"},
	})

	resp := postRevJSON(t, srv.URL+"/api/revisions/"+itoa(id)+"/apply", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["status"] != "applied" || body["files"] != float64(1) {
		t.Errorf("body = %v, want status applied files 1", body)
	}
	if b, _ := os.ReadFile(filepath.Join(planDir, "a.md")); string(b) != "v2" {
		t.Errorf("a.md = %q, want the apply landed", b)
	}
	if want := []string{filepath.Join(planDir, "a.md")}; !reflect.DeepEqual(relent, want) {
		t.Errorf("re-lent %q, want %q — an applied revision must re-lend the docs it wrote", relent, want)
	}

	// Re-apply a decided revision → 409.
	resp = postRevJSON(t, srv.URL+"/api/revisions/"+itoa(id)+"/apply", "")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("re-apply: status = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// 404 unknown revision.
	resp = postRevJSON(t, srv.URL+"/api/revisions/99999/apply", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestApplyRevision409ConflictBody(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)
	if err := os.WriteFile(filepath.Join(planDir, "a.md"), []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := stageRevisionRow(t, db, taskID, planDir, []planrev.File{
		{DocPath: "a.md", Action: planrev.ActionUpdate,
			BaseHash: planrev.Sha256Hex([]byte("staged-base")), Proposed: "v2"},
	})

	resp := postRevJSON(t, srv.URL+"/api/revisions/"+itoa(id)+"/apply", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	conflicts := body["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1", len(conflicts))
	}
	c := conflicts[0].(map[string]any)
	if c["docPath"] != "a.md" ||
		c["baseHash"] != planrev.Sha256Hex([]byte("staged-base")) ||
		c["diskHash"] != planrev.Sha256Hex([]byte("live")) ||
		c["diff"] == "" {
		t.Errorf("conflict body = %v, want docPath/baseHash/diskHash/diff", c)
	}
	if b, _ := os.ReadFile(filepath.Join(planDir, "a.md")); string(b) != "live" {
		t.Errorf("a.md = %q — a conflicting apply must not write", b)
	}
}

func TestRejectRevision(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	planDir := t.TempDir()
	taskID := seedRevisionTask(t, db, planDir)
	id := stageRevisionRow(t, db, taskID, planDir, []planrev.File{
		{DocPath: "a.md", Action: planrev.ActionCreate, Proposed: "x"},
	})

	resp := postRevJSON(t, srv.URL+"/api/revisions/"+itoa(id)+"/reject", `{"note":"wrong direction"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body := decodeBody(t, resp); body["status"] != "rejected" {
		t.Errorf("body = %v, want status rejected", body)
	}
	rev, err := planrev.Get(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Status != planrev.StatusRejected || rev.DecidedBy != "operator" {
		t.Errorf("decision = %s/%s, want rejected/operator", rev.Status, rev.DecidedBy)
	}
	if !strings.Contains(rev.Reason, "Rejected: wrong direction") {
		t.Errorf("reason = %q, want the note appended", rev.Reason)
	}

	// Rejecting a decided revision → 409.
	resp = postRevJSON(t, srv.URL+"/api/revisions/"+itoa(id)+"/reject", `{}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("re-reject: status = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRevisionEndpoints503WithoutPlanning(t *testing.T) {
	AttachPlanning(nil)
	db, err := store.Open(filepath.Join(t.TempDir(), "rev503.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	for _, req := range []struct{ method, path string }{
		{"POST", "/api/epics/1/revisions"},
		{"GET", "/api/epics/1/revisions"},
		{"GET", "/api/revisions/1"},
		{"POST", "/api/revisions/1/apply"},
		{"POST", "/api/revisions/1/reject"},
	} {
		r, err := http.NewRequest(req.method, srv.URL+req.path, bytes.NewReader([]byte(`{"reason":"r"}`)))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", req.method, req.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
