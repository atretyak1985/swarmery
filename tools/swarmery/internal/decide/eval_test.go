package decide

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedTruth records one labelled decision: the daemon's stored answer and the
// operator's ground truth for it.
func seedTruth(t *testing.T, db *sql.DB, question, subject, answer, truth, truthAt string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, calibrated,
		backend, ground_truth, ground_truth_at, created_at)
		VALUES (?, ?, ?, 'h', ?, 0.8, 1, 'local', ?, ?, '2026-09-29T09:00:00Z')`,
		question, subject, subject, answer, truth, truthAt)
}

func evalQ(t *testing.T, rep EvalReport, id string) EvalQuestion {
	t.Helper()
	for _, q := range rep.Questions {
		if q.QuestionID == id {
			return q
		}
	}
	t.Fatalf("report has no %s row: %+v", id, rep.Questions)
	return EvalQuestion{}
}

func evalBackend(t *testing.T, q EvalQuestion, name string) EvalBackend {
	t.Helper()
	for _, b := range q.Backends {
		if b.Backend == name {
			return b
		}
	}
	t.Fatalf("%s has no %s backend row: %+v", q.QuestionID, name, q.Backends)
	return EvalBackend{}
}

// dbFingerprint is every table's row count plus a checksum of the decisions
// table's full content — what "the eval wrote nothing" is measured against.
func dbFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	var b strings.Builder
	for _, table := range []string{"decisions", "session_labels", "decide_modes", "sessions", "turns", "events"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		fmt.Fprintf(&b, "%s=%d ", table, n)
	}
	rows, err := db.Query(`SELECT id, question_id, subject, session_uuid, input_hash, answer, probs_json,
		COALESCE(confidence, -1), calibrated, backend, latency_ms, mode, rule_value, acted, error,
		COALESCE(ground_truth, '<null>'), COALESCE(ground_truth_at, '<null>'), created_at FROM decisions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	sum := sha256.New()
	cols := make([]any, 18)
	for i := range cols {
		cols[i] = new(any)
	}
	for rows.Next() {
		if err := rows.Scan(cols...); err != nil {
			t.Fatal(err)
		}
		for _, c := range cols {
			fmt.Fprintf(sum, "%v\x00", *(c.(*any)))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String() + hex.EncodeToString(sum.Sum(nil))
}

// totalChanges is SQLite's count of rows this connection inserted, updated or
// deleted since it opened (the store runs on a single connection).
func totalChanges(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The truth for a (subject, question) is the label written LAST — greatest
// ground_truth_at, then id — whatever order the rows were inserted in; and
// TruthSince bounds on that same timestamp.
func TestEvalLatestTruthWins(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-relabel", "2026-09-20T11:00:00.000Z", "success") // rules answer: shipped
	seedSession(t, db, "s-tie", "2026-09-20T11:00:00.000Z", "fail")        // rules answer: failed
	// s-relabel: the HIGHER id carries the OLDER label. Time wins, not id.
	seedTruth(t, db, QD2Outcome, "s-relabel", "partial", "shipped", "2026-09-30T10:00:00Z") // id 1, newest
	seedTruth(t, db, QD2Outcome, "s-relabel", "partial", "partial", "2026-09-29T10:00:00Z") // id 2, older
	// s-tie: the same instant twice — the higher id wins.
	seedTruth(t, db, QD2Outcome, "s-tie", "failed", "abandoned", "2026-09-29T12:00:00Z") // id 3
	seedTruth(t, db, QD2Outcome, "s-tie", "failed", "failed", "2026-09-29T12:00:00Z")    // id 4, wins

	rep, err := Eval(context.Background(), db, nil, EvalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := evalQ(t, rep, QD2Outcome)
	if rep.Subjects != 2 || q.Labelled != 2 || q.Replayed != 2 || q.Agree != 2 || len(q.Confusions) != 0 {
		t.Fatalf("outcome = %+v (subjects %d), want 2 labelled, both agreeing with their LATEST truth", q, rep.Subjects)
	}
	if q.Agreement == nil || *q.Agreement != 1 {
		t.Errorf("agreement = %v, want 1", q.Agreement)
	}
	// The recorded answers (partial vs shipped, failed vs failed) agree once.
	if q.RecordedAgree != 1 {
		t.Errorf("recorded agree = %d, want 1", q.RecordedAgree)
	}

	for _, tc := range []struct {
		since        string
		wantLabelled int
	}{
		{"2026-09-29T00:00:00Z", 2},
		{"2026-09-29T12:00:00Z", 2},      // inclusive
		{"2026-09-29T12:00:01Z", 1},      // s-tie's label is older
		{"2026-09-30T13:00:00+03:00", 1}, // 10:00Z in another zone — inclusive
		{"2026-10-01T00:00:00Z", 0},
	} {
		since, err := time.Parse(time.RFC3339, tc.since)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := Eval(context.Background(), db, nil, EvalOptions{TruthSince: since})
		if err != nil {
			t.Fatal(err)
		}
		if got := evalQ(t, rep, QD2Outcome).Labelled; got != tc.wantLabelled {
			t.Errorf("since %s: labelled = %d, want %d", tc.since, got, tc.wantLabelled)
		}
	}
}

// The eval applies the same cutoff to both of its comparisons — the replayed
// answer and the recorded one: an `other` label written after the operator's
// first `quota` label no longer agrees with a `quota` answer.
func TestEvalParentAgreementStopsOnceTheNewCausesAreInUse(t *testing.T) {
	db := openDB(t)
	const limit = "You've hit your session limit · resets 1:30am (UTC)" // R1 answers quota
	for _, uuid := range []string{"s-first-quota", "s-legacy-other", "s-late-other"} {
		seedEnding(t, db, uuid, syntheticModel, "stop_sequence", limit)
	}
	seedTruth(t, db, QD2Failure, "s-first-quota", "quota", "quota", "2026-10-02T10:00:00Z")
	seedTruth(t, db, QD2Failure, "s-legacy-other", "quota", "other", "2026-09-29T10:00:00Z")
	seedTruth(t, db, QD2Failure, "s-late-other", "quota", "other", "2026-10-03T10:00:00Z")

	rep, err := Eval(context.Background(), db, nil, EvalOptions{Questions: []string{QD2Failure}})
	if err != nil {
		t.Fatal(err)
	}
	q := evalQ(t, rep, QD2Failure)
	if q.Labelled != 3 || q.Replayed != 3 || q.Agree != 2 || q.RecordedAgree != 2 {
		t.Errorf("failure cause = labelled %d, replayed %d, agree %d, recorded agree %d — want 3, 3, 2, 2",
			q.Labelled, q.Replayed, q.Agree, q.RecordedAgree)
	}
}

// The eval reads and nothing else: with the local backend answering every
// question, every table's row count, the full content of decisions and the
// connection's change counter are identical before and after.
func TestEvalWritesNothing(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-1", "2026-09-20T11:00:00.000Z", "success")
	seedSession(t, db, "s-2", "2026-09-20T11:00:00.000Z", "")
	seedEmptySession(t, db, "s-empty", "2026-09-20T11:00:00.000Z", "")
	seedBash(t, db, "s-1", `git commit -m x`, "ok")
	for _, sess := range []string{"s-1", "s-2", "s-empty", "s-gone"} {
		seedTruth(t, db, QD2TaskType, sess, "bugfix", "bugfix", "2026-09-29T10:00:00Z")
		seedTruth(t, db, QD2Outcome, sess, "partial", "shipped", "2026-09-29T10:00:00Z")
		seedTruth(t, db, QD2Failure, sess, "other", "other", "2026-09-29T10:00:00Z")
	}
	mustExec(t, db, `INSERT INTO decisions (question_id, subject, input_hash, answer, error, created_at)
		VALUES ('d2.outcome', 's-2', 'h', '', 'local: timeout', '2026-09-29T09:00:00Z')`)
	if err := SetMode(db, QD2Outcome, ModeActive, time.Now()); err != nil { // an ACTIVE mode must not make the eval label
		t.Fatal(err)
	}
	s := &stub{name: BackendLocal, a: Answer{Value: "other", Confidence: 0.95, Calibrated: true},
		byQ: map[string]Answer{QD2Outcome: {Value: "shipped", Confidence: 0.95, Calibrated: true}}}
	e := &Engine{DB: db, Local: s, DefaultModes: map[string]Mode{"d2": ModeActive}}

	before, changesBefore := dbFingerprint(t, db), totalChanges(t, db)
	rep, err := Eval(context.Background(), db, e, EvalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if after := dbFingerprint(t, db); after != before {
		t.Errorf("the eval changed the database:\nbefore %s\nafter  %s", before, after)
	}
	if n := totalChanges(t, db) - changesBefore; n != 0 {
		t.Errorf("the eval's connection changed %d rows, want 0", n)
	}
	if l, _ := LabelFor(db, "s-1"); l != nil {
		t.Errorf("the eval wrote session labels: %+v", l)
	}
	// It did do the work it claims: both sessions with turns were replayed, and
	// the local backend was really asked (5 calls — s-1's outcome is a rule).
	if !rep.LLM || rep.Subjects != 4 || s.calls != 5 {
		t.Errorf("llm=%v subjects=%d backend calls=%d, want true/4/5", rep.LLM, rep.Subjects, s.calls)
	}
	for _, id := range EvalQuestions {
		if q := evalQ(t, rep, id); q.Labelled != 4 || q.Replayed != 2 || q.Skipped != 2 ||
			q.SkipReasons[EvalSkipZeroTurns] != 1 || q.SkipReasons[EvalSkipNoSession] != 1 {
			t.Errorf("%s = %+v, want 4 labelled, 2 replayed, one zero-turns and one no-session skip", id, q)
		}
	}
}

// With no local backend the engine is unconfigured and the eval still runs:
// the rules answer what they know, everything else is counted unanswered, and
// no backend is ever called.
func TestEvalRulesOnly(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-verdict", "2026-09-20T11:00:00.000Z", "success") // rules: shipped — agrees
	seedSession(t, db, "s-wrong", "2026-09-20T11:00:00.000Z", "abandoned") // rules: abandoned — truth partial
	seedSession(t, db, "s-plain", "2026-09-20T11:00:00.000Z", "")          // no verdict: unanswered
	for sess, outcome := range map[string]string{"s-verdict": "shipped", "s-wrong": "partial", "s-plain": "failed"} {
		seedTruth(t, db, QD2TaskType, sess, "bugfix", "bugfix", "2026-09-29T10:00:00Z")
		seedTruth(t, db, QD2Outcome, sess, "failed", outcome, "2026-09-29T10:00:00Z")
		seedTruth(t, db, QD2Failure, sess, "auth", "other", "2026-09-29T10:00:00Z")
	}
	// A label on a question the eval cannot replay is left out, not miscounted.
	seedTruth(t, db, QD1, "phaserun:7", D1Blocked, D1Blocked, "2026-09-29T10:00:00Z")

	e := New(db, Config{})
	if e.Configured() {
		t.Fatal("an engine with no URL must be unconfigured")
	}
	rep, err := Eval(context.Background(), db, e, EvalOptions{})
	if err != nil {
		t.Fatalf("rules-only eval on an unconfigured engine: %v", err)
	}
	if rep.LLM || rep.Subjects != 3 || len(rep.Questions) != 3 {
		t.Fatalf("report = %+v, want rules-only, 3 subjects, the 3 D2 questions", rep)
	}

	out := evalQ(t, rep, QD2Outcome)
	if out.Labelled != 3 || out.Replayed != 3 || out.Skipped != 0 || out.Agree != 1 || out.Errors != 0 {
		t.Errorf("outcome = %+v, want 3 replayed, 1 agree", out)
	}
	if out.Agreement == nil || *out.Agreement != 1.0/3.0 {
		t.Errorf("outcome agreement = %v, want 1/3 (unanswered is a miss)", out.Agreement)
	}
	if r := evalBackend(t, out, BackendRules); r.N != 2 || r.Agree != 1 || r.Precision == nil || *r.Precision != 0.5 {
		t.Errorf("rules = %+v, want 2 answered, 1 agree, precision 0.5", r)
	}
	if l := evalBackend(t, out, BackendLocal); l.N != 0 || l.Precision != nil {
		t.Errorf("local = %+v, want nothing answered", l)
	}
	if u := evalBackend(t, out, evalUnanswered); u.N != 1 || u.Precision != nil {
		t.Errorf("unanswered = %+v, want 1", u)
	}
	if len(out.Confusions) != 1 || out.Confusions[0] != (EvalConfusion{Answer: "abandoned", Truth: "partial", Count: 1}) {
		t.Errorf("confusions = %+v, want abandoned → partial ×1", out.Confusions)
	}
	for _, b := range out.Buckets {
		if b.N != 0 {
			t.Errorf("a rule answer landed in a confidence bucket: %+v", out.Buckets)
		}
	}

	for _, id := range []string{QD2TaskType, QD2Failure} {
		q := evalQ(t, rep, id)
		if q.Replayed != 3 || q.Agree != 0 || evalBackend(t, q, evalUnanswered).N != 3 || q.Agreement == nil || *q.Agreement != 0 {
			t.Errorf("%s = %+v, want 3 replayed, all unanswered", id, q)
		}
	}
	// The stored answers are still scored: task_type matched, and the recorded
	// `auth` agrees with the `other` label through the parent rule.
	if got := evalQ(t, rep, QD2TaskType).RecordedAgree; got != 3 {
		t.Errorf("task_type recorded agree = %d, want 3", got)
	}
	if got := evalQ(t, rep, QD2Failure).RecordedAgree; got != 3 {
		t.Errorf("failure_cause recorded agree = %d, want 3 (auth agrees with other)", got)
	}
}

// With a local backend the report splits rules from local, applies the parent
// rule, ranks confusions, buckets non-rule confidence and counts failed calls
// as unanswered errors.
func TestEvalLocalBackendReport(t *testing.T) {
	db := openDB(t)
	for i, outcome := range []string{"success", "", "", ""} {
		seedSession(t, db, fmt.Sprintf("s-%d", i), "2026-09-20T11:00:00.000Z", outcome)
	}
	truths := map[string][3]string{ // task_type, outcome, failure_cause
		"s-0": {"bugfix", "shipped", "none"},
		"s-1": {"feature", "partial", "other"},
		"s-2": {"feature", "partial", "auth"},
		"s-3": {"docs", "failed", "timeout"},
	}
	for sess, tr := range truths {
		at := "2026-09-29T10:00:0" + sess[2:] + "Z" // s-3 is the newest label
		seedTruth(t, db, QD2TaskType, sess, "bugfix", tr[0], at)
		seedTruth(t, db, QD2Outcome, sess, "partial", tr[1], at)
		seedTruth(t, db, QD2Failure, sess, "other", tr[2], at)
	}
	b := &failOnInput{byQ: map[string]Answer{
		QD2TaskType: {Value: "bugfix", Confidence: 0.95, Calibrated: true},
		QD2Outcome:  {Value: "partial", Confidence: 0.55, Calibrated: true},
		QD2Failure:  {Value: "auth", Confidence: 0.35, Calibrated: true},
	}, failQuestion: QD2TaskType, failAfter: 3}
	e := &Engine{Local: b}

	var progress []string
	rep, err := Eval(context.Background(), db, e, EvalOptions{Progress: func(done, total, errs int) {
		progress = append(progress, fmt.Sprintf("%d/%d/%d", done, total, errs))
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(progress, " "); got != "1/4/0 2/4/0 3/4/0 4/4/1" {
		t.Errorf("progress = %q, want one call per subject with the error on the last", got)
	}

	task := evalQ(t, rep, QD2TaskType) // bugfix ×3 answered (truths docs, feature, feature), s-0's call failed
	if task.Replayed != 4 || task.Agree != 0 || task.Errors != 1 || evalBackend(t, task, evalUnanswered).N != 1 ||
		evalBackend(t, task, BackendLocal).N != 3 {
		t.Errorf("task_type = %+v, want 3 local answers, 1 errored call", task)
	}
	if len(task.Confusions) != 2 || task.Confusions[0] != (EvalConfusion{Answer: "bugfix", Truth: "feature", Count: 2}) ||
		task.Confusions[1] != (EvalConfusion{Answer: "bugfix", Truth: "docs", Count: 1}) {
		t.Errorf("task_type confusions = %+v, want bugfix → feature ×2 first", task.Confusions)
	}
	if task.Buckets[9] != (EvalBucket{N: 3, Agree: 0}) {
		t.Errorf("task_type bucket 0.9 = %+v, want 3 answers, none agreeing", task.Buckets[9])
	}

	out := evalQ(t, rep, QD2Outcome) // s-0 by rule (shipped), the rest local partial: 2 agree, 1 not
	if r, l := evalBackend(t, out, BackendRules), evalBackend(t, out, BackendLocal); r.N != 1 || r.Agree != 1 || l.N != 3 || l.Agree != 2 ||
		l.Precision == nil || *l.Precision != 2.0/3.0 {
		t.Errorf("outcome backends = rules %+v local %+v", r, l)
	}
	if out.Agree != 3 || out.Buckets[5] != (EvalBucket{N: 3, Agree: 2}) {
		t.Errorf("outcome = %+v, want 3 agree and the local answers in bucket 0.5", out)
	}

	fail := evalQ(t, rep, QD2Failure) // answer auth: agrees with truth other (parent) and auth; not none, not timeout
	if fail.Agree != 2 || fail.Buckets[3] != (EvalBucket{N: 4, Agree: 2}) {
		t.Errorf("failure_cause = %+v, want 2 agree (auth, and other through the parent rule)", fail)
	}

	// Limit keeps the newest labels; Questions narrows the report.
	rep, err = Eval(context.Background(), db, &Engine{}, EvalOptions{Limit: 1, Questions: []string{QD2Failure, QD2Outcome}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Subjects != 1 || len(rep.Questions) != 2 || rep.Questions[0].QuestionID != QD2Outcome || rep.Questions[1].QuestionID != QD2Failure {
		t.Errorf("limited report = %+v, want 1 subject and outcome, failure_cause in the canonical order", rep)
	}
	if q := evalQ(t, rep, QD2Failure); q.Labelled != 1 || q.RecordedAgree != 0 {
		t.Errorf("limit 1 kept %+v, want the newest label (s-3: recorded other vs truth timeout)", q)
	}
}

// failOnInput answers per question and fails one question after N calls to it.
type failOnInput struct {
	byQ          map[string]Answer
	failQuestion string
	failAfter    int
	seen         int
}

func (f *failOnInput) Name() string { return BackendLocal }
func (f *failOnInput) Ask(_ context.Context, q Question) (Answer, error) {
	if q.ID == f.failQuestion {
		f.seen++
		if f.seen > f.failAfter {
			return Answer{}, errors.New("timeout")
		}
	}
	return f.byQ[q.ID], nil
}

func TestEvalRejectsBadInput(t *testing.T) {
	db := openDB(t)
	if _, err := Eval(context.Background(), nil, nil, EvalOptions{}); err == nil {
		t.Error("a nil database must be refused")
	}
	for _, q := range []string{QD1, QD3Cause, "d2.nope"} {
		if _, err := Eval(context.Background(), db, nil, EvalOptions{Questions: []string{q}}); err == nil {
			t.Errorf("question %q cannot be replayed and must be refused", q)
		}
	}
	// An empty store is a valid, empty report.
	rep, err := Eval(context.Background(), db, nil, EvalOptions{})
	if err != nil || rep.Subjects != 0 || len(rep.Questions) != 3 || rep.Questions[0].Agreement != nil {
		t.Errorf("empty store: %+v %v, want three empty rows with no agreement", rep, err)
	}
	// A cancelled context stops the replay instead of finishing it.
	seedSession(t, db, "s-1", "2026-09-20T11:00:00.000Z", "success")
	seedTruth(t, db, QD2Outcome, "s-1", "shipped", "shipped", "2026-09-29T10:00:00Z")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Eval(ctx, db, nil, EvalOptions{}); err == nil {
		t.Error("a cancelled context must stop the eval")
	}
}

func TestEvalMissedFloors(t *testing.T) {
	half, none := 0.5, (*float64)(nil)
	rep := EvalReport{Questions: []EvalQuestion{
		{QuestionID: QD2Outcome, Agreement: &half},
		{QuestionID: QD2Failure, Agreement: none},
	}}
	if got := rep.MissedFloors(nil); len(got) != 0 {
		t.Errorf("no floors: %v", got)
	}
	if got := rep.MissedFloors(map[string]float64{QD2Outcome: 0.5}); len(got) != 0 {
		t.Errorf("a floor met exactly was reported missed: %v", got)
	}
	got := rep.MissedFloors(map[string]float64{QD2Outcome: 0.6, QD2Failure: 0, QD2TaskType: 0.1})
	if len(got) != 3 || !strings.HasPrefix(got[0], QD2Failure+": nothing replayed") ||
		!strings.HasPrefix(got[1], QD2Outcome+": agreement 0.500 is below floor 0.600") ||
		!strings.HasPrefix(got[2], QD2TaskType+": not in this eval") {
		t.Errorf("missed = %q", got)
	}
}

func TestRenderEval(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-1", "2026-09-20T11:00:00.000Z", "success")
	seedEmptySession(t, db, "s-empty", "2026-09-20T11:00:00.000Z", "")
	for _, sess := range []string{"s-1", "s-empty"} {
		seedTruth(t, db, QD2TaskType, sess, "bugfix", "feature", "2026-09-29T10:00:00Z")
		seedTruth(t, db, QD2Outcome, sess, "shipped", "shipped", "2026-09-29T10:00:00Z")
	}
	since := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	e := &Engine{Local: &stub{name: BackendLocal, a: Answer{Value: "bugfix", Confidence: 0.72, Calibrated: true}}}
	rep, err := Eval(context.Background(), db, e, EvalOptions{TruthSince: since})
	if err != nil {
		t.Fatal(err)
	}

	var text bytes.Buffer
	if err := RenderEval(&text, rep, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"decide eval — rules + local model, labels since 2026-09-29T00:00:00Z, 2 subjects",
		"question           labelled replayed skipped  agree agreement  recorded",
		"d2.task_type              2        1       1      0      0.0%  0/2 0.0%",
		"d2.outcome                2        1       1      1    100.0%  2/2 100.0%",
		"d2.failure_cause          0        0       0      0         —  0/0 —",
		"  rules            1      1    100.0%",
		"  unanswered       0      —         —  (errors: 0)",
		"  skipped: zero-turns 1",
		"        1  bugfix → feature",
		"  confusions (answer → truth): none",
		"  confidence (non-rule answers): none",
		"    bucket   0.0   0.1   0.2   0.3   0.4   0.5   0.6   0.7   0.8   0.9",
		"    n          0     0     0     0     0     0     0     1     0     0",
	} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text report lacks %q:\n%s", want, text.String())
		}
	}

	rules, err := Eval(context.Background(), db, nil, EvalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text.Reset()
	if err := RenderEval(&text, rules, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "decide eval — rules-only, all labels, 2 subjects") {
		t.Errorf("rules-only header missing:\n%s", text.String())
	}

	var js bytes.Buffer
	if err := RenderEval(&js, rep, true); err != nil {
		t.Fatal(err)
	}
	var back EvalReport
	if err := json.Unmarshal(js.Bytes(), &back); err != nil {
		t.Fatalf("JSON report does not parse: %v\n%s", err, js.String())
	}
	if !back.LLM || back.TruthSince != "2026-09-29T00:00:00Z" || len(back.Questions) != 3 ||
		back.Questions[0].QuestionID != QD2TaskType || len(back.Questions[0].Buckets) != 10 ||
		back.Questions[0].SkipReasons[EvalSkipZeroTurns] != 1 {
		t.Errorf("JSON round trip = %+v", back)
	}
	for _, key := range []string{`"questionId"`, `"labelled"`, `"replayed"`, `"skipReasons"`, `"agreement"`, `"recordedAgree"`, `"backends"`, `"precision"`, `"confusions"`, `"buckets"`} {
		if !strings.Contains(js.String(), key) {
			t.Errorf("JSON report lacks %s", key)
		}
	}
}
