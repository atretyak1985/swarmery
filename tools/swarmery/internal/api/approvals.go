// Phase 2 — approvals + hooks endpoints (frozen contract:
// docs/hooks-protocol.md, web/src/api/types.ts PermissionRequest).
//
// These are the API's first write endpoints. The approvals service is
// attached as a package variable (same pattern as AttachBus) so the parallel
// branch's Handler struct stays conflict-free.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
)

// approvalsSvc is attached once at daemon startup (nil → hooks endpoints 503).
var approvalsSvc *approvals.Service

// AttachApprovals wires the approvals service into the hooks/approvals endpoints.
func AttachApprovals(s *approvals.Service) { approvalsSvc = s }

// maxHookBody bounds the hook stdin pass-through (tool_input can embed file
// contents, but never tens of megabytes).
const maxHookBody = 4 << 20

// longPollGrace is the belt-and-braces slack the long-poll handler waits
// past expires_at before expiring the row itself (the sweeper normally
// beats it).
const longPollGrace = 3 * time.Second

// permissionRequestDTO mirrors PermissionRequest in web/src/api/types.ts —
// field names are FROZEN.
type permissionRequestDTO struct {
	ID          int64   `json:"id"`
	SessionID   int64   `json:"sessionId"`
	ToolName    string  `json:"toolName"`
	RequestJSON string  `json:"requestJson"`
	Status      string  `json:"status"`
	RequestedAt string  `json:"requestedAt"`
	ResolvedAt  *string `json:"resolvedAt"`
	ResolvedVia *string `json:"resolvedVia"`
	Reason      *string `json:"reason"`
	ExpiresAt   string  `json:"expiresAt"`
}

const permissionRequestSelect = `
	SELECT id, session_id, tool_name, request_json, status,
	       requested_at, resolved_at, resolved_via, reason, COALESCE(expires_at, '')
	FROM permission_requests`

func scanPermissionRequest(scan func(...any) error, p *permissionRequestDTO) error {
	return scan(&p.ID, &p.SessionID, &p.ToolName, &p.RequestJSON, &p.Status,
		&p.RequestedAt, &p.ResolvedAt, &p.ResolvedVia, &p.Reason, &p.ExpiresAt)
}

func (h *Handler) permissionRequestByID(id int64) (*permissionRequestDTO, error) {
	var p permissionRequestDTO
	err := scanPermissionRequest(
		h.DB.QueryRow(permissionRequestSelect+` WHERE id = ?`, id).Scan, &p)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ── D4: origin check middleware ──────────────────────────────────────────────

// requireLocalOrigin rejects state-changing requests that carry a foreign
// browser Origin (DNS-rebinding / CSRF hardening, D4). Requests without an
// Origin header (the shim, curl) pass — localhost trust is the v1 model.
func requireLocalOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !isLocalOrigin(o) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "cross-origin request rejected"})
			return
		}
		next(w, r)
	}
}

func isLocalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// ── POST /api/hooks/permission-request (long-poll) ───────────────────────────

// hookDecisionResponse is the 200 body the shim maps onto hookSpecificOutput.
type hookDecisionResponse struct {
	Decision string `json:"decision"` // allow | deny
	Message  string `json:"message,omitempty"`
}

func (h *Handler) hookPermissionRequest(w http.ResponseWriter, r *http.Request) {
	if approvalsSvc == nil {
		http.Error(w, `{"error":"approvals unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	approvalsSvc.Heartbeat()

	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return
	}
	in, err := approvals.ParseHookStdin(body)
	if err != nil {
		http.Error(w, `{"error":"malformed hook payload"}`, http.StatusBadRequest)
		return
	}

	id, ch, _, err := approvalsSvc.Open(in)
	switch {
	case errors.Is(err, approvals.ErrTooManyPending):
		http.Error(w, `{"error":"too many pending requests for session"}`, http.StatusTooManyRequests)
		return
	case errors.Is(err, approvals.ErrBadRequest):
		http.Error(w, `{"error":"malformed hook payload"}`, http.StatusBadRequest)
		return
	case err != nil:
		writeErr(w, err)
		return
	}

	// Long-poll: decision wakes us; the shim owns the 120 s wall clock, the
	// sweeper owns expiry — the local timer is only a safety net.
	timer := time.NewTimer(approvalsSvc.Timeout() + longPollGrace)
	defer timer.Stop()
	var d approvals.Decision
	select {
	case d = <-ch:
	case <-r.Context().Done():
		// Client disconnected mid-poll (terminal Esc/Ctrl-C killed the shim).
		approvalsSvc.Detach(id, ch)
		return
	case <-timer.C:
		// Sweeper missed its slot — expire ourselves (idempotent), then take
		// whatever decision may have raced in.
		if err := approvalsSvc.Expire(id); err != nil {
			writeErr(w, err)
			return
		}
		select {
		case d = <-ch:
		default:
			d = approvals.Decision{Status: approvals.StatusExpired}
		}
	}

	switch d.Status {
	case approvals.StatusApproved:
		writeJSON(w, hookDecisionResponse{Decision: "allow"}, nil)
	case approvals.StatusDenied:
		writeJSON(w, hookDecisionResponse{Decision: "deny", Message: d.Reason}, nil)
	default:
		// expired / resolved_elsewhere → no decision → 204, shim fails open.
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── POST /api/hooks/stop ─────────────────────────────────────────────────────

// hookStop is the heartbeat + phase-2.5 readiness channel: always 202,
// payload unused in phase 2 beyond liveness.
func (h *Handler) hookStop(w http.ResponseWriter, r *http.Request) {
	if approvalsSvc != nil {
		approvalsSvc.Heartbeat()
	}
	io.Copy(io.Discard, io.LimitReader(r.Body, maxHookBody))
	w.WriteHeader(http.StatusAccepted)
}

// ── POST /api/approvals/{id} (dashboard decision) ────────────────────────────

func (h *Handler) resolveApproval(w http.ResponseWriter, r *http.Request) {
	if approvalsSvc == nil {
		http.Error(w, `{"error":"approvals unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid request id"}`, http.StatusBadRequest)
		return
	}
	var body struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	var status string
	switch body.Action {
	case "approve":
		status = approvals.StatusApproved
	case "deny":
		status = approvals.StatusDenied
	default:
		http.Error(w, `{"error":"action must be 'approve' or 'deny'"}`, http.StatusBadRequest)
		return
	}

	err = approvalsSvc.Resolve(id, status, "dashboard", body.Reason)
	switch {
	case errors.Is(err, approvals.ErrNotFound):
		http.Error(w, `{"error":"permission request not found"}`, http.StatusNotFound)
		return
	case errors.Is(err, approvals.ErrAlreadyResolved):
		http.Error(w, `{"error":"permission request already resolved"}`, http.StatusConflict)
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	p, err := h.permissionRequestByID(id)
	writeJSON(w, p, err)
}

// ── GET /api/approvals?status=&limit= ────────────────────────────────────────

// listApprovals lists permission requests newest-first. status defaults to
// 'pending'; 'resolved' selects every terminal status; 'all' everything; a
// concrete status name filters exactly. limit is optional.
func (h *Handler) listApprovals(w http.ResponseWriter, r *http.Request) {
	query := permissionRequestSelect
	args := []any{}
	switch status := r.URL.Query().Get("status"); status {
	case "", "pending":
		query += ` WHERE status = 'pending'`
	case "resolved":
		query += ` WHERE status != 'pending'`
	case "all":
		// no filter
	default:
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY requested_at DESC, id DESC`
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			http.Error(w, `{"error":"invalid limit"}`, http.StatusBadRequest)
			return
		}
		query += ` LIMIT ?`
		args = append(args, n)
	}

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []permissionRequestDTO{}
	for rows.Next() {
		var p permissionRequestDTO
		if err := scanPermissionRequest(rows.Scan, &p); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, p)
	}
	writeJSON(w, out, rows.Err())
}
