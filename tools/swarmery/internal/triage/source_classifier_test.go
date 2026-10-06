package triage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	dec "github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
)

const classifierSystemPath = "/system"

// newClassifierEnv is a triage service over a fresh store with three projects
// (1 = /repo, 2 = the System project, 3 = /other) and a ClassifierSource on the
// same database, registered.
func newClassifierEnv(t *testing.T, j Judge) (*Service, *ClassifierSource) {
	t.Helper()
	s := newTestService(t, j)
	for _, p := range []struct {
		id   int64
		path string
	}{{1, "/repo"}, {2, classifierSystemPath}, {3, "/other"}} {
		mustExecT(t, s.DB, `INSERT INTO projects(id, path, slug, first_seen) VALUES(?, ?, ?, '2026-01-01T00:00:00Z')`,
			p.id, p.path, fmt.Sprintf("p%d", p.id))
	}
	src := &ClassifierSource{DB: s.DB, SystemPath: classifierSystemPath,
		Now: func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }}
	s.Register(src)
	return s, src
}

func mustExecT(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// seedClassifierSession is a finished session with one main-thread user turn
// (the label queue hides a session with no turn).
func seedClassifierSession(t *testing.T, db *sql.DB, uuid string, project int64, title string) {
	t.Helper()
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, title, started_at, ended_at)
		VALUES (?, ?, ?, '2026-10-01T10:00:00.000Z', '2026-10-01T11:00:00.000Z')`, project, uuid, title)
	seedClassifierTurn(t, db, uuid, 1, "user", nil, "please fix the parser")
}

func seedClassifierTurn(t *testing.T, db *sql.DB, uuid string, seq int, role string, agent any, text string) {
	t.Helper()
	mustExecT(t, db, `INSERT INTO turns (session_id, seq, role, started_at, agent_name, text)
		VALUES ((SELECT id FROM sessions WHERE session_uuid = ?), ?, ?, '2026-10-01T10:30:00.000Z', ?, ?)`,
		uuid, seq, role, agent, text)
}

// seedDecision is one answered, unlabelled local-model decision; it returns its id.
func seedDecision(t *testing.T, db *sql.DB, question, uuid, answer, createdAt string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence,
		calibrated, backend, created_at) VALUES (?, ?, ?, 'h', ?, 0.6, 1, 'local', ?)`,
		question, uuid, uuid, answer, createdAt)
	if err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func ref(id int64) string { return strconv.FormatInt(id, 10) }

func truthOf(t *testing.T, db *sql.DB, id int64) (truth, source string) {
	t.Helper()
	var tr sql.NullString
	if err := db.QueryRow(`SELECT ground_truth, ground_truth_source FROM decisions WHERE id=?`, id).Scan(&tr, &source); err != nil {
		t.Fatal(err)
	}
	return tr.String, source
}

func queued(t *testing.T, db *sql.DB) map[int64]bool {
	t.Helper()
	q, err := dec.LabelQueue(db, dec.QueueOptions{Limit: -1, SystemPath: classifierSystemPath})
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]bool{}
	for _, it := range q {
		out[it.ID] = true
	}
	return out
}

func TestClassifierCollectGroupsSessions(t *testing.T) {
	s, src := newClassifierEnv(t, &stubJudge{})
	seedClassifierSession(t, s.DB, "aaaaaaaa-1111", 1, "fix the parser")
	tt := seedDecision(t, s.DB, dec.QD2TaskType, "aaaaaaaa-1111", "bugfix", "2026-10-02T10:00:00Z")
	oc := seedDecision(t, s.DB, dec.QD2Outcome, "aaaaaaaa-1111", "shipped", "2026-10-02T10:00:01Z")
	fc := seedDecision(t, s.DB, dec.QD2Failure, "aaaaaaaa-1111", "none", "2026-10-02T10:00:02Z")
	seedClassifierSession(t, s.DB, "bbbbbbbb-2222", 3, "") // older, untitled
	old := seedDecision(t, s.DB, dec.QD2Outcome, "bbbbbbbb-2222", "failed", "2026-10-01T09:00:00Z")
	seedClassifierSession(t, s.DB, "ssssssss-3333", 2, "system run") // System project
	seedDecision(t, s.DB, dec.QD2Outcome, "ssssssss-3333", "shipped", "2026-09-01T09:00:00Z")
	seedDecision(t, s.DB, dec.QD2Outcome, "", "shipped", "2026-09-01T08:00:00Z") // no session

	items, err := src.Collect(context.Background(), Scope{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want the two non-System sessions", items)
	}
	b, a := items[0], items[1]
	if b.Key != "bbbbbbbb-2222" || b.Title != "bbbbbbbb" || b.ProjectID != 3 || b.WaitingSince != "2026-10-01T09:00:00Z" ||
		len(b.Parts) != 1 || b.Parts[0].Ref != ref(old) {
		t.Fatalf("oldest item = %+v", b)
	}
	if a.Kind != kindClassifier || a.Class != "" || a.Key != "aaaaaaaa-1111" || a.Title != "fix the parser" ||
		a.ProjectID != 1 || a.WaitingSince != "2026-10-02T10:00:00Z" {
		t.Fatalf("item = %+v", a)
	}
	wantParts := []Part{
		{Ref: ref(tt), Label: dec.QD2TaskType, Allowed: dec.OptionsFor(dec.QD2TaskType)},
		{Ref: ref(oc), Label: dec.QD2Outcome, Allowed: dec.OptionsFor(dec.QD2Outcome)},
		{Ref: ref(fc), Label: dec.QD2Failure, Allowed: dec.OptionsFor(dec.QD2Failure)},
	}
	if len(a.Parts) != 3 {
		t.Fatalf("parts = %+v", a.Parts)
	}
	for i, p := range a.Parts {
		w := wantParts[i]
		if p.Ref != w.Ref || p.Label != w.Label || !slices.Equal(p.Allowed, w.Allowed) {
			t.Errorf("part %d = %+v, want %+v", i, p, w)
		}
	}
	for _, it := range items {
		if it.Instruction != "" || it.Evidence != "" {
			t.Fatalf("Collect built evidence for %s", it.Key)
		}
	}

	if items, _ := src.Collect(context.Background(), Scope{}, 1); len(items) != 1 || items[0].Key != "bbbbbbbb-2222" {
		t.Fatalf("limit 1 = %+v", items)
	}
	if items, _ := src.Collect(context.Background(), Scope{ProjectID: 1}, 0); len(items) != 1 || items[0].Key != "aaaaaaaa-1111" {
		t.Fatalf("project scope = %+v", items)
	}
}

func TestClassifierCollectSkipsUningestedSession(t *testing.T) {
	s, src := newClassifierEnv(t, &stubJudge{})
	id := seedDecision(t, s.DB, dec.QD2Outcome, "later-1", "shipped", "2026-10-02T10:00:00Z")
	if !queued(t, s.DB)[id] {
		t.Fatal("precondition: the decision is in the label queue")
	}
	items, err := src.Collect(context.Background(), Scope{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %+v, want none while the session has no row", items)
	}
	seedClassifierSession(t, s.DB, "later-1", 1, "ingested")
	items, err = src.Collect(context.Background(), Scope{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "later-1" || items[0].ProjectID != 1 || len(items[0].Parts) != 1 ||
		items[0].Parts[0].Ref != ref(id) {
		t.Fatalf("items = %+v, want the ingested session", items)
	}
}

func TestClassifierPrepareBounds(t *testing.T) {
	s, src := newClassifierEnv(t, &stubJudge{})
	uuid := "prep-session"
	mustExecT(t, s.DB, `INSERT INTO sessions (project_id, session_uuid, title, started_at, ended_at)
		VALUES (1, ?, 'parser work', '2026-10-01T10:00:00.000Z', '2026-10-01T11:00:00.000Z')`, uuid)
	opening := "OPENING " + strings.Repeat("€", 1500) // 4,508 bytes, 3-byte runes
	seedClassifierTurn(t, s.DB, uuid, 1, "user", nil, opening)
	seedClassifierTurn(t, s.DB, uuid, 2, "assistant", "helper", "SUBAGENT-ONLY")
	for seq := 3; seq <= 7; seq++ {
		seedClassifierTurn(t, s.DB, uuid, seq, "assistant", nil, fmt.Sprintf("T%d ", seq)+strings.Repeat("ä", 1000))
	}
	seedClassifierTurn(t, s.DB, uuid, 8, "user", nil, "")
	id := seedDecision(t, s.DB, dec.QD2Outcome, uuid, "abandoned", "2026-10-02T10:00:00Z")

	items, err := src.Collect(context.Background(), Scope{}, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("collect = %+v, %v", items, err)
	}
	it := items[0]
	if err := src.Prepare(context.Background(), &it); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(it.Instruction, classifierInstruction) ||
		!strings.Contains(it.Instruction, ref(id)+" "+dec.QD2Outcome) || !strings.Contains(it.Instruction, `"abandoned"`) ||
		!strings.Contains(it.Instruction, strings.Join(dec.OptionsFor(dec.QD2Outcome), ", ")) {
		t.Fatalf("instruction = %q", it.Instruction)
	}
	digest, err := dec.SessionEvidence(s.DB, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(it.Evidence, digest) {
		t.Fatalf("evidence does not open with the D2 digest:\n%s", it.Evidence)
	}
	if !utf8.ValidString(it.Evidence) {
		t.Fatal("evidence cut inside a rune")
	}
	if strings.Contains(it.Evidence, "SUBAGENT-ONLY") || strings.Contains(it.Evidence, "T3 ") {
		t.Fatal("excerpt includes a sub-agent turn or a turn before the last four")
	}
	excerpt := strings.TrimPrefix(it.Evidence, digest)
	var sawOpening bool
	var turnLines int
	for _, line := range strings.Split(excerpt, "\n") {
		switch {
		case strings.HasPrefix(line, "OPENING "):
			sawOpening = true
			if len(line) > classifierOpeningBytes || len(line) < classifierOpeningBytes-3 {
				t.Errorf("opening is %d bytes, want ≤ %d and cut close to it", len(line), classifierOpeningBytes)
			}
		case strings.HasPrefix(line, "assistant: "):
			turnLines++
			if body := strings.TrimPrefix(line, "assistant: "); len(body) > classifierTurnBytes {
				t.Errorf("turn is %d bytes, want ≤ %d", len(body), classifierTurnBytes)
			}
		}
	}
	if !sawOpening || turnLines != 4 {
		t.Fatalf("opening=%v turn lines=%d, want the opening and 4 turns:\n%s", sawOpening, turnLines, excerpt)
	}
	t.Logf("prepared prompt: instruction %d + evidence %d = %d bytes",
		len(it.Instruction), len(it.Evidence), len(it.Instruction)+len(it.Evidence))

	// A session with no turns still prepares: the excerpt is just empty.
	mustExecT(t, s.DB, `INSERT INTO sessions (project_id, session_uuid, title, started_at)
		VALUES (1, 'bare', 'bare', '2026-10-01T10:00:00.000Z')`)
	bare := Item{Kind: kindClassifier, Key: "bare", Parts: []Part{{Ref: ref(seedDecision(t, s.DB, dec.QD2Outcome, "bare", "shipped", "2026-10-02T10:00:00Z"))}}}
	if err := src.Prepare(context.Background(), &bare); err != nil {
		t.Fatal(err)
	}
	if d, _ := dec.SessionEvidence(s.DB, "bare"); bare.Evidence != d {
		t.Fatalf("bare evidence = %q, want the digest only %q", bare.Evidence, d)
	}
}

func TestClassifierRunLabelsAndRejectsOffList(t *testing.T) {
	t.Setenv("SWARMERY_TRIAGE_SAMPLE", "0")
	j := &stubJudge{values: map[string]string{}}
	s, _ := newClassifierEnv(t, j)
	seedClassifierSession(t, s.DB, "run-1", 3, "parser")
	tt := seedDecision(t, s.DB, dec.QD2TaskType, "run-1", "feature", "2026-10-02T10:00:00Z")
	oc := seedDecision(t, s.DB, dec.QD2Outcome, "run-1", "abandoned", "2026-10-02T10:00:01Z")
	fc := seedDecision(t, s.DB, dec.QD2Failure, "run-1", "auth", "2026-10-02T10:00:02Z")
	j.values[ref(tt)] = "bugfix"
	j.values[ref(oc)] = "shipped"
	j.values[ref(fc)] = "zzz" // off the list

	r := mustRun(t, s, StartReq{Kinds: []string{kindClassifier}})
	st := states(t, s, r.ID)
	if st[ref(tt)] != StateApplied || st[ref(oc)] != StateApplied || st[ref(fc)] != StateRejected {
		t.Fatalf("states = %v, run = %+v", st, r)
	}
	for id, want := range map[int64]string{tt: "bugfix", oc: "shipped"} {
		if truth, source := truthOf(t, s.DB, id); truth != want || source != dec.TruthAgent {
			t.Errorf("decision %d = %q/%q, want %q/agent", id, truth, source, want)
		}
	}
	if truth, _ := truthOf(t, s.DB, fc); truth != "" {
		t.Fatalf("off-list value written: %q", truth)
	}
	q := queued(t, s.DB)
	if q[tt] || q[oc] || !q[fc] {
		t.Fatalf("queue = %v: labelled questions must leave it, the rejected one must stay", q)
	}
	vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.ProjectID == nil || *v.ProjectID != 3 || v.ItemKey != "run-1" {
			t.Fatalf("verdict %+v: want project 3 and key run-1", v)
		}
	}
}

func TestClassifierApplyLosesToOperator(t *testing.T) {
	s, src := newClassifierEnv(t, &stubJudge{})
	seedClassifierSession(t, s.DB, "op-1", 1, "x")
	id := seedDecision(t, s.DB, dec.QD2Outcome, "op-1", "shipped", "2026-10-02T10:00:00Z")
	if err := dec.RecordGroundTruth(s.DB, id, "failed", dec.TruthOperator, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := src.Apply(context.Background(), Item{}, Part{Ref: ref(id)}, "shipped", "", nil)
	if !errors.Is(err, dec.ErrAlreadyLabelled) {
		t.Fatalf("apply = %v, want ErrAlreadyLabelled", err)
	}
	if truth, source := truthOf(t, s.DB, id); truth != "failed" || source != dec.TruthOperator {
		t.Fatalf("operator label changed: %q/%q", truth, source)
	}
	// Undo never clears an operator label, and reports no error.
	if err := src.Undo(context.Background(), Verdict{Ref: ref(id)}); err != nil {
		t.Fatal(err)
	}
	if truth, source := truthOf(t, s.DB, id); truth != "failed" || source != dec.TruthOperator {
		t.Fatalf("undo cleared an operator label: %q/%q", truth, source)
	}
}

func TestClassifierUndoIdempotent(t *testing.T) {
	t.Setenv("SWARMERY_TRIAGE_SAMPLE", "0")
	s, src := newClassifierEnv(t, &stubJudge{value: "shipped"})
	seedClassifierSession(t, s.DB, "undo-1", 1, "x")
	id := seedDecision(t, s.DB, dec.QD2Outcome, "undo-1", "failed", "2026-10-02T10:00:00Z")
	r := mustRun(t, s, StartReq{})
	vs, err := s.ListVerdicts(VerdictFilter{RunID: r.ID})
	if err != nil || len(vs) != 1 || vs[0].State != StateApplied {
		t.Fatalf("verdicts = %+v, %v", vs, err)
	}
	if open, err := src.Open(context.Background(), ref(id)); err != nil || open {
		t.Fatalf("open after apply = %v, %v", open, err)
	}
	v, err := s.Undo(context.Background(), vs[0].ID)
	if err != nil || v.State != StateUndone {
		t.Fatalf("undo = %+v, %v", v, err)
	}
	if truth, source := truthOf(t, s.DB, id); truth != "" || source != "" {
		t.Fatalf("after undo = %q/%q", truth, source)
	}
	if !queued(t, s.DB)[id] {
		t.Fatal("undone question is not back in the queue")
	}
	if open, err := src.Open(context.Background(), ref(id)); err != nil || !open {
		t.Fatalf("open after undo = %v, %v", open, err)
	}
	if err := src.Undo(context.Background(), vs[0]); err != nil {
		t.Fatalf("second undo = %v", err)
	}
	if err := src.Undo(context.Background(), Verdict{Ref: ref(id)}); err != nil {
		t.Fatalf("undo of an unrecorded verdict = %v", err)
	}
	if open, err := src.Open(context.Background(), "999999"); err != nil || open {
		t.Fatalf("open unknown = %v, %v", open, err)
	}
}

func TestClassifierSampleHoldsFive(t *testing.T) {
	t.Setenv("SWARMERY_TRIAGE_SAMPLE", "")
	s, src := newClassifierEnv(t, &stubJudge{value: "shipped"})
	var ids []int64
	for i := range 8 {
		uuid := fmt.Sprintf("sample-%d", i)
		seedClassifierSession(t, s.DB, uuid, 1, uuid)
		ids = append(ids, seedDecision(t, s.DB, dec.QD2Outcome, uuid, "failed", fmt.Sprintf("2026-10-02T10:00:0%dZ", i)))
	}
	r := mustRun(t, s, StartReq{})
	st := states(t, s, r.ID)
	var samples, applied int
	var sampled []int64
	for _, id := range ids {
		truth, source := truthOf(t, s.DB, id)
		open, err := src.Open(context.Background(), ref(id))
		if err != nil {
			t.Fatal(err)
		}
		switch st[ref(id)] {
		case StateSample:
			samples++
			sampled = append(sampled, id)
			if truth != "" || !open {
				t.Errorf("sample %d was labelled: %q", id, truth)
			}
		case StateApplied:
			applied++
			if truth != "shipped" || source != dec.TruthAgent || open {
				t.Errorf("applied %d = %q/%q open=%v", id, truth, source, open)
			}
		default:
			t.Errorf("decision %d state %q", id, st[ref(id)])
		}
	}
	if samples != 5 || applied != 3 {
		t.Fatalf("samples=%d applied=%d, want 5 and 3 (states %v)", samples, applied, st)
	}
	if rows := mustAudit(t, s.DB); len(rows) != 0 {
		t.Fatalf("audit before any operator answer = %+v", rows)
	}

	// The operator answers one sampled question; the next sweep audits only it.
	answered := sampled[0]
	if err := dec.RecordGroundTruth(s.DB, answered, "shipped", dec.TruthOperator, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepOpen(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = states(t, s, r.ID)
	for _, id := range sampled {
		want := StateSample
		if id == answered {
			want = StateAudited
		}
		if st[ref(id)] != want {
			t.Errorf("after sweep decision %d = %q, want %q", id, st[ref(id)], want)
		}
	}
	rows := mustAudit(t, s.DB)
	if len(rows) != 1 || rows[0] != (AuditRow{QuestionID: dec.QD2Outcome, Answered: 1, Agree: 1}) {
		t.Fatalf("audit = %+v, want one agreeing d2.outcome answer", rows)
	}
}
