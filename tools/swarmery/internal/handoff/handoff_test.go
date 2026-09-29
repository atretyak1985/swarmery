package handoff

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// testNow is the fixed evaluation instant every fixture is seeded around.
var testNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

func fmtTS(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?)`, fmtTS(testNow.AddDate(0, 0, -30)))
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v\n%s", err, q)
	}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// seedSession inserts a session row. cwd may be "" (NULL-ish) or a path.
func seedSession(t *testing.T, db *sql.DB, id int64, uuid, cwd string, startedAgo time.Duration) {
	t.Helper()
	var cwdArg any
	if cwd == "" {
		cwdArg = nil
	} else {
		cwdArg = cwd
	}
	mustExec(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, cwd, git_branch, title, started_at, hidden, pruned)
		VALUES (?, 1, ?, 'active', ?, 'dev', ?, ?, 0, 0)`,
		id, uuid, cwdArg, "Task "+uuid, fmtTS(testNow.Add(-startedAgo)))
}

// seedAssistantTurn inserts one assistant turn carrying a context footprint
// (tokens_in + cache_read + cache_write) and an ended_at, at the given seq.
func seedAssistantTurn(t *testing.T, db *sql.DB, sessionID int64, seq int, footprint int64, endedAgo time.Duration, text string) {
	t.Helper()
	// Split the footprint across the three fields the formula sums.
	in := footprint / 2
	cacheRead := footprint - in
	var textArg any
	if text == "" {
		textArg = nil
	} else {
		textArg = text
	}
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, ended_at, tokens_in, tokens_cache_read, tokens_cache_write, text)
		VALUES (?, ?, 'assistant', ?, ?, ?, ?, 0, ?)`,
		sessionID, seq, fmtTS(testNow.Add(-endedAgo-time.Minute)), fmtTS(testNow.Add(-endedAgo)),
		in, cacheRead, textArg)
}

// seedUserTurn inserts one user turn with prose at the given seq.
func seedUserTurn(t *testing.T, db *sql.DB, sessionID int64, seq int, endedAgo time.Duration, text string) {
	t.Helper()
	var textArg any
	if text == "" {
		textArg = nil
	} else {
		textArg = text
	}
	mustExec(t, db, `INSERT INTO turns (session_id, seq, role, started_at, ended_at, text)
		VALUES (?, ?, 'user', ?, ?, ?)`,
		sessionID, seq, fmtTS(testNow.Add(-endedAgo-time.Minute)), fmtTS(testNow.Add(-endedAgo)), textArg)
}

// seedHandoff inserts a prior handoffs row at the given context footprint.
func seedHandoff(t *testing.T, db *sql.DB, sessionID int64, ctxTokens int64) {
	t.Helper()
	mustExec(t, db, `INSERT INTO handoffs (session_id, path, context_tokens, created_at)
		VALUES (?, ?, ?, ?)`,
		sessionID, fmt.Sprintf("/tmp/handoffs/%d.md", sessionID), ctxTokens, fmtTS(testNow.Add(-time.Hour)))
}

func TestCandidates(t *testing.T) {
	db := testDB(t)

	// 1: over threshold + recent → IN.
	seedSession(t, db, 1, "over-recent", "/work/alpha", 10*time.Minute)
	seedAssistantTurn(t, db, 1, 1, Threshold+20_000, 10*time.Minute, "work")

	// 2: under threshold → OUT.
	seedSession(t, db, 2, "under", "/work/alpha", 10*time.Minute)
	seedAssistantTurn(t, db, 2, 1, Threshold-1, 10*time.Minute, "work")

	// 3: over threshold but stale (last turn older than ActivityWin) → OUT.
	seedSession(t, db, 3, "stale", "/work/alpha", 5*time.Hour)
	seedAssistantTurn(t, db, 3, 1, Threshold+50_000, ActivityWin+time.Hour, "work")

	// 4: over threshold + recent but System cwd → OUT.
	sysDir := t.TempDir() // stand-in System dir; overridden below via env-free injection
	seedSession(t, db, 4, "system", sysDir, 10*time.Minute)
	seedAssistantTurn(t, db, 4, 1, Threshold+50_000, 10*time.Minute, "work")

	// 5: over threshold + recent but already handed off within RegrowthDelta → OUT.
	seedSession(t, db, 5, "cooldown", "/work/alpha", 10*time.Minute)
	seedAssistantTurn(t, db, 5, 1, Threshold+10_000, 10*time.Minute, "work")
	seedHandoff(t, db, 5, Threshold+10_000-1) // last handoff ctx + delta > current → cooldown

	// 6: over threshold + recent, prior handoff but regrown past delta → IN again.
	seedSession(t, db, 6, "regrown", "/work/alpha", 10*time.Minute)
	seedAssistantTurn(t, db, 6, 1, Threshold+RegrowthDelta+30_000, 10*time.Minute, "work")
	seedHandoff(t, db, 6, Threshold+10_000) // last + delta ≤ current → eligible again

	// System exclusion is derived from ingest.SystemDir() in production; here we
	// verify the predicate directly by passing the System dir explicitly.
	cands, dropped, err := candidatesWithSysDir(db, testNow, sysDir)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0 (under the cap)", dropped)
	}
	got := map[string]int64{}
	for _, c := range cands {
		got[c.SessionUUID] = c.ContextTokens
	}
	if _, in := got["over-recent"]; !in {
		t.Errorf("over-recent must be a candidate; got %+v", cands)
	}
	if _, in := got["regrown"]; !in {
		t.Errorf("regrown (past delta) must be a candidate again; got %+v", cands)
	}
	for _, out := range []string{"under", "stale", "system", "cooldown"} {
		if _, in := got[out]; in {
			t.Errorf("%s must NOT be a candidate; got %+v", out, cands)
		}
	}

	// Ordering: footprint DESC (regrown has the larger footprint).
	if len(cands) >= 2 && cands[0].ContextTokens < cands[1].ContextTokens {
		t.Errorf("candidates must be footprint-DESC ordered; got %+v", cands)
	}
}

func TestCandidatesCapAndOverflow(t *testing.T) {
	db := testDB(t)
	total := MaxPerTick + 2
	for i := 1; i <= total; i++ {
		uuid := fmt.Sprintf("fat-%d", i)
		seedSession(t, db, int64(i), uuid, "/work/alpha", 10*time.Minute)
		// Distinct footprints so ordering is deterministic.
		seedAssistantTurn(t, db, int64(i), 1, Threshold+int64(i)*1000, 10*time.Minute, "work")
	}
	cands, dropped, err := candidatesWithSysDir(db, testNow, "/nonexistent-system")
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(cands) != MaxPerTick {
		t.Fatalf("len(cands) = %d, want cap %d", len(cands), MaxPerTick)
	}
	if dropped != total-MaxPerTick {
		t.Fatalf("dropped = %d, want %d (overflow logged, not silent)", dropped, total-MaxPerTick)
	}
	// The top MaxPerTick by footprint survive.
	if cands[0].ContextTokens < cands[len(cands)-1].ContextTokens {
		t.Errorf("cap must keep the fattest; got %+v", cands)
	}
}

func TestDigestTruncation(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, 1, "dig", "/work/alpha", time.Hour)

	long := strings.Repeat("x", 5000)
	seedAssistantTurn(t, db, 1, 1, 1000, time.Hour, long)    // must truncate to assistantTextCap
	seedAssistantTurn(t, db, 1, 2, 1000, 55*time.Minute, "") // empty → skipped
	seedAssistantTurn(t, db, 1, 3, 1000, 50*time.Minute, "short assistant note")
	seedUserTurn(t, db, 1, 4, 45*time.Minute, strings.Repeat("u", 3000)) // truncate to userTextCap
	seedUserTurn(t, db, 1, 5, 40*time.Minute, "")                        // empty → skipped

	// A file change so the Files-touched section is exercised.
	mustExec(t, db, `INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
		VALUES (1, ?, 'file_change', 'Edit', 'ok', '{}', 'fc-ev-1')`, fmtTS(testNow.Add(-time.Hour)))
	mustExec(t, db, `INSERT INTO file_changes (event_id, session_id, file_path, change_type, additions, deletions)
		VALUES (1, 1, 'internal/handoff/handoff.go', 'edit', 40, 3)`)

	dig, err := Digest(db, 1)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	// Long assistant text is truncated (must not contain the full 5000-run).
	if strings.Contains(dig, long) {
		t.Errorf("assistant text must be truncated to %d chars", assistantTextCap)
	}
	if !strings.Contains(dig, strings.Repeat("x", assistantTextCap)[:200]) {
		t.Errorf("digest must contain the (truncated) assistant text")
	}
	// Empty texts contribute nothing (no stray empty markers). "short assistant
	// note" survives.
	if !strings.Contains(dig, "short assistant note") {
		t.Errorf("non-empty assistant text must survive; digest=%q", dig)
	}
	// User text truncated.
	if strings.Contains(dig, strings.Repeat("u", 3000)) {
		t.Errorf("user text must be truncated to %d chars", userTextCap)
	}
	// File change present.
	if !strings.Contains(dig, "internal/handoff/handoff.go") {
		t.Errorf("digest must list touched files; digest=%q", dig)
	}
	// Session header present.
	if !strings.Contains(dig, "/work/alpha") {
		t.Errorf("digest must carry cwd; digest=%q", dig)
	}
}

// fakeRunner returns canned markdown and records the prompt it was handed.
type fakeRunner struct {
	out       string
	gotPrompt string
	err       error
}

func (f *fakeRunner) Run(_ context.Context, prompt string) (string, error) {
	f.gotPrompt = prompt
	return f.out, f.err
}

func TestGenerateWritesFileAndRow(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, 1, "gen-uuid", "/work/alpha", time.Hour)
	seedAssistantTurn(t, db, 1, 1, Threshold+5_000, 10*time.Minute, "did the thing")

	dir := t.TempDir()
	canned := "# Handoff: finish the widget\n## State\n- done\n"
	fr := &fakeRunner{out: canned}

	path, err := generateInto(db, fr, 1, testNow, dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Path is <dir>/<session_uuid>.md.
	wantPath := filepath.Join(dir, "gen-uuid.md")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	// File written with the runner's exact output, then the Go-recorded anchor.
	// /work/alpha does not exist, so the anchor is honestly unknown.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read handoff file: %v", err)
	}
	want := canned + "\n" + anchorHeading + "\n- unknown (cwd no longer exists)\n"
	if string(body) != want {
		t.Errorf("file body = %q, want %q", body, want)
	}
	// Prompt handed to the runner embeds the template + the digest.
	if !strings.Contains(fr.gotPrompt, "SESSION DIGEST") {
		t.Errorf("prompt must carry the digest marker; prompt=%q", fr.gotPrompt)
	}
	if !strings.Contains(fr.gotPrompt, "did the thing") {
		t.Errorf("prompt must carry the digest content; prompt=%q", fr.gotPrompt)
	}
	// A handoffs row was inserted with the context footprint at generation time.
	if n := count(t, db, `SELECT COUNT(*) FROM handoffs WHERE session_id = 1 AND path = ?`, wantPath); n != 1 {
		t.Fatalf("expected exactly one handoffs row, got %d", n)
	}
	var ctxTok int64
	if err := db.QueryRow(`SELECT context_tokens FROM handoffs WHERE session_id = 1`).Scan(&ctxTok); err != nil {
		t.Fatalf("scan ctx: %v", err)
	}
	if ctxTok != Threshold+5_000 {
		t.Errorf("stored context_tokens = %d, want %d", ctxTok, Threshold+5_000)
	}
}

// TestGenerateAppendsAnchor: the written brief is the model output followed by
// the Go-recorded anchor — the SAME facts the model saw in its digest — and the
// prompt asks for the Non-goals and Open-risks sections.
func TestGenerateAppendsAnchor(t *testing.T) {
	requireGit(t)
	repo := newDirtyRepo(t)
	db := testDB(t)
	seedSession(t, db, 1, "anchor-uuid", repo, time.Hour)
	seedAssistantTurn(t, db, 1, 1, Threshold+5_000, 10*time.Minute, "did the thing")

	fr := &fakeRunner{out: "# Handoff: x\n"}
	path, err := generateInto(db, fr, 1, testNow, t.TempDir())
	if err != nil {
		t.Fatalf("generateInto: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read handoff file: %v", err)
	}
	body := string(raw)

	// The anchor section is the tail of the file, after the model output.
	idx := strings.LastIndex(body, anchorHeading)
	if idx < 0 {
		t.Fatalf("file must contain the anchor heading; body=%q", body)
	}
	if !strings.HasPrefix(body, "# Handoff: x\n\n"+anchorHeading) {
		t.Errorf("anchor must follow the model output after one blank line; body=%q", body)
	}
	tail := body[idx:]
	if strings.Contains(tail[len(anchorHeading):], "\n## ") {
		t.Errorf("anchor must be the LAST section; tail=%q", tail)
	}
	head := gitT(t, repo, "rev-parse", "HEAD")
	for _, want := range []string{"- Root: " + repo, "- HEAD: " + head, "- Branch: main", "- Uncommitted (2):", "  - tracked.txt", "  - new file.txt"} {
		if !strings.Contains(tail, want) {
			t.Errorf("anchor tail missing %q; tail=%q", want, tail)
		}
	}
	// Computed once: the digest the model saw carries the identical block.
	if !strings.Contains(fr.gotPrompt, tail) {
		t.Errorf("the prompt's digest must carry the same anchor block as the file;\nprompt=%q\ntail=%q", fr.gotPrompt, tail)
	}

	// Prompt sections.
	for _, want := range []string{
		"## Non-goals",
		"## Open risks & failed checks",
		"Do not restate the repository anchor; it is appended automatically.",
		"≤120 lines",
		`write "unknown"`,
	} {
		if !strings.Contains(fr.gotPrompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
