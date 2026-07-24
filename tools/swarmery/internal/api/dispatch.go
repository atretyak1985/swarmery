package api

// Dispatcher control endpoints (fusion phase 3 — dispatcher): pause/resume the
// task dispatcher (global or per-project) and read its runtime status (slots,
// active runs, paused scopes, kill-switch). The dispatcher itself lives in
// internal/dispatch; these handlers are the thin write/read surface. The
// service is attached once at daemon startup (AttachDispatch) — the same
// package-var idiom as approvalsSvc — so httptest handlers built with
// &Handler{DB: db} stay hermetic (dispatchSvc nil ⇒ Poke is a no-op, the
// endpoints answer 503). Writes carry the same D4 requireLocalOrigin hardening
// as every other mutating endpoint.

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/dispatch"
)

// dispatchSvc is attached once at daemon startup (nil ⇒ dispatch endpoints 503
// and pokeDispatch is a no-op). Mirrors approvalsSvc.
var dispatchSvc *dispatch.Service

// AttachDispatch wires the dispatcher service into the api layer and gives it
// the api-owned task_updated emitter (so the WS envelope stays defined in one
// place — the frozen NoteTaskUpdated type, no new message type). Called from
// cmd/swarmery after the service is constructed and healed. In unit tests it is
// left nil so board writes never trigger a real headless spawn.
func AttachDispatch(s *dispatch.Service) {
	if s != nil {
		s.Notify = publishTaskUpdated
	}
	dispatchSvc = s
}

// pokeDispatch triggers a scheduling pass when the dispatcher is attached. A
// nil-safe wrapper so every board write site can call it unconditionally.
func pokeDispatch() {
	if dispatchSvc != nil {
		dispatchSvc.Poke()
	}
}

// getDispatch — GET /api/dispatch: the dispatcher status snapshot (enabled,
// global pause, caps, active runs, free slots, paused scopes). 503 when the
// dispatcher is not attached (serve --no-ingest, or a test handler).
func (h *Handler) getDispatch(w http.ResponseWriter, r *http.Request) {
	if dispatchSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "dispatcher not attached")
		return
	}
	st, err := dispatchSvc.Snapshot()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, st, nil)
}

// pauseDispatch — POST /api/dispatch/pause {scope:"global"|"project",
// projectId?, paused:bool}. Upserts the durable pause flag and pokes the
// scheduler so a resume admits immediately. requireLocalOrigin. 503 when the
// dispatcher is not attached.
func (h *Handler) pauseDispatch(w http.ResponseWriter, r *http.Request) {
	if dispatchSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "dispatcher not attached")
		return
	}
	var body struct {
		Scope     string `json:"scope"`
		ProjectID int64  `json:"projectId"`
		Paused    bool   `json:"paused"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	var scope string
	switch body.Scope {
	case "global":
		scope = "global"
	case "project":
		if body.ProjectID <= 0 {
			writeClientErr(w, http.StatusBadRequest, "projectId is required for scope=project")
			return
		}
		// Project must exist (a pause on a phantom project is a client bug).
		var one int
		if err := h.DB.QueryRow(`SELECT 1 FROM projects WHERE id=?`, body.ProjectID).Scan(&one); err != nil {
			writeClientErr(w, http.StatusBadRequest, "unknown project id")
			return
		}
		scope = dispatch.ProjectScope(body.ProjectID)
	default:
		writeClientErr(w, http.StatusBadRequest, `scope must be "global" or "project"`)
		return
	}

	if err := dispatchSvc.SetPause(scope, body.Paused); err != nil {
		writeErr(w, err)
		return
	}
	// A resume (paused=false) should admit backlog now; a pause is reflected on
	// the next pass. Poke covers both cheaply.
	pokeDispatch()
	writeJSON(w, map[string]any{"scope": scope, "paused": body.Paused}, nil)
}
