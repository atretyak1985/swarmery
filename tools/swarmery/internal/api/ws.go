// WS live-update endpoint (wave A). Message names and payload shapes follow
// the FROZEN WSMessage type in web/src/api/types.ts — payloads are the
// Session / Event DTOs from handlers.go. Protocol doc: docs/ws-protocol.md.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// wsBus is the ingest notification source, attached once at daemon startup
// (a package variable so wave A does not have to touch the Handler struct).
var wsBus *ingest.Bus

// AttachBus wires the ingest event bus into the /api/ws endpoint.
func AttachBus(b *ingest.Bus) { wsBus = b }

const (
	wsSubscriberBuffer = 256
	wsWriteTimeout     = 5 * time.Second
)

// wsEnvelope is the wire shape of one WSMessage (types.ts).
type wsEnvelope struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// wsEventPayload is the event_appended payload: the Event DTO plus its
// session id, so list views can attribute live events to a session card
// (contract change accepted at step 10 — see web/CONTRACT-REQUESTS.md).
type wsEventPayload struct {
	SessionID int64     `json:"sessionId"`
	Event     *eventDTO `json:"event"`
}

// GET /api/ws — upgrades to WebSocket and streams WSMessage JSON text frames.
func (h *Handler) ws(w http.ResponseWriter, r *http.Request) {
	if wsBus == nil {
		http.Error(w, `{"error":"live updates unavailable (ingest pipeline not running)"}`,
			http.StatusServiceUnavailable)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The daemon is a localhost tool; the vite dev server proxies /api
		// from another origin, so cross-origin upgrades must be allowed.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("warn: ws: accept: %v", err)
		return
	}
	defer c.Close(websocket.StatusInternalError, "server error")

	// The client never sends application messages; CloseRead pumps the read
	// side and cancels ctx when the peer disconnects.
	ctx := c.CloseRead(r.Context())

	ch, cancel := wsBus.Subscribe(wsSubscriberBuffer)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case n := <-ch:
			frame, err := h.buildWSMessage(n)
			if err != nil {
				log.Printf("warn: ws: build %s: %v", n.Type, err)
				continue
			}
			if frame == nil {
				continue // referenced row vanished — skip
			}
			writeCtx, cancelWrite := context.WithTimeout(ctx, wsWriteTimeout)
			err = c.Write(writeCtx, websocket.MessageText, frame)
			cancelWrite()
			if err != nil {
				return // peer gone or too slow
			}
		}
	}
}

// buildWSMessage hydrates a bus notification into a WSMessage frame using the
// same DTOs the REST endpoints serve. Returns (nil, nil) if the row is gone.
func (h *Handler) buildWSMessage(n ingest.Notification) ([]byte, error) {
	var payload any
	switch n.Type {
	case ingest.NoteSessionStarted, ingest.NoteSessionUpdated:
		s, err := h.sessionByID(n.SessionID)
		if err != nil {
			return nil, err
		}
		if s == nil {
			return nil, nil
		}
		payload = s
	case ingest.NoteEventAppended:
		e, err := h.eventByID(n.EventID)
		if err != nil {
			return nil, err
		}
		if e == nil {
			return nil, nil
		}
		payload = wsEventPayload{SessionID: n.SessionID, Event: e}
	default:
		return nil, errors.New("unknown notification type " + n.Type)
	}
	return json.Marshal(wsEnvelope{Type: n.Type, Payload: payload})
}

func (h *Handler) sessionByID(id int64) (*sessionDTO, error) {
	var s sessionDTO
	err := scanSession(h.DB.QueryRow(`
		SELECT s.id, s.project_id, p.slug, s.session_uuid, s.model, s.git_branch, s.cwd,
		       s.status, s.started_at, s.ended_at, s.title, s.source
		FROM sessions s JOIN projects p ON p.id = s.project_id WHERE s.id = ?`, id).Scan, &s)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (h *Handler) eventByID(id int64) (*eventDTO, error) {
	var e eventDTO
	var payload sql.NullString
	err := h.DB.QueryRow(`
		SELECT id, turn_id, ts, type, tool_name, parent_event_id, status, duration_ms, payload
		FROM events WHERE id = ?`, id).Scan(
		&e.ID, &e.TurnID, &e.TS, &e.Type, &e.ToolName,
		&e.ParentEventID, &e.Status, &e.DurationMs, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if payload.Valid {
		e.Payload = json.RawMessage(payload.String)
	}
	return &e, nil
}
