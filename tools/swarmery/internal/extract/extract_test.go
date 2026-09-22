package extract

// Phase 6 — on-demand LLM task extraction.
//
// Every test here runs against a fake Runner. No test in this package may spawn
// a real `claude`: the whole point of the Runner seam is that the expensive,
// non-deterministic half of the feature is replaceable, and a suite that paid
// for a model run would be neither fast nor repeatable.
//
// The properties under test are the ones that decide whether this feature is
// safe to hand an operator a button for:
//
//	idempotency — pressing twice must not double the board
//	honesty     — a model that answers in prose must FAIL, not read as "0 tasks"
//	scope       — a session capture refuses must be refused here identically
//
// The third is the one worth stating: this package deliberately owns no skip
// rules of its own. It asks internal/ingest.CaptureSkipReason, and
// TestDispatchedSessionIsRefused exists to catch the day someone "simplifies"
// that into a local copy.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const (
	extractSessionUUID = "ffeeddcc-1111-4000-8000-000000000001"
	extractProjectPath = "/Users/user/work/extract-app"
)

// fakeRunner returns a canned answer (or error) and records the prompts it was
// asked, so tests can assert on the digest that reached the model.
type fakeRunner struct {
	out  string
	err  error
	mu   sync.Mutex
	seen []string
	// block, when non-nil, holds Run until it is closed — the seam the
	// single-flight test needs to have two calls genuinely overlap.
	block chan struct{}
}

func (r *fakeRunner) Run(_ context.Context, prompt string) (string, error) {
	r.mu.Lock()
	r.seen = append(r.seen, prompt)
	r.mu.Unlock()
	if r.block != nil {
		<-r.block
	}
	return r.out, r.err
}

func (r *fakeRunner) prompts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "extract.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedSession creates a project + session with one opening prompt and one
// assistant turn — the minimum a digest needs to be non-empty.
func seedSession(t *testing.T, db *sql.DB, uuid, projectPath string) (projectID, sessionID int64) {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen) VALUES (?, ?, ?, ?)`,
		projectPath, strings.ReplaceAll(projectPath, "/", "-"), filepath.Base(projectPath),
		"2026-07-10T09:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	projectID, err = res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	res, err = db.Exec(`
		INSERT INTO sessions (project_id, session_uuid, status, started_at, cwd, source)
		VALUES (?, ?, 'active', '2026-07-10T09:00:00.000Z', ?, 'jsonl')`,
		projectID, uuid, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err = res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO turns (session_id, seq, role, started_at, text)
		VALUES (?, 1, 'assistant', '2026-07-10T09:00:01.000Z', ?)`,
		sessionID, "I refactored the retry helper but left the backoff jitter TODO."); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO events (session_id, ts, type, payload, dedup_key)
		VALUES (?, '2026-07-10T09:00:00.500Z', 'user_prompt', ?, ?)`,
		sessionID, `{"content":"Clean up the retry helper in internal/retry"}`,
		"evt-"+uuid); err != nil {
		t.Fatal(err)
	}
	return projectID, sessionID
}

// cards lists (title, board_column, origin) of every card a session produced,
// in insert order.
func cards(t *testing.T, db *sql.DB, sessionID int64) [][3]string {
	t.Helper()
	rows, err := db.Query(
		`SELECT title, board_column, origin FROM tasks WHERE origin_session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var title, col, origin string
		if err := rows.Scan(&title, &col, &origin); err != nil {
			t.Fatal(err)
		}
		out = append(out, [3]string{title, col, origin})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func answer(tasks ...string) string {
	var b strings.Builder
	b.WriteString("Here is what I found:\n\n```json\n[")
	for i, t := range tasks {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"title":%q,"prompt":%q}`, t, "do "+t)
	}
	b.WriteString("]\n```\n")
	return b.String()
}

// ── happy path + idempotency ─────────────────────────────────────────────────

// TestExtractInsertsSuggestedCards is the feature in one assertion: a valid
// answer becomes N Triage cards carrying origin='llm', each announced exactly
// once.
func TestExtractInsertsSuggestedCards(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	var notified []int64
	svc := &Service{
		DB:     db,
		Run:    &fakeRunner{out: answer("Add backoff jitter", "Document the retry budget")},
		Notify: func(id int64) { notified = append(notified, id) },
	}
	n, err := svc.ExtractTasks(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ExtractTasks: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted = %d, want 2", n)
	}
	got := cards(t, db, sessionID)
	if len(got) != 2 {
		t.Fatalf("cards = %v, want 2", got)
	}
	for _, c := range got {
		if c[1] != "triage" {
			t.Errorf("card %q landed in %q, want triage — an extraction is a SUGGESTION", c[0], c[1])
		}
		if c[2] != "llm" {
			t.Errorf("card %q has origin %q, want llm", c[0], c[2])
		}
	}
	if got[0][0] != "Add backoff jitter" {
		t.Errorf("first title = %q", got[0][0])
	}
	if len(notified) != 2 {
		t.Errorf("notified %d times, want 2 (one task_updated per REAL insert)", len(notified))
	}
}

// TestExtractIsIdempotent is why the button is safe to press twice: the same
// session and the same answer re-derive the same capture_keys, so the second
// run reports an honest 0 and announces nothing.
func TestExtractIsIdempotent(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	var notified int
	svc := &Service{
		DB:     db,
		Run:    &fakeRunner{out: answer("Add backoff jitter", "Document the retry budget")},
		Notify: func(int64) { notified++ },
	}
	if n, err := svc.ExtractTasks(context.Background(), sessionID); err != nil || n != 2 {
		t.Fatalf("first run: n=%d err=%v, want 2, nil", n, err)
	}
	n, err := svc.ExtractTasks(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n != 0 {
		t.Errorf("second run inserted %d, want 0 — a replay is not board activity", n)
	}
	if got := cards(t, db, sessionID); len(got) != 2 {
		t.Errorf("cards after two runs = %d, want 2 (no duplicates): %v", len(got), got)
	}
	if notified != 2 {
		t.Errorf("notified %d times across two runs, want 2", notified)
	}
}

// TestExtractDedupesTitlesDifferingOnlyInCaseAndSpacing pins the normalization
// the capture_key rests on: a model that re-words a title only in case or
// whitespace on a re-run must not deposit a second copy of the card.
func TestExtractDedupesTitlesDifferingOnlyInCaseAndSpacing(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	svc := &Service{DB: db, Run: &fakeRunner{out: answer("Add backoff jitter")}}
	if n, err := svc.ExtractTasks(context.Background(), sessionID); err != nil || n != 1 {
		t.Fatalf("first run: n=%d err=%v", n, err)
	}
	svc.Run = &fakeRunner{out: answer("ADD   backoff\tJITTER")}
	n, err := svc.ExtractTasks(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n != 0 {
		t.Errorf("re-worded title inserted %d card(s), want 0", n)
	}
}

// TestExtractCapsAtTenTasks: the prompt asks for ≤10 and the parse ENFORCES it,
// so a model that ignores the instruction cannot bury the Triage column.
func TestExtractCapsAtTenTasks(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	titles := make([]string, 0, 15)
	for i := 0; i < 15; i++ {
		titles = append(titles, fmt.Sprintf("Task number %d", i))
	}
	svc := &Service{DB: db, Run: &fakeRunner{out: answer(titles...)}}
	n, err := svc.ExtractTasks(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ExtractTasks: %v", err)
	}
	if n != maxTasks {
		t.Errorf("inserted = %d, want the %d cap", n, maxTasks)
	}
}

// TestExtractAcceptsEmptyArray: "this session left nothing behind" is a valid
// answer, and must not look like a failure.
func TestExtractAcceptsEmptyArray(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	svc := &Service{DB: db, Run: &fakeRunner{out: "```json\n[]\n```"}}
	n, err := svc.ExtractTasks(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("empty array must not be an error: %v", err)
	}
	if n != 0 {
		t.Errorf("inserted = %d, want 0", n)
	}
	if got := cards(t, db, sessionID); len(got) != 0 {
		t.Errorf("cards = %v, want none", got)
	}
}

// ── honesty: bad output fails loudly ─────────────────────────────────────────

// TestGarbageOutputInsertsNothingAndErrors is the property the 502 rests on. A
// model that answered in prose must NOT be read as "no tasks found": the two
// outcomes look identical from a count alone and call for opposite responses.
func TestGarbageOutputInsertsNothingAndErrors(t *testing.T) {
	for _, tc := range []struct{ name, out string }{
		{"prose", "I looked at the session and honestly there is nothing much to do here."},
		{"object not array", "```json\n{\"title\":\"x\",\"prompt\":\"y\"}\n```"},
		{"truncated fence", "```json\n[{\"title\":\"x\",\"prompt\":"},
		{"empty answer", "   \n  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

			notified := 0
			svc := &Service{DB: db, Run: &fakeRunner{out: tc.out}, Notify: func(int64) { notified++ }}
			n, err := svc.ExtractTasks(context.Background(), sessionID)
			var bad *ErrBadOutput
			if !errors.As(err, &bad) {
				t.Fatalf("err = %v, want *ErrBadOutput", err)
			}
			if n != 0 {
				t.Errorf("inserted = %d, want 0", n)
			}
			if got := cards(t, db, sessionID); len(got) != 0 {
				t.Errorf("cards = %v, want none", got)
			}
			if notified != 0 {
				t.Errorf("announced %d frames for a failed run, want 0", notified)
			}
		})
	}
}

// TestMalformedItemsAreDroppedNotFatal: one bad item must not cost the four
// good ones around it.
func TestMalformedItemsAreDroppedNotFatal(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	out := "```json\n[" +
		`{"title":"Good one","prompt":"do it"},` +
		`{"title":"","prompt":"no title"},` +
		`{"title":"No prompt","prompt":"  "},` +
		`{"title":"Good two","prompt":"do it too"}` +
		"]\n```"
	svc := &Service{DB: db, Run: &fakeRunner{out: out}}
	n, err := svc.ExtractTasks(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ExtractTasks: %v", err)
	}
	if n != 2 {
		t.Errorf("inserted = %d, want 2 (the two well-formed items)", n)
	}
}

// TestRunnerErrorSurfaces: a timeout or a missing binary reaches the caller
// verbatim, so the endpoint can put it in the 502 detail.
func TestRunnerErrorSurfaces(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	boom := errors.New("claude -p timed out after 5m0s")
	svc := &Service{DB: db, Run: &fakeRunner{err: boom}}
	n, err := svc.ExtractTasks(context.Background(), sessionID)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the runner's error", err)
	}
	if n != 0 {
		t.Errorf("inserted = %d, want 0", n)
	}
}

// ── scope: the skip rules are ingest's, not ours ─────────────────────────────

// TestDispatchedSessionIsRefused: a session that IS a dispatched run of a board
// task already has a card, and extracting from it would mint children of the
// card that spawned it. The verdict must come from
// internal/ingest.CaptureSkipReason — this test fails the day someone
// re-implements the rule locally and gets it subtly wrong.
func TestDispatchedSessionIsRefused(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)
	// The dispatcher parks the uuid on the task row before it spawns.
	if _, err := db.Exec(`
		INSERT INTO tasks (project_id, title, prompt, priority, status, created_at,
		                   source, external_id, board_column, dispatch_session_uuid)
		VALUES (1, 'parent', 'p', 5, 'running', '2026-07-10T09:00:00.000Z',
		        'queue', 'T-parent', 'in_progress', ?)`, extractSessionUUID); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{out: answer("should never run")}
	svc := &Service{DB: db, Run: run}

	if _, err := svc.ExtractTasks(context.Background(), sessionID); err == nil {
		t.Fatal("dispatched session extracted; want a skip error")
	} else {
		var skipped *ErrSkipped
		if !errors.As(err, &skipped) {
			t.Fatalf("err = %v, want *ErrSkipped", err)
		}
	}
	if got := run.prompts(); len(got) != 0 {
		t.Errorf("the model was run %d time(s) for an ineligible session — the skip must precede the spend", len(got))
	}
	if reason, ok, err := svc.Eligible(sessionID); err != nil || ok || reason == "" {
		t.Errorf("Eligible = (%q, %v, %v), want a reason and not-ok", reason, ok, err)
	}
}

// TestUnknownSessionIsNotFound backs the endpoint's 404.
func TestUnknownSessionIsNotFound(t *testing.T) {
	db := testDB(t)
	svc := &Service{DB: db, Run: &fakeRunner{out: answer("x")}}
	if _, err := svc.ExtractTasks(context.Background(), 4242); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
	if _, _, err := svc.Eligible(4242); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Eligible err = %v, want ErrSessionNotFound", err)
	}
}

// ── single-flight ────────────────────────────────────────────────────────────

// TestSingleFlightPerSession: a second click while the first run is in flight
// bounces instead of paying for a duplicate pass. The fake runner BLOCKS so the
// two calls genuinely overlap — a test that merely called twice in sequence
// would pass against no guard at all.
func TestSingleFlightPerSession(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	gate := make(chan struct{})
	run := &fakeRunner{out: answer("Add backoff jitter"), block: gate}
	svc := &Service{DB: db, Run: run}

	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(entered)
		_, err := svc.ExtractTasks(context.Background(), sessionID)
		done <- err
	}()
	<-entered
	// Wait until the first call is really inside Run (it has recorded a prompt),
	// so "in flight" is observed rather than assumed.
	for len(run.prompts()) == 0 {
	}

	if _, err := svc.ExtractTasks(context.Background(), sessionID); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("concurrent call err = %v, want ErrAlreadyRunning", err)
	}
	if !svc.Running(sessionID) {
		t.Error("Running = false while a run is in flight")
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("first run: %v", err)
	}
	if svc.Running(sessionID) {
		t.Error("Running = true after the run finished — the slot leaked")
	}
	// And the slot is reusable: the guard is per-run, not a one-shot latch.
	if _, err := svc.ExtractTasks(context.Background(), sessionID); err != nil {
		t.Fatalf("run after release: %v", err)
	}
}

// TestSingleFlightIsPerSession: two DIFFERENT sessions must not block each
// other — the guard is keyed, not global.
func TestSingleFlightIsPerSession(t *testing.T) {
	db := testDB(t)
	_, sessionA := seedSession(t, db, extractSessionUUID, extractProjectPath)
	_, sessionB := seedSession(t, db, "ffeeddcc-2222-4000-8000-000000000002", extractProjectPath+"-two")

	gate := make(chan struct{})
	run := &fakeRunner{out: answer("Shared answer"), block: gate}
	svc := &Service{DB: db, Run: run}

	done := make(chan error, 1)
	go func() {
		_, err := svc.ExtractTasks(context.Background(), sessionA)
		done <- err
	}()
	for len(run.prompts()) == 0 {
	}
	// B must not see A's slot. Its own Run call blocks on the same gate, so just
	// assert it got PAST the guard (it reaches the runner → a second prompt).
	goB := make(chan error, 1)
	go func() {
		_, err := svc.ExtractTasks(context.Background(), sessionB)
		goB <- err
	}()
	for len(run.prompts()) < 2 {
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("session A: %v", err)
	}
	if err := <-goB; err != nil {
		t.Fatalf("session B: %v", err)
	}
}

// ── digest ───────────────────────────────────────────────────────────────────

// TestDigestCarriesIntentAndWork: the model must see BOTH what the session was
// asked to do and what it said while doing it, or the tasks it names cannot
// stay inside the session's intent.
func TestDigestCarriesIntentAndWork(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	run := &fakeRunner{out: "```json\n[]\n```"}
	svc := &Service{DB: db, Run: run}
	if _, err := svc.ExtractTasks(context.Background(), sessionID); err != nil {
		t.Fatal(err)
	}
	got := run.prompts()
	if len(got) != 1 {
		t.Fatalf("prompts = %d, want 1", len(got))
	}
	for _, want := range []string{
		"Clean up the retry helper in internal/retry", // the opening prompt (intent)
		"left the backoff jitter TODO",                // the assistant turn (work)
		"fenced JSON array",                           // the contract
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
	if len(got[0]) > digestLimit+4096 {
		t.Errorf("prompt is %d bytes, well past the %d digest budget", len(got[0]), digestLimit)
	}
}

// TestDigestIsBounded: a marathon session must not blow the budget this feature
// exists to respect (the extraction has to be cheaper than the session).
func TestDigestIsBounded(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)
	huge := strings.Repeat("some very long assistant explanation. ", 4000)
	for seq := 2; seq < 60; seq++ {
		if _, err := db.Exec(`
			INSERT INTO turns (session_id, seq, role, started_at, text)
			VALUES (?, ?, 'assistant', '2026-07-10T09:00:02.000Z', ?)`,
			sessionID, seq, huge); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Digest(db, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(d)) > digestLimit {
		t.Errorf("digest is %d runes, want ≤ %d", len([]rune(d)), digestLimit)
	}
}

// ── phase-4 lifecycle sync applies to extracted cards ────────────────────────

// TestAcceptedExtractedCardMovesToReviewOnSessionEnd is the claim the design
// makes and never states in code: extracted cards get lifecycle sync FOR FREE.
//
// Nothing in internal/ingest knows this package exists. The sweep keys on
// `origin != 'manual'` + origin_session_id, and taskcap sets both here — so the
// only thing standing between an accepted llm card and the auto-move is that
// this package really does write those two columns. That is what is asserted:
// the card is extracted, accepted the way a user accepts one (a drag to
// in_progress), and then the session ends.
func TestAcceptedExtractedCardMovesToReviewOnSessionEnd(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	svc := &Service{DB: db, Run: &fakeRunner{out: answer("Add backoff jitter")}}
	if n, err := svc.ExtractTasks(context.Background(), sessionID); err != nil || n != 1 {
		t.Fatalf("ExtractTasks: n=%d err=%v", n, err)
	}
	// The user accepts the suggestion: triage → in_progress. No worktree — this
	// card was never dispatched, which is exactly the case the sweep owns.
	if _, err := db.Exec(`
		UPDATE tasks SET board_column = 'in_progress'
		 WHERE capture_key = ?`, CaptureKey(extractSessionUUID, "Add backoff jitter")); err != nil {
		t.Fatal(err)
	}

	moved := ingest.SweepSessionToReview(db, sessionID)
	if len(moved) != 1 {
		t.Fatalf("sweep moved %d cards, want 1 — an extracted card must be in scope for phase-4 sync", len(moved))
	}
	got := cards(t, db, sessionID)
	if len(got) != 1 || got[0][1] != "in_review" {
		t.Fatalf("card = %v, want in_review", got)
	}
	// And the sweep is idempotent over it: a second terminal transition (the
	// status ticker, then an operator kill) must not re-announce.
	if again := ingest.SweepSessionToReview(db, sessionID); len(again) != 0 {
		t.Errorf("second sweep moved %d cards, want 0", len(again))
	}
}

// TestUnacceptedExtractedCardIsNotSwept: a suggestion the user never acted on
// does not expire into review when the session ends. Suggestions are inert
// until accepted — the same rule captured cards live by.
func TestUnacceptedExtractedCardIsNotSwept(t *testing.T) {
	db := testDB(t)
	_, sessionID := seedSession(t, db, extractSessionUUID, extractProjectPath)

	svc := &Service{DB: db, Run: &fakeRunner{out: answer("Add backoff jitter")}}
	if _, err := svc.ExtractTasks(context.Background(), sessionID); err != nil {
		t.Fatal(err)
	}
	if moved := ingest.SweepSessionToReview(db, sessionID); len(moved) != 0 {
		t.Errorf("sweep moved %d untouched triage cards, want 0", len(moved))
	}
	if got := cards(t, db, sessionID); got[0][1] != "triage" {
		t.Errorf("card is in %q, want triage", got[0][1])
	}
}

// ── the model pin ────────────────────────────────────────────────────────────

// TestModelPin: headless runs that carry no override MUST name a full model id.
// Without --model the CLI inherits the account default, which is the expensive
// one; an alias would silently re-resolve over time.
func TestModelPin(t *testing.T) {
	if defaultModel != "claude-opus-5-5" {
		t.Errorf("defaultModel = %q, want claude-opus-5-5", defaultModel)
	}
}

// TestEffortPin: without --effort the CLI inherits its xhigh default —
// overkill for a mechanical classification pass (Opus 5.5 holds quality at
// medium at a fraction of the tokens).
func TestEffortPin(t *testing.T) {
	if defaultEffort != "medium" {
		t.Errorf("defaultEffort = %q, want medium", defaultEffort)
	}
}

// TestCaptureKeyShape pins the key format the phase specifies, since it is the
// permanent idempotency identity of every extracted card and re-keying would
// duplicate the whole board.
func TestCaptureKeyShape(t *testing.T) {
	k := CaptureKey(extractSessionUUID, "Add backoff jitter")
	if !strings.HasPrefix(k, "llm:"+extractSessionUUID+":") {
		t.Fatalf("key = %q, want llm:<uuid>:<hash>", k)
	}
	hash := strings.TrimPrefix(k, "llm:"+extractSessionUUID+":")
	if len(hash) != captureKeyHashLen {
		t.Errorf("hash is %d chars, want %d", len(hash), captureKeyHashLen)
	}
	// Same normalized title → same key, across separate calls.
	if k != CaptureKey(extractSessionUUID, "  add   BACKOFF jitter ") {
		t.Error("normalization is not stable across case/spacing")
	}
	// Different session → different key: the keyspace is scoped per session.
	if k == CaptureKey("ffeeddcc-9999-4000-8000-000000000009", "Add backoff jitter") {
		t.Error("the key is not session-scoped")
	}
}
