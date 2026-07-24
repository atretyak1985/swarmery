package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// boardTaskKeys is the frozen JSON key set of BoardTask in web/src/api/types.ts.
// Drift between boardTaskDTO and types.ts fails the WS shape assertion below.
var boardTaskKeys = []string{
	"id", "externalId", "projectId", "projectSlug", "title", "prompt",
	"priority", "status", "boardColumn", "paused", "userPaused",
	"dependencies", "model", "fileScope", "branch", "worktreePath",
	"dispatchError", "retryCount", "verifyVerdict", "verifyDetail",
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
