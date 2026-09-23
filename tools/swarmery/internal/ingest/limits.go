package ingest

// The usage-limit detector: the PRIMARY recorder of account_limit_hits.
//
// Every daemon-spawned run writes a transcript that ingest tails, and so does
// every terminal session — which is where the limit hits actually happen — so
// reading them here needs no per-engine spawn hook. The gate is the record's
// isApiErrorMessage flag, never the text alone: plan documents and design
// discussions quote the marker strings verbatim, and a text-only matcher would
// invent limit hits out of prose.
//
// No row carries the message text: only the account, the record's timestamp,
// the fixed-vocabulary scope and the two uuids.

import (
	"encoding/json"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// recordLimitHit inserts one account_limit_hits row when r is a flagged API
// error record whose text carries a recorded usage-limit shape. A no-op for any
// other record, for a record with no uuid, and for a transcript read with no
// projects-root context (its
// account is unknown; a later tail that knows the root records it, the
// record_uuid unique index keeping that to one row).
func (in *ingester) recordLimitHit(r *record) error {
	// No uuid, no row: the record_uuid unique index is what makes a re-tail
	// idempotent, and it does not cover the empty string.
	if !r.IsAPIErrorMessage || r.UUID == "" {
		return nil
	}
	account := AccountFor(in.originRoot)
	if account == "" {
		return nil
	}
	scope, ok := claudeprobe.LimitScope(apiErrorText(r))
	if !ok {
		return nil
	}
	_, err := store.InsertAccountLimitHit(in.tx, store.LimitHit{
		Account:     account,
		ObservedAt:  r.Timestamp,
		Scope:       scope,
		Source:      "transcript",
		SessionUUID: in.sessionUUID,
		RecordUUID:  r.UUID,
	})
	return err
}

// apiErrorText is the prose of an assistant record: its text blocks joined, or
// a plain-string content — the same shapes processRecords reads.
func apiErrorText(r *record) string {
	var m apiMessage
	if err := json.Unmarshal(r.Message, &m); err != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return ""
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}
