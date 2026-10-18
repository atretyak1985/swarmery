package api

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// sessionLinkCols is the swarmery link mod's live view of a session (migration
// 0107), declared beside the scan that reads it (same convention as
// sessionTerminalCols / sessionModelCols). Order matches sessionLinkScan.dest().
const sessionLinkCols = `,
	       s.link_status, s.link_seen_at, s.link_context_pct, s.link_rate_limits, s.link_cost_usd`

// linkThresholds is the status-ticker configuration link.live is judged by, so
// the dashboard's "live from the session mod" agrees with the ticker that lets
// link_status drive sessions.status (ingest.LinkFresh). Set by
// AttachStatusThresholds at startup; the zero value means the documented
// defaults, which is also what a daemon that never calls Attach runs with.
var linkThresholds ingest.Thresholds

// AttachStatusThresholds records the daemon's session-status thresholds
// (--active-window / --idle-window / --awaiting-after) so sessionDTO.link.live
// uses the same 2×AwaitAfter window as ingest.RecomputeStatuses. Call before
// NewServer.
func AttachStatusThresholds(t ingest.Thresholds) { linkThresholds = t }

// sessionLinkRateLimitDTO is one entry of the mod's $.session.usage()
// rateLimits[] (docs/mods-format.md §M2): kind is five_hour | seven_day on a
// subscription, percentUsed is 0–100, resetsAt is ISO 8601 when the mod knew it.
type sessionLinkRateLimitDTO struct {
	Kind        string  `json:"kind"`
	PercentUsed float64 `json:"percentUsed"`
	ResetsAt    *string `json:"resetsAt"`
}

// sessionLinkDTO is sessionDTO.Link: the latest state the swarmery link mod
// reported for this session, or null when no mod ever reported on it (every
// link_* column NULL).
//
// Live is the server's answer to "is the mod reporting on this session right
// now": link_seen_at younger than 2×AwaitAfter — the exact window
// ingest.RecomputeStatuses uses to let link_status drive sessions.status. The
// dashboard reads it for the source indicator; it never recomputes status.
//
// CostUSD is PER PROCESS (§M2): a resumed session restarts it from 0, so it is
// not the session's lifetime cost. The UI labels it that way.
type sessionLinkDTO struct {
	Status     *string                   `json:"status"`
	SeenAt     *string                   `json:"seenAt"`
	Live       bool                      `json:"live"`
	ContextPct *float64                  `json:"contextPct"`
	RateLimits []sessionLinkRateLimitDTO `json:"rateLimits"`
	CostUSD    *float64                  `json:"costUsd"`
	// Controllable: the session's mod polled the command queue within
	// link.PollFreshness, so the dashboard's Stop turn / Send prompt reach it
	// (docs/mods-link-protocol.md "Commands"). Always false while the control
	// channel is detached.
	Controllable bool `json:"controllable"`
}

// sessionLinkScan holds the sessionLinkCols tail of a session row.
type sessionLinkScan struct {
	status     sql.NullString
	seenAt     sql.NullString
	contextPct sql.NullFloat64
	rateLimits sql.NullString
	costUSD    sql.NullFloat64
}

// dest returns the scan targets in sessionLinkCols order.
func (l *sessionLinkScan) dest() []any {
	return []any{&l.status, &l.seenAt, &l.contextPct, &l.rateLimits, &l.costUSD}
}

// dto folds the tail into the DTO judged at now, or nil when every column is
// NULL and the session's mod is not polling for commands. sessionUUID is the
// Claude session id the controllability is keyed by. A link_rate_limits value
// that is not a JSON array of objects reads as an empty list — the mod's
// payload is untrusted input and must never turn a session read into a 500.
func (l *sessionLinkScan) dto(now time.Time, sessionUUID string) *sessionLinkDTO {
	controllable := sessionUUID != "" && linkControllable(sessionUUID)
	if !controllable && !l.status.Valid && !l.seenAt.Valid && !l.contextPct.Valid && !l.rateLimits.Valid && !l.costUSD.Valid {
		return nil
	}
	out := sessionLinkDTO{
		Controllable: controllable,
		Status:       nullStrPtr(l.status),
		SeenAt:       nullStrPtr(l.seenAt),
		Live:         l.seenAt.Valid && ingest.LinkFresh(l.seenAt.String, now, linkThresholds),
		ContextPct:   nullFloatPtr(l.contextPct),
		RateLimits:   parseLinkRateLimits(l.rateLimits.String),
		CostUSD:      nullFloatPtr(l.costUSD),
	}
	return &out
}

// parseLinkRateLimits decodes link_rate_limits, dropping entries without a
// kind. Anything unparsable yields [] (never nil, so the JSON is always an
// array).
func parseLinkRateLimits(raw string) []sessionLinkRateLimitDTO {
	out := []sessionLinkRateLimitDTO{}
	if raw == "" {
		return out
	}
	var parsed []sessionLinkRateLimitDTO
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return out
	}
	for _, r := range parsed {
		if r.Kind == "" {
			continue
		}
		if r.ResetsAt != nil && *r.ResetsAt == "" {
			r.ResetsAt = nil
		}
		out = append(out, r)
	}
	return out
}

func nullFloatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
