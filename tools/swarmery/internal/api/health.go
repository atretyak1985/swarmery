package api

// Parity wave: daemon health endpoint for the dashboard header.
//
// The ORIGINAL parity contract (snake_case) is preserved verbatim so the web
// header keeps working:
//   {"status":"ok","version":"<semver>","db_size_bytes":<int>,"watching":<bool>}
//
// "build" is additive next to it: the same semver plus the commit the running
// binary was built from, so the header can distinguish two builds of one
// release line (see internal/version).
//
// Fusion phase 9 (Console/DX) ADDS operational fields consumed by `swarmery
// status` / `swarmery console` (camelCase, additive — nothing above is renamed):
//   uptimeSec, migrationVersion, wsClients, ingestLagSec, dispatch{active,paused}
// dbSizeBytes duplicates db_size_bytes in camelCase for the new CLI without
// breaking the frozen snake_case reader.
//
// Two more additive fields follow the same rule — pluginDrift{error,warn} and
// autoModeClassifier{noVerdictLastHour,sessionsLastHour,lastAt,alerting}.

import (
	"database/sql"
	"net/http"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/automode"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/version"
)

// processStart is stamped once at daemon startup (AttachUptime) so /api/health
// can report uptimeSec. Zero until attached ⇒ uptime is reported as 0.
var processStart time.Time

// AttachUptime records the daemon's start instant for the health uptime field.
func AttachUptime(t time.Time) { processStart = t }

type healthDTO struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	DBSizeBytes int64  `json:"db_size_bytes"`
	Watching    bool   `json:"watching"`
	// build: the identity of the running binary — the release semver plus the
	// commit it was built from ("0.2.0-15-g41157a8-dirty"). Additive: `version`
	// stays bare semver for the frozen reader, so only this field moves between
	// two builds of the same release line.
	Build string `json:"build"`
	// hooks_last_seen: ISO timestamp of the most recent POST /api/hooks/*
	// (phase 2 heartbeat, additive optional per the frozen HealthResponse).
	// Kept in-memory in the approvals service — absent until the first hook
	// checks in after daemon start.
	HooksLastSeen *string `json:"hooks_last_seen,omitempty"`

	// ── fusion phase 9 additive operational fields (camelCase) ──
	UptimeSec        int64          `json:"uptimeSec"`
	DBSizeBytesCamel int64          `json:"dbSizeBytes"` // camelCase mirror for the CLI
	MigrationVersion int            `json:"migrationVersion"`
	WSClients        int            `json:"wsClients"`
	IngestLagSec     *int64         `json:"ingestLagSec"` // null when no events ingested yet
	Dispatch         healthDispatch `json:"dispatch"`
	// PluginDrift counts unresolved plugin_* findings — enabled plugins Claude
	// Code cannot actually load. The sidebar health line badges off this.
	PluginDrift healthPluginDrift `json:"pluginDrift"`
	// AutoModeClassifier says whether Claude Code's server-side auto mode
	// permission check is answering: how many tool calls it left without a
	// verdict in the last hour, and whether the outage alert is open.
	AutoModeClassifier healthAutoMode `json:"autoModeClassifier"`
}

// healthAutoMode is the auto mode classifier summary (internal/automode). Zero
// values mean "no refusal seen in the last hour" — and also "could not be
// read", because health never fails over this field.
type healthAutoMode struct {
	// NoVerdictLastHour counts the tool calls refused for want of a verdict.
	NoVerdictLastHour int `json:"noVerdictLastHour"`
	// SessionsLastHour is how many distinct sessions those calls belong to.
	SessionsLastHour int `json:"sessionsLastHour"`
	// LastAt is the newest such call's timestamp; null when there is none.
	LastAt *string `json:"lastAt"`
	// Alerting is true while the auto_mode_no_verdict alert is open.
	Alerting bool `json:"alerting"`
}

// autoModeCacheTTL is how long one computed summary is served. The ticker that
// raises the alert runs every minute, so a fresher number would not be a truer
// one.
const autoModeCacheTTL = 30 * time.Second

// autoModeCache is the in-memory copy of the last summary and when it was
// computed. The mutex is held across the recompute, so a burst of health polls
// on an expired entry costs one query, not one each.
type autoModeCache struct {
	mu  sync.Mutex
	at  time.Time
	val healthAutoMode
}

// healthPluginDrift counts unresolved plugin_* findings by severity. Zero
// values mean "scanned, nothing wrong"; a detector that cannot run reports
// itself as an error finding, so a blind detector never reads as healthy.
type healthPluginDrift struct {
	Error int `json:"error"`
	Warn  int `json:"warn"`
}

// healthDispatch is the zero-valued-when-absent dispatcher summary (the spec's
// "dispatch: {active, paused} (zero-value if Phase 3 absent)").
type healthDispatch struct {
	Active int  `json:"active"` // live runs in this process
	Paused bool `json:"paused"` // global pause flag
}

// GET /api/health
//
// db_size_bytes is computed from the live connection (page_count ×
// page_size), so it needs no filesystem access to the DB path. watching is
// true when the ingest pipeline is attached (serve without --no-ingest). The
// fusion-phase-9 operational fields are best-effort: a failed sub-query leaves
// its field at the zero value rather than failing the whole endpoint.
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	var size int64
	err := h.DB.QueryRow(
		`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&size)

	dto := healthDTO{
		Status:           "ok",
		Version:          version.Version,
		Build:            version.String(),
		DBSizeBytes:      size,
		DBSizeBytesCamel: size,
		Watching:         h.Watching,
		MigrationVersion: h.migrationVersion(),
		WSClients:        wsClientCount(),
		IngestLagSec:     h.ingestLagSec(),
		Dispatch:         dispatchHealth(),
		PluginDrift:      h.pluginDriftCounts(),

		AutoModeClassifier: h.autoModeHealth(time.Now()),
	}
	if !processStart.IsZero() {
		dto.UptimeSec = int64(time.Since(processStart).Seconds())
	}
	if approvalsSvc != nil {
		if t, ok := approvalsSvc.LastSeen(); ok {
			iso := t.UTC().Format(time.RFC3339)
			dto.HooksLastSeen = &iso
		}
	}
	writeJSON(w, dto, err)
}

// migrationVersion reads the highest applied schema-migration version. Best
// effort: 0 on any error (a health probe must never 500 over this).
func (h *Handler) migrationVersion() int {
	var v sql.NullInt64
	if err := h.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0
	}
	if !v.Valid {
		return 0
	}
	return int(v.Int64)
}

// ingestLagSec is (now − newest ingested event ts) in whole seconds, or nil
// when no events have been ingested yet (fresh DB) so the client can render
// "—" instead of a misleading 0. Negative skew is clamped to 0.
func (h *Handler) ingestLagSec() *int64 {
	var newest sql.NullString
	if err := h.DB.QueryRow(`SELECT MAX(ts) FROM events`).Scan(&newest); err != nil {
		return nil
	}
	if !newest.Valid || newest.String == "" {
		return nil
	}
	t, err := parseEventTS(newest.String)
	if err != nil {
		return nil
	}
	lag := int64(time.Since(t).Seconds())
	if lag < 0 {
		lag = 0
	}
	return &lag
}

// parseEventTS parses the ISO-8601 UTC timestamps events.ts stores. The ingest
// pipeline writes RFC3339 (optionally fractional); try both.
func parseEventTS(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// dispatchHealth returns the dispatcher summary, zero-valued when the dispatcher
// is not attached (serve --no-ingest, or a test handler) — the spec's "zero-value
// if Phase 3 absent".
func dispatchHealth() healthDispatch {
	if dispatchSvc == nil {
		return healthDispatch{}
	}
	st, err := dispatchSvc.Snapshot()
	if err != nil {
		return healthDispatch{}
	}
	return healthDispatch{Active: st.ActiveRuns, Paused: st.GlobalPaused}
}

// pluginDriftCounts counts unresolved plugin_* findings by severity. Best
// effort: health must never 500 over this. info-severity rows (plugin_note)
// are deliberately counted in neither bucket — they are not a problem.
func (h *Handler) pluginDriftCounts() healthPluginDrift {
	var out healthPluginDrift
	rows, err := h.DB.Query(
		`SELECT severity, COUNT(*) FROM config_lint_findings
		  WHERE resolved_at IS NULL AND rule LIKE 'plugin\_%' ESCAPE '\'
		  GROUP BY severity`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sev string
		var n int
		if err := rows.Scan(&sev, &n); err != nil {
			return out
		}
		switch sev {
		case "error":
			out.Error = n
		case "warn":
			out.Warn = n
		}
	}
	return out
}

// autoModeHealth summarises the auto mode classifier as of now: the no-verdict
// refusals of the last hour and whether the alert is open. Best effort, like
// every operational field here: a query that fails leaves its part at the zero
// value, never a 500. The result is kept for autoModeCacheTTL — the count is a
// bounded scan (the last hour of tool calls), but the health line is polled by
// every open dashboard tab.
func (h *Handler) autoModeHealth(now time.Time) healthAutoMode {
	c := &h.autoMode
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && now.Sub(c.at) >= 0 && now.Sub(c.at) < autoModeCacheTTL {
		return c.val
	}
	var out healthAutoMode
	if n, err := automode.Count(h.DB, now.Add(-automode.HealthWindow)); err == nil {
		out.NoVerdictLastHour, out.SessionsLastHour = n.Events, n.Sessions
		if n.LastAt != "" {
			last := n.LastAt
			out.LastAt = &last
		}
	}
	if alerting, err := automode.Alerting(h.DB); err == nil {
		out.Alerting = alerting
	}
	c.at, c.val = now, out
	return out
}
