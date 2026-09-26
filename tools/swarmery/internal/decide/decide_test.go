package decide

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "decide.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO projects(id, path, slug, first_seen) VALUES(1, '/repo', 'p', '2026-01-01T00:00:00Z')`)
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// fakeServer is an OpenAI-compatible chat-completions endpoint. reply builds
// the response for each request; the last request body is kept for asserts.
type fakeServer struct {
	mu    sync.Mutex
	last  map[string]any
	path  string
	calls int
	reply func(req map[string]any) (int, any)
	delay time.Duration
}

func (f *fakeServer) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.last, f.path = req, r.URL.Path
		f.calls++
		f.mu.Unlock()
		if f.delay > 0 {
			select {
			case <-time.After(f.delay):
			case <-r.Context().Done():
				return
			}
		}
		code, resp := f.reply(req)
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type lp struct {
	tok  string
	lp   float64
	alts map[string]float64
}

// chat renders a chat-completions response whose content is the concatenation
// of the tokens, each with its logprob and alternatives. nil tokens ⇒ no
// logprobs block at all.
func chat(content string, toks []lp) any {
	choice := map[string]any{"message": map[string]any{"role": "assistant", "content": content}}
	if toks != nil {
		var arr []any
		for _, t := range toks {
			var alts []any
			for k, v := range t.alts {
				alts = append(alts, map[string]any{"token": k, "logprob": v})
			}
			arr = append(arr, map[string]any{"token": t.tok, "logprob": t.lp, "top_logprobs": alts})
		}
		choice["logprobs"] = map[string]any{"content": arr}
	}
	return map[string]any{"choices": []any{choice}}
}

func blockedTokens(pBlocked float64) []lp {
	return []lp{
		{tok: `{"`, lp: 0}, {tok: `answer`, lp: 0}, {tok: `":"`, lp: 0},
		{tok: `blocked`, lp: math.Log(pBlocked), alts: map[string]float64{
			"blocked":  math.Log(pBlocked),
			"report":   math.Log((1 - pBlocked) * 0.75),
			"question": math.Log((1 - pBlocked) * 0.25),
		}},
		{tok: `"}`, lp: 0},
	}
}

func d1Question() Question {
	return Question{ID: QD1, Kind: KindChoice, Opts: D1Options, Prompt: "how did it end?", Input: "evidence"}
}

func TestLocal_AskSchemaConstrainedWithLogprobConfidence(t *testing.T) {
	f := &fakeServer{reply: func(map[string]any) (int, any) {
		return 200, chat(`{"answer":"blocked"}`, blockedTokens(0.9))
	}}
	srv := f.start(t)
	l := &Local{URL: srv.URL + "/v1", Model: "qwen"}
	a, err := l.Ask(context.Background(), d1Question())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if a.Value != D1Blocked || !a.Calibrated {
		t.Fatalf("answer = %+v, want calibrated blocked", a)
	}
	if math.Abs(a.Confidence-0.9) > 1e-6 {
		t.Errorf("confidence = %v, want 0.9", a.Confidence)
	}
	if math.Abs(a.Probs[D1Report]-0.075) > 1e-6 || math.Abs(a.Probs[D1Question]-0.025) > 1e-6 {
		t.Errorf("probs = %v", a.Probs)
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("path = %q", f.path)
	}
	if f.last["model"] != "qwen" || f.last["logprobs"] != true {
		t.Errorf("request = %v", f.last)
	}
	rf, _ := json.Marshal(f.last["response_format"])
	if !strings.Contains(string(rf), `"json_schema"`) || !strings.Contains(string(rf), `"enum":["report-with-next-step","blocked","question-for-operator","done"]`) {
		t.Errorf("response_format is not schema-constrained to the options: %s", rf)
	}
}

func TestLocal_TokenStraddlingTheQuoteStillMaps(t *testing.T) {
	f := &fakeServer{reply: func(map[string]any) (int, any) {
		return 200, chat(`{"answer": "done"}`, []lp{
			{tok: `{"answer":`, lp: 0},
			{tok: ` "do`, lp: math.Log(0.6), alts: map[string]float64{` "do`: math.Log(0.6), ` "re`: math.Log(0.3), `xyz`: math.Log(0.1)}},
			{tok: `ne"}`, lp: 0},
		})
	}}
	srv := f.start(t)
	a, err := (&Local{URL: srv.URL}).Ask(context.Background(), d1Question())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if a.Value != D1Done || !a.Calibrated || math.Abs(a.Confidence-0.6) > 1e-6 || math.Abs(a.Probs[D1Report]-0.3) > 1e-6 {
		t.Errorf("answer = %+v", a)
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("bare host path = %q, want /v1/chat/completions", f.path)
	}
}

func TestLocal_NoLogprobsIsUncalibrated(t *testing.T) {
	f := &fakeServer{reply: func(map[string]any) (int, any) { return 200, chat(`{"answer":"done"}`, nil) }}
	srv := f.start(t)
	a, err := (&Local{URL: srv.URL + "/v1/chat/completions"}).Ask(context.Background(), d1Question())
	if err != nil || a.Value != D1Done || a.Calibrated || a.Confidence != 0 {
		t.Fatalf("answer = %+v err=%v, want uncalibrated done", a, err)
	}
}

func TestLocal_Errors(t *testing.T) {
	cases := map[string]func(map[string]any) (int, any){
		"http 500":      func(map[string]any) (int, any) { return 500, map[string]any{"error": "boom"} },
		"not an option": func(map[string]any) (int, any) { return 200, chat(`{"answer":"maybe"}`, nil) },
		"no json":       func(map[string]any) (int, any) { return 200, chat(`I think blocked`, nil) },
		"no choices":    func(map[string]any) (int, any) { return 200, map[string]any{"choices": []any{}} },
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			srv := (&fakeServer{reply: reply}).start(t)
			if _, err := (&Local{URL: srv.URL}).Ask(context.Background(), d1Question()); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestLocal_Timeout(t *testing.T) {
	f := &fakeServer{delay: 2 * time.Second, reply: func(map[string]any) (int, any) { return 200, chat(`{"answer":"done"}`, nil) }}
	srv := f.start(t)
	start := time.Now()
	_, err := (&Local{URL: srv.URL, Timeout: 50 * time.Millisecond}).Ask(context.Background(), d1Question())
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %s, want a prompt timeout", err, time.Since(start))
	}
	if LocalTimeout != 5*time.Second || ClaudeTimeout != 60*time.Second {
		t.Error("the plan fixes the timeouts at 5s local / 60s claude")
	}
}

func TestQuestionKinds(t *testing.T) {
	if got := (Question{Kind: KindYesNo}).Options(); strings.Join(got, ",") != "yes,no" {
		t.Errorf("yes/no options = %v", got)
	}
	q := Question{Kind: KindScore}
	if v, ok := q.canonical(" 4 "); !ok || v != "4" {
		t.Errorf("score canonical = %q %v", v, ok)
	}
	if _, err := parseAnswer(q, `{"answer": 3}`); err != nil {
		t.Errorf("a bare numeric score must parse: %v", err)
	}
}

func TestClaude_UncalibratedSelfReport(t *testing.T) {
	c := &Claude{Run: func(_ context.Context, prompt string) (string, error) {
		if !strings.Contains(prompt, "Options: report-with-next-step") {
			t.Errorf("prompt lacks options: %s", prompt)
		}
		return "sure: {\"answer\": \"question-for-operator\", \"confidence\": 0.97}", nil
	}}
	a, err := c.Ask(context.Background(), d1Question())
	if err != nil || a.Value != D1Question || a.Calibrated || a.Confidence != 0.97 {
		t.Fatalf("answer = %+v err=%v", a, err)
	}
	if ClaudeModel != "claude-haiku-4-5" || ClaudeEffort != "low" {
		t.Error("the claude backend is pinned to haiku at low effort")
	}
	if _, err := (&Claude{Run: func(context.Context, string) (string, error) { return "", errors.New("down") }}).Ask(context.Background(), d1Question()); err == nil {
		t.Error("a failed spawn must be an error")
	}
}

// stub is a scripted backend.
type stub struct {
	name  string
	a     Answer
	byQ   map[string]Answer // per-question answers, overriding a
	err   error
	calls int
}

func (s *stub) Name() string { return s.name }
func (s *stub) Ask(_ context.Context, q Question) (Answer, error) {
	s.calls++
	if a, ok := s.byQ[q.ID]; ok {
		return a, s.err
	}
	return s.a, s.err
}

func decisionRows(t *testing.T, db *sql.DB) []map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT question_id, subject, answer, backend, mode, acted, error, COALESCE(ground_truth,''), calibrated FROM decisions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var q, subj, ans, be, mode, errText, truth string
		var acted, cal int
		if err := rows.Scan(&q, &subj, &ans, &be, &mode, &acted, &errText, &truth, &cal); err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]any{"q": q, "subject": subj, "answer": ans, "backend": be, "mode": mode,
			"acted": acted, "error": errText, "truth": truth, "calibrated": cal})
	}
	return out
}

func TestEngine_BackendOrderAndPrivacy(t *testing.T) {
	db := openDB(t)
	local := &stub{name: BackendLocal, err: errors.New("connection refused")}
	cl := &stub{name: BackendClaude, a: Answer{Value: D1Done}}
	e := &Engine{DB: db, Local: local, Claude: cl}

	// Not allowed to leave the machine: claude is never asked.
	if _, err := e.Decide(context.Background(), d1Question()); err == nil {
		t.Fatal("want the local error")
	}
	if cl.calls != 0 {
		t.Fatal("the claude backend was used for a question that may not leave the machine")
	}
	q := d1Question()
	q.AllowRemote = true
	a, err := e.Decide(context.Background(), q)
	if err != nil || a.Backend != BackendClaude || cl.calls != 1 {
		t.Fatalf("answer=%+v err=%v calls=%d, want the claude fallback", a, err, cl.calls)
	}
	// Rules answer first, with no backend call at all.
	q.RuleAnswer = "Blocked"
	a, _ = e.Decide(context.Background(), q)
	if a.Backend != BackendRules || a.Value != D1Blocked || a.Confidence != 1 || local.calls != 2 {
		t.Fatalf("rules answer = %+v (local calls %d)", a, local.calls)
	}
	rows := decisionRows(t, db)
	if len(rows) != 3 || rows[0]["error"] == "" || rows[1]["backend"] != BackendClaude || rows[2]["backend"] != BackendRules {
		t.Fatalf("every call must have a row: %v", rows)
	}
	if _, err := (&Engine{DB: db}).Decide(context.Background(), d1Question()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no backend: err = %v", err)
	}
	var nilEngine *Engine
	if _, err := nilEngine.Decide(context.Background(), d1Question()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil engine: err = %v", err)
	}
}

func ambiguousInput() D1Input {
	return D1Input{Engine: "phaserun", SubjectID: 7, SessionUUID: "u-1", LastText: "Next I would do b.", StopReason: "end_turn", Done: 1, Total: 2}
}

func TestD1_Branches(t *testing.T) {
	cases := []struct {
		name       string
		mode       Mode
		answer     Answer
		err        error
		wantAction Action
		wantActed  int
		wantNotify bool
	}{
		{"shadow blocked is ignored", ModeShadow, Answer{Value: D1Blocked, Confidence: 0.99, Calibrated: true}, nil, Keep, 0, false},
		{"active blocked stamps blocked", ModeActive, Answer{Value: D1Blocked, Confidence: 0.95, Calibrated: true}, nil, StampBlocked, 1, false},
		{"active question notifies", ModeActive, Answer{Value: D1Question, Confidence: 0.95, Calibrated: true}, nil, NotifyOperator, 1, true},
		{"active report continues", ModeActive, Answer{Value: D1Report, Confidence: 0.95, Calibrated: true}, nil, Keep, 0, false},
		{"active done trusts the checkboxes", ModeActive, Answer{Value: D1Done, Confidence: 0.95, Calibrated: true}, nil, Keep, 0, false},
		{"active below threshold is the safe default", ModeActive, Answer{Value: D1Report, Confidence: 0.5, Calibrated: true}, nil, NotifyOperator, 1, true},
		{"active uncalibrated is the safe default", ModeActive, Answer{Value: D1Report, Confidence: 0.99}, nil, NotifyOperator, 1, true},
		{"active classifier error keeps the rules", ModeActive, Answer{}, errors.New("down"), Keep, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			notified := false
			e := &Engine{DB: db, Local: &stub{name: BackendLocal, a: tc.answer, err: tc.err},
				DefaultModes: map[string]Mode{"d1": tc.mode}, Thresholds: map[string]float64{"d1": 0.85},
				OnNeedsOperator: func(NeedsOperator) { notified = true }}
			o := e.D1(context.Background(), ambiguousInput())
			if o.Action != tc.wantAction {
				t.Errorf("action = %v, want %v (%s)", o.Action, tc.wantAction, o.Detail)
			}
			if notified != tc.wantNotify {
				t.Errorf("notified = %v", notified)
			}
			rows := decisionRows(t, db)
			if len(rows) != 1 || rows[0]["acted"] != tc.wantActed || rows[0]["subject"] != "phaserun:7" || rows[0]["mode"] != string(tc.mode) {
				t.Errorf("rows = %v", rows)
			}
		})
	}
}

func TestD1_NoCallOutsideTheAmbiguousBranch(t *testing.T) {
	db := openDB(t)
	s := &stub{name: BackendLocal, a: Answer{Value: D1Blocked, Confidence: 1, Calibrated: true}}
	active := &Engine{DB: db, Local: s, DefaultModes: map[string]Mode{"d1": ModeActive}}
	for _, in := range []D1Input{
		{StopReason: "", Done: 1, Total: 2},         // no stop_reason recorded
		{StopReason: "max_tokens", Done: 1, Total: 2}, // not a plain end of turn
		{StopReason: "end_turn", Done: 2, Total: 2},   // all ticked
		{StopReason: "end_turn", Done: 0, Total: 0},   // nothing to measure
	} {
		if o := active.D1(context.Background(), in); o.Action != Keep {
			t.Errorf("%+v: action %v", in, o.Action)
		}
	}
	off := &Engine{DB: db, Local: s, DefaultModes: map[string]Mode{"d1": ModeOff}}
	off.D1(context.Background(), ambiguousInput())
	unconfigured := New(db, Config{Modes: map[string]Mode{"d1": ModeActive}})
	if o := unconfigured.D1(context.Background(), ambiguousInput()); o.Action != Keep {
		t.Error("an unconfigured engine must keep the rules")
	}
	var nilEngine *Engine
	nilEngine.Tracker().Decide(context.Background(), ambiguousInput())
	nilEngine.Tracker().Observe("done", 2)
	if s.calls != 0 || len(decisionRows(t, db)) != 0 {
		t.Errorf("calls=%d rows=%d, want none", s.calls, len(decisionRows(t, db)))
	}
}

func TestD1_GroundTruthAfterContinuation(t *testing.T) {
	cases := []struct {
		end            string
		before, after  int
		want           string
	}{
		{"blocked", 1, 1, D1Blocked},
		{"done", 1, 2, D1Report},
		{"continue", 1, 1, D1Question},
	}
	for _, c := range cases {
		if got := D1TruthAfterContinuation(c.end, c.before, c.after); got != c.want {
			t.Errorf("%+v → %q", c, got)
		}
	}
	db := openDB(t)
	e := &Engine{DB: db, Local: &stub{name: BackendLocal, a: Answer{Value: D1Report, Confidence: 0.9, Calibrated: true}}}
	tr := e.Tracker()
	tr.Observe("done", 2) // nothing pending yet
	tr.Decide(context.Background(), ambiguousInput())
	tr.Observe("done", 2)
	tr.Observe("done", 2) // recorded once
	rows := decisionRows(t, db)
	if len(rows) != 1 || rows[0]["truth"] != D1Report {
		t.Fatalf("rows = %v", rows)
	}
	stats, err := Summary(db, e)
	if err != nil {
		t.Fatal(err)
	}
	if stats[0].QuestionID != QD1 || stats[0].Calls != 1 || stats[0].WithTruth != 1 || stats[0].Agreement == nil || *stats[0].Agreement != 1 || stats[0].Histogram[9] != 1 {
		t.Errorf("stats = %+v", stats[0])
	}
	if err := RecordGroundTruth(db, 999, "x", time.Now()); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown id: %v", err)
	}
	if err := RecordGroundTruth(db, 1, " ", time.Now()); err == nil {
		t.Error("empty truth must be refused")
	}
}

func TestModes(t *testing.T) {
	db := openDB(t)
	e := &Engine{DB: db, DefaultModes: map[string]Mode{"d2": ModeActive}}
	if e.Mode(QD1) != ModeShadow || e.Mode(QD2Outcome) != ModeActive {
		t.Errorf("defaults: d1=%s d2=%s", e.Mode(QD1), e.Mode(QD2Outcome))
	}
	if err := SetMode(db, QD1, ModeActive, time.Now()); err != nil {
		t.Fatal(err)
	}
	if e.Mode(QD1) != ModeActive || ModeFor(db, QD1, nil) != ModeActive {
		t.Error("the dashboard switch must override the env default")
	}
	if SetMode(db, "nope", ModeOff, time.Now()) == nil || SetMode(db, QD1, "loud", time.Now()) == nil {
		t.Error("unknown question/mode must be refused")
	}
	var nilEngine *Engine
	if nilEngine.Mode(QD1) != ModeOff || nilEngine.Threshold(QD1) != DefaultThreshold || nilEngine.Configured() {
		t.Error("nil engine must be inert")
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	cfg, warn := ConfigFromEnv(get)
	if cfg.URL != "" || cfg.Claude || cfg.Modes["d1"] != ModeShadow || cfg.Modes["d2"] != ModeShadow || len(warn) != 0 {
		t.Fatalf("defaults = %+v %v", cfg, warn)
	}
	if New(nil, cfg).Configured() {
		t.Error("no URL and claude off must leave the engine inert")
	}
	env = map[string]string{"SWARMERY_DECIDE_URL": "http://lm:1234/v1", "SWARMERY_DECIDE_D1": "active",
		"SWARMERY_DECIDE_D2": "loud", "SWARMERY_DECIDE_CLAUDE": "maybe", "SWARMERY_DECIDE_D1_THRESHOLD": "0.9",
		"SWARMERY_DECIDE_D2_THRESHOLD": "7"}
	cfg, warn = ConfigFromEnv(get)
	if cfg.Modes["d1"] != ModeActive || cfg.Modes["d2"] != ModeShadow || cfg.Thresholds["d1"] != 0.9 || cfg.Claude || len(warn) != 3 {
		t.Errorf("cfg = %+v warn = %v", cfg, warn)
	}
	e := New(nil, cfg)
	if e.Local == nil || e.Claude != nil || e.Threshold(QD1) != 0.9 || !strings.Contains(cfg.String(), "http://lm:1234/v1") {
		t.Errorf("engine = %+v", e)
	}
	env["SWARMERY_DECIDE_CLAUDE"] = "on"
	cfg, _ = ConfigFromEnv(get)
	if New(nil, cfg).Claude == nil {
		t.Error("SWARMERY_DECIDE_CLAUDE=on must enable the claude backend")
	}
}

func seedSession(t *testing.T, db *sql.DB, uuid, ended, outcome string) {
	t.Helper()
	var out any
	if outcome != "" {
		out = outcome
	}
	mustExec(t, db, `INSERT INTO sessions (project_id, session_uuid, title, started_at, ended_at, outcome) VALUES (1, ?, 'fix the parser', '2026-09-20T10:00:00.000Z', ?, ?)`,
		uuid, ended, out)
}

func TestLabeler(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-old", "2026-09-20T11:00:00.000Z", "success")
	seedSession(t, db, "s-fresh", "2026-09-23T11:59:00.000Z", "")
	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	s := &stub{name: BackendLocal, byQ: map[string]Answer{
		QD2TaskType: {Value: "bugfix", Confidence: 0.9, Calibrated: true},
		QD2Outcome:  {Value: "partial", Confidence: 0.9, Calibrated: true},
		QD2Failure:  {Value: "none", Confidence: 0.9, Calibrated: true},
	}}

	// Unconfigured: a no-op.
	if n, err := (&Labeler{E: &Engine{DB: db}}).Run(context.Background()); n != 0 || err != nil {
		t.Fatalf("unconfigured labeler ran: %d %v", n, err)
	}

	// Shadow: decisions only, no labels; the fresh session is skipped.
	e := &Engine{DB: db, Local: s, Now: now, Thresholds: map[string]float64{"d2": 0.6}}
	n, err := (&Labeler{E: e}).Run(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("shadow run: %d %v", n, err)
	}
	if l, _ := LabelFor(db, "s-old"); l != nil {
		t.Fatalf("shadow mode wrote labels: %+v", l)
	}
	rows := decisionRows(t, db)
	if len(rows) != 3 || rows[1]["backend"] != BackendRules || rows[1]["answer"] != "shipped" {
		t.Fatalf("rows = %v", rows)
	}
	if n, _ := (&Labeler{E: e}).Run(context.Background()); n != 0 {
		t.Errorf("an asked session must not be asked again (%d)", n)
	}

	// Active: labels are written (the operator's own outcome wins via rules).
	mustExec(t, db, `DELETE FROM decisions`)
	e.DefaultModes = map[string]Mode{"d2": ModeActive}
	if _, err := (&Labeler{E: e}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	l, err := LabelFor(db, "s-old")
	if err != nil || l == nil || l.TaskType != "bugfix" || l.Outcome != "shipped" || l.FailureCause != "none" || l.Backend != BackendLocal {
		t.Fatalf("labels = %+v err=%v", l, err)
	}
	if l, _ := LabelFor(db, "s-fresh"); l != nil {
		t.Errorf("a session that ended minutes ago must wait for ingest: %+v", l)
	}
	counts, err := LabelCounts(db, "2026-09-20", "2026-09-20")
	if err != nil || len(counts) == 0 {
		t.Fatalf("counts = %v err=%v", counts, err)
	}

	// Below threshold ⇒ unknown.
	mustExec(t, db, `DELETE FROM decisions`)
	for k, a := range s.byQ {
		a.Confidence = 0.3
		s.byQ[k] = a
	}
	if _, err := (&Labeler{E: e}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l, _ := LabelFor(db, "s-old"); l.TaskType != LabelUnknown || l.Outcome != "shipped" {
		t.Errorf("below threshold: %+v", l)
	}

	// A failing backend stops the pass after one session and is retried later.
	seedSession(t, db, "s-other", "2026-09-21T11:00:00.000Z", "")
	mustExec(t, db, `DELETE FROM decisions`)
	s.err = errors.New("connection refused")
	s.calls = 0
	n, _ = (&Labeler{E: e}).Run(context.Background())
	if n != 1 || s.calls != 1 {
		t.Errorf("failing backend: labelled %d with %d calls, want 1/1", n, s.calls)
	}
}

func TestD3_ModesAndRuleView(t *testing.T) {
	db := openDB(t)
	in := D3Input{SessionUUID: "u3", PhaseName: "P", Summary: "s", TopComponent: "size_miss",
		Divergence: "The code was not what the plan assumed.", ModelFallback: true}

	// Unconfigured and off: no call, no row.
	if out := (&Engine{DB: db}).D3(context.Background(), in); out.Asked {
		t.Fatal("unconfigured engine asked D3")
	}
	s := &stub{name: BackendLocal, a: Answer{Value: "code-differs", Confidence: 0.9, Calibrated: true}}
	off := &Engine{DB: db, Local: s, DefaultModes: map[string]Mode{"d3": ModeOff}}
	if out := off.D3(context.Background(), in); out.Asked || s.calls != 0 {
		t.Fatal("D3 asked while off")
	}

	// Shadow: asked and recorded with the fallback as the rules' view, no label.
	shadow := &Engine{DB: db, Local: s}
	out := shadow.D3(context.Background(), in)
	if !out.Asked || out.Label != "" || out.DecisionID == 0 || out.Err != nil {
		t.Fatalf("shadow = %+v", out)
	}
	var rule, mode string
	if err := db.QueryRow(`SELECT rule_value, mode FROM decisions WHERE id = ?`, out.DecisionID).Scan(&rule, &mode); err != nil {
		t.Fatal(err)
	}
	if rule != "model-fallback" || mode != "shadow" {
		t.Fatalf("row rule=%q mode=%q", rule, mode)
	}

	// Active: the answer at/above the threshold, unknown below it, "" on error.
	active := &Engine{DB: db, Local: s, DefaultModes: map[string]Mode{"d3": ModeActive}, Thresholds: map[string]float64{"d3": 0.6}}
	if out := active.D3(context.Background(), in); out.Label != "code-differs" {
		t.Fatalf("active label = %q", out.Label)
	}
	s.a = Answer{Value: "code-differs", Confidence: 0.5, Calibrated: true}
	if out := active.D3(context.Background(), in); out.Label != LabelUnknown {
		t.Fatalf("below threshold label = %q", out.Label)
	}
	s.a, s.err = Answer{}, errors.New("down")
	if out := active.D3(context.Background(), in); out.Label != "" || out.Err == nil {
		t.Fatalf("errored call = %+v", out)
	}
	// An answer outside the taxonomy is a failed call, never a label.
	s.a, s.err = Answer{Value: "gremlins", Confidence: 0.99, Calibrated: true}, nil
	if out := active.D3(context.Background(), in); out.Label != "" || out.Err == nil {
		t.Fatalf("out-of-taxonomy answer = %+v", out)
	}
}

func TestConfigFromEnv_D3(t *testing.T) {
	env := map[string]string{"SWARMERY_DECIDE_D3": "active", "SWARMERY_DECIDE_D3_THRESHOLD": "0.7"}
	cfg, warn := ConfigFromEnv(func(k string) string { return env[k] })
	if cfg.Modes["d3"] != ModeActive || cfg.Thresholds["d3"] != 0.7 || len(warn) != 0 ||
		!strings.Contains(cfg.String(), "d3=active(0.70)") {
		t.Fatalf("cfg=%+v warn=%v str=%s", cfg, warn, cfg.String())
	}
	if !slices.Contains(KnownQuestions, QD3Cause) {
		t.Fatal("D3 missing from the Decisions page's question list")
	}
}

// LM Studio's reasoning parser strips the braces off a Qwen3 thinking model's
// schema-constrained reply. The grammar still bounded the value, so a bare or
// half-stripped option is the answer, and its logprob still gives confidence.
func TestLocal_BraceStrippedReplyStillAnswers(t *testing.T) {
	for _, content := range []string{`done`, `"done"`, `answer": "done`} {
		t.Run(content, func(t *testing.T) {
			f := &fakeServer{reply: func(map[string]any) (int, any) {
				i := strings.Index(content, "done")
				return 200, chat(content, []lp{
					{tok: content[:i], lp: 0},
					{tok: "done", lp: math.Log(0.8), alts: map[string]float64{"done": math.Log(0.8), "report": math.Log(0.2)}},
					{tok: content[i+len("done"):], lp: 0},
				})
			}}
			srv := f.start(t)
			a, err := (&Local{URL: srv.URL}).Ask(context.Background(), d1Question())
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			if a.Value != D1Done || !a.Calibrated || math.Abs(a.Confidence-0.8) > 1e-6 {
				t.Errorf("answer = %+v, want calibrated done at 0.8", a)
			}
		})
	}
}

// SWARMERY_DECIDE_SCHEMA=off drops response_format and SWARMERY_DECIDE_USER_SUFFIX
// reaches the question: the combination that makes LM Studio's MLX Qwen3 answer
// multi-token options in full (`"ref` truncation with the enum schema; an empty
// reply without it, the budget spent on hidden thinking).
func TestLocal_NoSchemaAndSuffix(t *testing.T) {
	f := &fakeServer{reply: func(map[string]any) (int, any) {
		return 200, chat("\n\n{\"answer\": \"report-with-next-step\"}", []lp{
			{tok: "\n\n", lp: 0}, {tok: `{"`, lp: 0}, {tok: `answer`, lp: 0}, {tok: `":`, lp: 0}, {tok: ` "`, lp: 0},
			{tok: `report`, lp: math.Log(0.7), alts: map[string]float64{"report": math.Log(0.7), "blocked": math.Log(0.3)}},
			{tok: `-with-next-step"}`, lp: 0},
		})
	}}
	srv := f.start(t)
	a, err := (&Local{URL: srv.URL, NoSchema: true, UserSuffix: "/no_think"}).Ask(context.Background(), d1Question())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if a.Value != D1Report || !a.Calibrated || math.Abs(a.Confidence-0.7) > 1e-6 {
		t.Errorf("answer = %+v, want calibrated report-with-next-step at 0.7", a)
	}
	if _, has := f.last["response_format"]; has {
		t.Error("response_format sent although NoSchema is set")
	}
	msgs, _ := json.Marshal(f.last["messages"])
	if !strings.Contains(string(msgs), `/no_think`) {
		t.Errorf("user suffix missing from the request: %s", msgs)
	}

	// The default request is unchanged: schema on, no suffix.
	f2 := &fakeServer{reply: func(map[string]any) (int, any) { return 200, chat(`{"answer":"done"}`, nil) }}
	srv2 := f2.start(t)
	if _, err := (&Local{URL: srv2.URL}).Ask(context.Background(), d1Question()); err != nil {
		t.Fatalf("default Ask: %v", err)
	}
	if _, has := f2.last["response_format"]; !has {
		t.Error("default request lost its response_format")
	}
}

func TestConfigFromEnv_SchemaAndSuffix(t *testing.T) {
	env := map[string]string{"SWARMERY_DECIDE_URL": "http://x/v1", "SWARMERY_DECIDE_SCHEMA": "off", "SWARMERY_DECIDE_USER_SUFFIX": " /no_think "}
	cfg, warn := ConfigFromEnv(func(k string) string { return env[k] })
	if !cfg.NoSchema || cfg.UserSuffix != "/no_think" || len(warn) != 0 {
		t.Fatalf("cfg = %+v warn = %v", cfg, warn)
	}
	if s := cfg.String(); !strings.Contains(s, "schema=off") || !strings.Contains(s, `suffix="/no_think"`) {
		t.Errorf("startup line hides the knobs: %s", s)
	}
	if l, ok := New(nil, cfg).Local.(*Local); !ok || !l.NoSchema || l.UserSuffix != "/no_think" {
		t.Errorf("New did not carry the knobs into the local backend: %+v", l)
	}
	env["SWARMERY_DECIDE_SCHEMA"] = "maybe"
	if cfg, warn := ConfigFromEnv(func(k string) string { return env[k] }); cfg.NoSchema || len(warn) != 1 {
		t.Errorf("bad value: cfg.NoSchema=%v warn=%v, want schema kept on with one warning", cfg.NoSchema, warn)
	}
}

// SWARMERY_DECIDE_TIMEOUT reaches the local backend: an LM Studio model the
// server unloaded after idling needs ~12 s to load, so the 5 s default failed
// the first call of every pass after a quiet stretch.
func TestConfigFromEnv_Timeout(t *testing.T) {
	env := map[string]string{"SWARMERY_DECIDE_URL": "http://x/v1", "SWARMERY_DECIDE_TIMEOUT": "30s"}
	cfg, warn := ConfigFromEnv(func(k string) string { return env[k] })
	if cfg.Timeout != 30*time.Second || len(warn) != 0 {
		t.Fatalf("cfg.Timeout = %v warn = %v, want 30s", cfg.Timeout, warn)
	}
	if !strings.Contains(cfg.String(), "timeout=30s") {
		t.Errorf("startup line hides the timeout: %s", cfg.String())
	}
	if l, ok := New(nil, cfg).Local.(*Local); !ok || l.Timeout != 30*time.Second {
		t.Errorf("New did not carry the timeout into the local backend: %+v", l)
	}
	for _, bad := range []string{"soon", "0s", "-1s", "10m"} {
		env["SWARMERY_DECIDE_TIMEOUT"] = bad
		if cfg, warn := ConfigFromEnv(func(k string) string { return env[k] }); cfg.Timeout != 0 || len(warn) != 1 {
			t.Errorf("%q: Timeout=%v warn=%v, want the default kept with one warning", bad, cfg.Timeout, warn)
		}
	}
	// Unset keeps the 5 s default in the backend.
	delete(env, "SWARMERY_DECIDE_TIMEOUT")
	if cfg, _ := ConfigFromEnv(func(k string) string { return env[k] }); cfg.Timeout != 0 {
		t.Errorf("unset: Timeout=%v, want 0 (⇒ LocalTimeout)", cfg.Timeout)
	}
}
