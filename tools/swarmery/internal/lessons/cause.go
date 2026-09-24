package lessons

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

// causeTimeout bounds one background classification end to end.
const causeTimeout = 2 * time.Minute

// causeMaxFailures caps errored D3 calls per run, so a paragraph the backend
// can never answer is not re-asked on every rescore forever.
const causeMaxFailures = 3

// CauseClassifier labels WHY a surprising run diverged from its forecast
// (learning-loop step 14.3) through the local decision classifier's D3
// question. Shadow first: the decisions table records every answer; the label
// lands on phase_surprise only when the question is ACTIVE. Analytics only —
// nothing reads the label to decide anything.
type CauseClassifier struct {
	DB *sql.DB
	E  *decide.Engine
	// Threshold is the surprise index a run must reach to be classified — the
	// same attention threshold lesson generation uses. nil ⇒ never.
	Threshold *float64
	// Now is the clock (test seam; nil ⇒ time.Now).
	Now func() time.Time
	// Go runs the background classification (test seam; nil ⇒ a goroutine).
	Go func(func())
}

func (c *CauseClassifier) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// AfterScore is the surprise scorer's hook. Like the lesson generator's, it
// only decides eligibility on the score and hands the rest to the background:
// classification must never delay or fail the scoring path.
func (c *CauseClassifier) AfterScore(st *surprise.Stored, source string) {
	if c == nil || st == nil || source == sourceBackfill || !c.E.Configured() ||
		c.E.Mode(decide.QD3Cause) == decide.ModeOff ||
		c.Threshold == nil || st.Index < *c.Threshold {
		return
	}
	phaseID, uuid := st.PhaseID, st.SessionUUID
	job := func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("error: lessons: d3 phase=%d uuid=%s panicked: %v", phaseID, uuid, p)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), causeTimeout)
		defer cancel()
		if _, err := c.Classify(ctx, phaseID, uuid); err != nil {
			log.Printf("warning: lessons: d3 phase=%d uuid=%s: %v", phaseID, uuid, err)
		}
	}
	if c.Go != nil {
		c.Go(job)
		return
	}
	go job()
}

// Classify runs one classification synchronously and returns the D3 outcome
// (Asked=false when the run was skipped). Idempotent per run: a run with an
// error-free D3 decision, or causeMaxFailures errored ones, is not asked again.
func (c *CauseClassifier) Classify(ctx context.Context, phaseID int64, uuid string) (decide.D3Outcome, error) {
	var ok, failed int
	if err := c.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN error = '' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN error <> '' THEN 1 ELSE 0 END), 0)
		FROM decisions WHERE question_id = ? AND subject = ?`, decide.QD3Cause, uuid).Scan(&ok, &failed); err != nil {
		return decide.D3Outcome{}, err
	}
	if ok > 0 || failed >= causeMaxFailures {
		return decide.D3Outcome{}, nil
	}
	in, eligible, _, err := LoadInput(c.DB, phaseID, uuid)
	if err != nil || !eligible {
		// No divergence paragraph yet: nothing to classify. The settled rescore
		// calls again once the Completion Report is written.
		return decide.D3Outcome{}, err
	}
	fallback := in.Actuals != nil && in.Actuals.ModelFallback
	out := c.E.D3(ctx, decide.D3Input{
		SessionUUID:   uuid,
		PhaseName:     in.PhaseName,
		Summary:       in.Surprise.Summary,
		TopComponent:  in.Surprise.Top,
		Divergence:    in.Divergence,
		ModelFallback: fallback,
	})
	if out.Err != nil || out.Label == "" {
		return out, nil
	}
	var conf any
	if out.Label != decide.LabelUnknown {
		conf = out.Confidence
	}
	if _, err := c.DB.ExecContext(ctx, `UPDATE phase_surprise SET divergence_cause = ?,
		divergence_cause_confidence = ?, divergence_cause_decision_id = ?, divergence_cause_at = ?
		WHERE session_uuid = ?`,
		out.Label, conf, out.DecisionID, c.now().UTC().Format(time.RFC3339), uuid); err != nil {
		return out, err
	}
	return out, nil
}
