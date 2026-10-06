package replyextract

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// realSpawns counts calls that reached the default runner. Every test injects
// Run, so any count above zero means a test would have spawned a real claude.
var realSpawns atomic.Int64

// TestMain makes reaching the real CLI a hard failure for the whole package:
// no test here may spawn claude.
func TestMain(m *testing.M) {
	spawn = func(context.Context, string) (string, error) {
		realSpawns.Add(1)
		return "", errors.New("test reached the real claude spawn")
	}
	code := m.Run()
	if n := realSpawns.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "guard: %d test call(s) reached the real claude spawn — inject Extractor.Run\n", n)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

var fixedNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

const goodAnswer = `Sure: {"question":"Drop the old table?","options":["Yes, drop it","No, keep it"],"recommended":"Yes, drop it"}`

// fakeRun is an injected model: it counts calls, keeps every prompt and
// answers with out.
type fakeRun struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	out     string
	err     error
}

func (f *fakeRun) run(_ context.Context, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.prompts = append(f.prompts, prompt)
	return f.out, f.err
}

func (f *fakeRun) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "replyextract.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) sql.Result {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, q)
	}
	return res
}

// seedSession inserts an awaiting_reply session with a user turn and one
// main-thread assistant turn carrying text; returns the session and turn ids.
func seedSession(t *testing.T, db *sql.DB, id int64, text string) (int64, int64) {
	t.Helper()
	mustExec(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source, entrypoint)
		VALUES (?, 1, ?, 'awaiting_reply', '2026-10-06T10:00:00Z', 'jsonl', 'cli')`, id, fmt.Sprintf("uuid-%d", id))
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (?, 0, 'user', '2026-10-06T10:00:00Z', 'go')`, id)
	return id, addTurn(t, db, id, 1, text)
}

func addTurn(t *testing.T, db *sql.DB, sessionID int64, seq int, text string) int64 {
	t.Helper()
	res := mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, text, stop_reason)
		VALUES (?, ?, 'assistant', '2026-10-06T10:01:00Z', ?, 'end_turn')`, sessionID, seq, text)
	tid, _ := res.LastInsertId()
	return tid
}

type row struct {
	status, question, options, recommended, model, errText string
}

func rowFor(t *testing.T, db *sql.DB, sessionID, turnID int64) row {
	t.Helper()
	var r row
	var q, o, rec, e sql.NullString
	if err := db.QueryRow(`SELECT status, question, options_json, recommended, model, error
		FROM reply_extracts WHERE session_id = ? AND turn_id = ?`, sessionID, turnID).
		Scan(&r.status, &q, &o, &rec, &r.model, &e); err != nil {
		t.Fatalf("reply_extracts row (%d, %d): %v", sessionID, turnID, err)
	}
	r.question, r.options, r.recommended, r.errText = q.String, o.String, rec.String, e.String
	return r
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reply_extracts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func newExtractor(db *sql.DB, f *fakeRun) *Extractor {
	return &Extractor{DB: db, Run: f.run, Now: func() time.Time { return fixedNow }}
}

// fire is one awaiting_reply transition, waited to completion.
func fire(e *Extractor, sessionID int64) {
	e.OnAwaitingReply(sessionID)
	e.Wait()
}

// (a) first transition → one call, an ok row with the parsed fields;
// (b) second transition on the same turn → no call (cache);
// (c) a new assistant turn → one more call.
func TestExtractCachesPerTurn(t *testing.T) {
	db := testDB(t)
	sid, turn1 := seedSession(t, db, 1, "I migrated the schema.\n\nShould I also drop the old table?")
	f := &fakeRun{out: goodAnswer}
	e := newExtractor(db, f)

	fire(e, sid) // (a)
	if f.count() != 1 {
		t.Fatalf("(a) calls = %d, want 1", f.count())
	}
	got := rowFor(t, db, sid, turn1)
	want := row{status: "ok", question: "Drop the old table?", options: `["Yes, drop it","No, keep it"]`,
		recommended: "Yes, drop it", model: route.ModelHaiku}
	if got != want {
		t.Errorf("(a) row = %+v, want %+v", got, want)
	}

	fire(e, sid) // (b)
	if f.count() != 1 {
		t.Errorf("(b) calls after a repeat transition on the same turn = %d, want 1 (cache hit)", f.count())
	}

	// A later subagent turn and a tool-only turn must not change the key.
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, text, agent_name) VALUES (?, 2, 'assistant', '2026-10-06T10:02:00Z', 'sub?', 'worker')`, sid)
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (?, 3, 'assistant', '2026-10-06T10:02:00Z', NULL)`, sid)
	fire(e, sid)
	if f.count() != 1 {
		t.Errorf("calls after subagent/empty turns = %d, want 1", f.count())
	}

	turn2 := addTurn(t, db, sid, 4, "Done. Deploy now?") // (c)
	fire(e, sid)
	if f.count() != 2 {
		t.Fatalf("(c) calls after a new assistant turn = %d, want 2", f.count())
	}
	if r := rowFor(t, db, sid, turn2); r.status != "ok" {
		t.Errorf("(c) new turn row status = %q, want ok", r.status)
	}
}

// (d) cap reached → no call. Rows from yesterday do not count.
func TestExtractDailyCap(t *testing.T) {
	db := testDB(t)
	f := &fakeRun{out: goodAnswer}
	e := newExtractor(db, f)
	e.DailyCap = 2

	s1, _ := seedSession(t, db, 1, "One?")
	s2, _ := seedSession(t, db, 2, "Two?")
	s3, _ := seedSession(t, db, 3, "Three?")
	// One row from yesterday must not count toward today's cap.
	mustExec(t, db, `INSERT INTO reply_extracts (session_id, turn_id, status, model, created_at)
		VALUES (?, 999, 'ok', 'm', '2026-10-05T23:59:59.999Z')`, s3)

	fire(e, s1)
	fire(e, s2)
	if f.count() != 2 {
		t.Fatalf("calls under the cap = %d, want 2", f.count())
	}
	fire(e, s3) // (d)
	if f.count() != 2 {
		t.Errorf("(d) calls with the cap reached = %d, want 2", f.count())
	}
	if n := countRows(t, db); n != 3 {
		t.Errorf("rows = %d, want 3 (no row written past the cap)", n)
	}

	// A new UTC day resets the budget.
	e.Now = func() time.Time { return fixedNow.Add(24 * time.Hour) }
	fire(e, s3)
	if f.count() != 3 {
		t.Errorf("calls on the next day = %d, want 3", f.count())
	}
}

// (e) malformed model output → an error row, never retried for that turn; a
// runner failure is stored the same way.
func TestExtractMalformedIsStoredAndNotRetried(t *testing.T) {
	db := testDB(t)
	sid, turn := seedSession(t, db, 1, "Which one?")
	f := &fakeRun{out: "I cannot answer that."}
	e := newExtractor(db, f)

	fire(e, sid)
	r := rowFor(t, db, sid, turn)
	if r.status != "error" || r.errText == "" || r.question != "" || r.model != route.ModelHaiku {
		t.Errorf("(e) row = %+v, want an error row with a reason and no question", r)
	}
	f.out = goodAnswer
	fire(e, sid)
	if f.count() != 1 {
		t.Errorf("(e) calls after the error row = %d, want 1 (not retried)", f.count())
	}

	sid2, turn2 := seedSession(t, db, 2, "Which other one?")
	f.err = errors.New("claude -p: exit status 1")
	fire(e, sid2)
	if r := rowFor(t, db, sid2, turn2); r.status != "error" || !strings.Contains(r.errText, "exit status 1") {
		t.Errorf("runner-failure row = %+v, want error carrying the cause", r)
	}
}

// (f) a transition that arrives while an extraction runs is dropped.
func TestExtractConcurrentTransitionIsDropped(t *testing.T) {
	db := testDB(t)
	s1, _ := seedSession(t, db, 1, "First?")
	s2, _ := seedSession(t, db, 2, "Second?")

	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	e := &Extractor{DB: db, Now: func() time.Time { return fixedNow }, Run: func(ctx context.Context, _ string) (string, error) {
		calls.Add(1)
		close(started)
		<-release
		return goodAnswer, nil
	}}

	e.OnAwaitingReply(s1)
	<-started
	e.OnAwaitingReply(s2) // must return at once and drop
	e.OnAwaitingReply(s1)
	close(release)
	e.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("(f) calls = %d, want 1 (concurrent transitions dropped)", n)
	}
	if n := countRows(t, db); n != 1 {
		t.Errorf("(f) rows = %d, want 1", n)
	}
}

// (g) the prompt carries at most MaxInputChars characters of the turn text —
// the TAIL, where the question sits.
func TestExtractPromptCarriesTheTail(t *testing.T) {
	db := testDB(t)
	text := "HEAD-MARKER " + strings.Repeat("é", 9000) + " TAIL-QUESTION?"
	sid, _ := seedSession(t, db, 1, text)
	f := &fakeRun{out: goodAnswer}
	fire(newExtractor(db, f), sid)

	if len(f.prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(f.prompts))
	}
	p := f.prompts[0]
	if !strings.Contains(p, "TAIL-QUESTION?") || strings.Contains(p, "HEAD-MARKER") {
		t.Errorf("(g) prompt does not carry the tail of the text")
	}
	start := strings.Index(p, "<message>\n") + len("<message>\n")
	end := strings.LastIndex(p, "\n</message>")
	if body := []rune(p[start:end]); len(body) > MaxInputChars {
		t.Errorf("(g) prompt carries %d characters of turn text, want ≤ %d", len(body), MaxInputChars)
	}
	if !strings.HasPrefix(p, promptHead) {
		t.Error("prompt does not start with the instruction block")
	}
}

// A session with no assistant prose is skipped without a call.
func TestExtractNoProseNoCall(t *testing.T) {
	db := testDB(t)
	mustExec(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (1, 1, 'u', 'awaiting_reply', '2026-10-06T10:00:00Z', 'jsonl')`)
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (1, 0, 'assistant', '2026-10-06T10:00:00Z', '   ')`)
	f := &fakeRun{out: goodAnswer}
	fire(newExtractor(db, f), 1)
	if f.count() != 0 || countRows(t, db) != 0 {
		t.Errorf("calls=%d rows=%d, want 0/0", f.count(), countRows(t, db))
	}
}

func TestParseValidates(t *testing.T) {
	long := strings.Repeat("x", 301)
	for _, tc := range []struct {
		name, out string
		ok        bool
	}{
		{"open-ended", `{"question":"What next?","options":[],"recommended":""}`, true},
		{"options omitted", `{"question":"What next?"}`, true},
		{"three options", `{"question":"Q?","options":["a","b","c"],"recommended":"c"}`, true},
		{"empty question", `{"question":" ","options":[],"recommended":""}`, false},
		{"question too long", `{"question":"` + long + `","options":[],"recommended":""}`, false},
		{"four options", `{"question":"Q?","options":["a","b","c","d"],"recommended":""}`, false},
		{"option too long", `{"question":"Q?","options":["` + strings.Repeat("y", 121) + `"],"recommended":""}`, false},
		{"empty option", `{"question":"Q?","options":["a",""],"recommended":""}`, false},
		{"recommended not an option", `{"question":"Q?","options":["a","b"],"recommended":"z"}`, false},
		{"no json", `nope`, false},
		{"broken json", `{"question": }`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parse(tc.out)
			if (err == nil) != tc.ok {
				t.Fatalf("parse(%q) err = %v, want ok=%v", tc.out, err, tc.ok)
			}
			if tc.ok && s.Options == nil {
				t.Error("options must be [] not null")
			}
		})
	}
}

// Default off: New builds nothing unless the flag is on and the cap positive.
func TestNewIsOffByDefault(t *testing.T) {
	db := testDB(t)
	if e := New(false, db, DefaultDailyCap); e != nil {
		t.Errorf("New(false, …) = %+v, want nil — nothing is built when --reply-extract is off", e)
	}
	if e := New(true, db, 0); e != nil {
		t.Errorf("New(true, cap 0) = %+v, want nil", e)
	}
	e := New(true, db, 7)
	if e == nil || e.DailyCap != 7 || e.Run != nil {
		t.Fatalf("New(true, cap 7) = %+v, want an extractor with cap 7 and the default runner", e)
	}
	if e.dailyCap() != 7 || (&Extractor{}).dailyCap() != DefaultDailyCap || (&Extractor{}).timeout() != DefaultTimeout {
		t.Error("defaults not applied")
	}
}
