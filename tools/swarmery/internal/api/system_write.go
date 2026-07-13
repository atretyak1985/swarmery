package api

// phase 4: system, Stage 2 (step-09) — the write surface for agents & skills.
// Every write goes through internal/sysedit (the ONLY code path allowed to
// modify config files — kill-switch, root fence, provenance, 409 conflict,
// backup, atomic write, forced rescan). This file only adds the HTTP shape:
// pre-write validation (parse → name-uniqueness → lint), the sysedit error
// mapping, and rollback-as-ordinary-write.
//
// Create/delete/restore live in system_create.go (step-11); hooks/
// settings.json writes are step-10 (hookcfg).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// sysEditor is attached once at daemon startup (nil → write endpoints 503) —
// same package-variable pattern as AttachApprovals/AttachOverlaysDir.
var sysEditor *sysedit.Editor

// AttachSysEditor wires the sysedit write pipeline into the system write
// endpoints.
func AttachSysEditor(e *sysedit.Editor) { sysEditor = e }

// maxSystemWriteBody bounds PUT/rollback bodies — component files are small
// markdown (mirrors maxHookBody).
const maxSystemWriteBody = 4 << 20

// systemWriteRequest is the PUT /api/system/{agents|skills}/{id} body.
type systemWriteRequest struct {
	Content    string `json:"content"`   // raw markdown, frontmatter included
	BaseHash   string `json:"base_hash"` // sha256 of the content the edit is based on
	ChangeNote string `json:"change_note"`
}

// systemRollbackRequest is the POST .../{id}/rollback body. base_hash guards
// the same race as PUT: the file may have changed after the preview opened.
type systemRollbackRequest struct {
	VersionID int64  `json:"version_id"`
	BaseHash  string `json:"base_hash"`
}

// systemWriteResponse is the success body of both PUT and rollback.
type systemWriteResponse struct {
	VersionID int64                    `json:"version_id"`
	Lint      []sysscan.ContentFinding `json:"lint"` // warnings only — they never block
}

// systemConflictDTO is the 409 body — enough for the UI to re-diff and retry.
type systemConflictDTO struct {
	Error    string `json:"error"`
	DiskHash string `json:"disk_hash"`
	BaseHash string `json:"base_hash"`
	Diff     string `json:"diff"` // base→disk unified diff, redacted
}

// ---- PUT /api/system/agents/{id} and /api/system/skills/{id} --------------

func (h *Handler) putSystemAgent(w http.ResponseWriter, r *http.Request) {
	h.putSystemItem(w, r, agentKind)
}

func (h *Handler) putSystemSkill(w http.ResponseWriter, r *http.Request) {
	h.putSystemItem(w, r, skillKind)
}

func (h *Handler) putSystemItem(w http.ResponseWriter, r *http.Request, k systemKind) {
	if sysEditor == nil {
		http.Error(w, `{"error":"system editor unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	var req systemWriteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSystemWriteBody)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if req.Content == "" || req.BaseHash == "" {
		http.Error(w, `{"error":"content and base_hash are required"}`, http.StatusBadRequest)
		return
	}

	// Validation order (step-09 contract): parse → name-uniqueness → lint.
	// Parse failure blocks (422); lint findings never do — they ride along
	// in the success response.
	name, findings, err := sysscan.LintContent(k.kind, []byte(req.Content))
	if err != nil {
		writeJSONStatus(w, http.StatusUnprocessableEntity,
			map[string]string{"error": "frontmatter parse error: " + err.Error()})
		return
	}
	if ok := h.checkSystemNameFree(w, k, id, name); !ok {
		return
	}

	vid, ok := h.systemWrite(w, k, id, []byte(req.Content), req.BaseHash, req.ChangeNote)
	if !ok {
		return
	}
	if findings == nil {
		findings = []sysscan.ContentFinding{}
	}
	writeJSON(w, systemWriteResponse{VersionID: vid, Lint: findings}, nil)
}

// ---- POST .../{id}/rollback ------------------------------------------------

func (h *Handler) rollbackSystemAgent(w http.ResponseWriter, r *http.Request) {
	h.rollbackSystemItem(w, r, agentKind)
}

func (h *Handler) rollbackSystemSkill(w http.ResponseWriter, r *http.Request) {
	h.rollbackSystemItem(w, r, skillKind)
}

// rollbackSystemItem restores an old version's content through the SAME
// WriteFile path as PUT — an ordinary append-style write, history is never
// rewritten. Note the *_versions UNIQUE(fk, content_hash) content-addressing:
// restoring byte-identical old content re-points current_version_id at the
// EXISTING version row instead of minting a duplicate — nothing is destroyed
// either way.
func (h *Handler) rollbackSystemItem(w http.ResponseWriter, r *http.Request, k systemKind) {
	if sysEditor == nil {
		http.Error(w, `{"error":"system editor unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	var req systemRollbackRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSystemWriteBody)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if req.VersionID <= 0 || req.BaseHash == "" {
		http.Error(w, `{"error":"version_id and base_hash are required"}`, http.StatusBadRequest)
		return
	}

	// The restored content comes from THIS item's history only (foreign
	// version ids are 404, same fence as the read endpoints).
	var content string
	err := h.DB.QueryRow(`SELECT content FROM `+k.verTable+` WHERE id = ? AND `+k.fkCol+` = ?`,
		req.VersionID, id).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"version not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	// Old content is accepted as-is (it already lived in the registry; the
	// scanner tolerates parse errors), but the name-uniqueness fence still
	// applies: the name may have been claimed by another item since, and a
	// collision would churn the scanner's (name, scope, project) upsert.
	// Lint rides along best-effort — unparseable history lints as empty.
	name, findings, lerr := sysscan.LintContent(k.kind, []byte(content))
	if lerr == nil {
		if ok := h.checkSystemNameFree(w, k, id, name); !ok {
			return
		}
	}

	note := fmt.Sprintf("rollback to v%d", req.VersionID)
	vid, ok := h.systemWrite(w, k, id, []byte(content), req.BaseHash, note)
	if !ok {
		return
	}
	if findings == nil {
		findings = []sysscan.ContentFinding{}
	}
	writeJSON(w, systemWriteResponse{VersionID: vid, Lint: findings}, nil)
}

// ---- shared write plumbing ---------------------------------------------------

// systemWrite runs one guarded write: sysedit pipeline → error mapping →
// change_note stamping. Returns (versionID, true) on success; on failure the
// response is already written.
func (h *Handler) systemWrite(w http.ResponseWriter, k systemKind, id int64,
	content []byte, baseHash, changeNote string) (int64, bool) {

	// change_note stamping must not clobber history: the rescan content-
	// addresses versions, so a write can re-point to an EXISTING row (e.g.
	// rollback). Only a row minted by THIS write (id above the pre-write
	// high-water mark) takes the note.
	var maxBefore int64
	if err := h.DB.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM ` + k.verTable).Scan(&maxBefore); err != nil {
		writeErr(w, err)
		return 0, false
	}

	vid, err := sysEditor.WriteFile(
		sysedit.ItemRef{Kind: k.kind, ID: id}, content, baseHash)
	if err != nil {
		writeSyseditError(w, k, err)
		return 0, false
	}
	if changeNote != "" && vid > maxBefore {
		if _, err := h.DB.Exec(`UPDATE `+k.verTable+` SET change_note = ? WHERE id = ?`,
			changeNote, vid); err != nil {
			writeErr(w, err)
			return 0, false
		}
	}
	return vid, true
}

// checkSystemNameFree enforces frontmatter-name uniqueness within the item's
// own (scope, project) tier — the scanner upserts by (name, scope, project),
// so a duplicate would silently merge two files into one row. An empty name
// is skipped (the scanner then falls back to the filename stem). Writes the
// error response itself; returns false when the caller must stop.
func (h *Handler) checkSystemNameFree(w http.ResponseWriter, k systemKind, id int64, name string) bool {
	if name == "" {
		return true
	}
	var scope string
	var projectID sql.NullInt64
	err := h.DB.QueryRow(`SELECT scope, project_id FROM `+k.table+` WHERE id = ? AND deleted = 0`,
		id).Scan(&scope, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"`+k.kind+` not found"}`, http.StatusNotFound)
		return false
	}
	if err != nil {
		writeErr(w, err)
		return false
	}
	var clash int64
	// `project_id IS ?` — the registry's NULL-tolerant match (registry.go).
	err = h.DB.QueryRow(`SELECT COUNT(*) FROM `+k.table+
		` WHERE deleted = 0 AND id <> ? AND name = ? AND scope = ? AND project_id IS ?`,
		id, name, scope, projectID).Scan(&clash)
	if err != nil {
		writeErr(w, err)
		return false
	}
	if clash > 0 {
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{
			"error": fmt.Sprintf("%s name %q already exists in the same scope", k.kind, name)})
		return false
	}
	return true
}

// writeSyseditError maps the sysedit typed errors onto the step-09 HTTP
// contract: ErrConflict → 409 {disk_hash, base_hash, diff}, ErrPluginManaged
// and ErrReadOnly → 403, ErrPathOutsideRoots → 400, ErrNotFound → 404.
// Step-11 adds the create/restore tier: ErrExists and ErrNotDeleted → 409.
func writeSyseditError(w http.ResponseWriter, k systemKind, err error) {
	var ce *sysedit.ConflictError
	switch {
	case errors.Is(err, sysedit.ErrExists):
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "a file already exists at the target path but is not in the registry — run a rescan and retry"})
	case errors.Is(err, sysedit.ErrNotDeleted):
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": k.kind + " is not deleted — nothing to restore"})
	case errors.As(err, &ce):
		// Redact BEFORE serving — the disk drift may embed a secret.
		writeJSONStatus(w, http.StatusConflict, systemConflictDTO{
			Error:    "content changed on disk since base_hash",
			DiskHash: ce.DiskHash,
			BaseHash: ce.BaseHash,
			Diff:     redact(ce.Diff),
		})
	case errors.Is(err, sysedit.ErrPluginManaged):
		http.Error(w, `{"error":"item is plugin-managed — edit it in the plugin's repo"}`,
			http.StatusForbidden)
	case errors.Is(err, sysedit.ErrReadOnly):
		http.Error(w, `{"error":"system editor is in readonly mode (`+sysedit.EnvReadOnly+`)"}`,
			http.StatusForbidden)
	case errors.Is(err, sysedit.ErrPathOutsideRoots):
		http.Error(w, `{"error":"file path outside known config roots"}`, http.StatusBadRequest)
	case errors.Is(err, sysedit.ErrNotFound):
		http.Error(w, `{"error":"`+k.kind+` not found"}`, http.StatusNotFound)
	default:
		writeErr(w, err)
	}
}

// writeJSONStatus writes one JSON body with an explicit status code.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("warn: encode response: %v", err) // headers already sent
	}
}
