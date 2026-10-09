package api

// PUT /api/projects/{id}/vcs/provider {"provider":"github"|"gitlab"} — the
// operator's answer to "which service hosts this repo?", asked once by the UI
// when GET /api/projects/{id}/vcs reports askProvider (an origin whose host
// Detect could not classify: not github.com / gitlab.com, no ssh alias to
// one, and no GitLab /api/v4/version answer).
//
// The answer is stored as `swarmery.vcs.provider` in the project's
// .claude/settings.local.json (repoprovider.PersistProviderAnswer — a
// read-modify-write that keeps every other key), which LoadConfig reads back as
// the project's explicit provider; the project's cached vcs answer is dropped
// so the next GET re-detects with it. 204 on success; 400 for any other value
// or a malformed body; 404 unknown project; 409 when the project has no path
// or its settings.local.json cannot be rewritten (not a JSON object, or not
// readable/writable).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// maxVcsProviderBodyBytes bounds the PUT body: one short JSON object.
const maxVcsProviderBodyBytes = 1 << 10

// putProjectVcsProvider handles PUT /api/projects/{id}/vcs/provider.
func (h *Handler) putProjectVcsProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxVcsProviderBodyBytes)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	kind := repoprovider.Kind(strings.TrimSpace(req.Provider))
	if kind != repoprovider.KindGitHub && kind != repoprovider.KindGitLab {
		writeClientErr(w, http.StatusBadRequest, `provider must be "github" or "gitlab"`)
		return
	}
	id, path, ok := h.projectPathByID(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(path) == "" {
		writeConflict(w, codeNoProjectPath, "project has no known path")
		return
	}
	if err := repoprovider.PersistProviderAnswer(path, kind); err != nil {
		if errors.Is(err, repoprovider.ErrUnknownProvider) {
			writeClientErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// The kind was validated above, so what is left is the project's own
		// .claude/settings.local.json — not a JSON object, or not readable /
		// writable in the operator's checkout. The state of that file is the
		// operator's to fix, and the message names it.
		writeClientErr(w, http.StatusConflict, err.Error())
		return
	}
	InvalidateVcsCache(id)
	w.WriteHeader(http.StatusNoContent)
}
