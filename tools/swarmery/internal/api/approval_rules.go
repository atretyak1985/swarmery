// Auto-approve rules API (control-plane v2 — notifications & rules): CRUD
// over approval_rules. Rule EVALUATION lives in internal/approvals (Open);
// this file only manages the rows. tool_pattern semantics + the
// deny-by-default matcher: internal/approvals/rules.go.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
)

// ruleTSFormat matches the millisecond-Z style of the other timestamps.
const ruleTSFormat = "2006-01-02T15:04:05.000Z"

// approvalRuleDTO mirrors ApprovalRule in web/src/api/types.ts.
type approvalRuleDTO struct {
	ID          int64   `json:"id"`
	ProjectID   *int64  `json:"projectId"`
	ProjectSlug *string `json:"projectSlug"`
	ToolPattern string  `json:"toolPattern"`
	Action      string  `json:"action"`
	Enabled     bool    `json:"enabled"`
	Note        *string `json:"note"`
	CreatedAt   string  `json:"createdAt"`
	// Source distinguishes hand-written rules ('manual') from ones a permission
	// preset compiled ('preset', fusion phase 11). Managed rules are read-only in
	// this manual CRUD surface — the preset owns their lifecycle.
	Source string `json:"source"`
}

const approvalRuleSelect = `
	SELECT r.id, r.project_id, p.slug, r.tool_pattern, r.action, r.enabled, r.note, r.created_at, r.source
	FROM approval_rules r LEFT JOIN projects p ON p.id = r.project_id`

func scanApprovalRule(scan func(...any) error, d *approvalRuleDTO) error {
	var enabled int64
	if err := scan(&d.ID, &d.ProjectID, &d.ProjectSlug, &d.ToolPattern, &d.Action,
		&enabled, &d.Note, &d.CreatedAt, &d.Source); err != nil {
		return err
	}
	d.Enabled = enabled != 0
	return nil
}

func (h *Handler) approvalRuleByID(id int64) (*approvalRuleDTO, error) {
	var d approvalRuleDTO
	err := scanApprovalRule(
		h.DB.QueryRow(approvalRuleSelect+` WHERE r.id = ?`, id).Scan, &d)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GET /api/approval-rules — every rule, newest first.
func (h *Handler) listApprovalRules(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(approvalRuleSelect + ` ORDER BY r.id DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []approvalRuleDTO{}
	for rows.Next() {
		var d approvalRuleDTO
		if err := scanApprovalRule(rows.Scan, &d); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, d)
	}
	writeJSON(w, out, rows.Err())
}

// POST /api/approval-rules {projectId?, toolPattern, note?} → 201 DTO.
func (h *Handler) createApprovalRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID   *int64 `json:"projectId"`
		ToolPattern string `json:"toolPattern"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	pattern := strings.TrimSpace(body.ToolPattern)
	if _, err := approvals.ParseRulePattern(pattern); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if body.ProjectID != nil {
		var one int
		err := h.DB.QueryRow(`SELECT 1 FROM projects WHERE id = ?`, *body.ProjectID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, `{"error":"unknown project id"}`, http.StatusBadRequest)
			return
		}
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	// source='manual' is explicit (also the column default): only Compile writes
	// source='preset', and only the manual surface writes here.
	res, err := h.DB.Exec(
		`INSERT INTO approval_rules (project_id, tool_pattern, note, source, created_at)
		 VALUES (?, ?, ?, 'manual', ?)`,
		body.ProjectID, pattern, nullableStr(body.Note),
		time.Now().UTC().Format(ruleTSFormat))
	if err != nil {
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	d, err := h.approvalRuleByID(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(d)
}

// ruleSourceOf reads a rule's source. Returns ("", nil) when the rule is
// absent (callers map that to 404).
func (h *Handler) ruleSourceOf(id int64) (string, error) {
	var source string
	err := h.DB.QueryRow(`SELECT source FROM approval_rules WHERE id = ?`, id).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return source, err
}

// rejectManagedRule writes a 409 when a rule is preset-managed (read-only in
// this manual surface). Returns true when it handled the response.
func rejectManagedRule(w http.ResponseWriter, source string) bool {
	if source == "preset" {
		writeClientErr(w, http.StatusConflict,
			"this rule is managed by the project's permission preset — change it via the preset, not here")
		return true
	}
	return false
}

// PATCH /api/approval-rules/{id} {enabled} → 200 DTO.
func (h *Handler) patchApprovalRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid rule id"}`, http.StatusBadRequest)
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Enabled == nil {
		http.Error(w, `{"error":"body must be {\"enabled\": true|false}"}`, http.StatusBadRequest)
		return
	}
	// Managed (preset) rules are read-only here — the preset owns them.
	source, err := h.ruleSourceOf(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if source == "" {
		http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound)
		return
	}
	if rejectManagedRule(w, source) {
		return
	}
	res, err := h.DB.Exec(`UPDATE approval_rules SET enabled = ? WHERE id = ?`,
		boolToInt(*body.Enabled), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound)
		return
	}
	d, err := h.approvalRuleByID(id)
	writeJSON(w, d, err)
}

// DELETE /api/approval-rules/{id} → 204.
func (h *Handler) deleteApprovalRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid rule id"}`, http.StatusBadRequest)
		return
	}
	// Managed (preset) rules are read-only here — deleting one would just be
	// clobbered by the next recompile; direct the user to the preset instead.
	source, err := h.ruleSourceOf(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if source == "" {
		http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound)
		return
	}
	if rejectManagedRule(w, source) {
		return
	}
	res, err := h.DB.Exec(`DELETE FROM approval_rules WHERE id = ?`, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, `{"error":"rule not found"}`, http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func nullableStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
