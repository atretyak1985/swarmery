package runcore

import "database/sql"

// LastAssistantText returns a session's final assistant turn text (by session
// uuid), or "" when the session or its transcript is not (yet) ingested.
//
// This is the ONE reader of "what did the run actually say when it stopped".
// Before phase 3 it existed twice — dispatch.lastAssistantText (which feeds
// sentinel classification and the carry-forward into the next playbook stage)
// and planning.lastAssistantText (which pulls PLAN SAVED: off the wizard's last
// turn) — and three engines that badly needed it had no copy at all: phaserun,
// planrun and verify all decided completion from the process EXIT CODE while
// the `PHASE DONE` / `PHASE BLOCKED` / `PLAN DONE` endings their own prompts
// demanded were written, ingested, and never read by anything.
//
// Note what `turns.text` is: text blocks ONLY. Ingest drops thinking blocks
// entirely (migration 0005), so this is the model's prose to the operator and
// never its extended reasoning — which is exactly the surface the prompts put
// their ending sentinel on.
//
// A missing transcript degrades to "" on purpose, and every caller treats "" as
// "no evidence" rather than as evidence of anything: ingest is a separate
// pipeline with its own lag, so a run whose transcript has not landed yet must
// fall through to whatever it did before this existed.
func LastAssistantText(db *sql.DB, uuid string) string {
	if db == nil || uuid == "" {
		return ""
	}
	var text sql.NullString
	err := db.QueryRow(`
		SELECT tr.text
		  FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid=? AND tr.role='assistant' AND tr.text IS NOT NULL
		 ORDER BY tr.seq DESC LIMIT 1`, uuid).Scan(&text)
	if err != nil || !text.Valid {
		return ""
	}
	return text.String
}
