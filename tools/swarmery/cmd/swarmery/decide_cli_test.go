package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// evalDB is a migrated store holding one finished session the operator marked
// a success, with its three D2 answers labelled — and with the newest
// migration's marker REMOVED, so a command that migrates on open is caught
// putting it back.
func evalDB(t *testing.T) (path string, migrations int) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "eval.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/repo', 'p', '2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions (id, project_id, session_uuid, title, started_at, ended_at, outcome)
		 VALUES (1, 1, 's-1', 'fix the parser', '2026-09-20T10:00:00.000Z', '2026-09-20T11:00:00.000Z', 'success')`,
		`INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (1, 1, 'assistant', '2026-09-20T10:30:00.000Z', 'Fixed.')`,
		`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, backend, ground_truth, ground_truth_at, created_at) VALUES
		 ('d2.task_type',     's-1', 's-1', 'h', 'bugfix',  0.9, 'local', 'bugfix',  '2026-09-29T10:00:00Z', '2026-09-29T09:00:00Z'),
		 ('d2.outcome',       's-1', 's-1', 'h', 'partial', 0.9, 'local', 'shipped', '2026-09-29T10:00:00Z', '2026-09-29T09:00:00Z'),
		 ('d2.failure_cause', 's-1', 's-1', 'h', 'none',    0.9, 'local', 'none',    '2026-09-29T10:00:00Z', '2026-09-29T09:00:00Z')`,
		`DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	return path, migrations
}

func noEnv(string) string { return "" }

func runDecideEval(t *testing.T, getenv func(string) string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = decideEval(context.Background(), args, &out, &errOut, getenv)
	return code, out.String(), errOut.String()
}

// Rules-only needs no backend: the operator's verdict answers d2.outcome, the
// rest is unanswered, the exit is 0 — and the database is neither migrated nor
// changed.
func TestDecideEval_RulesOnlyReadsAndExitsZero(t *testing.T) {
	path, migrations := evalDB(t)

	code, stdout, stderr := runDecideEval(t, noEnv, "--db", path)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	for _, want := range []string{"decide eval — rules-only, all labels, 1 subjects", "d2.outcome", "d2.task_type", "d2.failure_cause"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if stderr != "" {
		t.Errorf("rules-only wrote to stderr: %q", stderr)
	}

	db, err := store.OpenNoMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var gotMigrations, decisions, labels int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM schema_migrations), (SELECT COUNT(*) FROM decisions),
		(SELECT COUNT(*) FROM session_labels)`).Scan(&gotMigrations, &decisions, &labels); err != nil {
		t.Fatal(err)
	}
	if gotMigrations != migrations {
		t.Errorf("schema_migrations rows = %d, want %d: the eval migrated the database", gotMigrations, migrations)
	}
	if decisions != 3 || labels != 0 {
		t.Errorf("decisions = %d, session_labels = %d; want 3 and 0 (the eval writes nothing)", decisions, labels)
	}
}

func TestDecideEval_JSONOutAndFilters(t *testing.T) {
	path, _ := evalDB(t)
	outFile := filepath.Join(t.TempDir(), "report.json")

	code, stdout, stderr := runDecideEval(t, noEnv, "--db", path, "--json", "--out", outFile,
		"--questions", "outcome, d2.task_type", "--truth-since", "2026-09-29T00:00:00Z", "--limit", "5")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	var rep decide.EvalReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the JSON report: %v\n%s", err, stdout)
	}
	if rep.LLM || rep.TruthSince != "2026-09-29T00:00:00Z" || len(rep.Questions) != 2 ||
		rep.Questions[0].QuestionID != decide.QD2TaskType || rep.Questions[1].QuestionID != decide.QD2Outcome {
		t.Errorf("report = %+v, want rules-only, the since bound, task_type then outcome", rep)
	}
	if rep.Questions[1].Agree != 1 || rep.Questions[1].Agreement == nil || *rep.Questions[1].Agreement != 1 {
		t.Errorf("outcome = %+v, want the rule's shipped agreeing with the label", rep.Questions[1])
	}
	file, err := os.ReadFile(outFile)
	if err != nil || string(file) != stdout {
		t.Errorf("--out file differs from stdout (err %v):\n%s", err, file)
	}

	// A since bound after every label leaves nothing to replay — still exit 0.
	if code, stdout, _ := runDecideEval(t, noEnv, "--db", path, "--truth-since", "2026-10-01T00:00:00Z"); code != 0 ||
		!strings.Contains(stdout, "0 subjects") {
		t.Errorf("empty window: exit %d\n%s", code, stdout)
	}
}

// --llm asks the local model named by the daemon's own env knobs, reports
// progress on stderr and keeps stdout to the report — and still writes nothing.
func TestDecideEval_LLMAsksTheLocalModel(t *testing.T) {
	path, _ := evalDB(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{
			map[string]any{"message": map[string]any{"role": "assistant", "content": `{"answer":"other"}`}},
		}})
	}))
	defer srv.Close()
	env := map[string]string{"SWARMERY_DECIDE_URL": srv.URL + "/v1", "SWARMERY_DECIDE_MODEL": "test-model",
		"SWARMERY_DECIDE_CLAUDE": "on"} // even when the daemon enables it, the eval never builds the claude backend

	code, stdout, stderr := runDecideEval(t, func(k string) string { return env[k] }, "--db", path, "--llm")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	// task_type and failure_cause reach the model; outcome is answered by the rule.
	if calls != 2 {
		t.Errorf("local model calls = %d, want 2", calls)
	}
	if !strings.Contains(stdout, "decide eval — rules + local model, all labels, 1 subjects") {
		t.Errorf("stdout header:\n%s", stdout)
	}
	if !strings.Contains(stderr, "decide eval: 1/1 sessions, 0 backend errors") || !strings.Contains(stderr, "claude=false") {
		t.Errorf("stderr lacks the progress line or shows the claude backend on: %q", stderr)
	}
	if strings.Contains(stdout, "sessions, ") {
		t.Errorf("progress leaked into stdout:\n%s", stdout)
	}
	db, err := store.OpenNoMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var decisions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&decisions); err != nil || decisions != 3 {
		t.Errorf("decisions = %d (%v), want the 3 seeded rows: --llm must not record its calls", decisions, err)
	}
}

// Exit 1 is reserved for a missed floor; a floor that holds exits 0.
func TestDecideEval_Floors(t *testing.T) {
	path, _ := evalDB(t)
	if code, _, stderr := runDecideEval(t, noEnv, "--db", path, "--min-outcome", "1"); code != 0 {
		t.Errorf("outcome agreement is 1.0, floor 1 must hold: exit %d\n%s", code, stderr)
	}
	code, stdout, stderr := runDecideEval(t, noEnv, "--db", path, "--min-task", "0.5", "--min-failure", "0")
	if code != 1 {
		t.Fatalf("task_type is unanswered rules-only, floor 0.5 must be missed: exit %d", code)
	}
	if !strings.Contains(stderr, "floor missed — d2.task_type: agreement 0.000 is below floor 0.500") {
		t.Errorf("stderr does not name the missed floor: %q", stderr)
	}
	if strings.Contains(stderr, "d2.failure_cause") {
		t.Errorf("a floor of 0 was reported missed: %q", stderr)
	}
	if !strings.Contains(stdout, "d2.task_type") {
		t.Error("the report must still be printed when a floor is missed")
	}
}

// Asking for help is not an error: usage on stderr, no report, exit 0 — the
// same contract as `swarmery decide help`.
func TestDecideEval_HelpExitsZero(t *testing.T) {
	for _, flagName := range []string{"-h", "--help"} {
		t.Run(flagName, func(t *testing.T) {
			code, stdout, stderr := runDecideEval(t, noEnv, flagName)
			if code != 0 {
				t.Errorf("exit = %d, want 0\nstderr: %s", code, stderr)
			}
			if stdout != "" || !strings.HasPrefix(stderr, "usage:") {
				t.Errorf("help must print the usage alone on stderr: stdout %q stderr %q", stdout, stderr)
			}
		})
	}
}

// Exit 2: usage and database errors — including --llm with no local model.
func TestDecideEval_UsageAndDBErrors(t *testing.T) {
	path, _ := evalDB(t)
	absent := filepath.Join(t.TempDir(), "absent.db")
	for name, args := range map[string][]string{
		"unknown flag":          {"--db", path, "--nope"},
		"positional argument":   {"--db", path, "extra"},
		"bad truth-since":       {"--db", path, "--truth-since", "yesterday"},
		"negative limit":        {"--db", path, "--limit", "-1"},
		"floor above one":       {"--db", path, "--min-outcome", "1.5"},
		"negative floor":        {"--db", path, "--min-task", "-0.1"},
		"NaN floor":             {"--db", path, "--min-outcome", "NaN"},
		"unreplayable question": {"--db", path, "--questions", "d1.run_end"},
		"unknown question":      {"--db", path, "--questions", "mood"},
		"missing database":      {"--db", absent},
		"llm without a url":     {"--db", path, "--llm"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runDecideEval(t, noEnv, args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2\nstderr: %s", code, stderr)
			}
			if stdout != "" || stderr == "" {
				t.Errorf("an error must say why on stderr and print no report: stdout %q stderr %q", stdout, stderr)
			}
		})
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Errorf("a missing database was created at %s (stat err: %v)", absent, err)
	}
	if code := cmdDecide(nil); code != 2 {
		t.Errorf("cmdDecide(nil) = %d, want 2", code)
	}
	if code := cmdDecide([]string{"nuke"}); code != 2 {
		t.Errorf(`cmdDecide("nuke") = %d, want 2`, code)
	}
	if code := cmdDecide([]string{"help"}); code != 0 {
		t.Errorf(`cmdDecide("help") = %d, want 0`, code)
	}
}
