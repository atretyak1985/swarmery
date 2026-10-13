package plugindrift

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
)

// DefaultInterval is the drift-scan period. Drift changes only when a
// marketplace is pulled or a cache GC runs, so minutes are plenty.
const DefaultInterval = 5 * time.Minute

// Ticker runs periodic drift passes. OnNewError fires once per newly INSERTed
// error-severity finding — the hook the webhook event hangs off in phase 6.
type Ticker struct {
	DB         *sql.DB
	Detector   *Detector
	Interval   time.Duration
	OnNewError func(target, rule, message string)
}

// Run scans once immediately, then on every tick, until ctx is cancelled.
func (t *Ticker) Run(ctx context.Context) {
	iv := t.Interval
	if iv <= 0 {
		iv = DefaultInterval
	}
	t.Once(ctx)
	tick := time.NewTicker(iv)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.Once(ctx)
		}
	}
}

// Once runs a single pass: enumerate projects, scan, persist.
func (t *Ticker) Once(ctx context.Context) {
	projects, err := loadProjects(t.DB)
	if err != nil {
		log.Printf("plugindrift: load projects: %v", err)
		return
	}
	byRule := t.Detector.Scan(ctx, projects)
	// The mod advisory reads the DB, not the CLI, so it is evaluated even when
	// the detector went blind (nil on a query failure = leave its rows alone).
	byRule[RuleModValidateFailed] = modValidateFindings(t.DB, projects)
	for _, rule := range Rules {
		items, evaluated := byRule[rule]
		// nil means the detector could NOT evaluate this rule — leave its rows
		// alone. Syncing nil here would resolve every real finding the moment
		// the claude binary went missing, flipping the dashboard to green
		// exactly when detection went blind.
		if !evaluated || items == nil {
			continue
		}
		before := t.activeTargets(rule)
		if _, _, err := findings.Sync(t.DB, rule, items); err != nil {
			log.Printf("plugindrift: sync %s: %v", rule, err)
			continue
		}
		if t.OnNewError != nil {
			for _, it := range items {
				if it.Severity == "error" && !before[it.Target] {
					t.OnNewError(it.Target, rule, it.Message)
				}
			}
		}
	}
}

// activeTargets snapshots the rule's open targets before the sync, so a
// refreshed-in-place row is not mistaken for a new one.
func (t *Ticker) activeTargets(rule string) map[string]bool {
	out := map[string]bool{}
	rows, err := t.DB.Query(
		`SELECT target FROM config_lint_findings WHERE rule = ? AND resolved_at IS NULL`, rule)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err == nil {
			out[target] = true
		}
	}
	return out
}

// loadProjects reads every registered project and the plugin ids its
// settings.json enables. Projects that enable nothing are skipped.
//
// Deliberately repo-only: it does NOT fold in the declared settings overlays
// that internal/settingsoverlay resolves for the API's managed/pack view. Two
// reasons, both about what a finding here means:
//
//   - RuleEnabledNotInstalled says "enabled in settings.json but not installed
//     on this machine". An overlay's plugins are installed under whatever scope
//     the launcher owns, which `claude plugin list --json` reports against a
//     different project path — folding them in would manufacture an error-level
//     finding (and a webhook) for a plugin that loads fine in every session.
//   - The repair those findings drive runs `claude plugin install --scope
//     project`, which WRITES the repo's settings.json — exactly the thing an
//     overlay-managed project keeps out of its repo.
//
// The API compensates rather than papers over it: a plugin enabled only by an
// overlay renders as "unknown", never a green "ok" (see api/project_plugins.go).
func loadProjects(db *sql.DB) ([]Project, error) {
	rows, err := db.Query(`SELECT path FROM projects WHERE path <> '' ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		ids, _ := projectscan.ReadEnabledPlugins(path)
		if len(ids) == 0 {
			continue
		}
		out = append(out, Project{Path: path, Enabled: ids})
	}
	return out, rows.Err()
}

// RecordUnavailable writes the detector-unavailable finding directly, for the
// startup path where no Detector could be constructed at all. Recording the
// blindness is the point: a silent no-op would render as "no drift" in every
// surface, which is the failure this package exists to prevent.
func RecordUnavailable(db *sql.DB, cause error) {
	msg := "plugin drift detection is not running: " + cause.Error()
	if err := findings.Upsert(db, detectorTarget, RuleDetectorUnavailable, "error", msg); err != nil {
		log.Printf("plugindrift: record unavailable: %v", err)
	}
}
