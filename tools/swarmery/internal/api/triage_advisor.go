package api

// The advisor Source: proposed advisor recommendations as triage items. A run
// dismisses the informational ones on its own (R9 — the session it describes is
// over) and only SUGGESTS for every other class; turning a suggestion into a
// board card, an improve proposal or a commitment is the operator's accept.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/advisor"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

const (
	advisorKind = "advisor"
	// advisorEvidenceMax caps the stored evidence JSON shown to the judge.
	advisorEvidenceMax = 2000
	// advisorInformationalReason is the rule's answer for an informational item.
	advisorInformationalReason = "Informational: the session it describes is over, so nothing in it can change. The advice applies to future sessions."
)

// advisorAllowed is each class's closed value set — the policy table's
// advisor/<class> values, auto and suggest together.
var advisorAllowed = map[string][]string{
	triage.ClassInformational: {"dismiss"},
	triage.ClassCard:          {"fix-card", "dismiss"},
	triage.ClassImprove:       {"improve", "dismiss"},
	triage.ClassPlain:         {"track", "dismiss"},
}

// advisorInstruction is what the judge is asked per class; informational items
// are decided by rule and never judged.
var advisorInstruction = map[string]string{
	triage.ClassCard: "fix-card when an agent can fix this with one task in the project's repository; " +
		"in that case put in payload {\"title\": at most 80 characters, \"prompt\": a self-contained task: " +
		"what is wrong, the evidence, what done looks like, how to verify}. Otherwise dismiss.",
	triage.ClassImprove:       "improve when the evidence points at the agent's or skill's own instructions; otherwise dismiss.",
	triage.ClassPlain:         "track when the operator should commit to fixing it; dismiss when it is noise or already handled.",
	triage.ClassInformational: "",
}

type advisorSource struct{ h *Handler }

var (
	_ triage.Source  = (*advisorSource)(nil)
	_ triage.Decider = (*advisorSource)(nil)
)

func (*advisorSource) Kind() string { return advisorKind }

// Collect lists every proposed recommendation in scope. A scoped collect reuses
// buildRetroRecommendations' project attribution verbatim; a fleet-wide one
// attributes each row with the same two predicates (targetsProject,
// evidenceInProject) against every project at once — see advisorAttribution.
func (s *advisorSource) Collect(_ context.Context, sc triage.Scope, limit int) ([]triage.Item, error) {
	scope := ""
	if sc.ProjectID != 0 {
		scope = strconv.FormatInt(sc.ProjectID, 10)
	}
	recs, err := s.h.buildRetroRecommendations([]string{"proposed"}, scope)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(recs.Recommendations) > limit {
		recs.Recommendations = recs.Recommendations[:limit]
	}
	var attr *advisorAttribution
	if sc.ProjectID == 0 {
		if attr, err = s.h.newAdvisorAttribution(recs.Recommendations); err != nil {
			return nil, err
		}
	}
	registry := s.registry(recs.Recommendations)

	items := make([]triage.Item, 0, len(recs.Recommendations))
	for _, d := range recs.Recommendations {
		project := sc.ProjectID
		if attr != nil {
			project = attr.project(d)
		}
		improvable := d.TargetKind == improve.TargetSkill
		if d.TargetKind == improve.TargetAgent {
			_, improvable = registry[advisor.NormAgent(d.Target)]
		}
		class := triage.AdvisorClass(d.Rule, project != 0, improvable)
		ref := strconv.FormatInt(d.ID, 10)
		items = append(items, triage.Item{
			Kind: advisorKind, Class: class, Key: ref, ProjectID: project,
			Title: d.Title, WaitingSince: d.CreatedAt,
			Instruction: advisorInstruction[class],
			Evidence:    advisorEvidence(d),
			Parts: []triage.Part{{Ref: ref, Label: d.Rule + " " + d.Target,
				Allowed: append([]string(nil), advisorAllowed[class]...)}},
		})
	}
	return items, nil
}

// registry is the improve registry's agent set, read once per collect (the
// same set AgentInRegistry consults, without a git round-trip per row) and
// only when an agent-kind row needs it. A registry that cannot be read leaves
// every agent row non-improvable — plain, never a failed collect.
func (s *advisorSource) registry(recs []recommendationDTO) map[string]struct{} {
	if s.h.Improve == nil {
		return nil
	}
	for _, d := range recs {
		if d.TargetKind != improve.TargetAgent {
			continue
		}
		set, err := s.h.Improve.RegistryAgentSet()
		if err != nil {
			log.Printf("warning: triage: advisor: improve registry: %v", err)
			return nil
		}
		return set
	}
	return nil
}

// advisorEvidence is the judge's view of one recommendation.
func advisorEvidence(d recommendationDTO) string {
	return fmt.Sprintf("rule: %s\ntarget kind: %s\ntarget: %s\ntitle: %s\ndetail: %s\nevidence: %s",
		d.Rule, d.TargetKind, d.Target, d.Title, d.Detail, cutUTF8(string(d.Evidence), advisorEvidenceMax))
}

// cutUTF8 cuts s to at most n bytes without splitting a rune.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// advisorAttribution resolves recommendations to a project fleet-wide with the
// predicates buildRetroRecommendations applies for one project: a project- or
// memory-kind target naming the project's DB slug (targetsProject), else an
// evidence session_id belonging to the project (evidenceInProject). It is built
// with two queries for the whole listing; a row matching several projects goes
// to the target's project, else to the lowest project id among its sessions.
type advisorAttribution struct {
	slugs    map[string]int64              // project slug → id
	sessions map[int64]map[string]struct{} // project id → its evidence sessions
	ids      []int64                       // sessions' keys, ascending
}

func (h *Handler) newAdvisorAttribution(recs []recommendationDTO) (*advisorAttribution, error) {
	a := &advisorAttribution{slugs: map[string]int64{}, sessions: map[int64]map[string]struct{}{}}
	rows, err := h.DB.Query(`SELECT id, slug FROM projects`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			rows.Close()
			return nil, err
		}
		a.slugs[slug] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	var uuids []string
	for _, d := range recs {
		var parsed struct {
			SessionIDs []string `json:"session_ids"`
		}
		if json.Unmarshal(d.Evidence, &parsed) != nil {
			continue
		}
		for _, u := range parsed.SessionIDs {
			if _, dup := seen[u]; !dup {
				seen[u] = struct{}{}
				uuids = append(uuids, u)
			}
		}
	}
	const chunk = 500
	for start := 0; start < len(uuids); start += chunk {
		part := uuids[start:min(start+chunk, len(uuids))]
		args := make([]any, len(part))
		for i, u := range part {
			args[i] = u
		}
		rows, err := h.DB.Query(`SELECT session_uuid, project_id FROM sessions
			WHERE project_id IS NOT NULL AND session_uuid IN (?`+strings.Repeat(",?", len(part)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var u string
			var pid int64
			if err := rows.Scan(&u, &pid); err != nil {
				rows.Close()
				return nil, err
			}
			if a.sessions[pid] == nil {
				a.sessions[pid] = map[string]struct{}{}
			}
			a.sessions[pid][u] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for id := range a.sessions {
		a.ids = append(a.ids, id)
	}
	sort.Slice(a.ids, func(i, j int) bool { return a.ids[i] < a.ids[j] })
	return a, nil
}

// project is the recommendation's project id, 0 when none is attributable.
func (a *advisorAttribution) project(d recommendationDTO) int64 {
	if id, ok := a.slugs[d.Target]; ok && targetsProject(d.TargetKind, d.Target, map[string]struct{}{d.Target: {}}) {
		return id
	}
	for _, id := range a.ids {
		if evidenceInProject(string(d.Evidence), a.sessions[id]) {
			return id
		}
	}
	return 0
}

// Decide answers informational items by rule — no model call.
func (*advisorSource) Decide(it triage.Item) (triage.Answer, bool) {
	if it.Class != triage.ClassInformational {
		return triage.Answer{}, false
	}
	vals := make(map[string]string, len(it.Parts))
	for _, p := range it.Parts {
		vals[p.Ref] = "dismiss"
	}
	return triage.Answer{Values: vals, Reason: advisorInformationalReason}, true
}

// Apply dismisses an informational recommendation that is still proposed. It
// is the backstop behind the policy table: any other class or value is refused.
func (s *advisorSource) Apply(_ context.Context, it triage.Item, p triage.Part, value, _ string, _ json.RawMessage) (triage.Applied, error) {
	if it.Class != triage.ClassInformational || value != "dismiss" {
		return triage.Applied{}, fmt.Errorf("advisor: refusing to apply %q to a %s recommendation", value, it.Class)
	}
	id, err := strconv.ParseInt(p.Ref, 10, 64)
	if err != nil {
		return triage.Applied{}, fmt.Errorf("advisor: bad ref %q", p.Ref)
	}
	res, err := s.h.DB.Exec(`UPDATE recommendations SET status = 'dismissed', updated_at = ?
		WHERE id = ? AND status = 'proposed'`, advisorNow(), id)
	if err != nil {
		return triage.Applied{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return triage.Applied{}, err
	} else if n == 0 {
		return triage.Applied{}, errors.New("recommendation is no longer proposed")
	}
	return triage.Applied{Prior: json.RawMessage(`{"status":"proposed"}`)}, nil
}

// Undo reopens a dismissed recommendation. It writes SQL directly on purpose:
// dismissed → proposed is not a transition the operator's PATCH allows
// (legalRecTransition), and it must stay that way. Idempotent: a row that is
// no longer dismissed (already reopened) is left alone and nil is returned.
func (s *advisorSource) Undo(_ context.Context, v triage.Verdict) error {
	id, err := strconv.ParseInt(v.Ref, 10, 64)
	if err != nil {
		return fmt.Errorf("advisor: bad ref %q", v.Ref)
	}
	_, err = s.h.DB.Exec(`UPDATE recommendations SET status = 'proposed', updated_at = ?
		WHERE id = ? AND status = 'dismissed'`, advisorNow(), id)
	return err
}

// Open reports whether the recommendation is still proposed. Status only: the
// advisor refreshes updated_at on every pass, so a timestamp would always move.
func (s *advisorSource) Open(_ context.Context, ref string) (bool, error) {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return false, nil
	}
	var status string
	err = s.h.DB.QueryRow(`SELECT status FROM recommendations WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "proposed", nil
}

func advisorNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
