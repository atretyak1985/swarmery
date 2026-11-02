// A request answered in the terminal. Claude Code shows its own dialog beside
// the PermissionRequest hook and does not cancel the hook when the person
// answers there, so the shim's long-poll — and the dashboard card — stayed
// pending until expires_at (10 minutes by default) for a question that was
// already settled. The transcript says so first: the call's tool_result
// arrives and ingest sets the tool_call event's status. Sweep reads that and
// resolves the row resolved_elsewhere via 'terminal', the same record a
// shim's Detach leaves.
//
// It errs towards leaving a row alone. A row resolves only when a tool_call
// event of the same session carries the SAME call — the dedup hash of its
// stored input equals the row's — was written no earlier than answeredLookback
// before the request, and is the latest such event; an input the transcript
// masked differently simply does not match. A wrong match costs nothing
// either: resolved_elsewhere makes the shim answer 204, which leaves the
// decision to the native dialog, and never approves anything.
package approvals

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"
)

// answeredLookback is how long before its request a call's tool_call event
// may be written: the assistant message lands in the transcript just before
// the hook fires, and ingest may store it a little later than that.
const answeredLookback = 2 * time.Minute

// pendingCall is one pending row Sweep checks against the transcript.
type pendingCall struct {
	id          int64
	sessionID   int64
	sessionUUID string
	toolName    string
	hash        string
	requestedAt time.Time
}

// resolveAnsweredInTranscript is Sweep's transcript pass.
func (s *Service) resolveAnsweredInTranscript() {
	calls, err := s.pendingCalls()
	if err != nil {
		log.Printf("warn: approvals: answered-in-transcript query: %v", err)
		return
	}
	for _, c := range calls {
		answered, err := s.answeredInTranscript(c)
		if err != nil {
			log.Printf("warn: approvals: answered-in-transcript request %d: %v", c.id, err)
			continue
		}
		if !answered {
			continue
		}
		s.mu.Lock()
		err = s.resolveLocked(c.id, StatusResolvedElsewhere, "terminal", "", nil)
		s.mu.Unlock()
		if err != nil && !errors.Is(err, ErrAlreadyResolved) && !errors.Is(err, ErrNotFound) {
			log.Printf("warn: approvals: resolve answered request %d: %v", c.id, err)
		}
	}
}

func (s *Service) pendingCalls() ([]pendingCall, error) {
	rows, err := s.db.Query(
		`SELECT pr.id, pr.session_id, se.session_uuid, pr.tool_name, pr.dedup_hash, pr.requested_at
		 FROM permission_requests pr
		 JOIN sessions se ON se.id = pr.session_id
		 WHERE pr.status = 'pending' AND pr.dedup_hash IS NOT NULL AND pr.tool_name <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingCall
	for rows.Next() {
		var c pendingCall
		var uuid sql.NullString
		var requested string
		if err := rows.Scan(&c.id, &c.sessionID, &uuid, &c.toolName, &c.hash, &requested); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, requested)
		if err != nil || !uuid.Valid {
			continue
		}
		c.sessionUUID, c.requestedAt = uuid.String, at
		out = append(out, c)
	}
	return out, rows.Err()
}

// answeredInTranscript reports whether the latest tool_call event of c's call
// has its result.
func (s *Service) answeredInTranscript(c pendingCall) (bool, error) {
	since := c.requestedAt.Add(-answeredLookback).UTC().Format(tsFormat)
	rows, err := s.db.Query(
		`SELECT payload, status FROM events
		 WHERE session_id = ? AND type = 'tool_call' AND tool_name = ? AND ts >= ?
		 ORDER BY ts DESC, id DESC`,
		c.sessionID, c.toolName, since)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload, status sql.NullString
		if err := rows.Scan(&payload, &status); err != nil {
			return false, err
		}
		var p struct {
			Input json.RawMessage `json:"input"`
		}
		if !payload.Valid || json.Unmarshal([]byte(payload.String), &p) != nil || len(p.Input) == 0 {
			continue
		}
		if !sameCall(c, p.Input) {
			continue
		}
		// The latest event of this very call decides: a newer identical call
		// still waiting keeps the row pending.
		return status.Valid && status.String != "", nil
	}
	return false, rows.Err()
}

// sameCall reports whether a stored tool_call input is c's call. Ingest adds
// the call's tool_use_id to the input it stores, which the hook's tool_input
// does not carry, so that one key is dropped before hashing.
func sameCall(c pendingCall, input json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) == nil {
		if _, ok := m["tool_use_id"]; ok {
			delete(m, "tool_use_id")
			if b, err := json.Marshal(m); err == nil {
				input = b
			}
		}
	}
	hash, err := DedupHash(c.sessionUUID, c.toolName, input)
	return err == nil && hash == c.hash
}
