package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// fakeTriageDaemon serves POST /api/triage/runs and GET /api/triage/runs/{id}.
// The GET answers walk through states (the last one repeats).
type fakeTriageDaemon struct {
	mu         sync.Mutex
	postStatus int
	postBody   string
	states     []map[string]any
	gets       int
	gotBody    map[string]any
	gotRawBody string
	gotOrigin  []string
	onGet      func() // called on each GET of run 7, under the lock
}

func (f *fakeTriageDaemon) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.gotOrigin = append(f.gotOrigin, r.Header.Values("Origin")...)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/triage/runs":
			raw, _ := io.ReadAll(r.Body)
			f.gotRawBody = string(raw)
			f.gotBody = map[string]any{}
			if err := json.Unmarshal(raw, &f.gotBody); err != nil {
				t.Errorf("request body is not JSON: %q", raw)
			}
			w.WriteHeader(f.postStatus)
			io.WriteString(w, f.postBody)
		case r.Method == http.MethodGet && r.URL.Path == "/api/triage/runs/7":
			i := min(f.gets, len(f.states)-1)
			f.gets++
			if f.onGet != nil {
				f.onGet()
			}
			json.NewEncoder(w).Encode(f.states[i])
		default:
			http.NotFound(w, r)
		}
	})
}

// getCount reads how many times run 7 was polled, under the lock.
func (f *fakeTriageDaemon) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// received reads what the POST carried, under the lock.
func (f *fakeTriageDaemon) received() (body map[string]any, raw string, origin []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotBody, f.gotRawBody, slices.Clone(f.gotOrigin)
}

func runState(status string, extra map[string]any) map[string]any {
	m := map[string]any{"id": 7, "trigger": "schedule", "status": status, "applied": 41, "suggested": 0,
		"skipped": 6, "failed": 2, "rejected": 0, "costUsd": 0.8312, "error": "", "startedAt": "2026-10-06T03:30:00Z"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func startTriageDaemon(t *testing.T, f *fakeTriageDaemon) string {
	t.Helper()
	old := triagePollInterval
	triagePollInterval = 5 * time.Millisecond
	t.Cleanup(func() { triagePollInterval = old })
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return srv.URL
}

func runTriageRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runTriageRunCtx(t, context.Background(), args...)
}

func runTriageRunCtx(t *testing.T, ctx context.Context, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = triageRun(ctx, args, &out, &errOut, &http.Client{Timeout: 5 * time.Second})
	return code, out.String(), errOut.String()
}

// F3: a 404 on the poll means the run is gone; waiting on it is pointless.
func TestTriageRun_PollNotFoundEndsTheWait(t *testing.T) {
	// The fake serves run 7 only, so polling run 8 answers 404.
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":8}`}
	base := startTriageDaemon(t, f)
	// The context is a safety net so a regression fails fast instead of waiting 50 minutes.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	began := time.Now()
	code, out, errOut := runTriageRunCtx(t, ctx, "--url", base, "--wait")
	if code != 1 || !strings.Contains(errOut, "triage run 8 no longer exists") {
		t.Fatalf("exit %d stdout %q stderr %q; want 1 and 'triage run 8 no longer exists'", code, out, errOut)
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("took %s; a 404 must end the wait at once", took)
	}
}

// F4: a 409 without a parsable run id still exits 0, without inventing run 0.
func TestTriageRun_AlreadyActiveWithoutIDExitsZero(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusConflict, postBody: `busy`}
	base := startTriageDaemon(t, f)
	code, out, errOut := runTriageRun(t, "--url", base)
	if code != 0 || out != "a triage run is already active\n" {
		t.Errorf("exit %d stdout %q stderr %q; want 0 and 'a triage run is already active'", code, out, errOut)
	}
}

// F5: a cancelled context is a cancellation, not an unreachable daemon.
func TestTriageRun_CancelledBeforeStartExitsOne(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":7}`}
	base := startTriageDaemon(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, out, errOut := runTriageRunCtx(t, ctx, "--url", base, "--wait")
	if code != 1 || !strings.Contains(errOut, "cancelled") || strings.Contains(errOut, "unreachable") {
		t.Errorf("exit %d stdout %q stderr %q; want 1 and 'cancelled', not 'unreachable'", code, out, errOut)
	}
}

func TestTriageRun_CancelledDuringWaitExitsOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":7}`,
		states: []map[string]any{runState("running", nil)}, onGet: cancel}
	base := startTriageDaemon(t, f)
	code, out, errOut := runTriageRunCtx(t, ctx, "--url", base, "--wait")
	if code != 1 || !strings.Contains(errOut, "cancelled") {
		t.Errorf("exit %d stdout %q stderr %q; want 1 and 'cancelled' on stderr", code, out, errOut)
	}
}

func TestTriageRun_WaitOKPrintsSummary(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":7}`,
		states: []map[string]any{runState("running", nil), runState("running", nil), runState("ok", nil)}}
	base := startTriageDaemon(t, f)
	code, out, errOut := runTriageRun(t, "--url", base, "--wait")
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout %q stderr %q", code, out, errOut)
	}
	want := "triage run 7 started\ntriage run 7: ok · applied 41 · suggested 0 · skipped 6 · failed 2 · $0.83\n"
	if out != want {
		t.Errorf("stdout\n%q\nwant\n%q", out, want)
	}
	if gets := f.getCount(); gets < 3 {
		t.Errorf("polled %d times, want it to follow the run through running", gets)
	}
}

func TestTriageRun_NoWaitReturnsAfterStart(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":7}`, states: []map[string]any{runState("running", nil)}}
	base := startTriageDaemon(t, f)
	code, out, _ := runTriageRun(t, "--url", base)
	if gets := f.getCount(); code != 0 || out != "triage run 7 started\n" || gets != 0 {
		t.Errorf("exit %d stdout %q gets %d; want 0, the started line, no poll", code, out, gets)
	}
}

func TestTriageRun_AlreadyActiveExitsZero(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusConflict, postBody: `{"error":"triage: a run is already active","activeRunId":5}`}
	base := startTriageDaemon(t, f)
	code, out, errOut := runTriageRun(t, "--url", base, "--wait")
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr %q", code, errOut)
	}
	if out != "triage run 5 is already active\n" {
		t.Errorf("stdout %q", out)
	}
}

func TestTriageRun_FailedRunExitsOneWithError(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":7}`,
		states: []map[string]any{runState("failed", map[string]any{"error": "judge: claude exited 1"})}}
	base := startTriageDaemon(t, f)
	code, out, _ := runTriageRun(t, "--url", base, "--wait")
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout %q", code, out)
	}
	want := "triage run 7 started\ntriage run 7: failed · applied 41 · suggested 0 · skipped 6 · failed 2 · $0.83\nerror: judge: claude exited 1\n"
	if out != want {
		t.Errorf("stdout\n%q\nwant\n%q", out, want)
	}
}

func TestTriageRun_WaitTimeoutExitsOne(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":7}`, states: []map[string]any{runState("running", nil)}}
	base := startTriageDaemon(t, f)
	code, out, _ := runTriageRun(t, "--url", base, "--wait", "--wait-timeout", "30ms")
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout %q", code, out)
	}
	if !strings.Contains(out, "the run keeps going") {
		t.Errorf("stdout %q does not say the run keeps going", out)
	}
}

func TestTriageRun_UnreachableDaemonExitsTwo(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	code, _, errOut := runTriageRun(t, "--url", base)
	if code != 2 || !strings.Contains(errOut, "unreachable") {
		t.Errorf("exit %d stderr %q; want 2 and 'unreachable'", code, errOut)
	}
}

func TestTriageRun_FlagsReachTheBody(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":7}`}
	base := startTriageDaemon(t, f)
	code, _, errOut := runTriageRun(t, "--url", base, "--kinds", "classifier, advisor", "--cap", "60",
		"--project", "my-proj", "--trigger", "schedule")
	if code != 0 {
		t.Fatalf("exit %d; stderr %q", code, errOut)
	}
	body, _, origin := f.received()
	if got := body["kinds"]; !jsonEqual(got, []any{"classifier", "advisor"}) {
		t.Errorf("kinds = %v", got)
	}
	if body["cap"] != float64(60) || body["project"] != "my-proj" || body["trigger"] != "schedule" {
		t.Errorf("body = %v", body)
	}
	if len(origin) != 0 {
		t.Errorf("request carried Origin %v; want none", origin)
	}
}

func TestTriageRun_UnsetFlagsAreAbsent(t *testing.T) {
	f := &fakeTriageDaemon{postStatus: http.StatusAccepted, postBody: `{"id":7}`}
	base := startTriageDaemon(t, f)
	if code, _, errOut := runTriageRun(t, "--url", base); code != 0 {
		t.Fatalf("exit %d; stderr %q", code, errOut)
	}
	body, raw, origin := f.received()
	if len(body) != 0 {
		t.Errorf("body %s; want {} with no flag set", raw)
	}
	if len(origin) != 0 {
		t.Errorf("request carried Origin %v; want none", origin)
	}
}

func TestTriageRun_BadFlagsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"--trigger", "cron"},
		{"--cap", "-1"},
		{"--cap", "lots"},
		{"--wait-timeout", "soon"},
		{"--nope"},
		{"extra"},
	} {
		code, _, _ := runTriageRun(t, append([]string{"--url", "http://127.0.0.1:1"}, args...)...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// ---- triage check ----

// triageCheckDB seeds a migrated store: run 1 is a consistent fleet-wide
// operator run over session s-1 (decisions 1,2 — decision 1 sampled, 2 applied),
// with one session-less queued decision (3). extra statements adjust it.
func triageCheckDB(t *testing.T, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "triage.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	stmts := []string{
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/repo', 'p', '2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions (id, project_id, session_uuid, title, started_at) VALUES
		 (1, 1, 's-1', 'one', '2026-10-01T10:00:00.000Z'),
		 (2, 1, 's-2', 'two', '2026-10-01T11:00:00.000Z')`,
		`INSERT INTO turns (session_id, seq, role, started_at, text) VALUES
		 (1, 1, 'assistant', '2026-10-01T10:30:00.000Z', 'x'),
		 (2, 1, 'assistant', '2026-10-01T11:30:00.000Z', 'y')`,
		`INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, confidence, backend, created_at) VALUES
		 (1, 'd2.outcome',   's-1', 's-1', 'h', 'shipped', 0.9, 'local', '2026-10-02T09:00:00Z'),
		 (3, 'd2.task_type', 'run-9', '',  'h', 'feature', 0.9, 'local', '2026-10-02T09:00:00Z')`,
		`INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, confidence, backend, ground_truth, ground_truth_at, created_at) VALUES
		 (2, 'd2.task_type', 's-1', 's-1', 'h', 'bugfix', 0.9, 'local', 'bugfix', '2026-10-05T03:31:00Z', '2026-10-02T09:00:00Z')`,
		`INSERT INTO triage_runs (id, trigger, status, applied, suggested, skipped, failed, rejected, started_at, finished_at)
		 VALUES (1, 'operator', 'ok', 1, 1, 0, 0, 0, '2026-10-05T03:30:00Z', '2026-10-05T03:40:00Z')`,
		`INSERT INTO triage_verdicts (run_id, kind, ref, item_key, value, state, created_at) VALUES
		 (1, 'classifier', '1', 's-1', 'shipped', 'sample',  '2026-10-05T03:31:00Z'),
		 (1, 'classifier', '2', 's-1', 'bugfix',  'applied', '2026-10-05T03:31:00Z')`,
	}
	for _, q := range append(stmts, extra...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	return path
}

func runTriageCheck(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = triageCheck(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestTriageCheck_ConsistentRunExitsZeroWithoutWriting(t *testing.T) {
	path := triageCheckDB(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runTriageCheck(t, "--db", path)
	if code != 0 {
		t.Fatalf("exit %d; stdout\n%s\nstderr %s", code, out, errOut)
	}
	if strings.Contains(out, "MISMATCH") || !strings.Contains(out, "triage run 1") {
		t.Errorf("stdout\n%s", out)
	}
	// Decision 3 has no session: counted, not a failure.
	if !strings.Contains(out, "queue decisions with no session (out of the classifier's reach): 1") {
		t.Errorf("session-less decision not counted:\n%s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("triage check changed the database file")
	}

	// The byte comparison is a real detector: a write through the same open
	// path does change the file.
	db, err := store.OpenNoMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE triage_runs SET applied = applied + 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if written, _ := os.ReadFile(path); bytes.Equal(before, written) {
		t.Error("a real write left the file bytes unchanged; the no-write check proves nothing")
	}
}

func TestTriageCheck_CounterMismatchNamesTheCounter(t *testing.T) {
	path := triageCheckDB(t, `UPDATE triage_runs SET skipped = 3 WHERE id = 1`)
	code, out, _ := runTriageCheck(t, "--db", path, "--run", "1")
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout\n%s", code, out)
	}
	if !strings.Contains(out, "MISMATCH skipped: run says 3, rows 0") {
		t.Errorf("stdout does not name the skipped counter:\n%s", out)
	}
}

func TestTriageCheck_UndoneCountsAsApplied(t *testing.T) {
	path := triageCheckDB(t, `UPDATE triage_verdicts SET state = 'undone' WHERE ref = '2'`)
	if code, out, _ := runTriageCheck(t, "--db", path); code != 0 {
		t.Errorf("exit %d, want 0 — undone verdicts count as applied:\n%s", code, out)
	}
}

func TestTriageCheck_FailedStatusIsAMismatch(t *testing.T) {
	path := triageCheckDB(t, `UPDATE triage_runs SET status = 'failed' WHERE id = 1`)
	code, out, _ := runTriageCheck(t, "--db", path)
	if code != 1 || !strings.Contains(out, "MISMATCH status: failed") {
		t.Errorf("exit %d stdout\n%s", code, out)
	}
}

// queuedS2 queues decision 4 about the ingested session s-2, created before run 1 started.
const queuedS2 = `INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, confidence, backend, created_at)
	 VALUES (4, 'd2.outcome', 's-2', 's-2', 'h', 'shipped', 0.9, 'local', '2026-10-02T09:00:00Z')`

// bucketLine is the leftover-section line for one bucket with no suffix.
func bucketLine(label, name string, n int) string {
	return "  " + label + strings.Repeat(" ", 9-len(label)) + "queued sessions " + name + ": " + strconv.Itoa(n) + "\n"
}

// G1: a queued, ingested session nobody explains is informational by default
// and a mismatch only under --strict-leftovers.
func TestTriageCheck_UnexplainedLeftoverFailsOnlyWhenStrict(t *testing.T) {
	path := triageCheckDB(t, queuedS2)
	code, out, _ := runTriageCheck(t, "--db", path)
	if code != 0 {
		t.Fatalf("without the flag: exit %d, want 0; stdout\n%s", code, out)
	}
	want := "  info     queued sessions unexplained: 1 (may be beyond the run's cap; re-run with --strict-leftovers to fail on it)\n           s-2\n"
	if !strings.Contains(out, want) {
		t.Errorf("without the flag the unexplained line is not informational with the suffix:\n%s", out)
	}
	code, out, _ = runTriageCheck(t, "--db", path, "--strict-leftovers")
	if code != 1 {
		t.Fatalf("with --strict-leftovers: exit %d, want 1; stdout\n%s", code, out)
	}
	if !strings.Contains(out, "  MISMATCH queued sessions unexplained: 1\n           s-2\n") {
		t.Errorf("with --strict-leftovers the unexplained line is not a mismatch naming s-2:\n%s", out)
	}
}

// G1: a fully explained run prints the four buckets and no suffix.
func TestTriageCheck_ExplainedRunPrintsFourBuckets(t *testing.T) {
	path := triageCheckDB(t)
	code, out, _ := runTriageCheck(t, "--db", path, "--strict-leftovers")
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	want := bucketLine("info", "covered by this run", 1) +
		bucketLine("info", "held by an earlier run", 0) +
		bucketLine("info", "arrived after the run started", 0) +
		bucketLine("ok", "unexplained", 0)
	if !strings.Contains(out, want) {
		t.Errorf("stdout lacks the four bucket lines\n%s\nwant\n%s", out, want)
	}
}

// laterRun is run 6: a fleet-wide operator run that started after run 1 and
// wrote no verdict. Checked with --run 6, everything run 1 left is "earlier".
const laterRun = `INSERT INTO triage_runs (id, trigger, status, started_at, finished_at)
	 VALUES (6, 'operator', 'ok', '2026-10-06T03:30:00Z', '2026-10-06T03:40:00Z')`

// An undone verdict of an earlier run on the queued decision holds the session:
// the engine never judges that decision again.
func TestTriageCheck_UndoneFromEarlierRunIsHeld(t *testing.T) {
	path := triageCheckDB(t, queuedS2, laterRun,
		`INSERT INTO triage_verdicts (run_id, kind, ref, item_key, value, state, created_at)
		 VALUES (1, 'classifier', '4', 's-2', 'shipped', 'undone', '2026-10-05T03:32:00Z')`)
	code, out, _ := runTriageCheck(t, "--db", path, "--run", "6", "--strict-leftovers")
	// s-1 is held by run 1's sample, s-2 by run 1's undone verdict.
	if code != 0 || !strings.Contains(out, bucketLine("info", "covered by this run", 0)+bucketLine("info", "held by an earlier run", 2)) {
		t.Errorf("exit %d, want 0 with s-1 and s-2 held by an earlier run:\n%s", code, out)
	}
}

// The run's OWN applied verdict that the operator undid: the decision is back
// in the queue and the engine blocks it for good. That is covered, not a miss.
func TestTriageCheck_OwnUndoneVerdictCoversTheSession(t *testing.T) {
	for _, state := range []string{"undone", "undoing"} {
		t.Run(state, func(t *testing.T) {
			path := triageCheckDB(t, queuedS2,
				`INSERT INTO triage_verdicts (run_id, kind, ref, item_key, value, state, created_at)
				 VALUES (1, 'classifier', '4', 's-2', 'shipped', '`+state+`', '2026-10-05T03:32:00Z')`,
				`UPDATE triage_runs SET applied = applied + 1 WHERE id = 1`)
			code, out, _ := runTriageCheck(t, "--db", path, "--strict-leftovers")
			if code != 0 || !strings.Contains(out, bucketLine("info", "covered by this run", 2)) {
				t.Errorf("exit %d, want 0 with s-2 covered by this run:\n%s", code, out)
			}
		})
	}
}

// A holding verdict on ANOTHER decision of the same session — one that is no
// longer queued — does not hold the queued decision: the engine blocks each
// decision by its own ref, so it would have judged this one.
func TestTriageCheck_VerdictOnAnotherDecisionDoesNotHold(t *testing.T) {
	path := triageCheckDB(t, queuedS2, laterRun,
		`INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, confidence, backend, ground_truth, ground_truth_at, created_at)
		 VALUES (7, 'd2.task_type', 's-2', 's-2', 'h', 'bugfix', 0.9, 'local', 'bugfix', '2026-10-05T09:00:00Z', '2026-10-02T09:00:00Z')`,
		`INSERT INTO triage_verdicts (run_id, kind, ref, item_key, value, state, created_at)
		 VALUES (1, 'classifier', '7', 's-2', 'feature', 'undone', '2026-10-05T03:32:00Z')`)
	code, out, _ := runTriageCheck(t, "--db", path, "--run", "6", "--strict-leftovers")
	if code != 1 || !strings.Contains(out, "  MISMATCH queued sessions unexplained: 1\n           s-2\n") {
		t.Errorf("exit %d, want 1 naming s-2 as unexplained:\n%s", code, out)
	}
}

// A verdict a LATER run left does not explain what this run skipped.
func TestTriageCheck_VerdictFromLaterRunDoesNotExplain(t *testing.T) {
	path := triageCheckDB(t, queuedS2, laterRun,
		`INSERT INTO triage_verdicts (run_id, kind, ref, item_key, value, state, created_at)
		 VALUES (6, 'classifier', '4', 's-2', 'shipped', 'sample', '2026-10-06T03:32:00Z')`,
		`UPDATE triage_runs SET suggested = 1 WHERE id = 6`)
	code, out, _ := runTriageCheck(t, "--db", path, "--run", "1", "--strict-leftovers")
	if code != 1 || !strings.Contains(out, "  MISMATCH queued sessions unexplained: 1\n           s-2\n") {
		t.Errorf("run 1: exit %d, want 1 naming s-2 as unexplained:\n%s", code, out)
	}
	// Run 6 itself is fine: it covers s-2, and s-1 is held by run 1.
	if code, out, _ := runTriageCheck(t, "--db", path, "--run", "6", "--strict-leftovers"); code != 0 {
		t.Errorf("run 6: exit %d, want 0:\n%s", code, out)
	}
}

// The engine's own windows apply: a skip holds a decision for 7 days before
// the run's start, and one failure does not hold it at all (three do).
func TestTriageCheck_HeldFollowsTheEngineWindows(t *testing.T) {
	verdict := func(state, at string) string {
		return `INSERT INTO triage_verdicts (run_id, kind, ref, item_key, value, state, created_at)
		 VALUES (1, 'classifier', '4', 's-2', 'shipped', '` + state + `', '` + at + `')`
	}
	for name, tc := range map[string]struct {
		seed     []string
		counters string
		wantCode int
	}{
		"skipped a day before":   {[]string{verdict("skipped", "2026-10-05T03:32:00.000Z")}, "skipped = 1", 0},
		"skipped a month before": {[]string{verdict("skipped", "2026-09-01T03:32:00.000Z")}, "skipped = 1", 1},
		"one failure":            {[]string{verdict("failed", "2026-10-05T03:32:00.000Z")}, "failed = 1", 1},
		"three failures": {[]string{verdict("failed", "2026-10-05T03:32:00.000Z"), verdict("failed", "2026-10-05T03:33:00.000Z"),
			verdict("rejected", "2026-10-05T03:34:00.000Z")}, "failed = 2, rejected = 1", 0},
	} {
		t.Run(name, func(t *testing.T) {
			seed := append([]string{queuedS2, laterRun}, tc.seed...)
			path := triageCheckDB(t, append(seed, `UPDATE triage_runs SET `+tc.counters+` WHERE id = 1`)...)
			code, out, _ := runTriageCheck(t, "--db", path, "--run", "6", "--strict-leftovers")
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d:\n%s", code, tc.wantCode, out)
			}
		})
	}
}

// G1: decisions created after the run started were never seen by it. The
// fractional form sorts BEFORE the run's start as a string, so this also proves
// the timestamps are parsed, not compared as text.
func TestTriageCheck_DecisionAfterRunStartArrivedLater(t *testing.T) {
	for _, createdAt := range []string{"2026-10-05T04:00:00Z", "2026-10-05T03:30:00.500Z"} {
		t.Run(createdAt, func(t *testing.T) {
			path := triageCheckDB(t,
				`INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, confidence, backend, created_at)
				 VALUES (4, 'd2.outcome', 's-2', 's-2', 'h', 'shipped', 0.9, 'local', '`+createdAt+`')`)
			code, out, _ := runTriageCheck(t, "--db", path, "--strict-leftovers")
			if code != 0 || !strings.Contains(out, bucketLine("info", "arrived after the run started", 1)) {
				t.Errorf("exit %d, want 0 with s-2 arrived after the run started:\n%s", code, out)
			}
		})
	}
}

// G1: a project-scoped run is checked against its own project's queue only.
func TestTriageCheck_ProjectScopedRunUsesItsOwnQueue(t *testing.T) {
	path := triageCheckDB(t,
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (2, '/other', 'q', '2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions (id, project_id, session_uuid, title, started_at) VALUES (3, 2, 's-3', 'three', '2026-10-01T12:00:00.000Z')`,
		`INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (3, 1, 'assistant', '2026-10-01T12:30:00.000Z', 'z')`,
		`INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, confidence, backend, created_at)
		 VALUES (5, 'd2.outcome', 's-3', 's-3', 'h', 'shipped', 0.9, 'local', '2026-10-02T09:00:00Z')`,
		`INSERT INTO triage_runs (id, trigger, scope_project_id, status, started_at) VALUES (4, 'operator', 1, 'ok', '2026-10-05T05:00:00Z')`)
	code, out, _ := runTriageCheck(t, "--db", path, "--run", "4", "--strict-leftovers")
	if code != 0 || strings.Contains(out, "s-3") {
		t.Fatalf("exit %d, want 0 without s-3:\n%s", code, out)
	}
	want := bucketLine("info", "covered by this run", 0) +
		bucketLine("info", "held by an earlier run", 1) +
		bucketLine("info", "arrived after the run started", 0) +
		bucketLine("ok", "unexplained", 0)
	if !strings.Contains(out, want) {
		t.Errorf("project 1's queue is not s-1 alone\n%s\nwant\n%s", out, want)
	}
	// The same fixture seen by the fleet-wide run 1 does count s-3.
	if code, out, _ := runTriageCheck(t, "--db", path, "--run", "1", "--strict-leftovers"); code != 1 || !strings.Contains(out, "           s-3\n") {
		t.Errorf("fleet run: exit %d, want 1 naming s-3:\n%s", code, out)
	}
}

// G1: a run without the classifier kind skips rules 3 and 4.
func TestTriageCheck_RunWithoutClassifierSkipsLeftovers(t *testing.T) {
	path := triageCheckDB(t, queuedS2,
		`INSERT INTO triage_runs (id, trigger, kinds, status, started_at) VALUES (5, 'operator', '["advisor"]', 'ok', '2026-10-05T05:00:00Z')`)
	code, out, _ := runTriageCheck(t, "--db", path, "--run", "5", "--strict-leftovers")
	if code != 0 || !strings.Contains(out, "  info     classifier was not part of this run\n") {
		t.Fatalf("exit %d, want 0 with the not-part-of-this-run line:\n%s", code, out)
	}
	if strings.Contains(out, "queued sessions") || strings.Contains(out, "out of the classifier's reach") {
		t.Errorf("rules 3 and 4 ran for a run without the classifier:\n%s", out)
	}
}

func TestTriageCheck_SampleVerdictCoversTheSession(t *testing.T) {
	// s-2's decision was sampled by the run, by ref only (no item key): covered.
	path := triageCheckDB(t,
		`INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, confidence, backend, created_at)
		 VALUES (4, 'd2.outcome', 's-2', 's-2', 'h', 'shipped', 0.9, 'local', '2026-10-02T09:00:00Z')`,
		`INSERT INTO triage_verdicts (run_id, kind, ref, value, state, created_at)
		 VALUES (1, 'classifier', '4', 'shipped', 'sample', '2026-10-05T03:32:00Z')`,
		`UPDATE triage_runs SET suggested = 2 WHERE id = 1`)
	if code, out, _ := runTriageCheck(t, "--db", path); code != 0 {
		t.Errorf("exit %d, want 0:\n%s", code, out)
	}
}

// F1: a queued decision whose session has no sessions row is out of the
// classifier's reach — rule 4 (informational), not a rule-3 leftover.
func TestTriageCheck_NotIngestedSessionIsInformational(t *testing.T) {
	notIngested := `INSERT INTO decisions (id, question_id, subject, session_uuid, input_hash, answer, confidence, backend, created_at)
		 VALUES (5, 'd2.outcome', 's-9', 's-9', 'h', 'shipped', 0.9, 'local', '2026-10-02T09:00:00Z')`
	t.Run("alone it passes", func(t *testing.T) {
		path := triageCheckDB(t, notIngested)
		code, out, _ := runTriageCheck(t, "--db", path)
		if code != 0 {
			t.Fatalf("exit %d, want 0:\n%s", code, out)
		}
		want := "  info     queue decisions whose session is not ingested (out of the classifier's reach): 1\n           s-9\n"
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not name s-9 under the not-ingested line:\n%s", out)
		}
	})
	t.Run("an ingested leftover still fails", func(t *testing.T) {
		path := triageCheckDB(t, notIngested, queuedS2)
		code, out, _ := runTriageCheck(t, "--db", path, "--strict-leftovers")
		if code != 1 || !strings.Contains(out, "           s-2\n") {
			t.Fatalf("exit %d, want 1 naming s-2:\n%s", code, out)
		}
		if !strings.Contains(out, "  MISMATCH queued sessions unexplained: 1\n") {
			t.Errorf("s-9 counted as a leftover:\n%s", out)
		}
	})
}

// F2: every state in which a question legitimately stays queued covers its
// session; applied does not (an applied label takes the question out of the
// queue), so an applied-only queued session is unexplained.
func TestTriageCheck_CoveringStates(t *testing.T) {
	verdict := func(state string) string {
		return `INSERT INTO triage_verdicts (run_id, kind, ref, item_key, value, state, created_at)
		 VALUES (1, 'classifier', '4', 's-2', 'shipped', '` + state + `', '2026-10-05T03:32:00Z')`
	}
	for _, tc := range []struct {
		state, counter string
		wantCode       int
	}{
		{"sample", "suggested", 0},
		{"rejected", "rejected", 0},
		{"suggested", "suggested", 0},
		{"skipped", "skipped", 0},
		{"failed", "failed", 0},
		{"applied", "applied", 1},
	} {
		t.Run(tc.state, func(t *testing.T) {
			path := triageCheckDB(t, queuedS2, verdict(tc.state),
				`UPDATE triage_runs SET `+tc.counter+` = `+tc.counter+` + 1 WHERE id = 1`)
			code, out, _ := runTriageCheck(t, "--db", path, "--strict-leftovers")
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d:\n%s", code, tc.wantCode, out)
			}
			if tc.wantCode == 0 && !strings.Contains(out, bucketLine("info", "covered by this run", 2)) {
				t.Errorf("s-2 is not covered by this run:\n%s", out)
			}
			if tc.wantCode == 1 && !strings.Contains(out, "  MISMATCH queued sessions unexplained: 1\n           s-2\n") {
				t.Errorf("an applied-only queued session is not unexplained:\n%s", out)
			}
		})
	}
}

// A verdict from an earlier run does not cover the session for THIS run — the
// engine skipped it as blocked and wrote no row — but it holds the session:
// the leftover is explained, so the check passes even when strict.
func TestTriageCheck_VerdictFromEarlierRunHoldsTheSession(t *testing.T) {
	path := triageCheckDB(t, queuedS2, laterRun,
		`INSERT INTO triage_verdicts (run_id, kind, ref, item_key, value, state, created_at)
		 VALUES (1, 'classifier', '4', 's-2', 'shipped', 'sample', '2026-10-05T03:32:00Z')`,
		`UPDATE triage_runs SET suggested = suggested + 1 WHERE id = 1`)
	for _, args := range [][]string{{}, {"--strict-leftovers"}} {
		code, out, _ := runTriageCheck(t, append([]string{"--db", path, "--run", "6"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d, want 0:\n%s", args, code, out)
		}
		// Run 6 covers nothing itself; s-1 and s-2 are held by run 1.
		if !strings.Contains(out, bucketLine("info", "covered by this run", 0)+bucketLine("info", "held by an earlier run", 2)) {
			t.Errorf("%v: s-1 and s-2 not held by an earlier run:\n%s", args, out)
		}
	}
}

func TestTriageCheck_DefaultRunIsNewestFleetOperatorRun(t *testing.T) {
	// Run 2 is newer but scheduled, run 3 is newer but project-scoped: run 1 is checked.
	path := triageCheckDB(t,
		`INSERT INTO triage_runs (id, trigger, status, started_at) VALUES (2, 'schedule', 'failed', '2026-10-06T03:30:00Z')`,
		`INSERT INTO triage_runs (id, trigger, scope_project_id, status, started_at) VALUES (3, 'operator', 1, 'failed', '2026-10-06T04:30:00Z')`)
	code, out, _ := runTriageCheck(t, "--db", path)
	if code != 0 || !strings.Contains(out, "triage run 1\n") {
		t.Errorf("exit %d stdout\n%s", code, out)
	}
}

func TestTriageCheck_UnknownRunExitsOne(t *testing.T) {
	path := triageCheckDB(t)
	code, out, _ := runTriageCheck(t, "--db", path, "--run", "99")
	if code != 1 || !strings.Contains(out, "no triage run 99") {
		t.Errorf("exit %d stdout %q", code, out)
	}
}

func TestTriageCheck_UsageAndDBErrorsExitTwo(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.db")
	if code, _, _ := runTriageCheck(t, "--db", absent); code != 2 {
		t.Errorf("missing db: exit %d, want 2", code)
	}
	if _, err := os.Stat(absent); err == nil {
		t.Error("triage check created the missing database")
	}
	if code, _, _ := runTriageCheck(t, "--run", "x"); code != 2 {
		t.Errorf("bad --run: exit %d, want 2", code)
	}
}

// The check also passes on a connection that refuses writes outright.
func TestTriageCheck_AuditIsReadOnly(t *testing.T) {
	path := triageCheckDB(t)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Skipf("no read-only sqlite driver under this name: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Skipf("read-only open: %v", err)
	}
	var out bytes.Buffer
	ok, err := triageAudit(db, 0, true, &out)
	if err != nil || !ok {
		t.Errorf("audit on a read-only connection: ok=%v err=%v\n%s", ok, err, out.String())
	}
}
