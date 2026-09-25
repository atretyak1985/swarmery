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
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// trustedOrigins is the OPT-IN allow-list of extra browser origins that pass
// requireLocalOrigin, parsed from SWARMERY_TRUSTED_ORIGINS. Empty by default,
// and deliberately so: the daemon controls no name beyond the loopback ones.
// A bare friendly hostname like "swarmery" resolves wherever the resolver says
// — a DNS search domain can expand it to swarmery.<corp>, and a page served
// from THAT host would then pass the CSRF fence. So an alias is trusted only
// when the operator names it, and it is matched as a full origin
// (scheme://host[:port]): trusting http://swarmery:7777 does not also trust
// https://swarmery:9999.
var trustedOrigins map[string]bool

// AttachTrustedOrigins installs the opt-in allow-list (startup, before serve).
// Entries that are not http(s) origins are dropped rather than half-matched.
func AttachTrustedOrigins(origins []string) {
	m := make(map[string]bool, len(origins))
	for _, o := range origins {
		if n, ok := normalizeOrigin(o); ok {
			m[n] = true
		}
	}
	trustedOrigins = m
}

// normalizeOrigin reduces an origin to lowercase scheme://host[:port] with the
// scheme's default port dropped (browsers omit it), or reports false when the
// string is not an http(s) origin.
func normalizeOrigin(origin string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, true
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
	n, ok := normalizeOrigin(origin)
	return ok && trustedOrigins[n]
}

// ── POST /api/hooks/permission-request (long-poll) ───────────────────────────

// hookDecisionResponse is the 200 body the shim maps onto hookSpecificOutput.
// updatedInput (additive, hooks-protocol amendment 1) accompanies allow only:
// the {questions, answers} object of an AskUserQuestion answered from the
// dashboard, forwarded verbatim by the shim (spike E12).
type hookDecisionResponse struct {
	Decision     string          `json:"decision"` // allow | deny
	Message      string          `json:"message,omitempty"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
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
	case errors.Is(err, approvals.ErrExcludedProject):
		// Excluded cwd: still served, but nothing is persisted — no decision,
		// the shim fails open to the native dialog (same contract as expiry).
		w.WriteHeader(http.StatusNoContent)
		return
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
		resp := hookDecisionResponse{Decision: "allow"}
		if len(d.UpdatedInput) > 0 {
			if approvalsSvc.AnswerDelivery() == approvals.DeliveryDenyMessage {
				// Fallback wire form (--answer-delivery=deny-message): the row
				// stays approved — the human genuinely answered — only the
				// delivery flips; deny messages reach Claude verbatim as the
				// tool result (E3), so the agent continues with the answers.
				resp = hookDecisionResponse{
					Decision: "deny",
					Message:  "User answered via dashboard: " + d.Reason,
				}
			} else {
				resp.UpdatedInput = d.UpdatedInput
			}
		}
		writeJSON(w, resp, nil)
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
		Action  string                     `json:"action"`
		Reason  string                     `json:"reason"`
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	switch body.Action {
	case "approve":
		err = approvalsSvc.Resolve(id, approvals.StatusApproved, "dashboard", body.Reason)
	case "deny":
		err = approvalsSvc.Resolve(id, approvals.StatusDenied, "dashboard", body.Reason)
	case "answer":
		// AskUserQuestion answers (hooks-protocol amendment 1, spike E12).
		err = approvalsSvc.Answer(id, body.Answers)
	case "terminal":
		// "Answer in terminal →": deliberately NO decision — a plain allow
		// would resolve AskUserQuestion with empty answers (E12d). The row
		// resolves as resolved_elsewhere so the long-poll answers 204, the
		// shim fails open, and the native selector renders (E12e).
		err = approvalsSvc.Resolve(id, approvals.StatusResolvedElsewhere, "dashboard", "handed off to terminal")
	default:
		http.Error(w, `{"error":"action must be 'approve', 'deny', 'answer' or 'terminal'"}`, http.StatusBadRequest)
		return
	}

	switch {
	case errors.Is(err, approvals.ErrNotFound):
		http.Error(w, `{"error":"permission request not found"}`, http.StatusNotFound)
		return
	case errors.Is(err, approvals.ErrAlreadyResolved):
		http.Error(w, `{"error":"permission request already resolved"}`, http.StatusConflict)
		return
	case errors.Is(err, approvals.ErrInvalidAnswer):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
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
// concrete status name filters exactly. limit is optional. ?project=<slug|id|name>
// narrows to one project via the same predicate every other scoped endpoint uses
// (internal/api/scope.go). Built as a LOCAL query — do NOT fold this into
// permissionRequestSelect/scanPermissionRequest: those also back
// permissionRequestByID (the POST /api/approvals/{id} response) and both WS
// broadcast frames (ws.go:160-170), which must stay unjoined and unscoped.
func (h *Handler) listApprovals(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT pr.id, pr.session_id, pr.tool_name, pr.request_json, pr.status,
		       pr.requested_at, pr.resolved_at, pr.resolved_via, pr.reason,
		       COALESCE(pr.expires_at, '')
		FROM permission_requests pr
		JOIN sessions s ON s.id = pr.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE 1=1`
	args := []any{}
	switch status := r.URL.Query().Get("status"); status {
	case "", "pending":
		query += ` AND pr.status = 'pending'`
	case "resolved":
		query += ` AND pr.status != 'pending'`
	case "all":
		// WHERE 1=1 anchor already present — no extra filter.
	default:
		query += ` AND pr.status = ?`
		args = append(args, status)
	}

	// Deliberately NOT filtering p.archived = 0 (unlike stats.go:96-97): a
	// pending approval blocks a live agent even in an archived project; hiding
	// it here would strand it. Mirrors the invariant at
	// web/src/pages/Overview.tsx:1008 ("a pending approval must never be
	// invisible").
	projFilter, projArgs := scopeFilter(r)
	query += projFilter
	args = append(args, projArgs...)

	query += ` ORDER BY pr.requested_at DESC, pr.id DESC`
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
