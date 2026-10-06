package api

// The friction Source: recurring error groups (Retro → Friction) as triage
// items. The judge decides noise (a 30-day mute) or fixable (a T1
// recommendation plus a follow-up advisor suggestion the operator accepts).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

const (
	frictionKind    = "friction"
	frictionNoise   = "noise"
	frictionFixable = "fixable"
	// frictionRule is the recommendation rule a fixable verdict files.
	frictionRule = "T1"
	// frictionTitleRunes / frictionPromptBytes cut the judge's payload.
	frictionTitleRunes  = 80
	frictionPromptBytes = 8000

	frictionInstruction = "noise when no change to code, prompts or settings would stop it: a network blip, " +
		"a provider outage, an expected refusal. fixable when a concrete change would stop it; in that case put " +
		"in payload {\"title\": at most 80 characters, \"prompt\": a self-contained task: what is wrong, the " +
		"evidence, what done looks like, how to verify}. skip when the example is too generic to tell " +
		"(\"Bash error\", \"Exit code 1\")."
)

// errAlreadyTracked is Apply's answer when an open recommendation for the
// target appeared since Collect: nothing is filed twice.
var errAlreadyTracked = errors.New("already tracked")

// errRecentlyDismissed is Apply's answer when a recommendation for the target
// — of any rule — was dismissed inside the advisor's suppression window: a run
// does not override the operator's dismissal.
var errRecentlyDismissed = errors.New("dismissed recently")

// triageWindow is the 14-day window the triage sources read — the advisor's
// window and parseRange's default, built without a request.
func triageWindow() dateRange {
	dr, _ := parseRange(&http.Request{URL: &url.URL{}}) // no ?from/?to: cannot fail
	return dr
}

// triageScope is the project predicate for a triage scope (projects p joined).
func triageScope(sc triage.Scope) (string, []any) {
	if sc.ProjectID == 0 {
		return "", nil
	}
	return projectScopePredicate, scopeArgs(strconv.FormatInt(sc.ProjectID, 10))
}

type frictionSource struct {
	h *Handler

	mu     sync.Mutex
	groups map[string]errGroup // the last Collect's groups, for Prepare / Apply
}

var (
	_ triage.Source   = (*frictionSource)(nil)
	_ triage.Preparer = (*frictionSource)(nil)
)

func (*frictionSource) Kind() string { return frictionKind }

// Collect lists the window's untriaged error groups seen at least twice, most
// frequent first, each attributed to its newest sample session's project.
func (s *frictionSource) Collect(_ context.Context, sc triage.Scope, limit int) ([]triage.Item, error) {
	pf, pargs := triageScope(sc)
	all, err := s.h.errorGroups(triageWindow(), pf, pargs)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(all))
	for _, g := range all {
		if g.Count >= 2 {
			keys = append(keys, g.Key)
		}
	}
	states, err := s.h.frictionTriageStates(keys, time.Now())
	if err != nil {
		return nil, err
	}
	suppressed, err := recentlyDismissed(s.h.DB, "error_group", time.Now())
	if err != nil {
		return nil, err
	}
	var groups []errGroup // errorGroups returns count desc already
	for _, g := range all {
		if g.Count >= 2 && states[g.Key].State == frictionUntriaged && !suppressed[g.Key] {
			groups = append(groups, g)
		}
	}
	if limit > 0 && len(groups) > limit {
		groups = groups[:limit]
	}
	projects, err := s.sampleProjects(groups)
	if err != nil {
		return nil, err
	}

	cache := make(map[string]errGroup, len(groups))
	items := make([]triage.Item, 0, len(groups))
	for _, g := range groups {
		cache[g.Key] = g
		var project int64
		if len(g.Samples) > 0 {
			project = projects[g.Samples[0].SessionID]
		}
		items = append(items, triage.Item{
			Kind: frictionKind, Key: g.Key, ProjectID: project,
			Title: frictionTitle(g.Example), WaitingSince: g.LastTs,
			Parts: []triage.Part{{Ref: g.Key, Label: g.Key, Allowed: []string{frictionNoise, frictionFixable}}},
		})
	}
	s.mu.Lock()
	s.groups = cache
	s.mu.Unlock()
	return items, nil
}

func frictionTitle(example string) string {
	if rn := []rune(example); len(rn) > frictionTitleRunes {
		example = string(rn[:frictionTitleRunes])
	}
	return "Recurring error: " + example
}

// sampleProjects resolves the newest sample session of every group to its
// project with one query.
func (s *frictionSource) sampleProjects(groups []errGroup) (map[int64]int64, error) {
	out := map[int64]int64{}
	ids := make([]any, 0, len(groups))
	for _, g := range groups {
		if len(g.Samples) > 0 {
			ids = append(ids, g.Samples[0].SessionID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.h.DB.Query(`SELECT id, project_id FROM sessions WHERE project_id IS NOT NULL AND id IN (?`+
		strings.Repeat(",?", len(ids)-1)+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, project int64
		if err := rows.Scan(&id, &project); err != nil {
			return nil, err
		}
		out[id] = project
	}
	return out, rows.Err()
}

// group returns the error group for key from the last Collect, or recomputes
// the fleet-wide window when the cache does not hold it.
func (s *frictionSource) group(key string) (errGroup, error) {
	s.mu.Lock()
	g, ok := s.groups[key]
	s.mu.Unlock()
	if ok {
		return g, nil
	}
	all, err := s.h.errorGroups(triageWindow(), "", nil)
	if err != nil {
		return errGroup{}, err
	}
	for _, g := range all {
		if g.Key == key {
			return g, nil
		}
	}
	return errGroup{}, fmt.Errorf("error group %q is no longer in the window", key)
}

// Prepare fills the judge's instruction and the group's evidence: key,
// example, count, last seen and the sample sessions with their projects.
func (s *frictionSource) Prepare(_ context.Context, it *triage.Item) error {
	g, err := s.group(it.Key)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "key: %s\nexample: %s\ncount: %d\nlast seen: %s\nsample sessions:\n", g.Key, g.Example, g.Count, g.LastTs)
	for _, sm := range g.Samples {
		var title, project sql.NullString
		if err := s.h.DB.QueryRow(`SELECT s.title, COALESCE(p.name, p.slug) FROM sessions s
			LEFT JOIN projects p ON p.id = s.project_id WHERE s.id = ?`, sm.SessionID).Scan(&title, &project); err != nil &&
			!errors.Is(err, sql.ErrNoRows) {
			return err
		}
		fmt.Fprintf(&b, "- %q in project %s\n", title.String, project.String)
	}
	it.Instruction, it.Evidence = frictionInstruction, b.String()
	return nil
}

// frictionFix is the judge's fixable payload.
type frictionFix struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
}

// parseFrictionFix validates the (untrusted) payload and cuts it to size.
func parseFrictionFix(payload json.RawMessage) (frictionFix, error) {
	var f frictionFix
	if err := json.Unmarshal(payload, &f); err != nil {
		return frictionFix{}, fmt.Errorf("fixable payload: %w", err)
	}
	f.Title, f.Prompt = strings.TrimSpace(f.Title), strings.TrimSpace(f.Prompt)
	if f.Title == "" || f.Prompt == "" {
		return frictionFix{}, errors.New("fixable payload: title and prompt are required")
	}
	if rn := []rune(f.Title); len(rn) > frictionTitleRunes {
		f.Title = string(rn[:frictionTitleRunes])
	}
	f.Prompt = cutUTF8(f.Prompt, frictionPromptBytes)
	return f, nil
}

func (s *frictionSource) Apply(_ context.Context, it triage.Item, p triage.Part, value, reason string, payload json.RawMessage) (triage.Applied, error) {
	switch value {
	case frictionNoise:
		if err := triage.Mute(s.h.DB, p.Ref, reason, 0, time.Now()); err != nil {
			return triage.Applied{}, err
		}
		return triage.Applied{Prior: json.RawMessage(`{}`)}, nil
	case frictionFixable:
		fix, err := parseFrictionFix(payload)
		if err != nil {
			return triage.Applied{}, err
		}
		g, err := s.group(p.Ref)
		if err != nil {
			return triage.Applied{}, err
		}
		sessions := make([]string, 0, len(g.Samples))
		for _, sm := range g.Samples {
			sessions = append(sessions, sm.SessionUUID)
		}
		ev, err := json.Marshal(map[string]any{"count": g.Count, "last_ts": g.LastTs, "sessions": sessions, "example": g.Example})
		if err != nil {
			return triage.Applied{}, err
		}
		id, err := fileTriageRec(s.h.DB, frictionRule, "error_group", p.Ref, it.Title, reason, ev)
		if err != nil {
			return triage.Applied{}, err
		}
		ref := strconv.FormatInt(id, 10)
		follow := triage.Suggestion{Kind: advisorKind, Class: triage.ClassPlain, Ref: ref, ItemKey: ref,
			Title: it.Title, Value: "track", Reason: reason}
		if it.ProjectID != 0 { // a card needs a project
			card, err := json.Marshal(fix)
			if err != nil {
				return triage.Applied{}, err
			}
			follow.Class, follow.Value, follow.Payload, follow.ProjectID = triage.ClassCard, "fix-card", card, it.ProjectID
		}
		return triage.Applied{Prior: recPrior(id), Follow: []triage.Suggestion{follow}}, nil
	}
	return triage.Applied{}, fmt.Errorf("friction: unknown value %q", value)
}

// Undo is idempotent: a missing mute or an already-closed recommendation is nil.
// A noise verdict lifts the mute only while it is the newest applied noise
// verdict on the group: a later run that muted the group again owns the mute.
func (s *frictionSource) Undo(_ context.Context, v triage.Verdict) error {
	switch v.Value {
	case frictionNoise:
		newer, err := triage.AppliedAfter(s.h.DB, frictionKind, v.Ref, frictionNoise, v.ID)
		if err != nil || newer {
			return err
		}
		_, _, err = triage.Unmute(s.h.DB, v.Ref)
		return err
	case frictionFixable:
		return dismissTriageRec(s.h.DB, v.Prior)
	}
	return fmt.Errorf("friction: unknown value %q", v.Value)
}

// Open: the group still waits while its derived state is untriaged.
func (s *frictionSource) Open(_ context.Context, ref string) (bool, error) {
	st, err := s.h.frictionTriageStates([]string{ref}, time.Now())
	if err != nil {
		return false, err
	}
	return st[ref].State == frictionUntriaged, nil
}

// --- recommendation helpers shared by the friction and agent sources ---

func recPrior(id int64) json.RawMessage {
	return json.RawMessage(`{"recommendationId":` + strconv.FormatInt(id, 10) + `}`)
}

// dismissTriageRec dismisses the recommendation a verdict filed while it is
// still proposed; zero rows (already dismissed, accepted, gone) is nil.
func dismissTriageRec(db *sql.DB, prior json.RawMessage) error {
	var p struct {
		RecommendationID int64 `json:"recommendationId"`
	}
	if err := json.Unmarshal(prior, &p); err != nil {
		return fmt.Errorf("prior: %w", err)
	}
	if p.RecommendationID == 0 {
		return nil
	}
	_, err := db.Exec(`UPDATE recommendations SET status='dismissed', updated_at=? WHERE id=? AND status='proposed'`,
		advisorNow(), p.RecommendationID)
	return err
}

// rowQuerier is the read both *sql.DB and *sql.Tx offer.
type rowQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// recentlyDismissed is the set of targets of targetKind that carry a
// recommendation — of ANY rule — dismissed within the advisor's suppression
// window. A run does not re-file what the operator (or an undo) turned down,
// whichever rule the dismissed recommendation was filed under. Like the
// advisor, an unreadable timestamp counts as still suppressed.
func recentlyDismissed(q rowQuerier, targetKind string, now time.Time) (map[string]bool, error) {
	rows, err := q.Query(`SELECT target, updated_at FROM recommendations
		WHERE target_kind = ? AND status = 'dismissed'`, targetKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var target, updatedAt string
		if err := rows.Scan(&target, &updatedAt); err != nil {
			return nil, err
		}
		at, perr := time.Parse(time.RFC3339, updatedAt)
		if perr != nil || now.Sub(at) <= advisor.DismissSuppressDays*24*time.Hour {
			out[target] = true
		}
	}
	return out, rows.Err()
}

// fileTriageRec files one proposed recommendation with the advisor's dedup_key
// scheme (rule:target, or rule:target:<n+1> after a verified/resolved row; a
// dismissed row whose suppression window has passed is re-proposed in place).
// Any open recommendation on the target — whatever its rule — is
// errAlreadyTracked; any recommendation on it dismissed inside the suppression
// window — whatever its rule — is errRecentlyDismissed. One transaction.
func fileTriageRec(db *sql.DB, rule, targetKind, target, title, detail string, evidence []byte) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var open int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE target_kind = ? AND target = ?
		AND status IN ('proposed','accepted','adopted')`, targetKind, target).Scan(&open); err != nil {
		return 0, err
	}
	if open > 0 {
		return 0, errAlreadyTracked
	}
	suppressed, err := recentlyDismissed(tx, targetKind, time.Now())
	if err != nil {
		return 0, err
	}
	if suppressed[target] {
		return 0, errRecentlyDismissed
	}
	now := advisorNow()
	insert := func(dedup string) (int64, error) {
		res, err := tx.Exec(`INSERT INTO recommendations
			(rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'proposed', ?, ?, ?)`,
			rule, targetKind, target, title, detail, string(evidence), dedup, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	var id int64
	var status string
	err = tx.QueryRow(`SELECT id, status FROM recommendations WHERE rule = ? AND target = ?
		ORDER BY id DESC LIMIT 1`, rule, target).Scan(&id, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id, err = insert(rule + ":" + target)
	case err != nil:
		return 0, err
	case status == "dismissed":
		_, err = tx.Exec(`UPDATE recommendations SET status = 'proposed', target_kind = ?, title = ?, detail = ?,
			evidence = ?, baseline = NULL, updated_at = ? WHERE id = ? AND status = 'dismissed'`,
			targetKind, title, detail, string(evidence), now, id)
	case status == "verified" || status == "resolved":
		var n int64
		if err = tx.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE rule = ? AND target = ?`,
			rule, target).Scan(&n); err != nil {
			return 0, err
		}
		id, err = insert(rule + ":" + target + ":" + strconv.FormatInt(n+1, 10))
	default:
		return 0, errAlreadyTracked
	}
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
