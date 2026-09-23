package lessons

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// sourceBackfill mirrors actuals.SourceBackfill: a rescored history run never
// spends tokens on lessons.
const sourceBackfill = "backfill"

// generateTimeout bounds one background generation end to end.
const generateTimeout = 5 * time.Minute

// Generator produces lesson candidates for surprising, explained runs.
type Generator struct {
	DB     *sql.DB
	Runner Runner
	Cfg    Config
	// Now is the clock (test seam; nil ⇒ time.Now).
	Now func() time.Time
	// Changed is called with the phase's workspace task id after candidates
	// were written, so the dashboard refetches. nil ⇒ none.
	Changed func(taskID int64)
	// Go runs the background generation (test seam; nil ⇒ a goroutine).
	Go func(func())
}

// NewGenerator builds a Generator with the production runner.
func NewGenerator(db *sql.DB, cfg Config) *Generator {
	return &Generator{DB: db, Runner: ClaudeRunner{Model: cfg.Model}, Cfg: cfg}
}

func (g *Generator) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Outcome reports one Generate call.
type Outcome struct {
	Skipped  string      // non-empty ⇒ nothing was claimed or spent, and why
	Inserted int         // new candidate rows
	Linked   int         // lessons folded into an existing identity (14.5)
	Rejected []Rejection // lessons that failed validation
}

// AfterScore is the hook internal/surprise calls after it stores a run's
// score. It only decides eligibility on the score and hands the rest to the
// background: generation must never delay or fail the scoring path.
func (g *Generator) AfterScore(st *surprise.Stored, source string) {
	if g == nil || st == nil || !g.eligible(st.Index) || source == sourceBackfill {
		return
	}
	phaseID, uuid := st.PhaseID, st.SessionUUID
	job := func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("error: lessons: phase=%d uuid=%s generation panicked: %v", phaseID, uuid, p)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), generateTimeout)
		defer cancel()
		out, err := g.Generate(ctx, phaseID, uuid)
		switch {
		case err != nil:
			log.Printf("warning: lessons: phase=%d uuid=%s: %v", phaseID, uuid, err)
		case out.Skipped == "":
			log.Printf("lessons: phase=%d uuid=%s: %d new candidate(s), %d linked, %d rejected",
				phaseID, uuid, out.Inserted, out.Linked, len(out.Rejected))
		}
	}
	if g.Go != nil {
		g.Go(job)
		return
	}
	go job()
}

func (g *Generator) eligible(index float64) bool {
	return g.Cfg.Enabled && g.Cfg.Threshold != nil && index >= *g.Cfg.Threshold
}

// Generate runs one generation synchronously. Idempotent per run: the first
// call that finds the run eligible claims it in lesson_generations, and every
// later call is a no-op (Skipped), whatever the first one's result was.
func (g *Generator) Generate(ctx context.Context, phaseID int64, uuid string) (Outcome, error) {
	in, ok, reason, err := LoadInput(g.DB, phaseID, uuid)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{Skipped: reason}, nil
	}
	if !g.eligible(in.Surprise.Index) {
		return Outcome{Skipped: "below the surprise threshold, or generation is off"}, nil
	}
	now := g.now().UTC().Format(time.RFC3339)
	res, err := g.DB.Exec(`INSERT INTO lesson_generations (source_phase_run, phase_id, state, model, created_at)
		VALUES (?, ?, 'running', ?, ?) ON CONFLICT(source_phase_run) DO NOTHING`, uuid, phaseID, g.Cfg.Model, now)
	if err != nil {
		return Outcome{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Outcome{Skipped: "already generated for this run"}, nil
	}

	out, genErr := g.generate(ctx, in)
	g.finish(uuid, out, genErr)
	if genErr != nil {
		return out, genErr
	}
	if out.Inserted+out.Linked > 0 && g.Changed != nil {
		g.Changed(in.TaskID)
	}
	return out, nil
}

func (g *Generator) generate(ctx context.Context, in Input) (Outcome, error) {
	var out Outcome
	if g.Runner == nil {
		return out, fmt.Errorf("no runner configured")
	}
	raw, err := g.Runner.Run(ctx, BuildPrompt(in))
	if err != nil {
		return out, fmt.Errorf("generation run: %w", err)
	}
	parsed, err := ParseOutput(raw)
	if err != nil {
		return out, err
	}
	valid, rejected := Validate(parsed, in.Allowed)
	out.Rejected = rejected
	for i, c := range valid {
		linked, err := g.persist(in, i+1, c)
		switch {
		case errors.Is(err, ErrInvalid):
			out.Rejected = append(out.Rejected, Rejection{Title: c.Title, Reason: err.Error()})
		case err != nil:
			return out, err
		case linked:
			out.Linked++
		default:
			out.Inserted++
		}
	}
	return out, nil
}

// persist writes one validated lesson, linking it to an existing identity
// instead of duplicating it (14.5):
//
//   - a surprise lesson with the same norm_title that is still a candidate or
//     active absorbs this run as one more recurrence (no new row);
//   - a retro lesson with the same norm_title is recorded as linked_norm_title
//     on the new candidate, so the review queue offers the merge first.
//
// The INSERT's status is the literal 'candidate' — never a parameter — so a
// generated lesson cannot enter the table as anything but a candidate.
func (g *Generator) persist(in Input, seq int, c Candidate) (linked bool, err error) {
	norm := wsingest.NormalizeLessonTitle(c.Title)
	if norm == "" {
		return false, fmt.Errorf("%w: the title folds to an empty identity", ErrInvalid)
	}
	now := g.now().UTC().Format(time.RFC3339)
	var existing int64
	err = g.DB.QueryRow(`SELECT id FROM surprise_lessons
		WHERE norm_title = ? AND status IN ('candidate', 'active') ORDER BY id LIMIT 1`, norm).Scan(&existing)
	switch {
	case err == nil:
		return true, addRecurrence(g.DB, existing, in.SessionUUID, c.Evidence, now)
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	var retroNorm string
	if err := g.DB.QueryRow(`SELECT norm_title FROM retro_lessons WHERE norm_title = ? LIMIT 1`, norm).
		Scan(&retroNorm); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	ev, _ := json.Marshal(c.Evidence)
	runs, _ := json.Marshal([]string{in.SessionUUID})
	_, err = g.DB.Exec(`INSERT INTO surprise_lessons
		(source_phase_run, phase_id, seq, title, norm_title, guidance, area_globs, evidence_json, cause,
		 source_paragraph, surprise_index, status, linked_norm_title, recurrences, recurrence_runs_json,
		 model, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'candidate', ?, 1, ?, ?, ?, ?)
		ON CONFLICT(source_phase_run, seq) DO NOTHING`,
		in.SessionUUID, in.PhaseID, seq, c.Title, norm, c.Guidance, strings.Join(c.AreaGlobs, ","),
		string(ev), c.Cause, in.Divergence, in.Surprise.Index, retroNorm, string(runs), g.Cfg.Model, now, now)
	return retroNorm != "", err
}

// addRecurrence folds one more run (and its evidence) into an existing lesson.
// A run already recorded is a no-op, so a replay cannot inflate the count.
func addRecurrence(db *sql.DB, id int64, uuid string, evidence []string, now string) error {
	var runsJSON, evJSON string
	if err := db.QueryRow(`SELECT recurrence_runs_json, evidence_json FROM surprise_lessons WHERE id = ?`, id).
		Scan(&runsJSON, &evJSON); err != nil {
		return err
	}
	var runs, ev []string
	_ = json.Unmarshal([]byte(runsJSON), &runs)
	_ = json.Unmarshal([]byte(evJSON), &ev)
	for _, r := range runs {
		if r == uuid {
			return nil
		}
	}
	runs = append(runs, uuid)
	ev = union(ev, evidence)
	rb, _ := json.Marshal(runs)
	eb, _ := json.Marshal(ev)
	_, err := db.Exec(`UPDATE surprise_lessons SET recurrences = recurrences + 1, recurrence_runs_json = ?,
		evidence_json = ?, updated_at = ? WHERE id = ?`, string(rb), string(eb), now, id)
	return err
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (g *Generator) finish(uuid string, out Outcome, genErr error) {
	state, msg := "done", ""
	if genErr != nil {
		state, msg = "failed", capRunes(genErr.Error(), 2000)
	}
	rej := out.Rejected
	if rej == nil {
		rej = []Rejection{}
	}
	rb, _ := json.Marshal(rej)
	if _, err := g.DB.Exec(`UPDATE lesson_generations SET state = ?, inserted = ?, linked = ?,
		rejected_json = ?, error = ?, finished_at = ? WHERE source_phase_run = ?`,
		state, out.Inserted, out.Linked, string(rb), msg, g.now().UTC().Format(time.RFC3339), uuid); err != nil {
		log.Printf("warning: lessons: record generation %s: %v", uuid, err)
	}
}
