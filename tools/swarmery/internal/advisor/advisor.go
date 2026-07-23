// Package advisor is the deterministic (no LLM) rule engine of the retro
// improvement loop (phase 3): it folds aggregates already in SQLite into
// evidenced improvement recommendations and drives their lifecycle
//
//	proposed → accepted|dismissed → adopted → verified
//
// Run() evaluates the seven rules (rules.go) over the trailing 14-day window,
// upserts recommendations under the dedup contract, auto-detects adoption
// and verification. The daemon calls Run at startup and on a 24h ticker;
// POST /api/retro/advise calls it on demand.
//
// Per-kind lifecycle matrix (adoption signal + verification anchor):
//
//	kind        rules  adoption signal                  verification
//	agent       R2 R4  the target agent's CURRENT       metric ≥ VerifyImprovement
//	                   registry version was created     better over [adopted_at, now),
//	                   after accepted_at                ≥ VerifyAfterDays after adoption
//	                   (a target ABSENT from the registry — e.g. an ad-hoc ledger
//	                   label — has no adoption signal and verifies directly from
//	                   accepted, like error_group/config)
//	tool        R1     an enabled approval_rules row    same, anchored on adopted_at
//	                   covering the tool was created
//	                   after accepted_at
//	process     R5     the referenced retro_improve-    ≥ VerifyAfterDays after adoption
//	                   ments row's status flipped to    with the status STILL done →
//	                   done/closed/виконано             verified (no metric math)
//	error_group R3     none detectable — the rec        metric ≥ VerifyImprovement
//	config      R6     stays accepted                   better over [accepted_at, now),
//	                                                    ≥ VerifyAfterDays after
//	                                                    acceptance (skips adopted)
//
// Verification never fires on absence of data: each metric carries an
// activity floor (R1 ≥1 tool call, R2 ≥R2MinRuns runs, R4 ≥R4MinRows ledger
// rows, R6 >0 input tokens); a post window under the floor records the
// "insufficient post-adoption traffic" evidence note and the status stays
// put. Count metrics (R1 denied, R3 distinct error days) are normalized to
// per-day rates so baseline and post windows of different lengths compare
// fairly; ratio metrics (R2, R4, R6) need no normalization.
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
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
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
// detected, and compared against during verification. PerDay marks count
// metrics normalized to per-day rates (R1, R3); WindowDays is the explicit
// snapshot-window length the value was computed over.
type baseline struct {
	Metric     string  `json:"metric"`
	Value      float64 `json:"value"`
	PerDay     bool    `json:"per_day"`
	WindowDays float64 `json:"window_days"`
	Window     window  `json:"window"`
	AcceptedAt string  `json:"accepted_at,omitempty"`
	AdoptedAt  string  `json:"adopted_at,omitempty"`
}

// windowDaysOf is the window length in (fractional) days, 0 when unreadable.
func windowDaysOf(win window) float64 {
	from, err1 := time.Parse(time.RFC3339, win.From)
	to, err2 := time.Parse(time.RFC3339, win.To)
	if err1 != nil || err2 != nil {
		return 0
	}
	d := to.Sub(from).Hours() / 24
	if d < 0 {
		return 0
	}
	return d
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
		{"R7", func() ([]finding, error) { return r7StaleArchitectureMap(db, win, now) }},
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
		// The status predicate re-checks what we just read so a concurrent
		// dismiss/verify between the SELECT and this UPDATE wins the race.
		if _, err := db.Exec(`
			UPDATE recommendations
			   SET title = ?, detail = ?, evidence = ?, updated_at = ?
			 WHERE id = ? AND status IN ('proposed', 'accepted', 'adopted')`,
			f.title, f.detail, string(ev), nowS, id); err != nil {
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
			 WHERE id = ? AND status = 'dismissed'`, f.title, f.detail, string(ev), nowS, id); err != nil {
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

// accRec is one accepted recommendation with its parsed baseline snapshot.
type accRec struct {
	id     int64
	target string
	b      baseline
}

// acceptedRecs loads the accepted recommendations of one target kind whose
// baseline parses and carries accepted_at. Malformed baseline JSON is logged
// (never silently swallowed); rows without a snapshot are skipped.
func acceptedRecs(db *sql.DB, kind string) ([]accRec, error) {
	rows, err := db.Query(`
		SELECT id, target, baseline FROM recommendations
		 WHERE status = 'accepted' AND target_kind = ?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []accRec
	for rows.Next() {
		var id int64
		var target string
		var base sql.NullString
		if err := rows.Scan(&id, &target, &base); err != nil {
			return nil, err
		}
		if !base.Valid {
			continue // accepted without a baseline snapshot — nothing to compare
		}
		var b baseline
		if err := json.Unmarshal([]byte(base.String), &b); err != nil {
			log.Printf("warn: advisor: rec %d: malformed baseline json: %v", id, err)
			continue
		}
		if b.AcceptedAt == "" {
			continue
		}
		out = append(out, accRec{id: id, target: target, b: b})
	}
	return out, rows.Err()
}

// markAdopted is the guarded accepted → adopted transition. The prior-status
// predicate makes a concurrent dismiss (or any other writer) win the race:
// 0 rows affected → the flip silently does not happen.
func markAdopted(db *sql.DB, id int64, baselineJSON, nowS string) (bool, error) {
	res, err := db.Exec(`
		UPDATE recommendations SET status = 'adopted', baseline = ?, updated_at = ?
		 WHERE id = ? AND status = 'accepted'`, baselineJSON, nowS, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// adoptRec stamps adopted_at into the baseline and performs the guarded flip.
func adoptRec(db *sql.DB, r accRec, now time.Time, stats *Stats) error {
	b := r.b
	b.AdoptedAt = fmtTS(now)
	nb, err := json.Marshal(b)
	if err != nil {
		return err
	}
	flipped, err := markAdopted(db, r.id, string(nb), fmtTS(now))
	if err != nil {
		return err
	}
	if flipped {
		stats.Adopted++
	}
	return nil
}

// detectAdoption advances accepted → adopted per the kind matrix at the top
// of this file (agent: registry version, tool: covering approval rule,
// process: improvement flipped to done). error_group/config have no
// detectable adoption signal — verify() picks them up directly from accepted.
func detectAdoption(db *sql.DB, now time.Time, stats *Stats) error {
	if err := detectAgentAdoption(db, now, stats); err != nil {
		return err
	}
	if err := detectToolAdoption(db, now, stats); err != nil {
		return err
	}
	return detectProcessAdoption(db, now, stats)
}

// detectAgentAdoption flips accepted → adopted for agent-kind
// recommendations whose target agent's CURRENT registry version was created
// after the acceptance (accepted_at inside the baseline JSON, written by the
// PATCH handler) — i.e. someone actually changed the agent definition.
func detectAgentAdoption(db *sql.DB, now time.Time, stats *Stats) error {
	recs, err := acceptedRecs(db, "agent")
	if err != nil || len(recs) == 0 {
		return err
	}
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

	for _, r := range recs {
		acceptedAt, perr := time.Parse(time.RFC3339, r.b.AcceptedAt)
		if perr != nil {
			continue
		}
		ver, ok := versions[r.target]
		if !ok || !ver.After(acceptedAt) {
			continue
		}
		if err := adoptRec(db, r, now, stats); err != nil {
			return err
		}
	}
	return nil
}

// detectToolAdoption flips accepted → adopted for tool-kind (R1)
// recommendations once an ENABLED approval rule covering the tool exists with
// created_at after the acceptance — the friction the rec flagged was actually
// addressed with a rule (whether via the one-click board button or manually).
func detectToolAdoption(db *sql.DB, now time.Time, stats *Stats) error {
	recs, err := acceptedRecs(db, "tool")
	if err != nil || len(recs) == 0 {
		return err
	}
	rows, err := db.Query(`SELECT tool_pattern, created_at FROM approval_rules WHERE enabled = 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type ruleRow struct {
		rp      approvals.RulePattern
		created time.Time
	}
	var rules []ruleRow
	for rows.Next() {
		var pat, createdAt string
		if err := rows.Scan(&pat, &createdAt); err != nil {
			return err
		}
		rp, perr := approvals.ParseRulePattern(pat)
		if perr != nil {
			continue // unparseable rows are skipped, mirroring the evaluator
		}
		t, terr := time.Parse(time.RFC3339, createdAt)
		if terr != nil {
			continue
		}
		rules = append(rules, ruleRow{rp: rp, created: t})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range recs {
		acceptedAt, perr := time.Parse(time.RFC3339, r.b.AcceptedAt)
		if perr != nil {
			continue
		}
		// Coverage check reuses the ruleCoversTool twin over ONLY the rules
		// created after acceptance — a pre-existing rule is not adoption.
		var newer []approvals.RulePattern
		for _, rr := range rules {
			if rr.created.After(acceptedAt) {
				newer = append(newer, rr.rp)
			}
		}
		if !ruleCoversTool(newer, r.target) {
			continue
		}
		if err := adoptRec(db, r, now, stats); err != nil {
			return err
		}
	}
	return nil
}

// improvementDone reports whether the R5 target's retro_improvements row now
// carries a done-family status (the adoption AND verification signal for
// process recommendations). A vanished row is NOT done — absence of the row
// is absence of evidence.
func improvementDone(db *sql.DB, target string) (bool, error) {
	idx := strings.LastIndexByte(target, '#')
	if idx < 0 {
		return false, fmt.Errorf("malformed R5 target %q", target)
	}
	rowid, err := strconv.ParseInt(target[idx+1:], 10, 64)
	if err != nil {
		return false, fmt.Errorf("malformed R5 target %q: %w", target, err)
	}
	var status sql.NullString
	err = db.QueryRow(`SELECT status FROM retro_improvements WHERE id = ?`, rowid).Scan(&status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return r5DoneRe.MatchString(status.String), nil
}

// detectProcessAdoption flips accepted → adopted for process-kind (R5)
// recommendations whose referenced improvement's status now matches the
// done-family vocabulary — the stale improvement was actually acted on.
func detectProcessAdoption(db *sql.DB, now time.Time, stats *Stats) error {
	recs, err := acceptedRecs(db, "process")
	if err != nil {
		return err
	}
	for _, r := range recs {
		done, derr := improvementDone(db, r.target)
		if derr != nil {
			return derr
		}
		if !done {
			continue
		}
		if err := adoptRec(db, r, now, stats); err != nil {
			return err
		}
	}
	return nil
}

// ── verification ──────────────────────────────────────────────────────────

// markVerified is the guarded terminal transition. The status predicate
// covers BOTH verification entry points of the kind matrix — adopted
// (agent/tool/process) and accepted (error_group/config, which have no
// adoption signal) — and makes a concurrent dismiss win the race.
func markVerified(db *sql.DB, id int64, nowS string) (bool, error) {
	res, err := db.Exec(`
		UPDATE recommendations SET status = 'verified', updated_at = ?
		 WHERE id = ? AND status IN ('adopted', 'accepted')`, nowS, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// noteEvidence folds a verification observation into the rec's evidence JSON
// (note + optional post_adoption payload) without touching its status.
func noteEvidence(db *sql.DB, id int64, evidence, note string, post map[string]any, nowS string) error {
	var ev map[string]any
	if err := json.Unmarshal([]byte(evidence), &ev); err != nil || ev == nil {
		ev = map[string]any{}
	}
	ev["note"] = note
	if post != nil {
		ev["post_adoption"] = post
	}
	nb, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE recommendations SET evidence = ?, updated_at = ? WHERE id = ?`,
		string(nb), nowS, id)
	return err
}

// verify advances the lifecycle's last hop per the kind matrix at the top of
// this file. Metric kinds (agent/tool from adopted, error_group/config
// directly from accepted) recompute the metric over the post window once
// VerifyAfterDays have passed since their anchor: an improvement ≥
// VerifyImprovement vs the baseline flips them to verified. Process (R5)
// recommendations verify without metric math once the improvement stays done
// VerifyAfterDays after adoption. A post window under the metric's activity
// floor never verifies — absence of data is not improvement; it records the
// "insufficient post-adoption traffic" note and the status stays put.
func verify(db *sql.DB, now time.Time, stats *Stats) error {
	rows, err := db.Query(`
		SELECT id, rule, target_kind, target, baseline, evidence, status
		  FROM recommendations
		 WHERE (status = 'adopted' AND target_kind IN ('agent', 'tool', 'process'))
		    OR (status = 'accepted' AND target_kind IN ('agent', 'error_group', 'config'))`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type rec struct {
		id                               int64
		rule, targetKind, target, status string
		baseline, evidence               string
	}
	var recs []rec
	for rows.Next() {
		var r rec
		var base sql.NullString
		if err := rows.Scan(&r.id, &r.rule, &r.targetKind, &r.target, &base, &r.evidence, &r.status); err != nil {
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

	// Registry-known agent names (folded): accepted agent-kind recs for these
	// wait for adoption; a target ABSENT from the registry (e.g. an ad-hoc
	// delegation-ledger label that never had an agent file) has no adoption
	// signal, so it verifies directly from accepted like error_group/config.
	registryAgents := map[string]bool{}
	if arows, aerr := db.Query(`SELECT name FROM agents WHERE deleted = 0`); aerr == nil {
		for arows.Next() {
			var name string
			if err := arows.Scan(&name); err == nil {
				registryAgents[normAgent(name)] = true
			}
		}
		arows.Close()
	}

	nowS := fmtTS(now)
	for _, r := range recs {
		if r.status == "accepted" && r.targetKind == "agent" && registryAgents[r.target] {
			continue // registry agent — adoption (a version bump) is the signal to wait for
		}
		var b baseline
		if err := json.Unmarshal([]byte(r.baseline), &b); err != nil {
			log.Printf("warn: advisor: rec %d: malformed baseline json: %v", r.id, err)
			continue
		}
		// Anchor: adopted_at for adoption-capable kinds, accepted_at for the
		// direct-verify kinds (error_group/config skip adopted entirely).
		anchor := b.AdoptedAt
		if r.status == "accepted" {
			anchor = b.AcceptedAt
		}
		if anchor == "" {
			continue
		}
		anchorT, perr := time.Parse(time.RFC3339, anchor)
		if perr != nil || now.Sub(anchorT) < VerifyAfterDays*24*time.Hour {
			continue
		}

		if r.targetKind == "process" {
			// R5: no metric math — verified iff the improvement is STILL done.
			done, derr := improvementDone(db, r.target)
			if derr != nil {
				return derr
			}
			if !done {
				continue // re-opened — stay adopted until it settles
			}
			if err := noteEvidence(db, r.id, r.evidence, "improvement marked done", nil, nowS); err != nil {
				return err
			}
			flipped, verr := markVerified(db, r.id, nowS)
			if verr != nil {
				return verr
			}
			if flipped {
				stats.Verified++
			}
			continue
		}

		post := window{From: fmtTS(anchorT), To: nowS}
		curName, cur, ok, err := metricValue(db, r.rule, r.target, post)
		if err != nil {
			return err
		}
		// Metric-version self-healing: when a rule's metric is redefined (its
		// metricValue name changes), baselines snapshotted under the OLD
		// definition are numerically incompatible with the recomputed value.
		// Instead of comparing incompatible numbers, re-snapshot the baseline
		// under the current definition (preserving the accepted_at/adopted_at
		// lifecycle anchors) and skip comparison this cycle — verification
		// resumes next Run against the fresh baseline. Any future metric
		// redefinition re-baselines in-flight recs for free.
		if curName != b.Metric {
			if rerr := rebaseline(db, r.id, r.rule, r.target, b, now); rerr != nil {
				return rerr
			}
			log.Printf("advisor: rec %d: baseline metric changed (%s -> %s), re-baselined",
				r.id, b.Metric, curName)
			continue
		}
		if !ok {
			// I1: the post window lacks the metric's activity floor — never
			// verify on absence of data; record the observation and stay put.
			if err := noteEvidence(db, r.id, r.evidence, "insufficient post-adoption traffic",
				map[string]any{"window": post, "metric": b.Metric}, nowS); err != nil {
				return err
			}
			continue
		}
		if relImprovement(r.rule, b.Value, cur) >= VerifyImprovement {
			flipped, verr := markVerified(db, r.id, nowS)
			if verr != nil {
				return verr
			}
			if flipped {
				stats.Verified++
			}
			continue
		}
		// Not there yet — record the observation, status untouched.
		if err := noteEvidence(db, r.id, r.evidence, "no measurable improvement yet",
			map[string]any{"window": post, "metric": b.Metric, "value": cur}, nowS); err != nil {
			return err
		}
	}
	return nil
}

// rebaseline rewrites a rec's baseline as a fresh BaselineFor-style snapshot
// over the trailing WindowDays window ending now, preserving the lifecycle
// anchors (accepted_at/adopted_at) of the stale baseline it replaces — the
// verify() self-healing path for metric redefinitions.
func rebaseline(db *sql.DB, id int64, rule, target string, old baseline, now time.Time) error {
	win := window{From: fmtTS(now.AddDate(0, 0, -WindowDays)), To: fmtTS(now)}
	name, value, _, err := metricValue(db, rule, target, win)
	if err != nil {
		return err
	}
	b := baseline{
		Metric: name, Value: value,
		PerDay: perDayRule(rule), WindowDays: windowDaysOf(win),
		Window: win, AcceptedAt: old.AcceptedAt, AdoptedAt: old.AdoptedAt,
	}
	j, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE recommendations SET baseline = ?, updated_at = ? WHERE id = ?`,
		string(j), fmtTS(now), id)
	return err
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

// perDayRule reports whether the rule's metric is a per-day rate — count
// metrics normalized by window length so windows of different lengths
// compare fairly. Ratio metrics (R2/R4/R6) and the R5 flag are not.
func perDayRule(rule string) bool { return rule == "R1" || rule == "R3" }

// BaselineFor computes the rule's metric snapshot over the trailing
// WindowDays window ending now and returns the baseline JSON (metric name +
// value + per_day/window_days + window + accepted_at=now). The PATCH handler
// calls this when a recommendation flips to accepted; adoption detection
// later compares adoption signals against accepted_at.
func BaselineFor(db *sql.DB, rule, target string, now time.Time) (string, error) {
	win := window{From: fmtTS(now.AddDate(0, 0, -WindowDays)), To: fmtTS(now)}
	name, value, _, err := metricValue(db, rule, target, win)
	if err != nil {
		return "", err
	}
	b := baseline{
		Metric: name, Value: value,
		PerDay: perDayRule(rule), WindowDays: windowDaysOf(win),
		Window: win, AcceptedAt: fmtTS(now),
	}
	j, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return string(j), nil
}

// metricValue computes one rule's scalar metric for a target over a window —
// shared by BaselineFor (accept-time snapshot) and verify (post-adoption
// recompute), so both sides of the comparison use identical math. Count
// metrics (R1, R3) are normalized to per-day rates. ok=false means the
// window lacks the rule's activity floor (R1: no tool calls at all, R2: runs
// < R2MinRuns, R4: ledger rows < R4MinRows, R6: no input tokens) — the value
// then carries no signal and verification must not act on it.
func metricValue(db *sql.DB, rule, target string, win window) (name string, value float64, ok bool, err error) {
	wd := windowDaysOf(win)
	switch rule {
	case "R1":
		var calls, denied int64
		err = db.QueryRow(`
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN e.status = 'denied' THEN 1 ELSE 0 END), 0)
			  FROM events e
			  JOIN sessions s ON s.id = e.session_id
			  JOIN projects p ON p.id = s.project_id
			 WHERE e.tool_name = ?
			   AND e.type IN ('tool_call', 'skill_use', 'subagent_start')
			   AND e.ts >= ? AND e.ts < ? AND p.archived = 0`,
			target, win.From, win.To).Scan(&calls, &denied)
		if err != nil || calls < 1 || wd <= 0 {
			return "denied_per_day", 0, false, err
		}
		return "denied_per_day", float64(denied) / wd, true, nil
	case "R2":
		// behavior_failed_run_share is the behavior-failed-run share (distinct
		// runs with ≥1 BehaviorFixable error / runs) — the same grain as
		// r2AgentErrorRate and the Retro scorecards; infra noise and harness
		// mechanics are excluded (Classify). Clamped to ≤1: a run spanning the
		// window start can contribute a failed run without contributing to the
		// run count. The rename from failed_run_share makes pre-classification
		// baselines auto-rebaseline on the metric-name mismatch.
		acc, aerr := agentErrorWindow(db, win)
		if aerr != nil {
			return "behavior_failed_run_share", 0, false, aerr
		}
		if a, hit := acc[target]; hit && a.runs >= R2MinRuns {
			return "behavior_failed_run_share", min(1, float64(a.behaviorFailedRuns())/float64(a.runs)), true, nil
		}
		return "behavior_failed_run_share", 0, false, nil
	case "R3":
		days, derr := errGroupDays(db, target, win)
		if derr != nil || wd <= 0 {
			return "error_days_per_day", 0, false, derr
		}
		// No activity floor here on purpose: for a recurring-error group the
		// error simply no longer occurring IS the improvement.
		return "error_days_per_day", float64(days) / wd, true, nil
	case "R4":
		shares, serr := delegationShares(db, win)
		if serr != nil {
			return "redispatch_share", 0, false, serr
		}
		if c, hit := shares[target]; hit && c[1] >= R4MinRows {
			return "redispatch_share", float64(c[0]) / float64(c[1]), true, nil
		}
		return "redispatch_share", 0, false, nil
	case "R5":
		open, oerr := improvementStillOpen(db, target)
		if oerr != nil {
			return "open_stale_improvements", 0, false, oerr
		}
		v := 0.0
		if open {
			v = 1
		}
		return "open_stale_improvements", v, true, nil
	case "R6":
		rate, hasTraffic, rerr := cacheHitRate(db, win)
		return "cache_hit_rate", rate, hasTraffic && rerr == nil, rerr
	case "R7":
		// R7 (stale architecture map) has no DB-computable post-adoption metric:
		// staleness is filesystem-grounded. The baseline records ok=false so the
		// verify loop never acts on it; the rule re-proposes naturally if the map
		// stays stale after acceptance.
		return "stale_map", 0, false, nil
	default:
		return "", 0, false, fmt.Errorf("unknown rule %q", rule)
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
