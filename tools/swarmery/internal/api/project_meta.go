package api

// Project meta writes (migration 0015): PATCH /api/projects/{id} updates the
// dashboard-only fields — pinned (bool) and tags (array of strings). Both
// fields are optional; an omitted field keeps its value, an empty patch is a
// 400. Tags are normalized (trim, lowercase, first-win dedupe) with advisory
// caps so a hostile PATCH cannot bloat the row. Archived projects stay
// editable (the meta survives a restore). Same D4 origin hardening as the
// other write endpoints. Replies with the updated meta so the client can
// reconcile without a refetch.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const (
	maxProjectTags   = 12
	maxProjectTagLen = 32
)

type projectMetaPatch struct {
	Pinned *bool     `json:"pinned"`
	Tags   *[]string `json:"tags"`
}

type projectMetaDTO struct {
	Pinned bool     `json:"pinned"`
	Tags   []string `json:"tags"`
}

// normalizeTags trims, lowercases, de-duplicates (first win) and validates.
// The second return is a non-empty client error message on rejection.
func normalizeTags(in []string) ([]string, string) {
	if len(in) > maxProjectTags {
		return nil, "too many tags (max 12)"
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" {
			return nil, "empty tag"
		}
		if len(tag) > maxProjectTagLen {
			return nil, "tag too long (max 32 chars)"
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out, ""
}

// patchProject handles PATCH /api/projects/{id} — body {pinned?, tags?}.
func (h *Handler) patchProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var patch projectMetaPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if patch.Pinned == nil && patch.Tags == nil {
		writeClientErr(w, http.StatusBadRequest, "nothing to update — provide pinned and/or tags")
		return
	}

	sets := []string{}
	args := []any{}
	if patch.Pinned != nil {
		flag := 0
		if *patch.Pinned {
			flag = 1
		}
		sets = append(sets, "pinned = ?")
		args = append(args, flag)
	}
	if patch.Tags != nil {
		tags, msg := normalizeTags(*patch.Tags)
		if msg != "" {
			writeClientErr(w, http.StatusBadRequest, msg)
			return
		}
		encoded, err := json.Marshal(tags)
		if err != nil {
			writeErr(w, err)
			return
		}
		sets = append(sets, "tags = ?")
		args = append(args, string(encoded))
	}
	args = append(args, id)

	res, err := h.DB.Exec(`UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeClientErr(w, http.StatusNotFound, "project not found")
		return
	}

	// Echo the stored state back (not the patch) so the reply is the truth.
	out := projectMetaDTO{Tags: []string{}}
	var pinned int
	var tagsRaw string
	if err := h.DB.QueryRow(`SELECT pinned, tags FROM projects WHERE id = ?`, id).
		Scan(&pinned, &tagsRaw); err != nil {
		writeErr(w, err)
		return
	}
	out.Pinned = pinned != 0
	if err := json.Unmarshal([]byte(tagsRaw), &out.Tags); err != nil || out.Tags == nil {
		out.Tags = []string{}
	}
	writeJSON(w, out, nil)
}
