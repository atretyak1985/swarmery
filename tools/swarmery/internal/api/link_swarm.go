// Swarm (mods-swarmery-link phase 6): the read side of the link's
// session.send / session.receive / claim / release events, and the dashboard's
// relay of a message through a sending session's mod. The write side of the
// events is POST /api/link/events (link.go → internal/link, swarm.go).
// Contract: docs/mods-link-protocol.md, Swarm.
//
// The two GETs are read-only and, like every other dashboard GET, are not
// behind requireLocalOrigin (the Host fence in routes.go still applies). The
// relay POST is an SPA write behind requireLocalOrigin, like /link/prompt.
package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
)

const (
	swarmMessagesDefault = 100
	swarmMessagesMax     = 500
)

// swarmProjectID parses ?projectId: absent is 0 (every project).
func swarmProjectID(r *http.Request) (int64, bool) {
	v := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if v == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(v, 10, 64)
	return id, err == nil && id >= 0
}

// GET /api/swarm/messages?projectId=&since=<RFC3339>&limit=<1..500> — the
// swarm's messages, newest first (link.Message).
//
//	200 [link.Message]
//	400 projectId not a non-negative integer, since not RFC 3339, limit not a positive integer
func (h *Handler) swarmMessages(w http.ResponseWriter, r *http.Request) {
	projectID, ok := swarmProjectID(r)
	if !ok {
		writeClientErr(w, http.StatusBadRequest, "projectId must be an integer")
		return
	}
	q := r.URL.Query()
	since := ""
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeClientErr(w, http.StatusBadRequest, "since must be an RFC 3339 timestamp")
			return
		}
		since = link.Stamp(t)
	}
	limit := swarmMessagesDefault
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeClientErr(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, swarmMessagesMax)
	}
	msgs, err := link.ListMessages(r.Context(), h.DB, projectID, since, limit)
	writeJSON(w, msgs, err)
}

// swarmClaimsDTO is the GET /api/swarm/claims body.
type swarmClaimsDTO struct {
	Claims    []link.Claim    `json:"claims"`
	Conflicts []link.Conflict `json:"conflicts"`
}

// GET /api/swarm/claims?projectId= — the live file claims and the paths two or
// more sessions hold at once. Conflicts are advisory: nothing is blocked.
//
//	200 swarmClaimsDTO
//	400 projectId not a non-negative integer
func (h *Handler) swarmClaims(w http.ResponseWriter, r *http.Request) {
	projectID, ok := swarmProjectID(r)
	if !ok {
		writeClientErr(w, http.StatusBadRequest, "projectId must be an integer")
		return
	}
	now := time.Now()
	claims, err := link.LiveClaims(r.Context(), h.DB, projectID, now)
	if err != nil {
		writeErr(w, err)
		return
	}
	conflicts, err := link.Conflicts(r.Context(), h.DB, projectID, now)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, swarmClaimsDTO{Claims: claims, Conflicts: conflicts}, nil)
}

// linkMessageRequest is the POST /api/sessions/{id}/link/message body: to is
// the recipient's sessions.id (a number) or Claude session id (a string).
type linkMessageRequest struct {
	To   json.RawMessage `json:"to"`
	Text string          `json:"text"`
}

// messageQueued is the 202 body of the relay route.
type messageQueued struct {
	CommandID int64 `json:"commandId"`
	MessageID int64 `json:"messageId"`
}

// POST /api/sessions/{id}/link/message — relay a message to another session
// through the SENDING session's mod ({id} is the sender's sessions.id).
//
//	202 {commandId, messageId}
//	400 bad id or body, empty text, text > 4096 characters, unknown recipient, recipient == sender
//	404 unknown sender
//	409 the sender's mod is not polling (session has no link)
func (h *Handler) linkSessionMessage(w http.ResponseWriter, r *http.Request) {
	c := linkCommands
	if c == nil {
		writeLinkDisabled(w)
		return
	}
	sid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req linkMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "malformed body: want {\"to\":int|string,\"text\":string}")
		return
	}
	switch {
	case strings.TrimSpace(req.Text) == "":
		writeClientErr(w, http.StatusBadRequest, "text is required")
		return
	case utf8.RuneCountInString(req.Text) > link.MaxMessageBody:
		writeClientErr(w, http.StatusBadRequest, "text exceeds 4096 characters")
		return
	}
	var sender string
	switch err := h.DB.QueryRowContext(r.Context(), `SELECT session_uuid FROM sessions WHERE id = ?`, sid).Scan(&sender); {
	case errors.Is(err, sql.ErrNoRows):
		writeClientErr(w, http.StatusNotFound, "session not found")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	to, err := resolveRecipient(r.Context(), h.DB, req.To)
	switch {
	case err != nil:
		writeErr(w, err)
		return
	case to == "":
		writeClientErr(w, http.StatusBadRequest, "unknown recipient")
		return
	case to == sender:
		writeClientErr(w, http.StatusBadRequest, "recipient is the sender")
		return
	}
	cmdID, msgID, err := c.EnqueueMessage(r.Context(), sid, to, req.Text)
	switch {
	case errors.Is(err, link.ErrSessionNotFound):
		writeClientErr(w, http.StatusNotFound, "session not found")
	case errors.Is(err, link.ErrNotControllable):
		writeClientErr(w, http.StatusConflict, link.ErrNotControllable.Error())
	case errors.Is(err, link.ErrBadMessage):
		writeClientErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeErr(w, err)
	default:
		writeJSONStatus(w, http.StatusAccepted, messageQueued{CommandID: cmdID, MessageID: msgID})
	}
}

// resolveRecipient maps the request's to — a sessions.id number, or a string
// holding a Claude session id (or a sessions.id in digits) — to the session's
// uuid; "" when it names no session.
func resolveRecipient(ctx context.Context, db *sql.DB, raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", nil
	}
	byID := func(id int64) (string, error) {
		var uuid string
		err := db.QueryRowContext(ctx, `SELECT session_uuid FROM sessions WHERE id = ?`, id).Scan(&uuid)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return uuid, err
	}
	if raw[0] != '"' {
		var id int64
		if err := json.Unmarshal(raw, &id); err != nil {
			return "", nil
		}
		return byID(id)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", nil
	}
	if s = strings.TrimSpace(s); s == "" {
		return "", nil
	}
	var uuid string
	err := db.QueryRowContext(ctx, `SELECT session_uuid FROM sessions WHERE session_uuid = ?`, s).Scan(&uuid)
	switch {
	case err == nil:
		return uuid, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	if id, perr := strconv.ParseInt(s, 10, 64); perr == nil {
		return byID(id)
	}
	return "", nil
}
