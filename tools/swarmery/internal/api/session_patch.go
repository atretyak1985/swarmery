package api

// PATCH /api/sessions/{id} — partial session updates. Patchable fields:
//   - `outcome` ('success'|'fail'|'abandoned'|null to clear; migration 0014)
//   - `title`   (custom rename; null/"" clears the override → reverts to the
//                ingested ai-title; migration 0016)
// At least one known field must be present. The DELETE soft-hide contract
// (session_hide.go) is deliberately untouched — PATCH lives alongside it, with
// the same D4 requireLocalOrigin hardening.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// validOutcomes mirrors the CHECK constraint in migration 0014.
var validOutcomes = map[string]bool{"success": true, "fail": true, "abandoned": true}

// sessionTitleLimit caps a custom title, matching the ingest titleLimit so a
// rename can never be longer than an ingested one.
const sessionTitleLimit = 120

// patchSession handles PATCH /api/sessions/{id}.
func (h *Handler) patchSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid session id"}`, http.StatusBadRequest)
		return
	}
	// map[string]*string distinguishes {"field": null} (clear) from an absent
	// key (skip) and rejects non-string values at decode time.
	var body map[string]*string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	sets := []string{}
	args := []any{}
	resp := map[string]any{"id": id}

	if outcome, ok := body["outcome"]; ok {
		if outcome != nil && !validOutcomes[*outcome] {
			http.Error(w, `{"error":"outcome must be success|fail|abandoned|null"}`, http.StatusBadRequest)
			return
		}
		sets = append(sets, "outcome = ?")
		args = append(args, outcome)
		resp["outcome"] = outcome
	}

	if title, ok := body["title"]; ok {
		// null or blank clears the override (revert to the ingested title);
		// otherwise store the trimmed, length-capped custom title.
		var val *string
		if title != nil {
			trimmed := strings.TrimSpace(*title)
			if len(trimmed) > sessionTitleLimit {
				trimmed = trimmed[:sessionTitleLimit]
			}
			if trimmed != "" {
				val = &trimmed
			}
		}
		sets = append(sets, "custom_title = ?")
		args = append(args, val)
		resp["title"] = val
	}

	if len(sets) == 0 {
		http.Error(w, `{"error":"no patchable field (outcome, title) provided"}`, http.StatusBadRequest)
		return
	}

	args = append(args, id)
	res, err := h.DB.Exec(`UPDATE sessions SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	// Push a session_updated so the list/detail/overview refresh live (a
	// rename changes what every card shows).
	publishSessionUpdated(id)
	writeJSON(w, resp, nil)
}
