package api

// Permission-preset endpoints (fusion phase 11 — DESIGN.md §2 item 11):
//
//	GET /api/projects/{id}/permission-preset  → the effective policy view
//	PUT /api/projects/{id}/permission-preset  → set preset + overrides
//
// PUT compiles the preset into managed auto-approve rules (source='preset')
// via approvals.Compile, leaving manual rules untouched. Escalating to
// unrestricted or promoting command_exec/git_push to 'allow' is a privileged
// move gated behind {"confirm": true} → 428 with an explanation payload
// otherwise (R13). Writes carry the same D4 requireLocalOrigin hardening as
// every other mutating endpoint. A successful PUT pokes the dispatcher so an
// unlock (locked-down → other) admits any queued Todo task immediately.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
)

// getPermissionPreset — GET /api/projects/{id}/permission-preset. Returns the
// fail-closed default view for a project with no stored preset.
func (h *Handler) getPermissionPreset(w http.ResponseWriter, r *http.Request) {
	id, ok := h.presetProjectID(w, r)
	if !ok {
		return
	}
	view, err := approvals.EffectivePolicy(h.DB, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, view, nil)
}

// putPermissionPreset — PUT /api/projects/{id}/permission-preset
// {preset, overrides?, confirm?}. requireLocalOrigin.
func (h *Handler) putPermissionPreset(w http.ResponseWriter, r *http.Request) {
	id, ok := h.presetProjectID(w, r)
	if !ok {
		return
	}
	var body struct {
		Preset    string            `json:"preset"`
		Overrides map[string]string `json:"overrides"`
		Confirm   bool              `json:"confirm"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !approvals.KnownPreset(body.Preset) {
		writeClientErr(w, http.StatusBadRequest,
			`preset must be "unrestricted", "approval-required", or "locked-down"`)
		return
	}
	if body.Overrides == nil {
		body.Overrides = map[string]string{}
	}
	if msg := approvals.ValidateOverrides(body.Overrides); msg != "" {
		writeClientErr(w, http.StatusBadRequest, msg)
		return
	}

	// R13 escalation gate: a privileged move needs explicit confirm → 428.
	if escalations := approvals.Escalations(body.Preset, body.Overrides); len(escalations) > 0 && !body.Confirm {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionRequired) // 428
		json.NewEncoder(w).Encode(map[string]any{
			"error":       "confirmation required",
			"reason":      "this widens what agents can do without asking — confirm to proceed",
			"escalations": escalations,
		})
		return
	}

	if _, err := approvals.Compile(h.DB, id, body.Preset, body.Overrides); err != nil {
		writeErr(w, err)
		return
	}

	// A preset change alters admission (locked-down blocks; unlocking admits) —
	// poke the dispatcher so a queued Todo task moves without waiting a poll.
	pokeDispatch()

	view, err := approvals.EffectivePolicy(h.DB, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, view, nil)
}

// presetProjectID parses {id} and confirms the project exists, writing the
// appropriate 4xx and returning ok=false otherwise.
func (h *Handler) presetProjectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid project id")
		return 0, false
	}
	var one int
	if err := h.DB.QueryRow(`SELECT 1 FROM projects WHERE id = ?`, id).Scan(&one); err != nil {
		writeClientErr(w, http.StatusNotFound, "project not found")
		return 0, false
	}
	return id, true
}
