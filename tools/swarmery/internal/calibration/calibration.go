// Package calibration answers "how well do our forecasts predict?" per agent,
// model, effort and project (Opus 5.5 / learning-loop phase 16.4).
//
// The input is phase_surprise (0082): one scored run per row, each carrying the
// forecast-vs-actual detail the scorer already computed. Nothing is re-scored
// here; this package only groups and counts. Per group it reports
//
//   - areaHitRate      matched areas / (matched + missed + unexpected), pooled
//     over the group's runs — penalises both directions of an area miss;
//   - bandAccuracy     share of size and duration band comparisons that landed
//     exactly (distance 0), pooled;
//   - outcomeAccuracy  share of runs whose outcome component is 0 (measurable
//     runs only);
//   - meanSurprise     the mean surprise index;
//   - buckets          a confidence calibration curve: the forecast's declared
//     confidence in five equal-width buckets against the share of runs whose
//     forecast HELD (no major miss — the scorer's own definition).
//
// SAMPLE-SIZE GATE. A group with fewer than MinSamples (20) non-post-hoc
// samples is NOT RETURNED: the report carries only a count of hidden groups.
// A calibration curve drawn from a handful of runs is noise that reads as a
// finding, and the UI must never be able to draw it. Post-hoc forecasts (a
// prior written after the Completion Report) are excluded from every group:
// they cannot have been predictions.
package calibration

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

// MinSamples is the gate below which a group is never shown.
const MinSamples = 20

// Dimensions a report may group by.
const (
	DimAgent   = "agent"
	DimModel   = "model"
	DimEffort  = "effort"
	DimProject = "project"
)

// AllDims is the default grouping.
var AllDims = []string{DimAgent, DimModel, DimEffort, DimProject}

// Unknown labels a dimension the run did not record.
const Unknown = "unknown"

// Sample is one scored run.
type Sample struct {
	Agent, Model, Effort, Project string
	Index                         float64
	Components                    map[string]*float64
	Detail                        surprise.Detail
}

// Bucket is one point of the confidence calibration curve.
type Bucket struct {
	Lo             float64 `json:"lo"`
	Hi             float64 `json:"hi"`
	N              int     `json:"n"`
	MeanConfidence float64 `json:"meanConfidence"`
	HeldRate       float64 `json:"heldRate"`
}

// Group is one shown calibration group.
type Group struct {
	Key             map[string]string `json:"key"`
	Samples         int               `json:"samples"`
	AreaHitRate     *float64          `json:"areaHitRate"`
	BandAccuracy    *float64          `json:"bandAccuracy"`
	OutcomeAccuracy *float64          `json:"outcomeAccuracy"`
	MeanSurprise    float64           `json:"meanSurprise"`
	Buckets         []Bucket          `json:"buckets"`
}

// Report is the calibration view.
type Report struct {
	Dims         []string `json:"dims"`
	MinSamples   int      `json:"minSamples"`
	Groups       []Group  `json:"groups"`
	HiddenGroups int      `json:"hiddenGroups"`
	HiddenRuns   int      `json:"hiddenRuns"`
}

// ParseDims validates a comma-separated dimension list ("" = AllDims).
func ParseDims(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return append([]string(nil), AllDims...), nil
	}
	valid := map[string]bool{DimAgent: true, DimModel: true, DimEffort: true, DimProject: true}
	seen := map[string]bool{}
	var out []string
	for _, d := range strings.Split(s, ",") {
		d = strings.ToLower(strings.TrimSpace(d))
		if !valid[d] {
			return nil, fmt.Errorf("unknown calibration dimension %q (want agent, model, effort or project)", d)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out, nil
}

// Load reads every non-post-hoc scored run with its grouping labels. The model
// is the run session's model (the phase doc's **Model:** when the session was
// never ingested); the effort is epic_phases.run_effort when the phase's
// CURRENT run is this one (0086 — earlier runs did not record it); the agent is
// the most frequent named agent in the run's turns, else "main".
func Load(db *sql.DB) ([]Sample, error) {
	rows, err := db.Query(`SELECT ps.surprise_index, ps.components_json, ps.detail_json,
		COALESCE(p.slug, ''),
		COALESCE(NULLIF(s.model, ''), e.doc_model, ''),
		COALESCE(CASE WHEN e.run_session_uuid = ps.session_uuid THEN e.run_effort END, ''),
		COALESCE((SELECT tu.agent_name FROM turns tu WHERE tu.session_id = s.id
		          AND tu.agent_name IS NOT NULL AND tu.agent_name <> ''
		          GROUP BY tu.agent_name ORDER BY COUNT(*) DESC, tu.agent_name LIMIT 1), '')
		FROM phase_surprise ps
		LEFT JOIN epic_phases e ON e.id = ps.phase_id
		LEFT JOIN tasks t ON t.id = e.workspace_task_id
		LEFT JOIN projects p ON p.id = t.project_id
		LEFT JOIN sessions s ON s.session_uuid = ps.session_uuid
		WHERE ps.forecast_post_hoc = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var (
			s             Sample
			comps, detail string
		)
		if err := rows.Scan(&s.Index, &comps, &detail, &s.Project, &s.Model, &s.Effort, &s.Agent); err != nil {
			return nil, err
		}
		s.Components = map[string]*float64{}
		_ = json.Unmarshal([]byte(comps), &s.Components)
		_ = json.Unmarshal([]byte(detail), &s.Detail)
		if i := strings.Index(s.Model, "["); i > 0 {
			s.Model = s.Model[:i] // "claude-x[1m]" → "claude-x": a context window is not a model
		}
		if s.Agent == "" {
			s.Agent = "main"
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func label(v string) string {
	if strings.TrimSpace(v) == "" {
		return Unknown
	}
	return v
}

func (s Sample) key(dims []string) map[string]string {
	k := map[string]string{}
	for _, d := range dims {
		switch d {
		case DimAgent:
			k[d] = label(s.Agent)
		case DimModel:
			k[d] = label(s.Model)
		case DimEffort:
			k[d] = label(s.Effort)
		case DimProject:
			k[d] = label(s.Project)
		}
	}
	return k
}

func keyString(dims []string, k map[string]string) string {
	parts := make([]string, len(dims))
	for i, d := range dims {
		parts[i] = k[d]
	}
	return strings.Join(parts, "\x00")
}

// Build groups samples. Groups under minSamples are counted, never returned.
// Pure; unit-tested.
func Build(samples []Sample, dims []string, minSamples int) Report {
	rep := Report{Dims: dims, MinSamples: minSamples, Groups: []Group{}}
	byKey := map[string][]Sample{}
	keys := map[string]map[string]string{}
	var order []string
	for _, s := range samples {
		k := s.key(dims)
		ks := keyString(dims, k)
		if _, ok := byKey[ks]; !ok {
			order = append(order, ks)
			keys[ks] = k
		}
		byKey[ks] = append(byKey[ks], s)
	}
	sort.Strings(order)
	for _, ks := range order {
		ss := byKey[ks]
		if len(ss) < minSamples {
			rep.HiddenGroups++
			rep.HiddenRuns += len(ss)
			continue
		}
		rep.Groups = append(rep.Groups, summarize(keys[ks], ss))
	}
	sort.SliceStable(rep.Groups, func(i, j int) bool { return rep.Groups[i].Samples > rep.Groups[j].Samples })
	return rep
}

func ratio(n, of int) *float64 {
	if of == 0 {
		return nil
	}
	v := round4(float64(n) / float64(of))
	return &v
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// held is "the forecast held": no axis missed by a major margin — the
// scorer's majorMiss, stored in the detail.
func held(s Sample) bool { return !s.Detail.MajorMiss }

func summarize(key map[string]string, ss []Sample) Group {
	g := Group{Key: key, Samples: len(ss)}
	var matched, areaTotal, bandHit, bandTotal, outHit, outTotal int
	var sum float64
	type acc struct {
		n         int
		conf, hel float64
	}
	buckets := make([]acc, 5)
	for _, s := range ss {
		sum += s.Index
		m := len(s.Detail.MatchedAreas)
		matched += m
		areaTotal += m + len(s.Detail.MissedAreas) + len(s.Detail.UnexpectedAreas)
		for _, d := range []*int{s.Detail.SizeDistance, s.Detail.DurationDistance} {
			if d != nil {
				bandTotal++
				if *d == 0 {
					bandHit++
				}
			}
		}
		if v := s.Components[surprise.CompOutcomeMiss]; v != nil {
			outTotal++
			if *v == 0 {
				outHit++
			}
		}
		if c := s.Detail.Confidence; c != nil && *c >= 0 && *c <= 1 {
			i := int(*c * 5)
			if i > 4 {
				i = 4
			}
			buckets[i].n++
			buckets[i].conf += *c
			if held(s) {
				buckets[i].hel++
			}
		}
	}
	g.AreaHitRate, g.BandAccuracy, g.OutcomeAccuracy = ratio(matched, areaTotal), ratio(bandHit, bandTotal), ratio(outHit, outTotal)
	g.MeanSurprise = round4(sum / float64(len(ss)))
	g.Buckets = []Bucket{}
	for i, b := range buckets {
		if b.n == 0 {
			continue
		}
		g.Buckets = append(g.Buckets, Bucket{Lo: float64(i) / 5, Hi: float64(i+1) / 5, N: b.n,
			MeanConfidence: round4(b.conf / float64(b.n)), HeldRate: round4(b.hel / float64(b.n))})
	}
	return g
}

// Compute loads and builds in one call.
func Compute(db *sql.DB, dims []string, minSamples int) (Report, error) {
	ss, err := Load(db)
	if err != nil {
		return Report{}, err
	}
	return Build(ss, dims, minSamples), nil
}

func pct(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *p*100)
}

// calibrationGap is the mean |confidence − held rate| over the buckets,
// weighted by bucket size: 0 is perfectly calibrated.
func calibrationGap(bs []Bucket) *float64 {
	var n int
	var gap float64
	for _, b := range bs {
		n += b.N
		gap += math.Abs(b.MeanConfidence-b.HeldRate) * float64(b.N)
	}
	if n == 0 {
		return nil
	}
	v := round4(gap / float64(n))
	return &v
}

// Render writes the report as a text table (the model-upgrade routine's
// comparison column) or JSON.
func Render(w io.Writer, rep Report, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Fprintf(w, "forecast calibration by %s (groups under %d non-post-hoc samples hidden: %d groups, %d runs)\n",
		strings.Join(rep.Dims, "/"), rep.MinSamples, rep.HiddenGroups, rep.HiddenRuns)
	if len(rep.Groups) == 0 {
		_, err := fmt.Fprintln(w, "no group has enough samples yet")
		return err
	}
	fmt.Fprintf(w, "%-48s %7s %9s %6s %8s %13s %8s\n", "group", "samples", "area-hit", "bands", "outcome", "mean-surprise", "cal-gap")
	for _, g := range rep.Groups {
		name := keyString(rep.Dims, g.Key)
		name = strings.ReplaceAll(name, "\x00", " / ")
		gap := "—"
		if v := calibrationGap(g.Buckets); v != nil {
			gap = fmt.Sprintf("%.2f", *v)
		}
		fmt.Fprintf(w, "%-48s %7d %9s %6s %8s %13.2f %8s\n", name, g.Samples, pct(g.AreaHitRate),
			pct(g.BandAccuracy), pct(g.OutcomeAccuracy), g.MeanSurprise, gap)
	}
	return nil
}
