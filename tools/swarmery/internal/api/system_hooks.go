package api

// phase 4: system (step-10) — hooks toggle/edit, the only settings.json write
// surface. All file IO goes through the sysedit hooks pipeline (backup,
// atomic tmp+rename, 409 by base_hash against DISK truth, forced rescan);
// managed='swarmery' rows are refused so the UI can never cut off swarmery's
// own data collection. base_hash is the hooks row content_hash the client
// loaded from GET /api/system/hooks. After a successful edit the rescan
// reassigns row ids (per-source_file delete-and-insert) — clients refetch the
// list instead of trusting the old id.

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
)

// hookEditor is the sysedit editor behind the hooks write endpoints, attached
// as a package variable (pattern: AttachBus / AttachApprovals). nil (serve
// --no-ingest, no scanner to converge the registry) serves 503.
var hookEditor *sysedit.Editor

// AttachHookEditor wires the sysedit editor into the hooks write endpoints.
func AttachHookEditor(ed *sysedit.Editor) { hookEditor = ed }

type hookToggleRequest struct {
	Enabled  *bool  `json:"enabled"`
	BaseHash string `json:"base_hash"`
}

type hookUpdateRequest struct {
	Command  string `json:"command"`
	Timeout  *int64 `json:"timeout"` // seconds; absent removes the key
	BaseHash string `json:"base_hash"`
}

// POST /api/system/hooks/{id}/toggle {enabled, base_hash}
func (h *Handler) toggleSystemHook(w http.ResponseWriter, r *http.Request) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	if hookEditor == nil {
		hookErrJSON(w, http.StatusServiceUnavailable, "hooks editor not attached (serve without --no-ingest)")
		return
	}
	var req hookToggleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil || req.BaseHash == "" {
		hookErrJSON(w, http.StatusBadRequest, "want JSON body {enabled: bool, base_hash: string}")
		return
	}
	writeHookEditResult(w, hookEditor.ToggleHook(id, *req.Enabled, req.BaseHash))
}

// PUT /api/system/hooks/{id} {command, timeout?, base_hash}
func (h *Handler) updateSystemHook(w http.ResponseWriter, r *http.Request) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	if hookEditor == nil {
		hookErrJSON(w, http.StatusServiceUnavailable, "hooks editor not attached (serve without --no-ingest)")
		return
	}
	var req hookUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Command == "" || req.BaseHash == "" {
		hookErrJSON(w, http.StatusBadRequest, "want JSON body {command: string, timeout?: number, base_hash: string}")
		return
	}
	writeHookEditResult(w, hookEditor.UpdateHook(id, req.Command, req.Timeout, req.BaseHash))
}

// writeHookEditResult maps the sysedit error contract onto HTTP:
// ErrHookManaged/ErrReadOnly → 403, ErrConflict → 409 {disk_hash, base_hash,
// diff}, ErrNotFound → 404, anything else → 500.
func writeHookEditResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, map[string]string{"status": "ok"}, nil)
	case errors.Is(err, sysedit.ErrHookManaged):
		hookErrJSON(w, http.StatusForbidden,
			"hook is managed by the swarmery installer — manage it via `swarmery hooks`")
	case errors.Is(err, sysedit.ErrReadOnly):
		hookErrJSON(w, http.StatusForbidden, "readonly mode ("+sysedit.EnvReadOnly+")")
	case errors.Is(err, sysedit.ErrConflict):
		var ce *sysedit.ConflictError
		body := map[string]string{"error": "content changed on disk since base_hash"}
		if errors.As(err, &ce) {
			body["disk_hash"], body["base_hash"], body["diff"] = ce.DiskHash, ce.BaseHash, ce.Diff
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(body)
	case errors.Is(err, sysedit.ErrNotFound):
		hookErrJSON(w, http.StatusNotFound, "hook not found")
	default:
		writeErr(w, err)
	}
}

func hookErrJSON(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
