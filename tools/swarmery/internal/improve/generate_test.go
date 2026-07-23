package improve

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// mockRunner returns a fixed stdout or error and records the prompt.
type mockRunner struct {
	out     string
	err     error
	prompts []string
}

func (m *mockRunner) Run(_ context.Context, prompt string) (string, error) {
	m.prompts = append(m.prompts, prompt)
	return m.out, m.err
}

func newService(t *testing.T, db *sql.DB, r Runner) *Service {
	t.Helper()
	return &Service{DB: db, Runner: r}
}

type proposalRow struct {
	agent, path, sha, diff, rationale, status string
	errCol                                    sql.NullString
	recID                                     sql.NullInt64
}

func readProposal(t *testing.T, db *sql.DB, id int64) proposalRow {
	t.Helper()
	var p proposalRow
	if err := db.QueryRow(`
		SELECT agent, agent_path, base_sha256, diff, rationale, status, error, recommendation_id
		  FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&p.agent, &p.path, &p.sha, &p.diff, &p.rationale, &p.status, &p.errCol, &p.recID); err != nil {
		t.Fatalf("read proposal %d: %v", id, err)
	}
	return p
}

func TestGenerateSuccess(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "agent body")
	runner := &mockRunner{out: validOut}
	svc := newService(t, db, runner)

	recID := int64(7)
	// The FK demands a real recommendation row.
	mustExec(t, db, `INSERT INTO recommendations
		(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (7, 'R2', 'agent', 'tech-lead', 't', 'd', '{}', 'accepted', 'R2:tech-lead', ?, ?)`, day(1), day(1))

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead", RecommendationID: &recID})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	p := readProposal(t, db, id)
	if p.status != "proposed" {
		t.Errorf("status = %q, want proposed", p.status)
	}
	if !strings.Contains(p.diff, "+new guardrail line") || p.rationale == "" {
		t.Errorf("diff/rationale not persisted: %+v", p)
	}
	if p.errCol.Valid {
		t.Errorf("error column = %q, want NULL", p.errCol.String)
	}
	if !p.recID.Valid || p.recID.Int64 != 7 {
		t.Errorf("recommendation_id = %+v, want 7", p.recID)
	}
	if p.sha == "" || p.path != "/x/tech-lead.md" {
		t.Errorf("base coordinates wrong: %+v", p)
	}
	// The prompt embedded the agent file and the evidence bundle.
	if len(runner.prompts) != 1 || !strings.Contains(runner.prompts[0], "agent body") ||
		!strings.Contains(runner.prompts[0], "## Scorecard") {
		t.Error("prompt missing agent content or evidence bundle")
	}
}

func TestGenerateDedupConflict(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	svc := newService(t, db, &mockRunner{out: validOut})

	if _, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"}); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	_, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if !errors.Is(err, ErrOpenProposal) {
		t.Fatalf("second Generate err = %v, want ErrOpenProposal", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_change_proposals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("proposals = %d, want 1 (conflict writes nothing)", n)
	}
}

func TestGenerateRunnerError(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	svc := newService(t, db, &mockRunner{err: errors.New("claude -p: exit status 1; stderr: boom")})

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate must capture the runner error in the row, got %v", err)
	}
	p := readProposal(t, db, id)
	if p.status != "failed" {
		t.Errorf("status = %q, want failed", p.status)
	}
	if !p.errCol.Valid || !strings.Contains(p.errCol.String, "stderr: boom") {
		t.Errorf("error column = %+v, want the runner stderr", p.errCol)
	}
	if p.recID.Valid {
		t.Errorf("ad-hoc trigger must keep recommendation_id NULL, got %d", p.recID.Int64)
	}
}

func TestGenerateNoChange(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	svc := newService(t, db, &mockRunner{out: "## Diff\n```diff\n```\n## Rationale\nEvidence too thin.\n"})

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	p := readProposal(t, db, id)
	if p.status != "failed" {
		t.Errorf("status = %q, want failed", p.status)
	}
	if !p.errCol.Valid || p.errCol.String != "model found no justified change" {
		t.Errorf("error column = %+v, want the no-change sentinel text", p.errCol)
	}
}

// A malformed model output (contract violation) also lands as failed, and a
// failed row does NOT block a fresh Generate (only proposed|approved do).
func TestGenerateContractViolationThenRegenerate(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	runner := &mockRunner{out: "no sections at all"}
	svc := newService(t, db, runner)

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if p := readProposal(t, db, id); p.status != "failed" || !strings.Contains(p.errCol.String, "output contract") {
		t.Errorf("contract violation row = %+v, want failed with contract error", p)
	}

	runner.out = validOut
	id2, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("regenerate after failed: %v", err)
	}
	if p := readProposal(t, db, id2); p.status != "proposed" {
		t.Errorf("regenerated status = %q, want proposed", p.status)
	}
}

func TestGenerateUnknownAgent(t *testing.T) {
	db := openDB(t)
	svc := newService(t, db, &mockRunner{out: validOut})
	if _, err := svc.Generate(context.Background(), GenerateReq{Agent: "ghost"}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("err = %v, want ErrAgentNotFound", err)
	}
}

func TestRetry(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	runner := &mockRunner{err: errors.New("transient failure")}
	svc := newService(t, db, runner)

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Retry after the runner recovers: failed → proposed, in place.
	runner.err, runner.out = nil, validOut
	if err := svc.Retry(context.Background(), id); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	p := readProposal(t, db, id)
	if p.status != "proposed" || p.errCol.Valid || !strings.Contains(p.diff, "+new guardrail line") {
		t.Errorf("retried row = %+v, want proposed with diff and NULL error", p)
	}

	// Not-failed rows are not retriable; unknown ids are not found.
	if err := svc.Retry(context.Background(), id); !errors.Is(err, ErrNotRetriable) {
		t.Errorf("retry of proposed row: err = %v, want ErrNotRetriable", err)
	}
	if err := svc.Retry(context.Background(), 999); !errors.Is(err, ErrProposalNotFound) {
		t.Errorf("retry of unknown id: err = %v, want ErrProposalNotFound", err)
	}
}

// Retry stays failed (with a refreshed error) when the runner fails again.
func TestRetryFailsAgain(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	runner := &mockRunner{err: errors.New("first failure")}
	svc := newService(t, db, runner)

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	runner.err = errors.New("second failure")
	if err := svc.Retry(context.Background(), id); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	p := readProposal(t, db, id)
	if p.status != "failed" || !strings.Contains(p.errCol.String, "second failure") {
		t.Errorf("row after failed retry = %+v, want failed with refreshed error", p)
	}
}
