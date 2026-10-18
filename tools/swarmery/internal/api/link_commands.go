// The swarmery link control channel (mods-swarmery-link phase 4): the
// dashboard queues stop / prompt commands for a mod-linked session, the mod
// long-polls them and acks. Contract: docs/mods-link-protocol.md "Commands".
//
// Two route families with two fences:
//   - the mod's GET /api/link/commands and POST /api/link/commands/{id}/ack sit
//     behind linkAuth (bearer link-token, no origin fence), like /api/link/events;
//   - the SPA's /api/sessions/…/link/* writes sit behind requireLocalOrigin.
//
// The queue is attached as package state (AttachLinkCommands), the same
// pattern as AttachLink: with nothing attached every route answers 503
// {"error":"link disabled"}.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
)

const (
	// maxPromptText bounds a dashboard prompt, in bytes.
	maxPromptText = 16 << 10
	// maxBulkSessions bounds one bulk-prompt selection.
	maxBulkSessions = 50
	// maxCommandBody bounds every command request body (a 16 KiB prompt,
	// JSON-escaped, plus 50 ids fits easily).
	maxCommandBody = 256 << 10
	// skipNotFound / skipNoLink are the bulk-prompt skip reasons.
	skipNotFound = "not found"
	skipNoLink   = "session has no link"
)

// linkCommands is the attached queue; nil disables the control channel (503).
var linkCommands *link.Commands

// AttachLinkCommands wires the command queue into the control routes and
// points its link_command notes at the WS bus (daemon startup). nil detaches.
func AttachLinkCommands(c *link.Commands) {
	if c != nil {
		c.SetPublish(publishLink)
	}
	linkCommands = c
}

// linkControllable reports whether the session with this Claude session id has
// a mod polling for commands right now (false while the queue is detached).
// It is what the session DTO's link.controllable carries.
func linkControllable(sessionUUID string) bool {
	c := linkCommands
	return c != nil && c.Controllable(sessionUUID)
}

// writeLinkDisabled is the 503 every control route answers while detached.
func writeLinkDisabled(w http.ResponseWriter) {
	writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "link disabled"})
}

// GET /api/link/commands?session=<claude session id>&wait=<0..25> — the mod's
// long-poll.
//
//	200 link.Command   the oldest deliverable command, now marked delivered
//	204                nothing deliverable before wait elapsed / the request ended
//	400                session missing, wait not an integer or negative
//	401 / 503          token / link disabled (linkAuth)
func (h *Handler) linkCommandsWait(w http.ResponseWriter, r *http.Request) {
	c := linkCommands
	if c == nil {
		writeLinkDisabled(w)
		return
	}
	q := r.URL.Query()
	session := strings.TrimSpace(q.Get("session"))
	if session == "" {
		writeClientErr(w, http.StatusBadRequest, "session is required")
		return
	}
	wait, ok := parseLinkWait(q.Get("wait"))
	if !ok {
		writeClientErr(w, http.StatusBadRequest, "wait must be an integer number of seconds, 0..25")
		return
	}
	cmd, err := c.WaitNext(r.Context(), session, wait)
	if err != nil {
		if r.Context().Err() != nil {
			return // the mod hung up mid-claim; nothing to answer
		}
		writeErr(w, err)
		return
	}
	if cmd == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, cmd, nil)
}

// parseLinkWait reads the wait query parameter: missing is 0, above 25 is
// clamped to link.MaxWait; not an integer, or negative, is not ok.
func parseLinkWait(raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, false
	}
	if time.Duration(n) > link.MaxWait/time.Second {
		return link.MaxWait, true // compared before multiplying: no overflow
	}
	return time.Duration(n) * time.Second, true
}

// linkAckRequest is the POST /api/link/commands/{id}/ack body.
type linkAckRequest struct {
	Result  string `json:"result"`
	Message string `json:"message"`
}

// POST /api/link/commands/{id}/ack — the mod's verdict on a delivered command.
//
//	204   recorded (or a repeat ack: idempotent, nothing changes)
//	400   id not an integer, body not JSON, result not done|failed, message > 500 chars
//	404   no command with that id
func (h *Handler) linkCommandAck(w http.ResponseWriter, r *http.Request) {
	c := linkCommands
	if c == nil {
		writeLinkDisabled(w)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req linkAckRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "malformed body: want {\"result\":\"done\"|\"failed\",\"message\"?:string}")
		return
	}
	switch err := c.Ack(r.Context(), id, req.Result, req.Message); {
	case errors.Is(err, link.ErrCommandNotFound):
		writeClientErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, link.ErrBadAck):
		writeClientErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeErr(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// commandQueued is the 202 body of the per-session control routes.
type commandQueued struct {
	CommandID int64 `json:"commandId"`
}

// enqueueForSession queues one command for the session in the {id} path
// segment and answers 202 / 400 / 404 / 409.
func enqueueForSession(w http.ResponseWriter, r *http.Request, c *link.Commands, kind string, payload any) {
	sid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	id, err := c.Enqueue(r.Context(), sid, kind, payload)
	switch {
	case errors.Is(err, link.ErrSessionNotFound):
		writeClientErr(w, http.StatusNotFound, "session not found")
	case errors.Is(err, link.ErrNotControllable):
		writeClientErr(w, http.StatusConflict, link.ErrNotControllable.Error())
	case err != nil:
		writeErr(w, err)
	default:
		writeJSONStatus(w, http.StatusAccepted, commandQueued{CommandID: id})
	}
}

// POST /api/sessions/{id}/link/stop — queue a $.turn.abort for the session's mod.
func (h *Handler) linkSessionStop(w http.ResponseWriter, r *http.Request) {
	c := linkCommands
	if c == nil {
		writeLinkDisabled(w)
		return
	}
	enqueueForSession(w, r, c, link.CmdStop, struct{}{})
}

// linkPromptRequest is the POST /api/sessions/{id}/link/prompt body.
type linkPromptRequest struct {
	Text   string `json:"text"`
	AsUser bool   `json:"asUser"`
}

// POST /api/sessions/{id}/link/prompt — queue a $.prompt.submit.
func (h *Handler) linkSessionPrompt(w http.ResponseWriter, r *http.Request) {
	c := linkCommands
	if c == nil {
		writeLinkDisabled(w)
		return
	}
	var req linkPromptRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "malformed body: want {\"text\":string,\"asUser\"?:bool}")
		return
	}
	if msg := checkPromptText(req.Text); msg != "" {
		writeClientErr(w, http.StatusBadRequest, msg)
		return
	}
	enqueueForSession(w, r, c, link.CmdPrompt, link.PromptPayload{Text: req.Text, AsUser: req.AsUser})
}

// checkPromptText returns the 400 message for an unusable prompt text, or "".
func checkPromptText(text string) string {
	if strings.TrimSpace(text) == "" {
		return "text is required"
	}
	if len(text) > maxPromptText {
		return "text exceeds 16 KiB"
	}
	return ""
}

// linkBulkPromptRequest is the POST /api/sessions/link/bulk-prompt body.
type linkBulkPromptRequest struct {
	SessionIDs []int64 `json:"sessionIds"`
	Text       string  `json:"text"`
}

type bulkQueued struct {
	ID        int64 `json:"id"`
	CommandID int64 `json:"commandId"`
}

type bulkSkipped struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

type bulkPromptResponse struct {
	Queued  []bulkQueued  `json:"queued"`
	Skipped []bulkSkipped `json:"skipped"`
}

// POST /api/sessions/link/bulk-prompt — queue one prompt (asUser false) for
// every selected session that has a polling mod; the rest are skipped with a
// reason. A repeated id is queued once.
//
//	200 {queued:[{id,commandId}], skipped:[{id,reason}]}
//	400 empty text, text > 16 KiB, empty list, more than 50 ids
func (h *Handler) linkBulkPrompt(w http.ResponseWriter, r *http.Request) {
	c := linkCommands
	if c == nil {
		writeLinkDisabled(w)
		return
	}
	var req linkBulkPromptRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "malformed body: want {\"sessionIds\":[int],\"text\":string}")
		return
	}
	if msg := checkPromptText(req.Text); msg != "" {
		writeClientErr(w, http.StatusBadRequest, msg)
		return
	}
	switch {
	case len(req.SessionIDs) == 0:
		writeClientErr(w, http.StatusBadRequest, "sessionIds is required")
		return
	case len(req.SessionIDs) > maxBulkSessions:
		writeClientErr(w, http.StatusBadRequest, "sessionIds exceeds 50")
		return
	}
	resp := bulkPromptResponse{Queued: []bulkQueued{}, Skipped: []bulkSkipped{}}
	seen := make(map[int64]bool, len(req.SessionIDs))
	payload := link.PromptPayload{Text: req.Text}
	for _, sid := range req.SessionIDs {
		if seen[sid] {
			continue
		}
		seen[sid] = true
		id, err := c.Enqueue(r.Context(), sid, link.CmdPrompt, payload)
		switch {
		case errors.Is(err, link.ErrSessionNotFound):
			resp.Skipped = append(resp.Skipped, bulkSkipped{ID: sid, Reason: skipNotFound})
		case errors.Is(err, link.ErrNotControllable):
			resp.Skipped = append(resp.Skipped, bulkSkipped{ID: sid, Reason: skipNoLink})
		case err != nil:
			writeErr(w, err)
			return
		default:
			resp.Queued = append(resp.Queued, bulkQueued{ID: sid, CommandID: id})
		}
	}
	writeJSON(w, resp, nil)
}
