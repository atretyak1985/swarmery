package lessons

// The generation input: what the model is allowed to see, and — the same set —
// what it is allowed to cite. Every fact is rendered next to its evidence id as
// an [E:kind:id] marker (retroanalysis's vocabulary), and Allowed collects
// exactly those ids, so validation can reject any id the input never offered.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

// maxDiffFiles bounds the diff stat handed to the model (largest churn first).
const maxDiffFiles = 40

// Forecast is one phase_forecasts row as the prompt renders it.
type Forecast struct {
	Kind         string
	Areas        []string
	Files        []string
	SizeBand     string
	DurationBand string
	Outcome      string
	Risks        []string
	Confidence   *float64
	PostHoc      bool
}

// Actuals is the phase_actuals row of the run.
type Actuals struct {
	Files          []gitstat.FileStat
	Areas          []string
	LinesAdded     *int64
	LinesRemoved   *int64
	SizeBand       string
	DurationS      *int64
	Outcome        string
	VerifyVerdict  string
	TestFailures   *int64
	TestUnexpected *int64
	ModelFallback  bool
	StartPoint     string
}

// Input is everything one generation reads.
type Input struct {
	PhaseID     int64
	SessionUUID string
	TaskID      int64
	PlanID      string
	PhaseName   string
	Surprise    surprise.Stored
	Forecasts   []Forecast
	Actuals     *Actuals
	Divergence  string
	// Allowed is the set of "kind:id" evidence ids the prompt offers.
	Allowed map[string]bool
}

// LoadInput reads a scored run's input. ok=false (no error) means the run has
// nothing to learn from yet: no score, or no divergence paragraph.
func LoadInput(db *sql.DB, phaseID int64, uuid string) (in Input, ok bool, reason string, err error) {
	st, err := surprise.LoadBySession(db, uuid)
	if err != nil {
		return in, false, "", err
	}
	if st == nil || st.PhaseID != phaseID {
		return in, false, "the run has no surprise score", nil
	}
	in = Input{PhaseID: phaseID, SessionUUID: uuid, Surprise: *st}
	var report string
	err = db.QueryRow(`
		SELECT e.workspace_task_id, e.name, COALESCE(e.completion_report, ''), COALESCE(t.external_id, '')
		  FROM epic_phases e LEFT JOIN tasks t ON t.id = e.workspace_task_id
		 WHERE e.id = ?`, phaseID).Scan(&in.TaskID, &in.PhaseName, &report, &in.PlanID)
	if errors.Is(err, sql.ErrNoRows) {
		return in, false, "the phase no longer exists", nil
	}
	if err != nil {
		return in, false, "", err
	}
	if in.Divergence = ExtractDivergence(report); in.Divergence == "" {
		return in, false, `the Completion Report has no "Where reality diverged" paragraph`, nil
	}
	if in.Forecasts, err = loadForecasts(db, phaseID); err != nil {
		return in, false, "", err
	}
	if in.Actuals, err = loadActuals(db, uuid); err != nil {
		return in, false, "", err
	}
	in.Allowed = allowedIDs(in)
	return in, true, "", nil
}

func loadForecasts(db *sql.DB, phaseID int64) ([]Forecast, error) {
	rows, err := db.Query(`
		SELECT kind, areas_json, files_json, size_band, duration_band, outcome, risks_json,
		       confidence, post_hoc
		  FROM phase_forecasts WHERE phase_id = ? ORDER BY id`, phaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Forecast
	seen := map[string]bool{}
	for rows.Next() {
		var (
			f                   Forecast
			areas, files, risks string
			conf                sql.NullFloat64
			postHoc             int
		)
		if err := rows.Scan(&f.Kind, &areas, &files, &f.SizeBand, &f.DurationBand, &f.Outcome,
			&risks, &conf, &postHoc); err != nil {
			return nil, err
		}
		// One forecast per kind: the evidence id is "forecast:<kind>", so a second
		// block of the same kind would be uncitable as itself.
		if f.Kind == "" || seen[f.Kind] {
			continue
		}
		seen[f.Kind] = true
		_ = json.Unmarshal([]byte(areas), &f.Areas)
		_ = json.Unmarshal([]byte(files), &f.Files)
		_ = json.Unmarshal([]byte(risks), &f.Risks)
		if conf.Valid {
			v := conf.Float64
			f.Confidence = &v
		}
		f.PostHoc = postHoc != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

func loadActuals(db *sql.DB, uuid string) (*Actuals, error) {
	var (
		a                           Actuals
		files, areas, size, verdict sql.NullString
		added, removed, dur, tf, tu sql.NullInt64
		fallback                    sql.NullInt64
	)
	err := db.QueryRow(`
		SELECT files_json, areas_json, lines_added, lines_removed, size_band, duration_s, outcome,
		       verify_verdict, test_failures, test_failures_unexpected, model_fallback, start_point
		  FROM phase_actuals WHERE session_uuid = ?`, uuid).Scan(
		&files, &areas, &added, &removed, &size, &dur, &a.Outcome, &verdict, &tf, &tu, &fallback, &a.StartPoint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if files.Valid {
		_ = json.Unmarshal([]byte(files.String), &a.Files)
		sort.SliceStable(a.Files, func(i, j int) bool {
			return a.Files[i].Added+a.Files[i].Removed > a.Files[j].Added+a.Files[j].Removed
		})
		if len(a.Files) > maxDiffFiles {
			a.Files = a.Files[:maxDiffFiles]
		}
	}
	if areas.Valid {
		_ = json.Unmarshal([]byte(areas.String), &a.Areas)
	}
	a.SizeBand, a.VerifyVerdict = size.String, verdict.String
	a.LinesAdded, a.LinesRemoved = nullInt(added), nullInt(removed)
	a.DurationS, a.TestFailures, a.TestUnexpected = nullInt(dur), nullInt(tf), nullInt(tu)
	a.ModelFallback = fallback.Valid && fallback.Int64 != 0
	return &a, nil
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// allowedIDs is the evidence vocabulary of one input. BuildPrompt renders a
// marker for every id here and for nothing else.
func allowedIDs(in Input) map[string]bool {
	out := map[string]bool{
		fmt.Sprintf("phase:%d", in.PhaseID):      true,
		fmt.Sprintf("divergence:%d", in.PhaseID): true,
		"surprise:" + in.SessionUUID:             true,
		"run:" + in.SessionUUID:                  true,
	}
	for _, f := range in.Forecasts {
		out["forecast:"+f.Kind] = true
	}
	if a := in.Actuals; a != nil {
		out["actuals:"+in.SessionUUID] = true
		for _, f := range a.Files {
			out["file:"+f.Path] = true
		}
		if a.StartPoint != "" {
			out["commit:"+a.StartPoint] = true
		}
	}
	return out
}

func marker(id string) string { return "[E:" + id + "]" }

// BuildPrompt renders the generation prompt.
func BuildPrompt(in Input) string {
	var b strings.Builder
	b.WriteString(promptHead)
	fmt.Fprintf(&b, "\n## Run %s\n", marker("run:"+in.SessionUUID))
	fmt.Fprintf(&b, "Plan %s, phase %q %s.\n", orDash(in.PlanID), in.PhaseName, marker(fmt.Sprintf("phase:%d", in.PhaseID)))

	fmt.Fprintf(&b, "\n## Surprise %s\n", marker("surprise:"+in.SessionUUID))
	fmt.Fprintf(&b, "Index %.2f (0 = went as predicted, 1 = missed on every axis); largest component: %s.\n",
		in.Surprise.Index, orDash(in.Surprise.Top))
	if in.Surprise.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", in.Surprise.Summary)
	}
	if d := in.Surprise.Detail; len(d.UnexpectedAreas) > 0 || len(d.MissedAreas) > 0 {
		fmt.Fprintf(&b, "Areas changed but not forecast: %s. Forecast areas never touched: %s.\n",
			joinOrDash(d.UnexpectedAreas), joinOrDash(d.MissedAreas))
	}

	for _, f := range in.Forecasts {
		fmt.Fprintf(&b, "\n## Forecast (%s) %s\n", f.Kind, marker("forecast:"+f.Kind))
		fmt.Fprintf(&b, "areas: %s; files: %s; size: %s; duration: %s; outcome: %s",
			joinOrDash(f.Areas), joinOrDash(f.Files), orDash(f.SizeBand), orDash(f.DurationBand), orDash(f.Outcome))
		if f.Confidence != nil {
			fmt.Fprintf(&b, "; confidence: %.2f", *f.Confidence)
		}
		if f.PostHoc {
			b.WriteString("; written after the first edit (a report, not a prediction)")
		}
		b.WriteString(".\n")
		if len(f.Risks) > 0 {
			fmt.Fprintf(&b, "risks: %s\n", strings.Join(f.Risks, "; "))
		}
	}

	if a := in.Actuals; a != nil {
		fmt.Fprintf(&b, "\n## Actuals %s\n", marker("actuals:"+in.SessionUUID))
		fmt.Fprintf(&b, "outcome: %s; size: %s; lines +%s/-%s; duration: %s; verify: %s; test failures: %s (outside forecast: %s); ended on a weaker model: %t.\n",
			orDash(a.Outcome), orDash(a.SizeBand), intOrDash(a.LinesAdded), intOrDash(a.LinesRemoved),
			durOrDash(a.DurationS), orDash(a.VerifyVerdict), intOrDash(a.TestFailures), intOrDash(a.TestUnexpected),
			a.ModelFallback)
		if a.StartPoint != "" {
			fmt.Fprintf(&b, "diff measured from %s\n", marker("commit:"+a.StartPoint))
		}
		if len(a.Files) > 0 {
			b.WriteString("\n### Diff stat (largest churn first)\n")
			for _, f := range a.Files {
				fmt.Fprintf(&b, "- +%d/-%d %s\n", f.Added, f.Removed, marker("file:"+f.Path))
			}
		}
	}

	fmt.Fprintf(&b, "\n## Where reality diverged %s\n%s\n", marker(fmt.Sprintf("divergence:%d", in.PhaseID)), in.Divergence)
	b.WriteString(promptTail)
	return b.String()
}

const promptHead = `You extract durable lessons from ONE software-agent run that landed far from its own forecast.
A lesson is guidance a FUTURE run in the same code area should receive before it starts, so it does not repeat the miss.

Rules:
- Return 0, 1 or 2 lessons. Zero is the right answer when the divergence paragraph does not explain a cause that will recur (bad luck, one-off env trouble, a mis-sized estimate with no lesson in it).
- "guidance": ONE imperative sentence, specific to this codebase (name the file, table, flag or convention). No generic advice ("write tests", "read the code first").
- "area_globs": 1-5 repo-relative globs where the lesson applies (e.g. "tools/x/internal/ingest/**"). Never "*".
- "cause": one sentence saying what differed from the assumption and why, taken from the divergence paragraph.
- "evidence": 1-8 ids copied VERBATIM from the [E:kind:id] markers below, without the brackets (e.g. "divergence:12", "file:internal/x.go"). Never invent an id; a lesson citing an id that is not below is discarded.
`

const promptTail = `
Reply with ONLY this JSON object, no prose, no code fence:
{"lessons":[{"title":"…","guidance":"…","area_globs":["…"],"cause":"…","evidence":["…"]}]}
`

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func joinOrDash(v []string) string {
	if len(v) == 0 {
		return "—"
	}
	return strings.Join(v, ", ")
}

func intOrDash(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprint(*v)
}

func durOrDash(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%dm", *v/60)
}
