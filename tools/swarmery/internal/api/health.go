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
//
// db{openConnections,inUse,waitCount,waitDurationMs} is the connection pool's
// own counters (sql.DB.Stats). It runs no query, so it is the one part of this
// response that stays true while the store's single connection is held by
// someone else — the first thing to read when health itself is slow.

import (
	"context"
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
	// DB is the database pool's counters, read from sql.DB.Stats() with no
	// query. The store runs on one connection (internal/store.Open), so a
	// waitCount climbing while inUse stays 1 names a long holder of it.
	DB healthDBPool `json:"db"`
}

// healthDBPool is the subset of sql.DBStats that tells a starved pool from a
// slow query: how many connections exist, how many are checked out, and how
// many callers have queued for one (cumulative since open) and for how long.
type healthDBPool struct {
	OpenConnections int   `json:"openConnections"`
	InUse           int   `json:"inUse"`
	WaitCount       int64 `json:"waitCount"`
	WaitDurationMs  int64 `json:"waitDurationMs"`
}

// dbPoolStats maps sql.DBStats onto the health shape.
func dbPoolStats(st sql.DBStats) healthDBPool {
	return healthDBPool{
		OpenConnections: st.OpenConnections,
		InUse:           st.InUse,
		WaitCount:       st.WaitCount,
		WaitDurationMs:  st.WaitDuration.Milliseconds(),
	}
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
//
// The request path itself runs no query once the first snapshot exists: the
// database-backed fields (size, migrationVersion, the newest event behind
// ingestLagSec, dispatch, pluginDrift, autoModeClassifier) come from
// healthSlowCache, refreshed in the background at most every healthSlowTTL.
// Phase 1 of the link-ingest stall measured health's ~6 sequential acquires of
// the store's one connection at 3–40 s per probe, and its MAX(ts) alone at
// 10.7% of all connection time (reports/link-ingest-stall.md).
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	// Read the pool counters first: this is what the pool looked like when the
	// probe arrived, and it stays live whatever the snapshot's age.
	pool := dbPoolStats(h.DB.Stats())
	now := time.Now()
	slow, _ := h.healthSnapshot(now)

	dto := healthDTO{
		Status:           "ok",
		Version:          version.Version,
		Build:            version.String(),
		DBSizeBytes:      slow.sizeBytes,
		DBSizeBytesCamel: slow.sizeBytes,
		Watching:         h.Watching,
		MigrationVersion: slow.migrationVersion,
		WSClients:        wsClientCount(),
		IngestLagSec:     lagSince(slow.newestEvent, now),
		Dispatch:         slow.dispatch,
		PluginDrift:      slow.pluginDrift,

		AutoModeClassifier: slow.autoMode,
		DB:                 pool,
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
	writeJSON(w, dto, slow.sizeErr)
}

// healthSlowTTL is how old a snapshot may be before a probe starts a
// background refresh. The probe still answers from the old one, so the fields
// can lag by this much plus one refresh; ingestLagSec is computed from the
// cached newest event at answer time, so it can read high by up to that, never
// low.
const healthSlowTTL = 5 * time.Second

// healthFirstFillWait bounds how long a probe waits for the very first
// snapshot. Past it the probe answers with zero values (ingestLagSec null)
// while the fill finishes in the background — health never queues behind the
// pool for longer than this, even on a cold, starved daemon.
const healthFirstFillWait = 2 * time.Second

// healthQueryTimeout caps one whole refresh: computeHealthSlow shares a single
// context of this length across all of its queries, so a wedged connection
// cannot hold the single-flight refresh forever.
const healthQueryTimeout = 30 * time.Second

// healthSlow is the database-backed part of the health answer.
type healthSlow struct {
	sizeBytes int64
	// sizeErr is the size query's error: the frozen contract answers 500 when
	// the store cannot report its own size.
	sizeErr          error
	migrationVersion int
	newestEvent      *time.Time // nil when no event was ingested yet
	dispatch         healthDispatch
	pluginDrift      healthPluginDrift
	autoMode         healthAutoMode
}

// healthSlowCache is the last snapshot and the single-flight refresh. A
// refresh in flight is shared by every probe that finds the snapshot stale (or
// absent): there is never more than one.
type healthSlowCache struct {
	mu         sync.Mutex
	at         time.Time // zero until the first fill lands
	val        healthSlow
	refreshing chan struct{} // non-nil while a refresh runs; closed when it lands
}

// healthSnapshot returns the current snapshot and whether one exists. A stale
// snapshot is returned as is and refreshed in the background; with none yet,
// the call waits up to healthFirstFillWait for the first fill.
func (h *Handler) healthSnapshot(now time.Time) (healthSlow, bool) {
	c := &h.healthSlow
	c.mu.Lock()
	if !c.at.IsZero() {
		val := c.val
		if age := now.Sub(c.at); age < 0 || age >= healthSlowTTL {
			h.refreshHealthLocked()
		}
		c.mu.Unlock()
		return val, true
	}
	done := h.refreshHealthLocked()
	c.mu.Unlock()

	timer := time.NewTimer(healthFirstFillWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val, !c.at.IsZero()
}

// refreshHealthLocked starts a background refresh unless one is running, and
// returns the channel that closes when the running one lands. c.mu is held.
func (h *Handler) refreshHealthLocked() <-chan struct{} {
	c := &h.healthSlow
	if c.refreshing != nil {
		return c.refreshing
	}
	done := make(chan struct{})
	c.refreshing = done
	go func() {
		val := h.computeHealthSlow()
		c.mu.Lock()
		c.val, c.at, c.refreshing = val, time.Now(), nil
		c.mu.Unlock()
		close(done)
	}()
	return done
}

// computeHealthSlow runs every database-backed health read. Each field is best
// effort, as before the cache: a failed read leaves its zero value.
func (h *Handler) computeHealthSlow() healthSlow {
	ctx, cancel := context.WithTimeout(context.Background(), healthQueryTimeout)
	defer cancel()
	var out healthSlow
	out.sizeErr = h.rdb().QueryRowContext(ctx,
		`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&out.sizeBytes)
	out.migrationVersion = h.migrationVersion(ctx)
	out.newestEvent = h.newestEvent(ctx)
	out.dispatch = dispatchHealth()
	out.pluginDrift = h.pluginDriftCounts(ctx)
	out.autoMode = h.autoModeHealth(time.Now())
	return out
}

// migrationVersion reads the highest applied schema-migration version. Best
// effort: 0 on any error (a health probe must never 500 over this).
func (h *Handler) migrationVersion(ctx context.Context) int {
	var v sql.NullInt64
	if err := h.rdb().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0
	}
	if !v.Valid {
		return 0
	}
	return int(v.Int64)
}

// newestEvent reads the newest ingested event's ts, or nil when no events have
// been ingested yet (fresh DB) or the read fails.
func (h *Handler) newestEvent(ctx context.Context) *time.Time {
	var newest sql.NullString
	if err := h.rdb().QueryRowContext(ctx, `SELECT MAX(ts) FROM events`).Scan(&newest); err != nil {
		return nil
	}
	if !newest.Valid || newest.String == "" {
		return nil
	}
	t, err := parseEventTS(newest.String)
	if err != nil {
		return nil
	}
	return &t
}

// lagSince is ingestLagSec: (now − newest ingested event ts) in whole
// seconds, or nil when no events have been ingested yet so the client can
// render "—" instead of a misleading 0. Negative skew is clamped to 0.
func lagSince(newest *time.Time, now time.Time) *int64 {
	if newest == nil {
		return nil
	}
	lag := int64(now.Sub(*newest).Seconds())
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
func (h *Handler) pluginDriftCounts(ctx context.Context) healthPluginDrift {
	var out healthPluginDrift
	rows, err := h.rdb().QueryContext(ctx,
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
	if n, err := automode.Count(h.rdb(), now.Add(-automode.HealthWindow)); err == nil {
		out.NoVerdictLastHour, out.SessionsLastHour = n.Events, n.Sessions
		if n.LastAt != "" {
			last := n.LastAt
			out.LastAt = &last
		}
	}
	if alerting, err := automode.Alerting(h.rdb()); err == nil {
		out.Alerting = alerting
	}
	c.at, c.val = now, out
	return out
}
