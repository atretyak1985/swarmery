// Package advisor is the deterministic (no LLM) rule engine of the retro
// improvement loop (phase 3): it folds aggregates already in SQLite into
// evidenced improvement recommendations and drives their lifecycle
//
//	proposed → accepted|dismissed → adopted → verified
//
// Run() evaluates the six rules (rules.go) over the trailing 14-day window,
// upserts recommendations under the dedup contract, auto-detects adoption
// (a NEW agent_versions row after acceptance) and verification (the rule's
// metric improved ≥ VerifyImprovement vs the baseline snapshot ≥
// VerifyAfterDays after adoption). The daemon calls Run at startup and on a
// 24h ticker; POST /api/retro/advise calls it on demand.
//
// Dedup contract (dedup_key = rule + ':' + target, numeric ':2'/':3' suffix
// only for re-raising after verified):
//   - proposed|accepted|adopted → evidence/detail/updated_at refreshed in
//     place, status untouched;
//   - dismissed → suppressed until updated_at is older than
//     DismissSuppressDays, then flipped back to proposed with fresh evidence;
//   - verified → closed permanently; a later re-fire inserts a fresh row
//     with the next numeric suffix.
package advisor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Lifecycle thresholds.
const (
	// DismissSuppressDays: a dismissed recommendation is not re-proposed until
	// this many days after dismissal.
	DismissSuppressDays = 30
	// VerifyAfterDays: minimum days after adoption before verification runs.
	VerifyAfterDays = 7
	// VerifyImprovement: minimum relative metric improvement vs the baseline
	// snapshot to flip adopted → verified.
	VerifyImprovement = 0.20
)

// tsFmt matches the ingest timestamp shape so string range predicates behave.
const tsFmt = "2006-01-02T15:04:05.000Z"

func fmtTS(t time.Time) string { return t.UTC().Format(tsFmt) }

// Stats is one Run's outcome tally.
type Stats struct {
	Proposed int `json:"proposed"`
	Updated  int `json:"updated"`
	Adopted  int `json:"adopted"`
	Verified int `json:"verified"`
}

func (s Stats) String() string {
	return fmt.Sprintf("proposed %d, updated %d, adopted %d, verified %d",
		s.Proposed, s.Updated, s.Adopted, s.Verified)
}

// baseline is the JSON snapshot stored in recommendations.baseline: written
// at accept time (BaselineFor), extended with adopted_at when adoption is
// detected, and compared against during verification.
type baseline struct {
	Metric     string  `json:"metric"`
	Value      float64 `json:"value"`
	Window     window  `json:"window"`
	AcceptedAt string  `json:"accepted_at,omitempty"`
	AdoptedAt  string  `json:"adopted_at,omitempty"`
}

// Run evaluates all rules over the WindowDays window ending now, upserts
// recommendations, and advances the lifecycle (adoption + verification).
func Run(db *sql.DB, now time.Time) (Stats, error) {
	var stats Stats
	win := window{From: fmtTS(now.AddDate(0, 0, -WindowDays)), To: fmtTS(now)}

	evals := []struct {
		name string
		fn   func() ([]finding, error)
	}{
		{"R1", func() ([]finding, error) { return r1DeniedTools(db, win) }},
		{"R2", func() ([]finding, error) { return r2AgentErrorRate(db, win) }},
		{"R3", func() ([]finding, error) { return r3RecurringErrors(db, win) }},
		{"R4", func() ([]finding, error) { return r4Redispatch(db, win) }},
		{"R5", func() ([]finding, error) { return r5StaleImprovements(db, win, now) }},
		{"R6", func() ([]finding, error) { return r6CacheRegression(db, win, now) }},
	}
	for _, e := range evals {
		fs, err := e.fn()
		if err != nil {
			return stats, fmt.Errorf("advisor %s: %w", e.name, err)
		}
		for _, f := range fs {
			if err := upsert(db, f, now, &stats); err != nil {
				return stats, fmt.Errorf("advisor %s upsert %q: %w", e.name, f.target, err)
			}
		}
	}

	if err := detectAdoption(db, now, &stats); err != nil {
		return stats, fmt.Errorf("advisor adoption: %w", err)
	}
	if err := verify(db, now, &stats); err != nil {
		return stats, fmt.Errorf("advisor verify: %w", err)
	}
	return stats, nil
}

// upsert applies the dedup contract to one finding.
func upsert(db *sql.DB, f finding, now time.Time, stats *Stats) error {
	ev, err := json.Marshal(f.evidence)
	if err != nil {
		return err
	}
	nowS := fmtTS(now)

	var id int64
	var status, updatedAt string
	err = db.QueryRow(`
		SELECT id, status, updated_at FROM recommendations
		 WHERE rule = ? AND target = ?
		 ORDER BY id DESC LIMIT 1`, f.rule, f.target).Scan(&id, &status, &updatedAt)
	switch {
	case err == sql.ErrNoRows:
		return insertProposed(db, f, f.rule+":"+f.target, string(ev), nowS, stats)
	case err != nil:
		return err
	}

	switch status {
	case "proposed", "accepted", "adopted":
		// Open recommendation → keep the numbers fresh, never touch status.
		if _, err := db.Exec(`
			UPDATE recommendations
			   SET title = ?, detail = ?, evidence = ?, updated_at = ?
			 WHERE id = ?`, f.title, f.detail, string(ev), nowS, id); err != nil {
			return err
		}
		stats.Updated++
		return nil
	case "dismissed":
		dismissedAt, perr := time.Parse(time.RFC3339, updatedAt)
		if perr != nil || now.Sub(dismissedAt) <= DismissSuppressDays*24*time.Hour {
			return nil // still suppressed (or unreadable timestamp — stay safe)
		}
		// Suppression window elapsed → re-propose in place with fresh evidence.
		if _, err := db.Exec(`
			UPDATE recommendations
			   SET status = 'proposed', title = ?, detail = ?, evidence = ?,
			       baseline = NULL, updated_at = ?
			 WHERE id = ?`, f.title, f.detail, string(ev), nowS, id); err != nil {
			return err
		}
		stats.Proposed++
		return nil
	case "verified":
		// Closed permanently → fresh row with the next numeric suffix.
		var n int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE rule = ? AND target = ?`,
			f.rule, f.target).Scan(&n); err != nil {
			return err
		}
		key := f.rule + ":" + f.target + ":" + strconv.FormatInt(n+1, 10)
		return insertProposed(db, f, key, string(ev), nowS, stats)
	default:
		return fmt.Errorf("recommendation %d: unknown status %q", id, status)
	}
}

func insertProposed(db *sql.DB, f finding, dedupKey, evidence, nowS string, stats *Stats) error {
	_, err := db.Exec(`
		INSERT INTO recommendations
			(rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'proposed', ?, ?, ?)`,
		f.rule, f.targetKind, f.target, f.title, f.detail, evidence, dedupKey, nowS, nowS)
	if err != nil {
		return err
	}
	stats.Proposed++
	return nil
}

// ── adoption detection ────────────────────────────────────────────────────

// detectAdoption flips accepted → adopted for agent-kind recommendations
// whose target agent's CURRENT registry version was created after the
// acceptance (accepted_at inside the baseline JSON, written by the PATCH
// handler) — i.e. someone actually changed the agent definition.
func detectAdoption(db *sql.DB, now time.Time, stats *Stats) error {
	// Current version created_at per folded registry agent name.
	versions := map[string]time.Time{}
	vrows, err := db.Query(`
		SELECT a.name, v.created_at
		  FROM agents a
		  JOIN agent_versions v ON v.id = a.current_version_id
		 WHERE a.deleted = 0`)
	if err != nil {
		return err
	}
	defer vrows.Close()
	for vrows.Next() {
		var name, createdAt string
		if err := vrows.Scan(&name, &createdAt); err != nil {
			return err
		}
		if t, perr := time.Parse(time.RFC3339, createdAt); perr == nil {
			key := normAgent(name)
			if t.After(versions[key]) {
				versions[key] = t
			}
		}
	}
	if err := vrows.Err(); err != nil {
		return err
	}

	rows, err := db.Query(`
		SELECT id, target, baseline FROM recommendations
		 WHERE status = 'accepted' AND target_kind = 'agent'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type adopt struct {
		id       int64
		baseline string
	}
	var adopts []adopt
	for rows.Next() {
		var id int64
		var target string
		var base sql.NullString
		if err := rows.Scan(&id, &target, &base); err != nil {
			return err
		}
		if !base.Valid {
			continue // accepted without a baseline snapshot — nothing to compare
		}
		var b baseline
		if err := json.Unmarshal([]byte(base.String), &b); err != nil || b.AcceptedAt == "" {
			continue
		}
		acceptedAt, err := time.Parse(time.RFC3339, b.AcceptedAt)
		if err != nil {
			continue
		}
		ver, ok := versions[target]
		if !ok || !ver.After(acceptedAt) {
			continue
		}
		b.AdoptedAt = fmtTS(now)
		nb, err := json.Marshal(b)
		if err != nil {
			return err
		}
		adopts = append(adopts, adopt{id, string(nb)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range adopts {
		if _, err := db.Exec(`
			UPDATE recommendations SET status = 'adopted', baseline = ?, updated_at = ?
			 WHERE id = ?`, a.baseline, fmtTS(now), a.id); err != nil {
			return err
		}
		stats.Adopted++
	}
	return nil
}

// ── verification ──────────────────────────────────────────────────────────

// verify recomputes each adopted recommendation's metric over the
// post-adoption window once VerifyAfterDays have passed: a relative
// improvement ≥ VerifyImprovement vs the baseline value flips it to
// verified; otherwise a note is recorded in the evidence JSON and it stays
// adopted for the next Run.
func verify(db *sql.DB, now time.Time, stats *Stats) error {
	rows, err := db.Query(`
		SELECT id, rule, target, baseline, evidence FROM recommendations
		 WHERE status = 'adopted'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type rec struct {
		id           int64
		rule, target string
		baseline     string
		evidence     string
	}
	var recs []rec
	for rows.Next() {
		var r rec
		var base sql.NullString
		if err := rows.Scan(&r.id, &r.rule, &r.target, &base, &r.evidence); err != nil {
			return err
		}
		if !base.Valid {
			continue
		}
		r.baseline = base.String
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range recs {
		var b baseline
		if err := json.Unmarshal([]byte(r.baseline), &b); err != nil || b.AdoptedAt == "" {
			continue
		}
		adoptedAt, err := time.Parse(time.RFC3339, b.AdoptedAt)
		if err != nil || now.Sub(adoptedAt) < VerifyAfterDays*24*time.Hour {
			continue
		}
		post := window{From: fmtTS(adoptedAt), To: fmtTS(now)}
		_, cur, err := metricValue(db, r.rule, r.target, post)
		if err != nil {
			return err
		}
		if relImprovement(r.rule, b.Value, cur) >= VerifyImprovement {
			if _, err := db.Exec(`
				UPDATE recommendations SET status = 'verified', updated_at = ?
				 WHERE id = ?`, fmtTS(now), r.id); err != nil {
				return err
			}
			stats.Verified++
			continue
		}
		// Not there yet — record the observation, stay adopted.
		var ev map[string]any
		if err := json.Unmarshal([]byte(r.evidence), &ev); err != nil || ev == nil {
			ev = map[string]any{}
		}
		ev["note"] = "no measurable improvement yet"
		ev["post_adoption"] = map[string]any{"window": post, "metric": b.Metric, "value": cur}
		nb, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := db.Exec(`
			UPDATE recommendations SET evidence = ?, updated_at = ? WHERE id = ?`,
			string(nb), fmtTS(now), r.id); err != nil {
			return err
		}
	}
	return nil
}

// relImprovement is the relative metric improvement vs the baseline value,
// direction-aware: every rule metric improves DOWNWARD (denied counts, error
// rates, recurring days, re-dispatch shares, open improvements) except R6's
// cache hit rate, which improves UPWARD. A zero baseline cannot improve
// relatively (division guard) — except downward metrics already at 0, which
// only counts as improved if they stayed there (0 → 0 is not ≥ 20% better).
func relImprovement(rule string, base, cur float64) float64 {
	if base == 0 {
		return 0
	}
	if rule == "R6" {
		return (cur - base) / base
	}
	return (base - cur) / base
}

// ── metric snapshots ──────────────────────────────────────────────────────

// BaselineFor computes the rule's metric snapshot over the trailing
// WindowDays window ending now and returns the baseline JSON (metric name +
// value + window + accepted_at=now). The PATCH handler calls this when a
// recommendation flips to accepted; adoption detection later compares
// agent-version timestamps against accepted_at.
func BaselineFor(db *sql.DB, rule, target string, now time.Time) (string, error) {
	win := window{From: fmtTS(now.AddDate(0, 0, -WindowDays)), To: fmtTS(now)}
	name, value, err := metricValue(db, rule, target, win)
	if err != nil {
		return "", err
	}
	b := baseline{Metric: name, Value: value, Window: win, AcceptedAt: fmtTS(now)}
	j, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return string(j), nil
}

// metricValue computes one rule's scalar metric for a target over a window —
// shared by BaselineFor (accept-time snapshot) and verify (post-adoption
// recompute), so both sides of the comparison use identical math.
func metricValue(db *sql.DB, rule, target string, win window) (name string, value float64, err error) {
	switch rule {
	case "R1":
		var n int64
		err = db.QueryRow(`
			SELECT COUNT(*)
			  FROM events e
			  JOIN sessions s ON s.id = e.session_id
			  JOIN projects p ON p.id = s.project_id
			 WHERE e.tool_name = ? AND e.status = 'denied'
			   AND e.type IN ('tool_call', 'skill_use', 'subagent_start')
			   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`,
			target, win.From, win.To).Scan(&n)
		return "denied_count", float64(n), err
	case "R2":
		acc, aerr := agentErrorWindow(db, win)
		if aerr != nil {
			return "", 0, aerr
		}
		if a, ok := acc[target]; ok && a.runs > 0 {
			return "error_rate", float64(a.errors) / float64(a.runs), nil
		}
		return "error_rate", 0, nil
	case "R3":
		days, derr := errGroupDays(db, target, win)
		return "distinct_error_days", float64(days), derr
	case "R4":
		shares, serr := delegationShares(db, win)
		if serr != nil {
			return "", 0, serr
		}
		if c, ok := shares[target]; ok && c[1] > 0 {
			return "redispatch_share", float64(c[0]) / float64(c[1]), nil
		}
		return "redispatch_share", 0, nil
	case "R5":
		open, oerr := improvementStillOpen(db, target)
		if oerr != nil {
			return "", 0, oerr
		}
		v := 0.0
		if open {
			v = 1
		}
		return "open_stale_improvements", v, nil
	case "R6":
		rate, _, rerr := cacheHitRate(db, win)
		return "cache_hit_rate", rate, rerr
	default:
		return "", 0, fmt.Errorf("unknown rule %q", rule)
	}
}

// errGroupDays counts the distinct local days the folded error group
// occurred on within the window (the R3 grain).
func errGroupDays(db *sql.DB, key string, win window) (int, error) {
	rows, err := db.Query(`
		SELECT e.type, e.tool_name, e.payload, e.ts
		  FROM events e
		  JOIN sessions s ON s.id = e.session_id
		  JOIN projects p ON p.id = s.project_id
		 WHERE e.status = 'error'
		   AND e.type IN ('error','tool_call','skill_use','subagent_start','subagent_stop','test_run')
		   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`,
		win.From, win.To)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	days := map[string]struct{}{}
	for rows.Next() {
		var typ, ts string
		var toolName, payload sql.NullString
		if err := rows.Scan(&typ, &toolName, &payload, &ts); err != nil {
			return 0, err
		}
		if normalizeErrKey(extractErrMsg(typ, toolName, payload)) != key {
			continue
		}
		if day, ok := localDay(ts); ok {
			days[day] = struct{}{}
		}
	}
	return len(days), rows.Err()
}

// improvementStillOpen reports whether the R5 target (external task id +
// '#' + retro_improvements rowid) still names an open high-priority
// improvement.
func improvementStillOpen(db *sql.DB, target string) (bool, error) {
	idx := strings.LastIndexByte(target, '#')
	if idx < 0 {
		return false, fmt.Errorf("malformed R5 target %q", target)
	}
	rowid, err := strconv.ParseInt(target[idx+1:], 10, 64)
	if err != nil {
		return false, fmt.Errorf("malformed R5 target %q: %w", target, err)
	}
	var priority, status sql.NullString
	err = db.QueryRow(`SELECT priority, status FROM retro_improvements WHERE id = ?`, rowid).
		Scan(&priority, &status)
	if err == sql.ErrNoRows {
		return false, nil // row gone (rescan replaced it) — treat as resolved
	}
	if err != nil {
		return false, err
	}
	return r5PriorityRe.MatchString(priority.String) && !r5DoneRe.MatchString(status.String), nil
}
