package prune

import (
	"database/sql"
	"log"
	"os"
	"strconv"
	"time"
)

// Scheduled retention (agent-memory phase 2). `swarmery prune` has always
// existed as a manual, destructive CLI pass; nothing ever ran it, so telemetry
// grew without bound. Tick is the same pass on the daemon's daily maintenance
// goroutine, with two deliberate differences from the CLI:
//
//   - It never VACUUMs (Options{Vacuum: false}). VACUUM rewrites the whole
//     database file and blocks every writer; the daemon is a live writer, so
//     it trades disk space back for staying responsive. The CLI still VACUUMs.
//   - Its window comes from SWARMERY_RETENTION_DAYS, not a required flag: an
//     unattended job needs a safe default, where the CLI wants an explicit act.
const (
	// DefaultRetentionDays is the window used when SWARMERY_RETENTION_DAYS is
	// unset or unparseable. Two months keeps a full month of week-over-week
	// comparison intact even at the end of the window.
	DefaultRetentionDays = 60

	// MinRetentionDays is the floor any non-zero window is raised to.
	// internal/api/project_health.go (see its "Retention assumption" comment)
	// reads LIVE tables only — never daily_rollups — to compare this week
	// against the previous one. Below 14 days its prev-week window is empty
	// and costPrevWeekUsd silently goes null, so a shorter retention would
	// break that endpoint rather than merely shrink history.
	MinRetentionDays = 14

	// RetentionDaysEnv is the window knob. "0" disables scheduled pruning.
	RetentionDaysEnv = "SWARMERY_RETENTION_DAYS"
)

// FirstTickDelay is how long the daemon waits after startup before its FIRST
// retention pass. It is not politeness: the store runs a single SQLite
// connection, and startup is exactly when ingest is replaying transcripts on
// it. A prune fired inline at startup contends with that replay, and on a large
// store the operator sees an unresponsive dashboard with no log line for
// minutes. Delaying the first pass costs nothing — retention is a daily
// policy — and lets ingest settle first.
const FirstTickDelay = 5 * time.Minute

// RetentionDays resolves the scheduled-prune window from the environment:
//
//	unset/empty  → DefaultRetentionDays
//	"0"          → 0 (scheduled pruning disabled; the CLI still works)
//	1..13        → MinRetentionDays, with a warning (see MinRetentionDays)
//	negative     → DefaultRetentionDays, with a warning (nonsensical window)
//	non-integer  → DefaultRetentionDays, with a warning
//	>= 14        → the value
//
// It warns rather than failing: a typo in a launchd plist must not stop the
// daemon from starting, and every fallback here is the conservative direction
// (keep MORE data, never less).
func RetentionDays() int {
	raw, ok := os.LookupEnv(RetentionDaysEnv)
	if !ok || raw == "" {
		return DefaultRetentionDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("warn: %s=%q is not an integer — falling back to %d days",
			RetentionDaysEnv, raw, DefaultRetentionDays)
		return DefaultRetentionDays
	}
	switch {
	case n == 0:
		return 0
	case n < 0:
		log.Printf("warn: %s=%d is negative — falling back to %d days",
			RetentionDaysEnv, n, DefaultRetentionDays)
		return DefaultRetentionDays
	case n < MinRetentionDays:
		log.Printf("warn: %s=%d is below the %d-day floor that /api/projects/health "+
			"needs for its week-over-week comparison (see internal/api/project_health.go, "+
			"\"Retention assumption\") — using %d days",
			RetentionDaysEnv, n, MinRetentionDays, MinRetentionDays)
		return MinRetentionDays
	default:
		return n
	}
}

// Tick runs one scheduled retention pass as of now, over sessions that ended
// more than days ago. days <= 0 is a no-op returning zero Stats: the caller
// decides whether to log "disabled", and a guard here means a future caller
// cannot accidentally prune everything with a zero window.
//
// The cutoff format matches the CLI's and the schema's stored TEXT timestamps,
// so it compares lexicographically.
func Tick(db *sql.DB, now time.Time, days int) (Stats, error) {
	if days <= 0 {
		return Stats{}, nil
	}
	cutoff := now.UTC().AddDate(0, 0, -days).Format("2006-01-02T15:04:05.000Z")
	return RunWithOptions(db, cutoff, false, Options{Vacuum: false})
}
