package runcore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// StopReasonRefusal is the API stop_reason a safeguard produces. Opus 5.5 ships
// bio, cyber and reasoning-extraction classifiers; when one fires, the message
// ends with this and Claude Code moves the session onto an older model.
const StopReasonRefusal = "refusal"

// LastStopReason is the stop_reason of a session's NEWEST assistant turn
// (migration 0078), or "" when unknown — which is what every turn ingested
// before that migration reports, and what an un-ingested session reports too.
//
// Deliberately NOT restricted to turns with prose, unlike LastAssistantText: a
// refused turn frequently has no text block at all, and restricting the query
// the same way would make the refusal invisible precisely when it happened.
func LastStopReason(db *sql.DB, uuid string) string {
	if db == nil || uuid == "" {
		return ""
	}
	var reason sql.NullString
	err := db.QueryRow(`
		SELECT tr.stop_reason
		  FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid=? AND tr.role='assistant'
		 ORDER BY tr.seq DESC LIMIT 1`, uuid).Scan(&reason)
	if err != nil || !reason.Valid {
		return ""
	}
	return strings.TrimSpace(reason.String)
}

// refusalCategoryKeys are the payload keys READ from a model_refusal_fallback
// record when it happens to carry one.
//
// The record's shape is NOT catalogued — docs/jsonl-format.md counts exactly one
// occurrence in the whole corpus and lists no fields — so this is a best-effort
// read of whatever is there, never a dependency. Every caller works fine with an
// empty category; nothing branches on it, it only makes the blocked reason more
// useful when the harness turns out to say which classifier fired.
var refusalCategoryKeys = []string{"category", "refusalCategory", "refusal_category", "reason"}

// RefusalCategory returns the safeguard category of a session's most recent
// model_fallback event, or "" when there is none, the payload has no key this
// recognises, or the daemon has not ingested the event yet.
func RefusalCategory(db *sql.DB, uuid string) string {
	if db == nil || uuid == "" {
		return ""
	}
	var payload sql.NullString
	err := db.QueryRow(`
		SELECT ev.payload
		  FROM events ev JOIN sessions se ON se.id = ev.session_id
		 WHERE se.session_uuid=? AND ev.type='model_fallback'
		 ORDER BY ev.ts DESC, ev.id DESC LIMIT 1`, uuid).Scan(&payload)
	if err != nil || !payload.Valid {
		return ""
	}
	return categoryFromPayload([]byte(payload.String))
}

// categoryFromPayload digs a category out of the event payload, which wraps the
// verbatim JSONL line under "raw". Both levels are searched because the wrapper
// is ingest's own and could be flattened later.
func categoryFromPayload(payload []byte) string {
	var top map[string]json.RawMessage
	if json.Unmarshal(payload, &top) != nil {
		return ""
	}
	if c := categoryFromObject(top); c != "" {
		return c
	}
	raw, ok := top["raw"]
	if !ok {
		return ""
	}
	var inner map[string]json.RawMessage
	if json.Unmarshal(raw, &inner) != nil {
		return ""
	}
	return categoryFromObject(inner)
}

func categoryFromObject(obj map[string]json.RawMessage) string {
	for _, key := range refusalCategoryKeys {
		v, ok := obj[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// RefusalDetail renders the blocked reason for a refused run, and reports
// whether the stop reason is a refusal at all.
//
// The wording names the mechanism rather than blaming the executor: a safeguard
// refusal is not a failure of the run, it is the platform declining the request,
// and the operator's next move (rephrase the task, split the file, run it
// attended) depends on knowing which of the two happened.
func RefusalDetail(stopReason, category string) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(stopReason), StopReasonRefusal) {
		return "", false
	}
	if c := strings.TrimSpace(category); c != "" {
		return fmt.Sprintf("safeguard refusal (%s)", c), true
	}
	return "safeguard refusal (no category reported)", true
}

// BlockedOrRefused is the blocked test used where there is no tick count to
// classify with — the doc-unreadable branch of every engine's settle.
//
// The executor's OWN sentinel wins over the inferred one: a run that wrote
// `PHASE BLOCKED: the migration slot is taken` after being refused is telling
// the operator the more useful half of the story, and both answers stamp the
// same state anyway.
func BlockedOrRefused(text, stopReason, category string) (reason string, ok bool) {
	if reason, ok := BlockedReason(text); ok {
		return reason, true
	}
	return RefusalDetail(stopReason, category)
}

// ClassifyRunEnd is ClassifyEnd with the transcript's stop_reason folded in: a
// run whose last turn was REFUSED is blocked, whatever its tick count says.
//
// It is one function rather than a second check at each call site because the
// ordering is the whole point. A refusal that reached ClassifyEnd unnoticed
// classifies as `continue` (criteria unticked, no sentinel) and the engine
// spends two more billed turns resuming a session into the same classifier —
// the most expensive possible reading of a stop the platform already explained.
// And when the criteria DO all happen to be ticked, plain ClassifyEnd stamps a
// green `done` over a session that was cut off mid-work.
//
// Refusal is checked AFTER the sentinel for the reason BlockedOrRefused gives,
// and BEFORE the done/continue split because it outranks both.
func ClassifyRunEnd(text, stopReason, category string, done, total int) (EndState, string) {
	if reason, ok := BlockedOrRefused(text, stopReason, category); ok {
		return EndBlocked, reason
	}
	return ClassifyEnd(text, done, total)
}
