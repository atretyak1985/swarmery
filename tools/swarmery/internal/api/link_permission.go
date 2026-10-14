// The swarmery link approvals channel (mods-swarmery-link phase 5): the mod
// asks from inside its tool.check hook and polls for the decision. Contract:
// docs/mods-link-protocol.md "Permission".
//
// Layered on the shim's channel (docs/hooks-protocol.md, unchanged): the same
// permission_requests rows through the same approvals.Service, so the
// dashboard, the rules, the prod-deploy guard and the needs-you queue treat a
// mod row exactly like a shim row. The routes sit behind linkAuth (bearer
// link-token, no origin fence) like the rest of /api/link/*.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
)

// modPollMs is the poll length the 202 tells the mod to use (ms): one GET
// holds at most approvals.ModMaxWait.
const modPollMs = int(approvals.ModMaxWait / time.Millisecond)

// modHookEvent is the hook_event_name stamped on a mod row's request_json.
const modHookEvent = "tool.check"

// linkPermissionRequest is the POST /api/link/permission body.
type linkPermissionRequest struct {
	SessionID      string          `json:"sessionId"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"toolName"`
	ToolInput      json.RawMessage `json:"toolInput"`
	ToolUseID      string          `json:"toolUseId"`
	AgentID        string          `json:"agentId"`
	PermissionMode string          `json:"permissionMode"`
}

// modRequestJSON is the request_json stored for a mod row: the hook-stdin
// shape (snake_case keys, tool_input) the shim stores, so parseQuestions,
// requestPreview and the web read a mod row unchanged.
type modRequestJSON struct {
	SessionID      string          `json:"session_id"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	AgentID        string          `json:"agent_id,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	HookEventName  string          `json:"hook_event_name"`
}

// linkPermissionResponse is every 200/202 body of the two routes; the
// omitempty fields are absent where the contract does not list them.
type linkPermissionResponse struct {
	ID        int64           `json:"id,omitempty"`
	Status    string          `json:"status,omitempty"`
	Decision  string          `json:"decision,omitempty"`
	Message   *string         `json:"message,omitempty"`
	Answer    json.RawMessage `json:"answer,omitempty"`
	Via       string          `json:"via,omitempty"`
	ExpiresAt string          `json:"expiresAt,omitempty"`
	PollMs    int             `json:"pollMs,omitempty"`
}

// modHookInput turns the mod's body into the service's HookInput, with the
// synthesized hook-stdin request_json.
func modHookInput(req linkPermissionRequest) (approvals.HookInput, error) {
	toolInput := req.ToolInput
	if len(toolInput) == 0 {
		toolInput = json.RawMessage("null")
	}
	raw, err := json.Marshal(modRequestJSON{
		SessionID: req.SessionID, Cwd: req.Cwd, ToolName: req.ToolName, ToolInput: toolInput,
		ToolUseID: req.ToolUseID, AgentID: req.AgentID, PermissionMode: req.PermissionMode,
		HookEventName: modHookEvent,
	})
	if err != nil {
		return approvals.HookInput{}, err
	}
	return approvals.HookInput{
		Raw: raw, SessionUUID: req.SessionID, ToolName: req.ToolName, ToolInput: toolInput,
		CWD: req.Cwd, ToolUseID: req.ToolUseID, Channel: approvals.ChannelMod,
	}, nil
}

// POST /api/link/permission — the mod asks about one tool call.
//
//	200 {"id":N,"decision":"local"}               prod deploy: confirm in the terminal
//	200 {"id":N,"decision":"allow","via":"rule"}  an auto-approve rule matched
//	200 {"id":N, …the GET body…}                  a re-POST of a call already decided
//	202 {"id":N,"status":"pending","expiresAt":…,"pollMs":25000}
//	204                                           excluded project (no decision)
//	400 / 413 / 429                               bad body / body too large / too many pending
//	401 / 503                                     token / link or approvals off
func (h *Handler) linkPermissionOpen(w http.ResponseWriter, r *http.Request) {
	svc := approvalsSvc
	if svc == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "approvals unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	var req linkPermissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeClientErr(w, http.StatusRequestEntityTooLarge, "body exceeds 4 MiB")
			return
		}
		writeClientErr(w, http.StatusBadRequest, "malformed body: want {\"sessionId\",\"toolName\",\"toolInput\",…}")
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.ToolName) == "" {
		writeClientErr(w, http.StatusBadRequest, "sessionId and toolName are required")
		return
	}
	in, err := modHookInput(req)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "malformed toolInput")
		return
	}

	id, d, pending, err := svc.OpenMod(in)
	if writeOpenErr(w, err) {
		return
	}
	if pending {
		var expiresAt string
		if err := h.DB.QueryRow(
			`SELECT COALESCE(expires_at, '') FROM permission_requests WHERE id = ?`, id).Scan(&expiresAt); err != nil {
			writeErr(w, err)
			return
		}
		writeJSONStatus(w, http.StatusAccepted, linkPermissionResponse{
			ID: id, Status: approvals.StatusPending, ExpiresAt: expiresAt, PollMs: modPollMs,
		})
		return
	}
	writeJSON(w, modOpenBody(id, d), nil)
}

// modOpenBody is the POST 200 body for a row that is already terminal: the
// local-only hand-off and the rule approval have their own forms, anything
// else is the GET body plus the id.
func modOpenBody(id int64, d approvals.Decision) linkPermissionResponse {
	switch {
	case d.Status == approvals.StatusResolvedElsewhere && d.Via == approvals.ViaLocalOnly:
		return linkPermissionResponse{ID: id, Decision: "local"}
	case d.Status == approvals.StatusApproved && d.Via == approvals.ViaRule:
		return linkPermissionResponse{ID: id, Decision: "allow", Via: approvals.ViaRule}
	default:
		b := modDecisionBody(d)
		b.ID = id
		return b
	}
}

// modDecisionBody is the GET 200 body for a decision. An AskUserQuestion
// answer always travels as {"decision":"allow","answer":{questions,answers}}
// whatever --answer-delivery says: the mod returns it as tool.call's
// { result } (spike M4), which needs no deny-message fallback.
func modDecisionBody(d approvals.Decision) linkPermissionResponse {
	switch d.Status {
	case approvals.StatusApproved:
		b := linkPermissionResponse{Decision: "allow"}
		if len(d.UpdatedInput) > 0 {
			b.Answer = d.UpdatedInput
		}
		return b
	case approvals.StatusDenied:
		msg := d.Reason
		return linkPermissionResponse{Decision: "deny", Message: &msg}
	default:
		// expired / resolved_elsewhere: no decision — the mod hands the call
		// back to the engine's own verdict.
		return linkPermissionResponse{Decision: "pass", Status: d.Status}
	}
}

// GET /api/link/permission/{id}?session=<uuid>&wait=<seconds> — one poll.
//
//	200 {"decision":"allow"} | {"decision":"allow","answer":{…}}
//	    | {"decision":"deny","message":…} | {"decision":"pass","status":…}
//	204   still pending after wait
//	400   id not an integer, session missing, wait not an integer
//	404   unknown id, or a row of another session
func (h *Handler) linkPermissionWait(w http.ResponseWriter, r *http.Request) {
	svc := approvalsSvc
	if svc == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "approvals unavailable"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	q := r.URL.Query()
	session := strings.TrimSpace(q.Get("session"))
	if session == "" {
		writeClientErr(w, http.StatusBadRequest, "session is required")
		return
	}
	wait, ok := parsePermissionWait(q.Get("wait"))
	if !ok {
		writeClientErr(w, http.StatusBadRequest, "wait must be an integer number of seconds")
		return
	}
	d, pending, err := svc.WaitMod(r.Context(), id, session, wait)
	switch {
	case errors.Is(err, approvals.ErrNotFound):
		writeClientErr(w, http.StatusNotFound, "permission request not found")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	if r.Context().Err() != nil {
		return // the mod hung up mid-poll; the lease keeps the row
	}
	if pending {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, modDecisionBody(d), nil)
}

// parsePermissionWait reads the wait parameter: missing is the full
// approvals.ModMaxWait (25 s), a negative value 0, above 25 clamped to 25;
// not an integer is not ok.
func parsePermissionWait(raw string) (time.Duration, bool) {
	if raw == "" {
		return approvals.ModMaxWait, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	switch {
	case n <= 0:
		return 0, true
	case time.Duration(n) > approvals.ModMaxWait/time.Second:
		return approvals.ModMaxWait, true // compared before multiplying: no overflow
	default:
		return time.Duration(n) * time.Second, true
	}
}
