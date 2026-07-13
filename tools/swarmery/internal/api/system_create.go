package api

// phase 4: system, Stage 2 (step-11) — agent creation from the canonical
// template + soft delete + restore. Creation is the only endpoint where a
// file path is COMPOSED (from scope + name) instead of resolved from the DB;
// sysedit.CreateFile re-fences it under the known config roots and commits
// with O_EXCL semantics — an orphan file on disk (present, unscanned) is a
// 409 "run a rescan", never a clobber. Deletes stay soft: sysedit.DeleteFile
// moves the file into config-backups and flags deleted=1; the name is NOT
// freed (UNIQUE(name, scope, project_id) still holds the row) — that is what
// the restore endpoint is for.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// maxSystemCreateBody bounds the create form body — it is a handful of
// short strings, far below the PUT content bound.
const maxSystemCreateBody = 1 << 20

// systemCreateAgentRequest is the POST /api/system/agents body.
type systemCreateAgentRequest struct {
	Name        string   `json:"name"`
	Scope       string   `json:"scope"`      // global | project
	ProjectID   *int64   `json:"project_id"` // required when scope=project
	Description string   `json:"description"`
	Model       string   `json:"model"`
	Tools       []string `json:"tools"`
	Boundaries  string   `json:"boundaries"`
}

// systemCreateResponse is the 201 body: the freshly scanned row identity
// plus the same ride-along lint as the PUT surface (warnings never block).
type systemCreateResponse struct {
	ID        int64                    `json:"id"`
	VersionID int64                    `json:"version_id"`
	Lint      []sysscan.ContentFinding `json:"lint"`
}

// kebabName is the create-form name contract: lowercase kebab-case, so the
// file stem, the frontmatter name, and the registry name always agree.
var kebabName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ---- POST /api/system/agents ------------------------------------------------

func (h *Handler) createSystemAgent(w http.ResponseWriter, r *http.Request) {
	if sysEditor == nil {
		http.Error(w, `{"error":"system editor unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	var req systemCreateAgentRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSystemCreateBody)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if !kebabName.MatchString(req.Name) {
		http.Error(w, `{"error":"name must be kebab-case (lowercase letters, digits, single dashes)"}`,
			http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Description) == "" {
		http.Error(w, `{"error":"description is required"}`, http.StatusBadRequest)
		return
	}
	// Frontmatter scalars are single-line by the template contract.
	for _, s := range append([]string{req.Description, req.Model}, req.Tools...) {
		if strings.ContainsAny(s, "\r\n") {
			http.Error(w, `{"error":"description, model and tools must be single-line"}`,
				http.StatusBadRequest)
			return
		}
	}

	// Scope → NULL-tolerant project binding + target directory.
	var projectID any // nil for global — matches the registry's `project_id IS ?`
	var dir string
	switch req.Scope {
	case "global":
		if req.ProjectID != nil {
			http.Error(w, `{"error":"project_id is only valid with scope=project"}`, http.StatusBadRequest)
			return
		}
		dir = filepath.Join(sysEditor.ClaudeDir(), "agents")
	case "project":
		if req.ProjectID == nil {
			http.Error(w, `{"error":"project_id is required with scope=project"}`, http.StatusBadRequest)
			return
		}
		var projPath string
		err := h.DB.QueryRow(`SELECT path FROM projects WHERE id = ? AND archived = 0`,
			*req.ProjectID).Scan(&projPath)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !filepath.IsAbs(projPath)) {
			// non-absolute = the '(unknown)' placeholder row — not a real target
			http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		dir = filepath.Join(projPath, ".claude", "agents")
		projectID = *req.ProjectID
	default:
		http.Error(w, `{"error":"scope must be global or project"}`, http.StatusBadRequest)
		return
	}

	// Name-uniqueness fence INCLUDING soft-deleted rows: a delete never frees
	// the name (the row keeps UNIQUE(name, scope, project_id) — restore flow).
	var existingID int64
	var existingDeleted int
	err := h.DB.QueryRow(`SELECT id, deleted FROM agents WHERE name = ? AND scope = ? AND project_id IS ?`,
		req.Name, req.Scope, projectID).Scan(&existingID, &existingDeleted)
	switch {
	case err == nil && existingDeleted == 1:
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("agent %q exists soft-deleted in this scope — restore it instead of creating a new one (POST /api/system/agents/%d/restore)",
				req.Name, existingID),
			"restore_id": existingID,
		})
		return
	case err == nil:
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("agent %q already exists in this scope", req.Name),
			"id":    existingID,
		})
		return
	case !errors.Is(err, sql.ErrNoRows):
		writeErr(w, err)
		return
	}

	content, err := sysedit.RenderAgentMD(sysedit.AgentTemplate{
		Name:        req.Name,
		Description: req.Description,
		Model:       req.Model,
		Tools:       req.Tools,
		Boundaries:  req.Boundaries,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	// Same gate as PUT (step-09): the candidate must parse; lint rides along.
	_, findings, err := sysscan.LintContent(sysscan.KindAgent, content)
	if err != nil {
		writeJSONStatus(w, http.StatusUnprocessableEntity,
			map[string]string{"error": "generated frontmatter does not parse: " + err.Error()})
		return
	}

	if err := sysEditor.CreateFile(filepath.Join(dir, req.Name+".md"), content); err != nil {
		writeSyseditError(w, agentKind, err)
		return
	}

	// The forced rescan inside CreateFile just minted the row + first version.
	var id int64
	var vid sql.NullInt64
	if err := h.DB.QueryRow(
		`SELECT id, current_version_id FROM agents WHERE name = ? AND scope = ? AND project_id IS ? AND deleted = 0`,
		req.Name, req.Scope, projectID).Scan(&id, &vid); err != nil {
		writeErr(w, fmt.Errorf("file created but the rescan did not register it: %w", err))
		return
	}
	if findings == nil {
		findings = []sysscan.ContentFinding{}
	}
	writeJSONStatus(w, http.StatusCreated, systemCreateResponse{ID: id, VersionID: vid.Int64, Lint: findings})
}

// ---- DELETE /api/system/agents/{id} ------------------------------------------

// deleteSystemAgent is soft-only: sysedit.DeleteFile moves the file into
// ~/.swarmery/config-backups/<ts>/… and flags deleted=1 — content is never
// destroyed. origin=plugin → 403 via the shared sysedit error mapping.
func (h *Handler) deleteSystemAgent(w http.ResponseWriter, r *http.Request) {
	if sysEditor == nil {
		http.Error(w, `{"error":"system editor unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	if err := sysEditor.DeleteFile(sysedit.ItemRef{Kind: sysscan.KindAgent, ID: id}); err != nil {
		writeSyseditError(w, agentKind, err)
		return
	}
	writeJSON(w, map[string]bool{"deleted": true}, nil)
}

// ---- POST /api/system/agents/{id}/restore --------------------------------------

// restoreSystemAgent re-creates the latest stored version at the row's
// file_path (exclusive — an orphan file at that path is 409 "rescan") and
// the forced rescan flips deleted back to 0.
func (h *Handler) restoreSystemAgent(w http.ResponseWriter, r *http.Request) {
	if sysEditor == nil {
		http.Error(w, `{"error":"system editor unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	if err := sysEditor.RestoreFile(sysedit.ItemRef{Kind: sysscan.KindAgent, ID: id}); err != nil {
		writeSyseditError(w, agentKind, err)
		return
	}
	var vid sql.NullInt64
	if err := h.DB.QueryRow(`SELECT current_version_id FROM agents WHERE id = ? AND deleted = 0`,
		id).Scan(&vid); err != nil {
		writeErr(w, fmt.Errorf("file restored but the rescan did not revive the row: %w", err))
		return
	}
	writeJSON(w, map[string]int64{"id": id, "version_id": vid.Int64}, nil)
}
