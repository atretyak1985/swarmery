package decide

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// D2's question ids: three labels per finished session, for analytics only.
const (
	QD2TaskType = "d2.task_type"
	QD2Outcome  = "d2.outcome"
	QD2Failure  = "d2.failure_cause"
)

// KnownQuestions is every question the dashboard lists, in display order.
var KnownQuestions = []string{QD1, QD2TaskType, QD2Outcome, QD2Failure, QD3Cause}

// Label vocabularies (fixed taxonomies). LabelUnknown is the below-threshold
// safe default. FailureCauses only ever grows at the END: the order is the one
// the dashboard lists, and a cause added later names its FailureParent.
var (
	TaskTypes     = []string{"feature", "bugfix", "refactor", "docs", "research", "review", "ops", "planning", "other"}
	Outcomes      = []string{"shipped", "partial", "abandoned", "failed"}
	FailureCauses = []string{"none", "tool-error", "test-failure", "blocked-on-operator", "refusal", "timeout", "context-exhausted", "scope-misread", "other",
		"auth", "quota", "api-error"}
)

const LabelUnknown = "unknown"

// FailureParent maps a cause added after labelling began onto every label an
// operator used for it before it existed: `other` by default, and the cause the
// 2026-09-29/30 triage actually wrote — a logged-out CLI was blocked on the
// operator, an API error was filed as a tool error.
var FailureParent = map[string][]string{
	"auth":      {"other", "blocked-on-operator"},
	"quota":     {"other"},
	"api-error": {"other", "tool-error"},
}

// Agrees reports whether answer matches truth for questionID: equal, or (for
// d2.failure_cause, and only when the truth is a LEGACY label — legacyTruth)
// truth is one of the answer's parents. Never the reverse: a truth of "auth"
// does not agree with an answer of "other". A parent label written after the
// new causes were in use was chosen over them and is matched as written. An
// empty truth agrees with nothing — there is no label to match.
func Agrees(questionID, truth, answer string, legacy bool) bool {
	if truth == "" {
		return false
	}
	if strings.EqualFold(truth, answer) {
		return true
	}
	if questionID != QD2Failure || !legacy {
		return false
	}
	return slices.ContainsFunc(FailureParent[strings.ToLower(answer)], func(p string) bool {
		return strings.EqualFold(truth, p)
	})
}

// legacyTruth reports whether a ground truth recorded at truthAt predates
// since — the moment the operator started using the causes FailureParent maps
// (newCausesInUseSince). No cutoff yet, no recorded time, or a time that does
// not parse are all legacy: the parent mapping is how every label was read
// before the cutoff existed.
func legacyTruth(truthAt, since string) bool {
	if since == "" || truthAt == "" {
		return true
	}
	at, errAt := time.Parse(time.RFC3339, truthAt)
	cut, errCut := time.Parse(time.RFC3339, since)
	if errAt != nil || errCut != nil {
		return true
	}
	return at.Before(cut)
}

// newCausesInUseSince is when the operator first recorded one of the causes
// FailureParent maps as d2.failure_cause ground truth — the first moment those
// causes demonstrably existed for them — or "" when they never have. A data
// cutoff, not a release date: the causes reach an operator whenever their
// daemon is updated. ground_truth_at is written as RFC 3339 UTC
// (RecordGroundTruth), so MIN over the text is the earliest instant.
func newCausesInUseSince(db *sql.DB) (string, error) {
	causes := make([]any, 0, len(FailureParent)+1)
	causes = append(causes, QD2Failure)
	for c := range FailureParent {
		causes = append(causes, c)
	}
	var since string
	err := db.QueryRow(`
		SELECT COALESCE(MIN(ground_truth_at), '') FROM decisions
		 WHERE question_id = ? AND COALESCE(ground_truth_at, '') <> ''
		   AND LOWER(ground_truth) IN (`+strings.TrimSuffix(strings.Repeat("?,", len(FailureParent)), ",")+`)`,
		causes...).Scan(&since)
	return since, err
}

// d2DigestBytes caps the per-session digest; never a full transcript.
const d2DigestBytes = 1500

// Labeler is the D2 background pass over newly finished sessions.
type Labeler struct {
	E *Engine
	// Batch caps sessions per pass (0 ⇒ 20); MinAge skips sessions that ended
	// too recently for their transcript to be fully ingested (0 ⇒ 10m).
	Batch  int
	MinAge time.Duration
}

type d2Session struct {
	uuid, title, started, ended, outcome string
}

// Run labels up to Batch unlabelled finished sessions. A no-op — zero sessions,
// no query — when the engine is unconfigured (no URL, claude off) or D2's
// outcome question is off. In shadow mode it only records decisions; in active
// mode it also writes session_labels, with `unknown` below the threshold.
func (l *Labeler) Run(ctx context.Context) (int, error) {
	e := l.E
	if !e.Configured() || e.DB == nil || e.Mode(QD2Outcome) == ModeOff {
		return 0, nil
	}
	batch := l.Batch
	if batch <= 0 {
		batch = 20
	}
	minAge := l.MinAge
	if minAge <= 0 {
		minAge = 10 * time.Minute
	}
	cutoff := e.now().Add(-minAge).UTC().Format("2006-01-02T15:04:05.000Z")
	// A session is DONE when every enabled D2 question has an error-free row — not
	// just the outcome one: a pass that answered outcome and then timed out on
	// failure_cause must come back for it. A session is GIVEN UP after
	// d2MaxFailures errored calls, so one digest the backend can never answer
	// does not stall every older session behind it forever. A session with no
	// turn at all is never a candidate: there is nothing for a question to read,
	// and its answers would only be noise in the labelling queue.
	enabled := make([]any, 0, 3)
	for _, q := range []string{QD2TaskType, QD2Outcome, QD2Failure} {
		if e.Mode(q) != ModeOff {
			enabled = append(enabled, q)
		}
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(enabled)), ",")
	args := append([]any{cutoff}, enabled...)
	args = append(args, len(enabled), QD2TaskType, QD2Outcome, QD2Failure, d2MaxFailures, batch)
	rows, err := e.DB.QueryContext(ctx, `
		SELECT s.session_uuid, COALESCE(s.custom_title, s.title, ''), COALESCE(s.started_at, ''),
		       COALESCE(s.ended_at, ''), COALESCE(s.outcome, '')
		  FROM sessions s
		 WHERE s.ended_at IS NOT NULL AND s.ended_at <= ? AND s.hidden = 0
		   AND EXISTS (SELECT 1 FROM turns t WHERE t.session_id = s.id)
		   AND (SELECT COUNT(DISTINCT d.question_id) FROM decisions d
		         WHERE d.subject = s.session_uuid AND d.error = '' AND d.question_id IN (`+ph+`)) < ?
		   AND (SELECT COUNT(*) FROM decisions d
		         WHERE d.subject = s.session_uuid AND d.error <> '' AND d.question_id IN (?, ?, ?)) < ?
		 ORDER BY s.ended_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return 0, err
	}
	var todo []d2Session
	for rows.Next() {
		var s d2Session
		if err := rows.Scan(&s.uuid, &s.title, &s.started, &s.ended, &s.outcome); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range todo {
		if ctx.Err() != nil {
			break
		}
		n++
		if failed := l.label(ctx, s); failed {
			// The backend is down or misbehaving: stop this pass rather than
			// burn one failed call per session. The session stays unlabelled and
			// the next pass retries it, up to d2MaxFailures errored calls.
			break
		}
	}
	return n, nil
}

// d2MaxFailures is how many errored D2 calls a session may collect before the
// labeler stops asking about it. Three passes (45 minutes at the default
// cadence) is long enough to ride out a backend restart.
const d2MaxFailures = 3

// operatorOutcome maps the operator's own verdict (sessions.outcome) onto D2's
// vocabulary — the rules backend's answer when it exists.
var operatorOutcome = map[string]string{"success": "shipped", "fail": "failed", "abandoned": "abandoned"}

// d2Digest is the evidence every D2 question reads: title, the plan phase the
// session executed (if any, with the head of its goal), the session's time
// window, the ship-evidence block, then the tail of the last assistant
// message. The window line deliberately avoids a bare `ended:` key — a small
// model echoed it back as an off-list outcome. facts are the session's, loaded
// once by the caller and shared with the rules.
func d2Digest(db *sql.DB, s d2Session, facts d2Facts) string {
	return fmt.Sprintf("title: %s\n%ssession window: %s → %s\n%slast assistant message (tail):\n%s",
		s.title, runContext(facts.run), s.started, s.ended, shipEvidence(db, facts, s.uuid, s.outcome),
		tail(runcore.LastAssistantText(db, s.uuid), d2DigestBytes))
}

// d2OpeningBytes caps the first user turn quoted to the task-type question;
// d2OpeningMatchBytes is how much of it a title must repeat to make the quote
// redundant.
const (
	d2OpeningBytes      = 200
	d2OpeningMatchBytes = 40
)

// openingLine is the one evidence line only the task-type question reads: the
// head of the session's first user turn. It says what the session was asked to
// do when the title does not — a title is written later, from the whole
// session, so a session that was asked to write a handoff file is titled after
// the work it hands off. "" for a plan engine run (its digest names the phase
// instead) and when the title already opens with the same words.
func openingLine(f d2Facts, title string) string {
	if f.run.kind != "" || f.opening == "" {
		return ""
	}
	if strings.HasPrefix(strings.Join(strings.Fields(title), " "), truncate(f.opening, d2OpeningMatchBytes)) {
		return ""
	}
	return "first request: " + f.opening + "\n"
}

// d2TaskTypeDefs says what each task type means, one clause per option, as the
// operator's own labels use them: `docs` is any text written for people (a
// how-to block and a session handoff file included), `review` judges existing
// work, `ops` is git and machine chores, `other` is a probe with no task.
var d2TaskTypeDefs = map[string]string{
	"feature":  "built or changed what the product does",
	"bugfix":   "something was broken, wrong or slow and the session found the cause or fixed it",
	"refactor": "restructured existing code without changing what it does",
	"docs":     "wrote text for people to read: documentation, a how-to block, a README, licence or contributor files, a post or reply, a session handoff file",
	"research": "answered a question about how something works and changed nothing",
	"review":   "scored, checked or judged existing work (a pull request, finished work, an agent's run) without changing it",
	"ops":      "only git or machine chores: commit, push, pull requests, merges, branch clean-up, install, deploy, login, settings",
	"planning": "wrote or revised a plan for later work and built nothing",
	"other":    "a trivial probe or bare command with no task (`p`, `say ok`), rare: only when nothing else fits",
}

// taskTypePrompt is the task-type question over opts: the lead, then the
// definition of each option offered and of no other.
func taskTypePrompt(opts []string) string {
	defs := make([]string, 0, len(opts))
	for _, o := range opts {
		defs = append(defs, o+" = "+d2TaskTypeDefs[o])
	}
	return "What kind of task was this coding session? Judge by what it was asked to do. " + strings.Join(defs, "; ") + "."
}

// d2RunTaskTypeSuffix closes the prompt of a session that ran a plan phase (or
// a whole plan), whose options also drop `planning` (runTaskTypes).
const d2RunTaskTypeSuffix = " A session that executes a plan phase is the kind of work the phase does, never planning."

// The task-type prompts: for any session, and for a plan engine run.
var (
	d2TaskTypePrompt    = taskTypePrompt(TaskTypes)
	d2RunTaskTypePrompt = taskTypePrompt(runTaskTypes) + d2RunTaskTypeSuffix
)

// The outcome and failure-cause prompts. The outcome prompt defines each option
// against the evidence block so "stopped talking" is not read as "abandoned"
// when the work landed — or when the work WAS the answer: a one-shot
// question-and-answer session commits nothing and still ships.
const (
	d2OutcomePrompt = "How did the session end? Judge by the evidence block first. " +
		"shipped = work was committed, pushed or merged, or every phase criterion was ticked; " +
		"a one-shot question-and-answer session that ended with a final answer and no error shipped; " +
		"partial = some work landed but not all of it; " +
		"abandoned = the session stopped with no landed work, no final answer and no failure; " +
		"failed = the work was attempted and did not work, or the account or the API stopped it."
	d2FailurePrompt = "If the session did not ship, what was the main cause? " +
		"(none if it shipped — commits, a merged PR or all criteria ticked in the evidence mean it shipped, " +
		"and so does a final answer with no error) " +
		"auth = the CLI was not logged in or its login expired; " +
		"quota = a usage or spend limit stopped the session; " +
		"api-error = the API was unreachable, overloaded or dropped the response."
)

// d2Questions builds the three D2 questions for one session, in asking order
// (task type, outcome, failure cause): id, prompt, options, the shared digest
// and the deterministic rules' answer where a rule applies (d2_rules.go), the
// operator's own verdict first. The task-type question alone reads one line
// more, ahead of the digest (openingLine); the other two read the digest as it
// is. It is the single path the labeler and the offline eval (eval.go) share,
// so a replay asks exactly what the daemon asks. phaseRunFeature switches rule
// R5 on (Engine.R5PhaseRunFeature). It only reads.
func d2Questions(db *sql.DB, s d2Session, phaseRunFeature bool) []Question {
	facts := d2FactsFor(db, s.uuid)
	rules := d2Rules(facts, s.outcome, phaseRunFeature)
	digest := d2Digest(db, s, facts)
	taskPrompt, taskTypes := d2TaskTypePrompt, TaskTypes
	if facts.run.kind != "" {
		taskPrompt, taskTypes = d2RunTaskTypePrompt, runTaskTypes
	}
	q := func(id, prompt string, opts []string, input string, rule d2RuleAnswer) Question {
		return Question{ID: id, Kind: KindChoice, Opts: opts, Prompt: prompt,
			Input: input, RuleAnswer: rule.value, RuleID: rule.rule, Subject: s.uuid, SessionUUID: s.uuid}
	}
	return []Question{
		q(QD2TaskType, taskPrompt, taskTypes, openingLine(facts, s.title)+digest, rules.taskType),
		q(QD2Outcome, d2OutcomePrompt, Outcomes, digest, rules.outcome),
		q(QD2Failure, d2FailurePrompt, FailureCauses, digest, rules.failure),
	}
}

// label asks one session's D2 questions and reports whether a MODEL call
// failed (the caller then stops the pass).
//
// A failed model call skips only the questions still bound for the model: a
// question the rules answer needs no backend, so it is asked and recorded
// whatever happened to the ones before it — a timed-out or off-list task type
// (asked first) must not cost the session its rule-answered outcome and cause.
//
// A question that already has an error-free row from an earlier pass is not
// asked again: its stored answer stands. A session comes back after a failed
// pass (until d2MaxFailures), and without this every retry would record the
// answers it already has a second and a third time.
func (l *Labeler) label(ctx context.Context, s d2Session) (failed bool) {
	e := l.E
	prior := answeredD2(e.DB, s.uuid)
	labels := map[string]string{}
	for _, q := range d2Questions(e.DB, s, e.R5PhaseRunFeature) {
		labels[q.ID] = LabelUnknown
		if e.Mode(q.ID) == ModeOff {
			continue
		}
		a, answered := prior[q.ID]
		if !answered {
			if failed && q.RuleAnswer == "" {
				continue
			}
			var err error
			if a, err = e.Decide(ctx, q); err != nil {
				failed = true
				continue
			}
		}
		if e.clears(q.ID, a) {
			labels[q.ID] = a.Value
		}
	}
	if failed || e.Mode(QD2Outcome) != ModeActive {
		return failed
	}
	if _, err := e.DB.Exec(`
		INSERT INTO session_labels (session_uuid, task_type, outcome, failure_cause, backend, labeled_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_uuid) DO UPDATE SET task_type=excluded.task_type, outcome=excluded.outcome,
		  failure_cause=excluded.failure_cause, backend=excluded.backend, labeled_at=excluded.labeled_at`,
		s.uuid, labels[QD2TaskType], labels[QD2Outcome], labels[QD2Failure], backendSummary(e), e.now().UTC().Format(time.RFC3339)); err != nil {
		log.Printf("warning: decide: d2 labels for %s: %v", s.uuid, err)
	}
	return false
}

// answeredD2 reads the session's D2 answers already recorded without an error,
// the newest per question, in the shape Decide returned them. A read that fails
// is logged and yields none: the questions are then asked again, which costs
// calls and duplicate rows but loses nothing.
func answeredD2(db *sql.DB, uuid string) map[string]Answer {
	out := map[string]Answer{}
	rows, err := db.Query(`
		SELECT question_id, answer, COALESCE(confidence, 0), calibrated, backend
		  FROM decisions
		 WHERE subject = ? AND error = '' AND question_id IN (?, ?, ?)
		 ORDER BY id`, uuid, QD2TaskType, QD2Outcome, QD2Failure)
	if err != nil {
		log.Printf("warning: decide: d2 recorded answers for %s: %v", uuid, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var a Answer
		var calibrated int
		if err := rows.Scan(&id, &a.Value, &a.Confidence, &calibrated, &a.Backend); err != nil {
			log.Printf("warning: decide: d2 recorded answers for %s: %v", uuid, err)
			return map[string]Answer{}
		}
		a.Calibrated = calibrated != 0
		out[id] = a // ascending id: the newest row wins
	}
	if err := rows.Err(); err != nil {
		log.Printf("warning: decide: d2 recorded answers for %s: %v", uuid, err)
		return map[string]Answer{}
	}
	return out
}

func backendSummary(e *Engine) string {
	if e.Local != nil {
		return BackendLocal
	}
	return BackendClaude
}

// SessionLabel is one session's D2 labels.
type SessionLabel struct {
	SessionUUID  string `json:"sessionUuid"`
	TaskType     string `json:"taskType"`
	Outcome      string `json:"outcome"`
	FailureCause string `json:"failureCause"`
	Backend      string `json:"backend"`
	LabeledAt    string `json:"labeledAt"`
}

// LabelFor reads a session's labels; nil, nil when it has none.
func LabelFor(db *sql.DB, uuid string) (*SessionLabel, error) {
	var l SessionLabel
	err := db.QueryRow(`SELECT session_uuid, task_type, outcome, failure_cause, backend, labeled_at
		FROM session_labels WHERE session_uuid=?`, uuid).
		Scan(&l.SessionUUID, &l.TaskType, &l.Outcome, &l.FailureCause, &l.Backend, &l.LabeledAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// LabelCount is one (field, value) tally over a window.
type LabelCount struct {
	Field string `json:"field"`
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// LabelCounts tallies the labels of sessions that started in [fromDay, toDay]
// (YYYY-MM-DD, inclusive), skipping `unknown`. Empty when nothing is labelled.
func LabelCounts(db *sql.DB, fromDay, toDay string) ([]LabelCount, error) {
	rows, err := db.Query(`
		SELECT f, v, COUNT(*) FROM (
		  SELECT 'task_type' AS f, l.task_type AS v, s.started_at AS st FROM session_labels l JOIN sessions s ON s.session_uuid = l.session_uuid
		  UNION ALL SELECT 'outcome', l.outcome, s.started_at FROM session_labels l JOIN sessions s ON s.session_uuid = l.session_uuid
		  UNION ALL SELECT 'failure_cause', l.failure_cause, s.started_at FROM session_labels l JOIN sessions s ON s.session_uuid = l.session_uuid
		) WHERE v <> 'unknown' AND substr(st, 1, 10) BETWEEN ? AND ?
		GROUP BY f, v ORDER BY f, COUNT(*) DESC, v`, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LabelCount
	for rows.Next() {
		var c LabelCount
		if err := rows.Scan(&c.Field, &c.Value, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// QuestionStats is one row of the Decisions view.
type QuestionStats struct {
	QuestionID string   `json:"questionId"`
	Mode       string   `json:"mode"`
	Threshold  float64  `json:"threshold"`
	Calls      int      `json:"calls"`
	Errors     int      `json:"errors"`
	Acted      int      `json:"acted"`
	WithTruth  int      `json:"withTruth"`
	Agreed     int      `json:"agreed"`
	Agreement  *float64 `json:"agreement"`
	Histogram  []int    `json:"histogram"` // 10 confidence buckets, [0,0.1) … [0.9,1]
}

// Summary computes the per-question stats for every known question.
func Summary(db *sql.DB, e *Engine) ([]QuestionStats, error) {
	byID := map[string]*QuestionStats{}
	out := make([]QuestionStats, len(KnownQuestions))
	for i, id := range KnownQuestions {
		mode := ModeFor(db, id, nil)
		if e != nil {
			mode = e.Mode(id)
		}
		out[i] = QuestionStats{QuestionID: id, Mode: string(mode), Threshold: e.Threshold(id), Histogram: make([]int, 10)}
		byID[id] = &out[i]
	}
	// Read before the cursor opens: the store runs one connection.
	since, err := newCausesInUseSince(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT question_id, answer, confidence, error, acted, COALESCE(ground_truth, ''),
		COALESCE(ground_truth_at, '') FROM decisions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, answer, errText, truth, truthAt string
		var conf sql.NullFloat64
		var acted int
		if err := rows.Scan(&id, &answer, &conf, &errText, &acted, &truth, &truthAt); err != nil {
			return nil, err
		}
		s := byID[id]
		if s == nil {
			continue
		}
		s.Calls++
		s.Acted += acted
		if errText != "" {
			s.Errors++
			continue
		}
		if conf.Valid {
			b := int(conf.Float64 * 10)
			s.Histogram[min(max(b, 0), 9)]++
		}
		if truth != "" {
			s.WithTruth++
			if Agrees(id, truth, answer, legacyTruth(truthAt, since)) {
				s.Agreed++
			}
		}
	}
	for i := range out {
		if out[i].WithTruth > 0 {
			v := float64(out[i].Agreed) / float64(out[i].WithTruth)
			out[i].Agreement = &v
		}
	}
	return out, rows.Err()
}
