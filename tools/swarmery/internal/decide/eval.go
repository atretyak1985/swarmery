package decide

// The offline eval: replay the classifier over the ground truth the operator
// already recorded, and report how often it agrees. It is the measuring stick
// for every rule or prompt change — run it before and after, compare the tables.
//
// IT ONLY READS. Every statement here is a SELECT, the answers are computed in
// memory through Engine.ask (the unexported, non-recording half of Decide), and
// no decisions row, label or mode is ever written — so it is safe against the
// live database while the daemon serves (`swarmery decide eval` opens it with
// store.OpenNoMigrate).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
)

// EvalQuestions are the questions the eval can replay: the ones whose evidence
// is rebuilt from the session alone (D2). D1 and D3 read a run's state at the
// moment it ended, which the store does not keep.
var EvalQuestions = []string{QD2TaskType, QD2Outcome, QD2Failure}

// Skip reasons: why a labelled subject could not be replayed.
const (
	EvalSkipNoSession = "no-session" // the subject's sessions row is gone
	EvalSkipZeroTurns = "zero-turns" // the session has no turn to read (never had one, or pruned)
)

// evalUnanswered is the backend row for questions nothing answered: no rule,
// and no local model (rules-only mode) or a failed call.
const evalUnanswered = "unanswered"

// evalTopConfusions caps the confusion list per question.
const evalTopConfusions = 10

// EvalOptions narrows and observes one replay.
type EvalOptions struct {
	// TruthSince keeps only labels whose ground_truth_at is at or after it; the
	// zero time keeps every label.
	TruthSince time.Time
	// Questions is a subset of EvalQuestions; empty ⇒ all of them.
	Questions []string
	// Limit caps the subjects replayed, newest label first; <= 0 ⇒ no cap.
	Limit int
	// Progress, when set, is called after each subject with the running totals.
	Progress func(done, total, errs int)
}

// EvalReport is one replay's result.
type EvalReport struct {
	TruthSince string `json:"truthSince,omitempty"`
	// LLM is true when the engine had a local backend; false is rules-only.
	LLM       bool           `json:"llm"`
	Subjects  int            `json:"subjects"`
	Questions []EvalQuestion `json:"questions"`
}

// EvalQuestion is one question's row of the report.
type EvalQuestion struct {
	QuestionID string `json:"questionId"`
	// Labelled counts subjects with ground truth (the latest label per subject);
	// Replayed the ones whose question was rebuilt; Skipped the rest, by reason.
	Labelled    int            `json:"labelled"`
	Replayed    int            `json:"replayed"`
	Skipped     int            `json:"skipped"`
	SkipReasons map[string]int `json:"skipReasons"`
	// Agree counts replayed answers that match the truth (Agrees); Agreement is
	// Agree / Replayed — an unanswered question is a miss — nil when nothing was
	// replayed.
	Agree     int      `json:"agree"`
	Agreement *float64 `json:"agreement"`
	// Errors counts backend calls that failed (a subset of the unanswered row).
	Errors int `json:"errors"`
	// RecordedAgree is the same comparison over the answer STORED on each
	// labelled row — what the daemon said when it asked, before any replay.
	RecordedAgree int           `json:"recordedAgree"`
	Backends      []EvalBackend `json:"backends"`
	// Rules splits the rules backend's answers by the rule that gave each
	// (Question.RuleID), in id order; empty when no rule answered.
	Rules      []EvalRule      `json:"rules"`
	Confusions []EvalConfusion `json:"confusions"`
	// Buckets are ten confidence buckets, [0,0.1) … [0.9,1], over NON-rule
	// answers (a rule's confidence is always 1 and says nothing).
	Buckets []EvalBucket `json:"buckets"`
}

// EvalBackend is one backend's share of a question: how many it answered and
// how many of those agreed. Precision is nil when it answered nothing, and for
// the unanswered row.
type EvalBackend struct {
	Backend   string   `json:"backend"`
	N         int      `json:"n"`
	Agree     int      `json:"agree"`
	Precision *float64 `json:"precision"`
}

// EvalRule is one rule's share of a question: how many replayed questions it
// answered (N; Coverage is N over the question's replayed count), and how many
// of those agreed (Precision is Agree over N).
type EvalRule struct {
	Rule      string   `json:"rule"`
	N         int      `json:"n"`
	Agree     int      `json:"agree"`
	Precision *float64 `json:"precision"`
	Coverage  *float64 `json:"coverage"`
}

// evalRuleUnnamed is the row for a rule answer that carries no rule id.
const evalRuleUnnamed = "(unnamed)"

// EvalConfusion is one (answer, truth) disagreement and how often it happened.
type EvalConfusion struct {
	Answer string `json:"answer"`
	Truth  string `json:"truth"`
	Count  int    `json:"count"`
}

// EvalBucket is one confidence bucket.
type EvalBucket struct {
	N     int `json:"n"`
	Agree int `json:"agree"`
}

// evalTruth is the latest label of one (subject, question).
type evalTruth struct {
	truth, at, recorded string
}

// evalSubject is one subject's labels, keyed by question id.
type evalSubject struct {
	uuid   string
	newest string // greatest ground_truth_at among its labels
	truths map[string]evalTruth
}

// Eval replays the classifier over existing ground truth and reports per
// question how often the replayed answer agrees with it. With an engine that
// has no local backend (rules-only) a question is answered only when its
// RuleAnswer is set; everything else is counted as unanswered. It never writes.
func Eval(ctx context.Context, db *sql.DB, e *Engine, opts EvalOptions) (EvalReport, error) {
	if db == nil {
		return EvalReport{}, errors.New("decide: eval needs a database")
	}
	if e == nil {
		e = &Engine{}
	}
	questions, err := evalQuestionSet(opts.Questions)
	if err != nil {
		return EvalReport{}, err
	}
	subjects, err := evalTruthSet(ctx, db, questions, opts.TruthSince)
	if err != nil {
		return EvalReport{}, err
	}
	if opts.Limit > 0 && len(subjects) > opts.Limit {
		subjects = subjects[:opts.Limit]
	}
	// Over EVERY label, not only the TruthSince window: the cutoff is when the
	// operator first used a new cause, wherever that label falls.
	since, err := newCausesInUseSince(db)
	if err != nil {
		return EvalReport{}, err
	}

	rep := EvalReport{LLM: e.Local != nil, Subjects: len(subjects)}
	if !opts.TruthSince.IsZero() {
		rep.TruthSince = opts.TruthSince.UTC().Format(time.RFC3339)
	}
	tallies := map[string]*evalTally{}
	for _, id := range questions {
		tallies[id] = newEvalTally()
	}

	errs := 0
	for i, sub := range subjects {
		if err := ctx.Err(); err != nil {
			return EvalReport{}, err
		}
		for id, tr := range sub.truths {
			t := tallies[id]
			t.labelled++
			if Agrees(id, tr.truth, tr.recorded, legacyTruth(tr.at, since)) {
				t.recordedAgree++
			}
		}
		s, reason, err := evalSession(ctx, db, sub.uuid)
		if err != nil {
			return EvalReport{}, err
		}
		if reason != "" {
			for id := range sub.truths {
				tallies[id].skip(reason)
			}
		} else {
			for _, q := range d2Questions(db, s, e.R5PhaseRunFeature) {
				tr, ok := sub.truths[q.ID]
				if !ok {
					continue
				}
				a, err := e.ask(ctx, q)
				if ctx.Err() != nil {
					return EvalReport{}, ctx.Err()
				}
				if err != nil && !errors.Is(err, ErrNotConfigured) {
					errs++
				}
				tallies[q.ID].answer(q.ID, tr.truth, legacyTruth(tr.at, since), a, err)
			}
		}
		if opts.Progress != nil {
			opts.Progress(i+1, len(subjects), errs)
		}
	}

	for _, id := range questions {
		rep.Questions = append(rep.Questions, tallies[id].report(id))
	}
	return rep, nil
}

// evalQuestionSet validates the requested questions, keeping EvalQuestions'
// order.
func evalQuestionSet(want []string) ([]string, error) {
	if len(want) == 0 {
		return EvalQuestions, nil
	}
	for _, q := range want {
		if !slices.Contains(EvalQuestions, q) {
			return nil, fmt.Errorf("decide: eval cannot replay %q; want one of %s", q, strings.Join(EvalQuestions, ", "))
		}
	}
	var out []string
	for _, q := range EvalQuestions {
		if slices.Contains(want, q) {
			out = append(out, q)
		}
	}
	return out, nil
}

// evalTruthSet reads the truth set: for each (subject, question) with ground
// truth, the row with the greatest ground_truth_at, then id. Subjects come back
// newest label first (then by uuid), so a Limit keeps the most recent labels.
func evalTruthSet(ctx context.Context, db *sql.DB, questions []string, since time.Time) ([]evalSubject, error) {
	args := make([]any, len(questions))
	for i, q := range questions {
		args[i] = q
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(questions)), ",")
	// Ascending by (ground_truth_at, id) within a key, so the LAST row read for a
	// key is the one that wins.
	rows, err := db.QueryContext(ctx, `
		SELECT subject, question_id, ground_truth, COALESCE(ground_truth_at, ''), answer, error
		  FROM decisions
		 WHERE ground_truth IS NOT NULL AND question_id IN (`+ph+`)
		 ORDER BY subject, question_id, COALESCE(ground_truth_at, ''), id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bySubject := map[string]*evalSubject{}
	for rows.Next() {
		var subject, id, truth, at, answer, errText string
		if err := rows.Scan(&subject, &id, &truth, &at, &answer, &errText); err != nil {
			return nil, err
		}
		sub := bySubject[subject]
		if sub == nil {
			sub = &evalSubject{uuid: subject, truths: map[string]evalTruth{}}
			bySubject[subject] = sub
		}
		if errText != "" {
			answer = "" // an errored call recorded no answer to compare
		}
		sub.truths[id] = evalTruth{truth: truth, at: at, recorded: answer}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]evalSubject, 0, len(bySubject))
	for _, sub := range bySubject {
		for id, tr := range sub.truths {
			if !since.IsZero() && !evalAtOrAfter(tr.at, since) {
				delete(sub.truths, id)
				continue
			}
			sub.newest = max(sub.newest, tr.at)
		}
		if len(sub.truths) > 0 {
			out = append(out, *sub)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].newest != out[j].newest {
			return out[i].newest > out[j].newest
		}
		return out[i].uuid < out[j].uuid
	})
	return out, nil
}

// evalAtOrAfter reports whether the stored ground_truth_at is at or after
// since. A value that is not RFC 3339 (or is missing) cannot be placed in the
// window and is left out of it.
func evalAtOrAfter(at string, since time.Time) bool {
	t, err := time.Parse(time.RFC3339, at)
	return err == nil && !t.Before(since)
}

// evalSession loads the session a subject names in the labeler's own shape.
// reason is non-empty when it cannot be replayed.
func evalSession(ctx context.Context, db *sql.DB, uuid string) (s d2Session, reason string, err error) {
	var turns int
	err = db.QueryRowContext(ctx, `
		SELECT s.session_uuid, COALESCE(s.custom_title, s.title, ''), COALESCE(s.started_at, ''),
		       COALESCE(s.ended_at, ''), COALESCE(s.outcome, ''),
		       EXISTS (SELECT 1 FROM turns t WHERE t.session_id = s.id)
		  FROM sessions s WHERE s.session_uuid = ?`, uuid).
		Scan(&s.uuid, &s.title, &s.started, &s.ended, &s.outcome, &turns)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s, EvalSkipNoSession, nil
	case err != nil:
		return s, "", err
	case turns == 0:
		return s, EvalSkipZeroTurns, nil
	}
	return s, "", nil
}

// evalTally accumulates one question's counts.
type evalTally struct {
	labelled, replayed, agree, errs, recordedAgree int
	skips                                          map[string]int
	backends                                       map[string]*EvalBackend
	rules                                          map[string]*EvalRule
	confusions                                     map[[2]string]int
	buckets                                        []EvalBucket
}

func newEvalTally() *evalTally {
	return &evalTally{
		skips:      map[string]int{},
		backends:   map[string]*EvalBackend{},
		rules:      map[string]*EvalRule{},
		confusions: map[[2]string]int{},
		buckets:    make([]EvalBucket, 10),
	}
}

func (t *evalTally) rule(id string) *EvalRule {
	if id == "" {
		id = evalRuleUnnamed
	}
	r := t.rules[id]
	if r == nil {
		r = &EvalRule{Rule: id}
		t.rules[id] = r
	}
	return r
}

func (t *evalTally) skip(reason string) { t.skips[reason]++ }

func (t *evalTally) backend(name string) *EvalBackend {
	b := t.backends[name]
	if b == nil {
		b = &EvalBackend{Backend: name}
		t.backends[name] = b
	}
	return b
}

// answer tallies one replayed question. A failed or absent answer is
// unanswered: it counts against the agreement and into no backend's precision.
// legacy is legacyTruth for the truth's record time.
func (t *evalTally) answer(questionID, truth string, legacy bool, a Answer, err error) {
	t.replayed++
	if err != nil {
		if !errors.Is(err, ErrNotConfigured) {
			t.errs++
		}
		t.backend(evalUnanswered).N++
		return
	}
	agrees := Agrees(questionID, truth, a.Value, legacy)
	b := t.backend(a.Backend)
	b.N++
	if agrees {
		t.agree++
		b.Agree++
	} else {
		t.confusions[[2]string{a.Value, truth}]++
	}
	if a.Backend == BackendRules {
		r := t.rule(a.Rule)
		r.N++
		if agrees {
			r.Agree++
		}
		return
	}
	bucket := &t.buckets[min(max(int(a.Confidence*10), 0), 9)]
	bucket.N++
	if agrees {
		bucket.Agree++
	}
}

func (t *evalTally) report(id string) EvalQuestion {
	q := EvalQuestion{QuestionID: id, Labelled: t.labelled, Replayed: t.replayed, SkipReasons: t.skips,
		Agree: t.agree, Agreement: ratio(t.agree, t.replayed), Errors: t.errs, RecordedAgree: t.recordedAgree,
		Rules: []EvalRule{}, Confusions: []EvalConfusion{}, Buckets: t.buckets}
	for _, n := range t.skips {
		q.Skipped += n
	}
	for _, r := range t.rules {
		row := *r
		row.Precision, row.Coverage = ratio(row.Agree, row.N), ratio(row.N, t.replayed)
		q.Rules = append(q.Rules, row)
	}
	sort.Slice(q.Rules, func(i, j int) bool { return q.Rules[i].Rule < q.Rules[j].Rule })
	// rules, local, unanswered always; any other backend that answered after them.
	names := []string{BackendRules, BackendLocal}
	for name := range t.backends {
		if !slices.Contains(names, name) && name != evalUnanswered {
			names = append(names, name)
		}
	}
	sort.Strings(names[2:])
	for _, name := range append(names, evalUnanswered) {
		b := *t.backend(name)
		if name != evalUnanswered {
			b.Precision = ratio(b.Agree, b.N)
		}
		q.Backends = append(q.Backends, b)
	}
	for k, n := range t.confusions {
		q.Confusions = append(q.Confusions, EvalConfusion{Answer: k[0], Truth: k[1], Count: n})
	}
	sort.Slice(q.Confusions, func(i, j int) bool {
		a, b := q.Confusions[i], q.Confusions[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Answer != b.Answer {
			return a.Answer < b.Answer
		}
		return a.Truth < b.Truth
	})
	if len(q.Confusions) > evalTopConfusions {
		q.Confusions = q.Confusions[:evalTopConfusions]
	}
	return q
}

// ratio is num/den, nil when den is 0 (nothing to measure is not 0%).
func ratio(num, den int) *float64 {
	if den == 0 {
		return nil
	}
	v := float64(num) / float64(den)
	return &v
}

// MissedFloors lists the questions whose agreement is below its floor, as
// operator-readable lines; empty when every floor holds. A question with a
// floor and nothing replayed misses it: an unmeasured agreement cannot clear a
// bar. A floor for a question the report does not carry is missed too.
func (r EvalReport) MissedFloors(floors map[string]float64) []string {
	ids := make([]string, 0, len(floors))
	for id := range floors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var missed []string
	for _, id := range ids {
		floor := floors[id]
		i := slices.IndexFunc(r.Questions, func(q EvalQuestion) bool { return q.QuestionID == id })
		switch {
		case i < 0:
			missed = append(missed, fmt.Sprintf("%s: not in this eval, floor %.3f not met", id, floor))
		case r.Questions[i].Agreement == nil:
			missed = append(missed, fmt.Sprintf("%s: nothing replayed, floor %.3f not met", id, floor))
		case *r.Questions[i].Agreement < floor:
			missed = append(missed, fmt.Sprintf("%s: agreement %.3f is below floor %.3f", id, *r.Questions[i].Agreement, floor))
		}
	}
	return missed
}

// RenderEval writes the report as a text table, or as indented JSON.
func RenderEval(w io.Writer, rep EvalReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	mode, window := "rules-only", "all labels"
	if rep.LLM {
		mode = "rules + local model"
	}
	if rep.TruthSince != "" {
		window = "labels since " + rep.TruthSince
	}
	fmt.Fprintf(w, "decide eval — %s, %s, %d subjects\n\n", mode, window, rep.Subjects)
	fmt.Fprintf(w, "%-18s %8s %8s %7s %6s %9s  %s\n", "question", "labelled", "replayed", "skipped", "agree", "agreement", "recorded")
	for _, q := range rep.Questions {
		fmt.Fprintf(w, "%-18s %8d %8d %7d %6d %9s  %d/%d %s\n", q.QuestionID, q.Labelled, q.Replayed, q.Skipped,
			q.Agree, evalPct(q.Agreement), q.RecordedAgree, q.Labelled, evalPct(ratio(q.RecordedAgree, q.Labelled)))
	}
	for _, q := range rep.Questions {
		fmt.Fprintf(w, "\n%s\n", q.QuestionID)
		fmt.Fprintf(w, "  %-11s %6s %6s %9s\n", "backend", "n", "agree", "precision")
		for _, b := range q.Backends {
			if b.Backend == evalUnanswered {
				fmt.Fprintf(w, "  %-11s %6d %6s %9s  (errors: %d)\n", b.Backend, b.N, "—", "—", q.Errors)
				continue
			}
			fmt.Fprintf(w, "  %-11s %6d %6d %9s\n", b.Backend, b.N, b.Agree, evalPct(b.Precision))
		}
		if len(q.Rules) > 0 {
			fmt.Fprintf(w, "  %-11s %6s %6s %9s %9s\n", "rule", "n", "agree", "precision", "coverage")
			for _, r := range q.Rules {
				fmt.Fprintf(w, "  %-11s %6d %6d %9s %9s\n", r.Rule, r.N, r.Agree, evalPct(r.Precision), evalPct(r.Coverage))
			}
		}
		if q.Skipped > 0 {
			reasons := make([]string, 0, len(q.SkipReasons))
			for reason, n := range q.SkipReasons {
				reasons = append(reasons, fmt.Sprintf("%s %d", reason, n))
			}
			sort.Strings(reasons)
			fmt.Fprintf(w, "  skipped: %s\n", strings.Join(reasons, ", "))
		}
		if len(q.Confusions) == 0 {
			fmt.Fprintln(w, "  confusions (answer → truth): none")
		} else {
			fmt.Fprintln(w, "  confusions (answer → truth):")
			for _, c := range q.Confusions {
				fmt.Fprintf(w, "    %5d  %s → %s\n", c.Count, c.Answer, c.Truth)
			}
		}
		if !slices.ContainsFunc(q.Buckets, func(b EvalBucket) bool { return b.N > 0 }) {
			fmt.Fprintln(w, "  confidence (non-rule answers): none")
			continue
		}
		fmt.Fprintln(w, "  confidence (non-rule answers):")
		for _, row := range []struct {
			label string
			cell  func(i int, b EvalBucket) string
		}{
			{"bucket", func(i int, _ EvalBucket) string { return fmt.Sprintf("%.1f", float64(i)/10) }},
			{"n", func(_ int, b EvalBucket) string { return fmt.Sprint(b.N) }},
			{"agree", func(_ int, b EvalBucket) string { return fmt.Sprint(b.Agree) }},
		} {
			fmt.Fprintf(w, "    %-6s", row.label)
			for i, b := range q.Buckets {
				fmt.Fprintf(w, " %5s", row.cell(i, b))
			}
			fmt.Fprintln(w)
		}
	}
	return nil
}

// evalPct renders a ratio as a percentage, "—" when there is nothing to measure.
func evalPct(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *v*100)
}
