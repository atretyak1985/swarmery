package lessons

// Effect verdicts and confounders (honest learning, phase 1 — D2–D5).
//
// A median drop credits a lesson with EVERYTHING that changed between its
// before-runs and its after-runs. Three changes are common enough in this fleet
// to be checked for every measured lesson, deterministically and without an
// LLM:
//
//	lesson        another ACTIVE surprise lesson whose activated_at falls inside
//	              the measured window and whose globs overlap this lesson's —
//	              both lessons were in the after-runs' prompts, so neither owns
//	              the drop alone. Overlap is Matches (surprise.AreaOverlap
//	              underneath), the rule injection selects with; no second matcher.
//	proposal      an agent_change_proposals row APPLIED (status 'applied',
//	              decided_at — there is no applied_at) inside the window, whose
//	              target file overlaps the lesson's globs, OR whose agent is one
//	              of the roles a phase run spawns (phaserun.SpawnedRoles). The
//	              second rule is coarse on purpose — a rewritten executor changes
//	              every phase run — and the confounder's detail says which rule
//	              matched.
//	model_switch  the dominant session model of the before-runs differs from the
//	              after-runs'. "Dominant" is a share of the runs with a known
//	              model at or above ModelShare (0.6 by default), so complexity
//	              routing that mixes tiers on both sides does not flip it on every
//	              lesson. Sessions carry no effort column, so effort is not
//	              compared. Plugin version bumps are deliberately ignored (D3):
//	              core bumps on almost every PR and would confound every lesson.
//
// THE WINDOW is the span the medians already use: from the earliest before-run
// to the latest after-run Effect selected — not a fixed time. Every confounder's
// detail names it. A lesson that is not measured (insufficient) has no window
// and is not checked: its verdict is insufficient whatever happened.
//
// PRECEDENCE: insufficient > confounded > helped (drop ≥ +Epsilon) / hurt
// (drop ≤ −Epsilon) / no_effect. Only helped and hurt score in injection
// ranking (StoredEffectiveness) and only no_effect and hurt may propose an
// 'ineffective' retirement (retire.go); confounded and insufficient do neither.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
)

// EffectVerdict is a lesson's attributable effect on its area's surprise.
type EffectVerdict string

const (
	EffectHelped       EffectVerdict = "helped"
	EffectHurt         EffectVerdict = "hurt"
	EffectNoEffect     EffectVerdict = "no_effect"
	EffectConfounded   EffectVerdict = "confounded"
	EffectInsufficient EffectVerdict = "insufficient"
)

// Confounder kinds.
const (
	ConfoundLesson      = "lesson"
	ConfoundProposal    = "proposal"
	ConfoundModelSwitch = "model_switch"
)

// Confounder is one change inside the measured window that could own the drop.
type Confounder struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	At     string `json:"at"` // RFC3339
	Detail string `json:"detail"`
}

// Window is the measured span: earliest before-run to latest after-run.
type Window struct {
	From, To time.Time
}

func (w Window) contains(t time.Time) bool { return !t.Before(w.From) && !t.After(w.To) }

func (w Window) String() string {
	return w.From.UTC().Format(time.RFC3339) + " → " + w.To.UTC().Format(time.RFC3339)
}

// Verdict applies the precedence to a measured drop. Pure; unit-tested.
func Verdict(drop *float64, confounders []Confounder, epsilon float64) EffectVerdict {
	switch {
	case drop == nil:
		return EffectInsufficient
	case len(confounders) > 0:
		return EffectConfounded
	case *drop >= epsilon:
		return EffectHelped
	case *drop <= -epsilon:
		return EffectHurt
	default:
		return EffectNoEffect
	}
}

// Scores reports whether the verdict is attributable enough to rank by.
func (v EffectVerdict) Scores() bool { return v == EffectHelped || v == EffectHurt }

// SortConfounders orders by at, then kind, then id: the stored order.
func SortConfounders(cs []Confounder) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].At != cs[j].At {
			return cs[i].At < cs[j].At
		}
		if cs[i].Kind != cs[j].Kind {
			return cs[i].Kind < cs[j].Kind
		}
		return cs[i].ID < cs[j].ID
	})
}

// MarshalConfounders is the confounders_json cell: sorted, "[]" for none.
func MarshalConfounders(cs []Confounder) string {
	if len(cs) == 0 {
		return "[]"
	}
	sorted := append([]Confounder(nil), cs...)
	SortConfounders(sorted)
	b, err := json.Marshal(sorted)
	if err != nil { // a slice of string structs cannot fail; keep the cell valid anyway
		return "[]"
	}
	return string(b)
}

func unmarshalConfounders(s string) []Confounder {
	out := []Confounder{}
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []Confounder{}
	}
	return out
}

// ── the three detectors (pure; unit-tested) ──

// lessonConfounders: other active lessons activated inside the window whose
// globs overlap self's.
func lessonConfounders(self Active, others []Active, w Window) []Confounder {
	var out []Confounder
	for _, o := range others {
		if o.ID == self.ID {
			continue
		}
		at, ok := parseTS(o.ActivatedAt)
		if !ok || !w.contains(at) {
			continue
		}
		if !Matches(self.AreaGlobs, Scope{Areas: o.AreaGlobs}) && !Matches(o.AreaGlobs, Scope{Areas: self.AreaGlobs}) {
			continue
		}
		title := strings.TrimSpace(o.Title)
		if title == "" {
			title = Ref(o.ID)
		}
		out = append(out, Confounder{Kind: ConfoundLesson, ID: Ref(o.ID), At: at.Format(time.RFC3339),
			Detail: fmt.Sprintf("%s %q was activated in the same area (%s) inside the measured window %s",
				Ref(o.ID), title, strings.Join(o.AreaGlobs, ", "), w)})
	}
	return out
}

// AppliedProposal is one applied agent_change_proposals row as the detector reads it.
type AppliedProposal struct {
	ID         int64
	Agent      string
	TargetKind string
	Path       string // target_path, or agent_path for an agent row (its target_path is '')
	DecidedAt  string
}

// proposalConfounders: applied proposals inside the window whose target
// overlaps the lesson's globs, or whose agent is a role phase runs spawn.
func proposalConfounders(self Active, ps []AppliedProposal, roles []string, w Window) []Confounder {
	spawned := map[string]bool{}
	for _, r := range roles {
		spawned[r] = true
	}
	var out []Confounder
	for _, p := range ps {
		at, ok := parseTS(p.DecidedAt)
		if !ok || !w.contains(at) {
			continue
		}
		var rule string
		switch {
		case p.Path != "" && Matches(self.AreaGlobs, Scope{Files: []string{p.Path}}):
			rule = fmt.Sprintf("its target %s overlaps the lesson's area", p.Path)
		case spawned[p.Agent]:
			rule = fmt.Sprintf("%s is a role phase runs spawn", p.Agent)
		default:
			continue
		}
		kind := p.TargetKind
		if kind == "" {
			kind = "agent"
		}
		out = append(out, Confounder{Kind: ConfoundProposal, ID: strconv.FormatInt(p.ID, 10), At: at.Format(time.RFC3339),
			Detail: fmt.Sprintf("%s proposal #%d for %s was applied inside the measured window %s: %s",
				kind, p.ID, p.Agent, w, rule)})
	}
	return out
}

// dominantModel is the model at least share of the runs with a known model ran
// on; "" when no model reaches it. n/of are its count and the known total.
func dominantModel(runs []AreaRun, share float64) (model string, n, of int) {
	counts := map[string]int{}
	for _, r := range runs {
		if m := modelid.Base(r.Model); m != "" {
			counts[m]++
			of++
		}
	}
	if of == 0 {
		return "", 0, 0
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if c := counts[k]; float64(c)/float64(of) >= share && c > n {
			model, n = k, c
		}
	}
	if model == "" {
		return "", 0, of
	}
	return model, n, of
}

// modelSwitchConfounder: the dominant model of the before-runs differs from the
// after-runs'. at is the first after-run on the new model.
func modelSwitchConfounder(pre, post []AreaRun, share float64, w Window) []Confounder {
	mb, nb, ofb := dominantModel(pre, share)
	ma, na, ofa := dominantModel(post, share)
	if mb == "" || ma == "" || mb == ma {
		return nil
	}
	at := w.To
	for _, r := range post {
		if modelid.Base(r.Model) == ma {
			at = r.At
			break
		}
	}
	return []Confounder{{Kind: ConfoundModelSwitch, ID: mb + "→" + ma, At: at.UTC().Format(time.RFC3339),
		Detail: fmt.Sprintf("the runs switched model inside the measured window %s: %s on %d/%d runs before, %s on %d/%d after",
			w, mb, nb, ofb, ma, na, ofa)}}
}

// ── loading what the detectors read ──

// confoundSource is the database state the detectors read for one pass.
type confoundSource struct {
	lessons   []Active
	proposals []AppliedProposal
	roles     []string
}

func loadConfoundSource(db *sql.DB) (confoundSource, error) {
	src := confoundSource{roles: phaserun.SpawnedRoles()}
	rows, err := db.Query(`SELECT id, title, area_globs, activated_at FROM surprise_lessons
		WHERE status = ? AND activated_at IS NOT NULL ORDER BY id`, StatusActive)
	if err != nil {
		return src, err
	}
	for rows.Next() {
		var (
			a     Active
			globs string
		)
		if err := rows.Scan(&a.ID, &a.Title, &globs, &a.ActivatedAt); err != nil {
			rows.Close()
			return src, err
		}
		a.AreaGlobs = splitGlobs(globs)
		src.lessons = append(src.lessons, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return src, err
	}
	rows, err = db.Query(`SELECT id, agent, target_kind, COALESCE(NULLIF(target_path, ''), agent_path, ''),
		COALESCE(decided_at, '') FROM agent_change_proposals WHERE status = 'applied' ORDER BY id`)
	if err != nil {
		return src, err
	}
	defer rows.Close()
	for rows.Next() {
		var p AppliedProposal
		if err := rows.Scan(&p.ID, &p.Agent, &p.TargetKind, &p.Path, &p.DecidedAt); err != nil {
			return src, err
		}
		src.proposals = append(src.proposals, p)
	}
	return src, rows.Err()
}

// confounders runs the three detectors for one measured lesson.
func (src confoundSource) confounders(l Active, pre, post []AreaRun, share float64) []Confounder {
	if len(pre) == 0 || len(post) == 0 {
		return nil
	}
	w := Window{From: pre[0].At, To: post[len(post)-1].At}
	var out []Confounder
	out = append(out, lessonConfounders(l, src.lessons, w)...)
	out = append(out, proposalConfounders(l, src.proposals, src.roles, w)...)
	out = append(out, modelSwitchConfounder(pre, post, share, w)...)
	SortConfounders(out)
	return out
}
