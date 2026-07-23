package improve

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// day renders a UTC timestamp at local noon, `back` days before today —
// inside the advisor's trailing 14-day window for back < 14.
func day(back int) string {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return start.AddDate(0, 0, -back).Add(12 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "improve.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v\n%s", err, q)
	}
}

// seedAgent registers one agent + current version in the sysscan tables.
func seedAgent(t *testing.T, db *sql.DB, id int64, name, origin, path, content string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO agents (id, name, scope, file_path, origin) VALUES (?, ?, 'global', ?, ?)`,
		id, name, path, origin)
	mustExec(t, db, `INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at)
		VALUES (?, ?, ?, ?, ?)`, id, id, "h"+name, content, day(1))
	mustExec(t, db, `UPDATE agents SET current_version_id = ? WHERE id = ?`, id, id)
}

func TestBuildEvidenceAgentSourceAndSHA(t *testing.T) {
	db := openDB(t)
	const localBody = "---\nname: tech-lead\n---\nlocal body"
	// Collision: the plugin row exists too, but local must win (override rule).
	seedAgent(t, db, 1, "core:tech-lead", "plugin", "/cache/core/agents/tech-lead.md", "plugin body")
	seedAgent(t, db, 2, "tech-lead", "local", "/repo/.claude/agents/tech-lead.md", localBody)

	ev, err := buildEvidence(db, "tech-lead", "")
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if ev.AgentPath != "/repo/.claude/agents/tech-lead.md" {
		t.Errorf("AgentPath = %q, want the local row's path", ev.AgentPath)
	}
	if ev.AgentContent != localBody {
		t.Errorf("AgentContent = %q, want the local version content", ev.AgentContent)
	}
	sum := sha256.Sum256([]byte(localBody))
	if want := hex.EncodeToString(sum[:]); ev.BaseSHA256 != want {
		t.Errorf("BaseSHA256 = %q, want %q", ev.BaseSHA256, want)
	}
	for _, want := range []string{"# Evidence — agent tech-lead", "## Scorecard", "## Ledger assessments", "## Open improvements", "## Transcript excerpts"} {
		if !strings.Contains(ev.Bundle, want) {
			t.Errorf("bundle missing section %q", want)
		}
	}
}

// A deleted registry row (or a fold miss) is a typed error the API turns
// into 404.
func TestBuildEvidenceMissingAgent(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	mustExec(t, db, `UPDATE agents SET deleted = 1 WHERE id = 1`)

	if _, err := buildEvidence(db, "tech-lead", ""); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("deleted agent: err = %v, want ErrAgentNotFound", err)
	}
	if _, err := buildEvidence(db, "ghost", ""); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("unknown agent: err = %v, want ErrAgentNotFound", err)
	}
}

func TestBuildEvidenceAssessmentFilter(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	mustExec(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', 'p', ?)`, day(10))
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, created_at, external_id) VALUES
		(1, 1, 'T', 'p', ?, '2026-07-20-checkout-flow')`, day(10))
	mustExec(t, db, `INSERT INTO task_delegations (task_id, seq, agent, phase, verdict, loops, quality, mistakes) VALUES
		(1, 1, 'core:tech-lead', 'phase-1', 'redo', 2, 3,    'missed the FK constraint'),
		(1, 2, 'tech-lead',      'phase-2', 'ok',   1, 5,    NULL),
		(1, 3, 'tech-lead',      'phase-3', 'ok',   1, NULL, 'silent scope creep'),
		(1, 4, 'debugger',       'phase-4', 'redo', 3, 1,    'wrong root cause')`)

	ev, err := buildEvidence(db, "tech-lead", "")
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if !strings.Contains(ev.Bundle, "missed the FK constraint") {
		t.Error("quality=3 row (core: notation) missing from the bundle")
	}
	if !strings.Contains(ev.Bundle, "silent scope creep") {
		t.Error("NULL-quality row with mistakes missing from the bundle")
	}
	if strings.Contains(ev.Bundle, "phase-2") {
		t.Error("clean quality=5 row leaked into the bundle")
	}
	if strings.Contains(ev.Bundle, "wrong root cause") {
		t.Error("another agent's assessment leaked into the bundle")
	}
	if !strings.Contains(ev.Bundle, "2026-07-20-checkout-flow") {
		t.Error("task external id missing from the assessment line")
	}
}

func TestBuildEvidenceImprovements(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	mustExec(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', 'p', ?)`, day(10))
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, created_at) VALUES (1, 1, 'T', 'p', ?)`, day(10))
	mustExec(t, db, `INSERT INTO task_retros (id, task_id, ingested_at) VALUES (1, 1, ?)`, day(5))
	mustExec(t, db, `INSERT INTO retro_improvements (retro_id, text, priority, status) VALUES
		(1, 'Sharpen Tech-Lead acceptance criteria', 'high', 'open'),
		(1, 'tech-lead should stop skipping tests',  'high', 'done'),
		(1, 'Unrelated tooling cleanup',             'low',  'open')`)

	ev, err := buildEvidence(db, "tech-lead", "")
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if !strings.Contains(ev.Bundle, "Sharpen Tech-Lead acceptance criteria") {
		t.Error("open case-insensitive mention missing from the bundle")
	}
	if strings.Contains(ev.Bundle, "stop skipping tests") {
		t.Error("done improvement leaked into the bundle")
	}
	if strings.Contains(ev.Bundle, "Unrelated tooling cleanup") {
		t.Error("non-mentioning improvement leaked into the bundle")
	}
}

func TestBuildEvidenceScorecardAndExcerpts(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	mustExec(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', 'p', ?)`, day(10))
	mustExec(t, db, `INSERT INTO sessions (id, project_id, session_uuid, started_at) VALUES (1, 1, 'u1', ?)`, day(2))
	// One run with a behavior-fixable tool error, framed by neighbor events.
	mustExec(t, db, `INSERT INTO events (id, session_id, ts, type, status, payload, dedup_key) VALUES
		(1, 1, ?, 'subagent_start', 'ok', '{"subagent_type":"tech-lead"}', 'a1'),
		(2, 1, ?, 'tool_call',      'ok', '{"note":"context before"}',     'a2')`, day(2), day(2))
	mustExec(t, db, `INSERT INTO events (id, session_id, parent_event_id, ts, type, tool_name, status, payload, dedup_key) VALUES
		(3, 1, 1, ?, 'tool_call', 'Bash', 'error', '{"result":"Error: exit code 2"}', 'a3')`, day(2))
	mustExec(t, db, `INSERT INTO events (id, session_id, ts, type, status, payload, dedup_key) VALUES
		(4, 1, ?, 'tool_call', 'ok', '{"note":"context after"}', 'a4')`, day(2))

	ev, err := buildEvidence(db, "tech-lead", "")
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if !strings.Contains(ev.Bundle, "runs: 1; failed runs: 1 (behavior-fixable: 1); error events: 1") {
		t.Errorf("scorecard line wrong:\n%s", ev.Bundle)
	}
	if !strings.Contains(ev.Bundle, "behavior_fixable=1") {
		t.Error("errors_by_class missing behavior_fixable tally")
	}
	if !strings.Contains(ev.Bundle, ">>> ") || !strings.Contains(ev.Bundle, "Error: exit code 2") {
		t.Errorf("excerpt missing the marked error event:\n%s", ev.Bundle)
	}
	if !strings.Contains(ev.Bundle, "context before") || !strings.Contains(ev.Bundle, "context after") {
		t.Error("excerpt missing ±context events")
	}
}

func TestBuildEvidenceSizeCaps(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	mustExec(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', 'p', ?)`, day(10))
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, created_at) VALUES (1, 1, 'T', 'p', ?)`, day(10))
	// One giant mistakes cell pushes the raw bundle far past the cap.
	huge := strings.Repeat("щось пішло не так; ", 4000) // ~100KB, multi-byte runes
	mustExec(t, db, `INSERT INTO task_delegations (task_id, seq, agent, quality, mistakes) VALUES (1, 1, 'tech-lead', 2, ?)`, huge)

	ev, err := buildEvidence(db, "tech-lead", "")
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if len(ev.Bundle) > bundleCap {
		t.Errorf("bundle size = %d bytes, want ≤ %d", len(ev.Bundle), bundleCap)
	}
	if !strings.Contains(ev.Bundle, "[evidence truncated]") {
		t.Error("truncated bundle missing its marker")
	}
	if !utf8.ValidString(ev.Bundle) {
		t.Error("truncation split a UTF-8 rune")
	}
}
