package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// boardTaskKeys is the frozen JSON key set of BoardTask in web/src/api/types.ts.
// Drift between boardTaskDTO and types.ts fails the WS shape assertion below.
// source / staleAfter / dispatchedPrompt (0066) ship backend-first: the SPA
// ignores keys it does not declare, and types.ts picks them up with the UI
// that renders them.
var boardTaskKeys = []string{
	"id", "externalId", "projectId", "projectSlug", "title", "prompt",
	"priority", "status", "boardColumn", "paused", "userPaused",
	"dependencies", "model", "playbook", "fileScope", "labels", "branch", "worktreePath",
	"dispatchError", "startPoint", "retryCount", "verifyRetryCount",
	"verifyVerdict", "verifyDetail", "resultNote",
	"agent", "origin", "originSessionId",
	"source", "staleAfter", "dispatchedPrompt", "pendingApprovalCount",
	"planExternalId",
	"columnMovedAt", "createdAt",
}

func postBoard(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"/api/board/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func patchBoard(t *testing.T, url string, id int64, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPatch,
		fmt.Sprintf("%s/api/board/tasks/%d", url, id),
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestBoardTaskCRUD(t *testing.T) {
	srv, _ := testServerWithDB(t) // fixture ingests one project (id 1)

	// --- Create: minimal body lands in triage with defaults ---
	resp := postBoard(t, srv.URL, `{"projectId":1,"title":"add waypoint editing","prompt":"do the thing"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.BoardColumn != "triage" || created.Priority != "normal" ||
		created.Status != "queued" || created.ExternalID[:2] != "T-" {
		t.Fatalf("created = %+v", created)
	}
	if created.Dependencies == nil || created.FileScope == nil ||
		len(created.Dependencies) != 0 || len(created.FileScope) != 0 {
		t.Errorf("created lists not empty-initialized: deps=%v scope=%v", created.Dependencies, created.FileScope)
	}
	if created.ColumnMovedAt != nil {
		t.Errorf("triage create should not set columnMovedAt, got %v", *created.ColumnMovedAt)
	}
	id := created.ID

	// --- Create: full body (priority, model, fileScope, dependencies, landing column) ---
	resp = postBoard(t, srv.URL,
		`{"projectId":1,"title":"t2","prompt":"p2","priority":"urgent","model":"opus","fileScope":["src/a","web/**"],"dependencies":["T-aaaaaa"],"boardColumn":"todo"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("full create status = %d", resp.StatusCode)
	}
	var full boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&full)
	resp.Body.Close()
	if full.Priority != "urgent" || full.Model == nil || *full.Model != "opus" ||
		len(full.FileScope) != 2 || len(full.Dependencies) != 1 || full.BoardColumn != "todo" {
		t.Fatalf("full create = %+v", full)
	}
	if full.ColumnMovedAt == nil {
		t.Error("non-triage landing column should set columnMovedAt")
	}

	// --- Validation failures → 400 ---
	for _, bad := range []string{
		`{"projectId":1,"title":"","prompt":"p"}`,                          // empty title
		`{"projectId":1,"title":"t","prompt":""}`,                          // empty prompt
		`{"projectId":1,"title":"t","prompt":"p","priority":"medium"}`,     // bad priority
		`{"projectId":1,"title":"t","prompt":"p","boardColumn":"backlog"}`, // bad column
		`{"projectId":999,"title":"t","prompt":"p"}`,                       // unknown project
		`not json`,
	} {
		r := postBoard(t, srv.URL, bad)
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s → %d, want 400", bad, r.StatusCode)
		}
		r.Body.Close()
	}

	// --- PATCH: move triage → todo sets columnMovedAt ---
	resp = patchBoard(t, srv.URL, id, `{"boardColumn":"todo"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch move status = %d", resp.StatusCode)
	}
	var moved boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&moved)
	resp.Body.Close()
	if moved.BoardColumn != "todo" || moved.ColumnMovedAt == nil {
		t.Fatalf("moved = %+v, want todo + columnMovedAt set", moved)
	}

	// --- PATCH: edit fields (priority, fileScope, paused) ---
	resp = patchBoard(t, srv.URL, id, `{"priority":"high","fileScope":["internal/api"],"paused":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch edit status = %d", resp.StatusCode)
	}
	var edited boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&edited)
	resp.Body.Close()
	if edited.Priority != "high" || len(edited.FileScope) != 1 || !edited.Paused {
		t.Fatalf("edited = %+v", edited)
	}

	// --- PATCH: remaining editable fields (model, dependencies, userPaused, prompt) ---
	resp = patchBoard(t, srv.URL, id,
		`{"model":"sonnet","dependencies":["T-bbbbbb","T-cccccc"],"userPaused":true,"prompt":"revised prompt"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch rest status = %d", resp.StatusCode)
	}
	var rest boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&rest)
	resp.Body.Close()
	if rest.Model == nil || *rest.Model != "sonnet" || len(rest.Dependencies) != 2 ||
		!rest.UserPaused || rest.Prompt != "revised prompt" {
		t.Fatalf("patch rest = %+v", rest)
	}
	// Clearing model with an empty string nulls it.
	resp = patchBoard(t, srv.URL, id, `{"model":""}`)
	json.NewDecoder(resp.Body).Decode(&rest)
	resp.Body.Close()
	if rest.Model != nil {
		t.Errorf("empty model should null the column, got %v", *rest.Model)
	}

	// --- PATCH: empty body is an idempotent no-op returning current state ---
	resp = patchBoard(t, srv.URL, id, `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty patch status = %d", resp.StatusCode)
	}
	var noop boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&noop)
	resp.Body.Close()
	if noop.ID != id || noop.BoardColumn != "todo" {
		t.Errorf("empty patch = %+v, want unchanged todo row", noop)
	}

	// --- PATCH: bad priority / malformed JSON → 400 ---
	resp = patchBoard(t, srv.URL, id, `{"priority":"critical"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("patch bad priority = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = patchBoard(t, srv.URL, id, `not json`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("patch malformed json = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// --- PATCH: illegal done → in_progress → 400 ---
	// First force the row to done (legal: todo→done is permissive).
	resp = patchBoard(t, srv.URL, id, `{"boardColumn":"done"}`)
	resp.Body.Close()
	resp = patchBoard(t, srv.URL, id, `{"boardColumn":"in_progress"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("done→in_progress = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	// any → archived is always allowed even from done.
	resp = patchBoard(t, srv.URL, id, `{"boardColumn":"archived"}`)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("done→archived = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// --- PATCH: bad column, empty title, unknown id ---
	resp = patchBoard(t, srv.URL, id, `{"boardColumn":"nope"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("patch bad column = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = patchBoard(t, srv.URL, id, `{"title":"  "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("patch empty title = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = patchBoard(t, srv.URL, 999999, `{"boardColumn":"todo"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("patch unknown id = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// --- GET filters: projectId + boardColumn ---
	// full task is in todo; id is archived now.
	var todoList []boardTaskDTO
	getJSON(t, srv.URL+"/api/board/tasks?projectId=1&boardColumn=todo", &todoList)
	if len(todoList) != 1 || todoList[0].ID != full.ID {
		t.Fatalf("todo filter = %+v, want [full task]", todoList)
	}
	var archivedList []boardTaskDTO
	getJSON(t, srv.URL+"/api/board/tasks?boardColumn=archived", &archivedList)
	if len(archivedList) != 1 || archivedList[0].ID != id {
		t.Fatalf("archived filter = %+v, want [id]", archivedList)
	}
	// Unknown column filter → 400.
	r := srvGet(t, srv.URL+"/api/board/tasks?boardColumn=bogus")
	if r != http.StatusBadRequest {
		t.Errorf("bad column filter = %d, want 400", r)
	}

	// --- Board rows do NOT leak into the workspace summary (GET /api/tasks) ---
	var wsSummary []map[string]any
	getJSON(t, srv.URL+"/api/tasks", &wsSummary)
	for _, row := range wsSummary {
		if row["title"] == "add waypoint editing" || row["title"] == "t2" {
			t.Errorf("board task leaked into workspace summary: %v", row)
		}
	}
}

// TestBoardTaskLabels: labels normalize on write (case/whitespace/dedup),
// default to [] rather than null, clear via PATCH, reject invalid input, and
// drive the ?label= list filter.
func TestBoardTaskLabels(t *testing.T) {
	srv, _ := testServerWithDB(t)

	// --- Create without labels → [] not null ---
	resp := postBoard(t, srv.URL, `{"projectId":1,"title":"plain","prompt":"p"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var plain boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&plain)
	resp.Body.Close()
	if plain.Labels == nil || len(plain.Labels) != 0 {
		t.Fatalf("labels not empty-initialized: %v", plain.Labels)
	}

	// --- Create with labels: normalize case/whitespace/dedup ---
	resp = postBoard(t, srv.URL,
		`{"projectId":1,"title":"jira card","prompt":"p","labels":["Jira-Ticket"," jira-ticket "]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("labeled create status = %d, want 201", resp.StatusCode)
	}
	var labeled boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&labeled)
	resp.Body.Close()
	if len(labeled.Labels) != 1 || labeled.Labels[0] != "jira-ticket" {
		t.Fatalf("labels = %v, want exactly [jira-ticket]", labeled.Labels)
	}

	// --- Invalid labels → 400: too many, spaces, too long ---
	tooMany := make([]string, 11)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("label%d", i)
	}
	tooManyJSON, _ := json.Marshal(tooMany)
	for _, bad := range []string{
		fmt.Sprintf(`{"projectId":1,"title":"t","prompt":"p","labels":%s}`, tooManyJSON),
		`{"projectId":1,"title":"t","prompt":"p","labels":["has space"]}`,
		`{"projectId":1,"title":"t","prompt":"p","labels":["` + strings.Repeat("x", 41) + `"]}`,
	} {
		r := postBoard(t, srv.URL, bad)
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s → %d, want 400", bad, r.StatusCode)
		}
		r.Body.Close()
	}

	// --- PATCH: set then clear labels ---
	resp = patchBoard(t, srv.URL, plain.ID, `{"labels":["Foo","bar"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch set labels status = %d", resp.StatusCode)
	}
	var withLabels boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&withLabels)
	resp.Body.Close()
	if len(withLabels.Labels) != 2 || withLabels.Labels[0] != "foo" || withLabels.Labels[1] != "bar" {
		t.Fatalf("patched labels = %v, want [foo bar]", withLabels.Labels)
	}
	resp = patchBoard(t, srv.URL, plain.ID, `{"labels":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch clear labels status = %d", resp.StatusCode)
	}
	var cleared boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&cleared)
	resp.Body.Close()
	if cleared.Labels == nil || len(cleared.Labels) != 0 {
		t.Fatalf("cleared labels = %v, want []", cleared.Labels)
	}
	// PATCH with an invalid label → 400, and it must not have touched the row.
	resp = patchBoard(t, srv.URL, plain.ID, `{"labels":["has space"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("patch invalid label = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// --- GET filter by label ---
	var filtered []boardTaskDTO
	getJSON(t, srv.URL+"/api/board/tasks?label=jira-ticket", &filtered)
	if len(filtered) != 1 || filtered[0].ID != labeled.ID {
		t.Fatalf("label filter = %+v, want [labeled task]", filtered)
	}
	// An invalid filter value is a 400, same shape as the boardColumn filter.
	r := srvGet(t, srv.URL+"/api/board/tasks?label="+"has%20space")
	if r != http.StatusBadRequest {
		t.Errorf("bad label filter = %d, want 400", r)
	}
}

// srvGet issues a GET and returns just the status code (for negative cases).
func srvGet(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestBoardTaskCrossOrigin: writes reject a foreign Origin (requireLocalOrigin).
func TestBoardTaskCrossOrigin(t *testing.T) {
	srv, _ := testServerWithDB(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/board/tasks",
		strings.NewReader(`{"projectId":1,"title":"t","prompt":"p"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d, want 403", resp.StatusCode)
	}
}

// TestBoardDoneTicksPhaseChecklist: moving a board task to done via PATCH must
// flip every remaining unchecked acceptance-criteria checkbox in the linked
// phase doc. The activate endpoint no longer exists (removed in
// interactive-planning-v2 phase 4), so we seed activated_board_task_id
// directly in the DB — the tick path in patchBoardTask only reads that column,
// not the removed endpoint.
func TestBoardDoneTicksPhaseChecklist(t *testing.T) {
	// Build a DB with the store schema so all migrations including epic_phases run.
	db, err := store.Open(filepath.Join(t.TempDir(), "tick.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Seed a project, a workspace epic task, a board task, and a phase doc with
	// one checked and two unchecked acceptance-criteria boxes.
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/q','q','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// Workspace task (the epic).
	if _, err := db.Exec(`INSERT INTO tasks(id, project_id, title, prompt, status, created_at, source, external_id)
		VALUES(10, 1, 'My Epic', 'goal', 'running', '2026-07-24T00:00:00Z', 'workspace', '2026-07-24-my-epic')`); err != nil {
		t.Fatal(err)
	}
	// Board task minted for phase 1 — starts in todo so we can move it to done.
	if _, err := db.Exec(`INSERT INTO tasks(id, project_id, title, prompt, status, created_at, source, external_id, board_column)
		VALUES(64, 1, 'Phase 1 board task', 'implement schema', 'queued', '2026-07-26T00:00:00Z', 'queue', 'T-aaaaaa', 'todo')`); err != nil {
		t.Fatal(err)
	}

	// Phase doc on disk: one checked box, two unchecked — the tick must flip the
	// two unchecked ones and leave the already-checked one alone.
	doc := filepath.Join(t.TempDir(), "phase-1-schema.md")
	docBody := "# Phase 1 — Schema\n\n## Acceptance criteria\n- [x] a\n- [ ] b\n- [ ] c\n"
	if err := os.WriteFile(doc, []byte(docBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed the epic_phases row with activated_board_task_id set directly — no
	// activate endpoint needed (it was removed in phase 4).
	if _, err := db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
		 activated_at, activated_board_task_id)
		VALUES(10, 1, 'Phase 1 — Schema', ?, '[]', 3, 1, '2026-07-26T00:00:00Z', 64)`, doc); err != nil {
		t.Fatal(err)
	}

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// PATCH board task 64 to done — this is the trigger for TickPhaseChecklist.
	resp := patchBoard(t, srv.URL, 64, `{"boardColumn":"done"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH to done = %d, want 200", resp.StatusCode)
	}

	// Assert wsingest.TickPhaseChecklist ran: both unchecked boxes must now be
	// checked in the doc file. The already-checked box must be untouched.
	got, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "- [ ]") {
		t.Errorf("doc still has unchecked boxes after board move to done:\n%s", got)
	}
	if !strings.Contains(string(got), "- [x] a") ||
		!strings.Contains(string(got), "- [x] b") ||
		!strings.Contains(string(got), "- [x] c") {
		t.Errorf("expected all three boxes checked; doc:\n%s", got)
	}
}

// TestWSTaskUpdatedShape: creating a board task emits a task_updated frame whose
// payload matches the frozen BoardTask key set exactly.
func TestWSTaskUpdatedShape(t *testing.T) {
	bus := ingest.NewBus()
	AttachBus(bus)
	t.Cleanup(func() { AttachBus(nil) })

	srv, db := testServerWithDB(t)

	// Pre-create a board task so we have a stable id to publish for.
	resp := postBoard(t, srv.URL, `{"projectId":1,"title":"ws task","prompt":"p"}`)
	var created boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == 0 {
		t.Fatal("no task id from create")
	}
	// A task_updated for a vanished row hydrates to nil (WS skips the frame).
	h := &Handler{DB: db}
	if gone, err := h.boardTaskByID(424242); err != nil || gone != nil {
		t.Errorf("boardTaskByID(missing) = %+v, %v; want nil, nil", gone, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	readFrame := newFrameReader(t, ctx, c, func() {
		bus.Publish(ingest.Notification{Type: ingest.NoteTaskUpdated, TaskID: created.ID})
	})

	frame := readFrame()
	assertEnvelope(t, frame, "task_updated")
	assertPayloadKeys(t, frame, boardTaskKeys)
	var bt struct {
		ID          int64  `json:"id"`
		ExternalID  string `json:"externalId"`
		BoardColumn string `json:"boardColumn"`
		Title       string `json:"title"`
	}
	if err := json.Unmarshal(frame["payload"], &bt); err != nil {
		t.Fatal(err)
	}
	if bt.ID != created.ID || bt.ExternalID == "" || bt.BoardColumn != "triage" || bt.Title != "ws task" {
		t.Errorf("task_updated payload = %+v, want hydrated board task %d", bt, created.ID)
	}
}

// TestBoardTaskExposesDispatchTruth: the two 0051 columns reach the client with
// their real values. The key-set assertion above proves the fields EXIST; this
// proves they carry the right ones — the failure mode a positional Scan invites
// is two same-typed columns silently swapping, which a key check cannot see.
// startPoint answers "graded against what?" on a card whose verdict is disputed.
func TestBoardTaskExposesDispatchTruth(t *testing.T) {
	srv, db := testServerWithDB(t)
	id := createdBoardTask(t, srv.URL, "dispatch truth")

	// The list endpoint is the only reader (there is no GET-by-id route); pick our
	// row out of it.
	fetch := func() boardTaskDTO {
		t.Helper()
		var list []boardTaskDTO
		getJSON(t, srv.URL+"/api/board/tasks?projectId=1", &list)
		for _, d := range list {
			if d.ID == id {
				return d
			}
		}
		t.Fatalf("task %d not in the board list", id)
		return boardTaskDTO{}
	}

	// A fresh card was never admitted: no start point, no spent verify budget.
	fresh := fetch()
	if fresh.StartPoint != nil {
		t.Errorf("startPoint = %q on a never-admitted card, want null", *fresh.StartPoint)
	}
	if fresh.VerifyRetryCount != 0 {
		t.Errorf("verifyRetryCount = %d, want 0", fresh.VerifyRetryCount)
	}

	// Now model an admitted card that failed verification twice while the
	// dispatcher healed it once: the two counters must not blur together.
	if _, err := db.Exec(
		`UPDATE tasks SET start_point='cafebabe', retry_count=1, verify_retry_count=2 WHERE id=?`,
		id); err != nil {
		t.Fatal(err)
	}
	got := fetch()
	if got.StartPoint == nil || *got.StartPoint != "cafebabe" {
		t.Errorf("startPoint = %v, want cafebabe", got.StartPoint)
	}
	if got.RetryCount != 1 {
		t.Errorf("retryCount = %d, want 1 (dispatch heals)", got.RetryCount)
	}
	if got.VerifyRetryCount != 2 {
		t.Errorf("verifyRetryCount = %d, want 2 (verify fix chain)", got.VerifyRetryCount)
	}
}

func deleteBoard(t *testing.T, url string, id int64) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/board/tasks/%d", url, id), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// createdBoardTask posts a minimal task and returns its row id.
func createdBoardTask(t *testing.T, url, title string) int64 {
	t.Helper()
	resp := postBoard(t, url, fmt.Sprintf(`{"projectId":1,"title":%q,"prompt":"p"}`, title))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %q status = %d, want 201", title, resp.StatusCode)
	}
	var d boardTaskDTO
	json.NewDecoder(resp.Body).Decode(&d)
	if d.ID == 0 {
		t.Fatalf("create %q returned no id", title)
	}
	return d.ID
}

// TestBoardTaskDelete: the permanent-removal path — 204 + gone from the list,
// 404 on a repeat, 404 for a non-board (workspace) row, 409 while running, and
// the task_sessions link rows cleaned up (foreign_keys(1) would otherwise
// refuse the delete of any task that ever ran).
func TestBoardTaskDelete(t *testing.T) {
	srv, db := testServerWithDB(t)

	// --- Happy path: created → deleted → gone from the board list ---
	id := createdBoardTask(t, srv.URL, "no longer relevant")
	resp := deleteBoard(t, srv.URL, id)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}
	var list []boardTaskDTO
	getJSON(t, srv.URL+"/api/board/tasks?projectId=1", &list)
	for _, d := range list {
		if d.ID == id {
			t.Fatalf("task %d still listed after delete", id)
		}
	}

	// --- Repeat delete and an unknown id are 404 ---
	for _, target := range []int64{id, 987654} {
		r := deleteBoard(t, srv.URL, target)
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("delete %d = %d, want 404", target, r.StatusCode)
		}
	}
	// A malformed id is a 400.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/board/tasks/abc", nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("delete bad id = %d, want 400", r.StatusCode)
	}

	// --- A running task is refused (409) until it leaves running ---
	running := createdBoardTask(t, srv.URL, "in flight")
	if _, err := db.Exec(
		`UPDATE tasks SET status='running', board_column='in_progress' WHERE id=?`, running); err != nil {
		t.Fatal(err)
	}
	r = deleteBoard(t, srv.URL, running)
	r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("delete running = %d, want 409", r.StatusCode)
	}
	var stillThere int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id=?`, running).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 1 {
		t.Fatal("409 must not delete the row")
	}

	// --- Linked sessions are unlinked so the FK does not block the delete ---
	var sessionID int64
	if err := db.QueryRow(`SELECT id FROM sessions LIMIT 1`).Scan(&sessionID); err != nil {
		t.Fatalf("fixture session: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO task_sessions(task_id, session_id, link_source, confidence)
		 VALUES(?,?, 'explicit', 1.0)`, running, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status='done', board_column='done' WHERE id=?`, running); err != nil {
		t.Fatal(err)
	}
	r = deleteBoard(t, srv.URL, running)
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete after stop = %d, want 204", r.StatusCode)
	}
	var links int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE task_id=?`, running).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Errorf("task_sessions links left behind: %d", links)
	}

	// --- A workspace-ingested row is not board-deletable ---
	res, err := db.Exec(
		`INSERT INTO tasks(project_id, title, prompt, status, created_at, source, external_id)
		 VALUES(1,'disk task','p','queued','2026-01-01T00:00:00.000Z','workspace','2026-01-01-disk')`)
	if err != nil {
		t.Fatal(err)
	}
	wsID, _ := res.LastInsertId()
	r = deleteBoard(t, srv.URL, wsID)
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("delete workspace row = %d, want 404", r.StatusCode)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id=?`, wsID).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 1 {
		t.Error("workspace row must survive a board delete")
	}
}

// TestBoardTaskDeleteCrossOrigin: DELETE carries the same origin hardening as
// the other board writes.
func TestBoardTaskDeleteCrossOrigin(t *testing.T) {
	srv, _ := testServerWithDB(t)
	id := createdBoardTask(t, srv.URL, "origin guard")
	req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/board/tasks/%d", srv.URL, id), nil)
	req.Header.Set("Origin", "http://evil.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin DELETE = %d, want 403", resp.StatusCode)
	}
}

// TestWSTaskDeletedShape: the delete frame is the thin {taskId, projectId} hint
// (the row is gone, so nothing can be hydrated) and it is emitted by the
// endpoint itself.
func TestWSTaskDeletedShape(t *testing.T) {
	bus := ingest.NewBus()
	AttachBus(bus)
	t.Cleanup(func() { AttachBus(nil) })

	srv, _ := testServerWithDB(t)
	id := createdBoardTask(t, srv.URL, "doomed")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	readFrame := newFrameReader(t, ctx, c, func() {
		bus.Publish(ingest.Notification{Type: ingest.NoteTaskDeleted, TaskID: id, ProjectID: 1})
	})
	frame := readFrame()
	assertEnvelope(t, frame, "task_deleted")
	assertPayloadKeys(t, frame, []string{"taskId", "projectId"})
	var p struct {
		TaskID    int64 `json:"taskId"`
		ProjectID int64 `json:"projectId"`
	}
	if err := json.Unmarshal(frame["payload"], &p); err != nil {
		t.Fatal(err)
	}
	if p.TaskID != id || p.ProjectID != 1 {
		t.Errorf("task_deleted payload = %+v, want {%d 1}", p, id)
	}
}
