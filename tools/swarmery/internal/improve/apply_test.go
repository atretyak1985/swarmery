package improve

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// fakeExec scripts the git/gh boundary for Apply tests. It records every Run
// call and answers from a per-command table keyed by the first two args
// (e.g. "git fetch", "git worktree", "bash scan", "gh pr"); a matched key not
// present falls through to "" (success). Files live in an in-memory map so the
// frontmatter + version-bump reads/writes are deterministic.
type fakeExec struct {
	// runResp maps a command signature to (stdout, err). Signature is
	// name + " " + args[0] (args[0] may be a subcommand or a path tail).
	runResp map[string]struct {
		out string
		err error
	}
	files   map[string][]byte
	runs    []string // signatures, in order
	removed []string // RemoveAll targets
	tmpDir  string   // returned by MkdirTemp
	writes  map[string][]byte
}

func newFakeExec() *fakeExec {
	return &fakeExec{
		runResp: map[string]struct {
			out string
			err error
		}{},
		files:  map[string][]byte{},
		writes: map[string][]byte{},
		tmpDir: "/tmp/improve-wt",
	}
}

func sig(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	// git commands: key on the subcommand (skip a leading -C <dir>).
	if name == "git" {
		i := 0
		if len(args) >= 2 && args[0] == "-C" {
			i = 2
		}
		if i < len(args) {
			return "git " + args[i]
		}
		return "git"
	}
	// bash scripts/scan-flavor.sh → "bash scan"; keep other name+arg pairs.
	if name == "bash" && strings.Contains(args[0], "scan-flavor") {
		return "bash scan"
	}
	return name + " " + args[0]
}

func (f *fakeExec) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	s := sig(name, args)
	f.runs = append(f.runs, s+" :: "+strings.Join(args, " "))
	if r, ok := f.runResp[s]; ok {
		return r.out, r.err
	}
	return "", nil
}

func (f *fakeExec) ReadFile(path string) ([]byte, error) {
	if b, ok := f.writes[path]; ok {
		return b, nil
	}
	if b, ok := f.files[path]; ok {
		return b, nil
	}
	return nil, &fakeNotExist{path}
}

func (f *fakeExec) WriteFile(path string, data []byte) error {
	f.writes[path] = append([]byte{}, data...)
	return nil
}

func (f *fakeExec) MkdirTemp() (string, error) { return f.tmpDir, nil }

func (f *fakeExec) RemoveAll(path string) error {
	f.removed = append(f.removed, path)
	return nil
}

type fakeNotExist struct{ path string }

func (e *fakeNotExist) Error() string { return "no such file: " + e.path }

func (f *fakeExec) ranSig(want string) bool {
	for _, r := range f.runs {
		if strings.HasPrefix(r, want+" ::") || r == want {
			return true
		}
	}
	return false
}

// applyDB opens a migrated store and returns it.
func applyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "apply.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedApprovedProposal inserts an approved proposal for agentPath whose
// base_sha256 matches the CURRENT registry content for that agent, and
// registers the agent so the re-read of agent_path resolves.
func seedApprovedProposal(t *testing.T, db *sql.DB, id int64, agent, agentPath, content, diff string) {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	base := hex.EncodeToString(sum[:])
	if _, err := db.Exec(`INSERT INTO agents (id, name, scope, file_path, origin)
		VALUES (?, ?, 'global', ?, 'local')`, id, agent, agentPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_versions (id, agent_id, content_hash, content, created_at)
		VALUES (?, ?, ?, ?, '2026-07-20T00:00:00Z')`, id, id, "h"+agent, content); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agents SET current_version_id = ? WHERE id = ?`, id, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_change_proposals
		(id, agent, agent_path, base_sha256, diff, rationale, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'because evidence', 'approved', '2026-07-20T09:08:07.000Z')`,
		id, agent, agentPath, base, diff); err != nil {
		t.Fatal(err)
	}
}

func applyRow(t *testing.T, db *sql.DB, id int64) (status string, prURL, errCol *string) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT status, pr_url, error FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&status, &prURL, &errCol); err != nil {
		t.Fatal(err)
	}
	return
}

// coreDiff changes only a core agent file → triggers the semver bump path.
const coreAgentPath = "/repo/plugins/core/agents/tech-lead.md"
const coreDiff = "--- a/plugins/core/agents/tech-lead.md\n+++ b/plugins/core/agents/tech-lead.md\n@@ -3 +3 @@\n-old\n+new\n"

// goodFrontmatter is the changed-file content the worktree holds after apply.
const goodFrontmatter = "---\nname: tech-lead\ndescription: orchestrator\n---\nbody\n"

// baseExec wires a fake for the happy core-only path: scan clean, numstat small,
// gh returns a PR url, and the worktree file has valid frontmatter + a bumpable
// plugin.json / marketplace.json.
func baseExec(repo, tmp string) *fakeExec {
	f := newFakeExec()
	f.tmpDir = tmp
	f.runResp["bash scan"] = struct {
		out string
		err error
	}{out: "── Flavor scan ──\n✓ clean — no project/domain tokens remain\n"}
	// numstat: one file, 1 added 1 deleted (2 changed lines).
	f.runResp["git diff"] = struct {
		out string
		err error
	}{out: "1\t1\tplugins/core/agents/tech-lead.md\n"}
	f.runResp["gh pr"] = struct {
		out string
		err error
	}{out: "https://github.com/atretyak1985/swarmery/pull/999\n"}
	// worktree files the guardrails read.
	f.files[filepath.Join(tmp, "plugins/core/agents/tech-lead.md")] = []byte(goodFrontmatter)
	f.files[filepath.Join(tmp, "plugins/core/.claude-plugin/plugin.json")] =
		[]byte("{\n  \"name\": \"core\",\n  \"version\": \"2.2.0\"\n}\n")
	f.files[filepath.Join(tmp, ".claude-plugin/marketplace.json")] =
		[]byte("{\n  \"metadata\": { \"version\": \"2.2.0\" }\n}\n")
	return f
}

func TestApplyHappyPath(t *testing.T) {
	db := applyDB(t)
	seedApprovedProposal(t, db, 1, "tech-lead", coreAgentPath, "body\n", coreDiff)
	f := baseExec("/repo", "/tmp/wt1")
	svc := &Service{DB: db, Repo: "/repo", Exec: f}

	if err := svc.Apply(context.Background(), 1); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	status, prURL, errCol := applyRow(t, db, 1)
	if status != "applied" {
		t.Fatalf("status = %q (err %v), want applied", status, deref(errCol))
	}
	if prURL == nil || !strings.Contains(*prURL, "/pull/999") {
		t.Fatalf("pr_url = %v, want the gh URL", prURL)
	}
	// worktree branch derived from created_at 2026-07-20.
	if !f.ranSig("git worktree") {
		t.Error("no worktree add")
	}
	found := false
	for _, r := range f.runs {
		if strings.Contains(r, "agent-improve/tech-lead-20260720") {
			found = true
		}
	}
	if !found {
		t.Errorf("branch name not derived from created_at; runs=%v", f.runs)
	}
	// Semver bumped in the worktree files.
	pj := f.writes[filepath.Join("/tmp/wt1", "plugins/core/.claude-plugin/plugin.json")]
	if !strings.Contains(string(pj), "2.2.1") {
		t.Errorf("plugin.json not bumped: %s", pj)
	}
	mj := f.writes[filepath.Join("/tmp/wt1", ".claude-plugin/marketplace.json")]
	if !strings.Contains(string(mj), "2.2.1") {
		t.Errorf("marketplace.json not bumped: %s", mj)
	}
	// worktree always removed.
	if len(f.removed) == 0 {
		t.Error("worktree not removed")
	}
}

func TestApplyRequiresApproved(t *testing.T) {
	db := applyDB(t)
	seedApprovedProposal(t, db, 1, "tech-lead", coreAgentPath, "body\n", coreDiff)
	if _, err := db.Exec(`UPDATE agent_change_proposals SET status='proposed' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	f := baseExec("/repo", "/tmp/wt2")
	svc := &Service{DB: db, Repo: "/repo", Exec: f}
	if err := svc.Apply(context.Background(), 1); err == nil {
		t.Fatal("Apply on a non-approved proposal must error")
	}
	if f.ranSig("git fetch") {
		t.Error("must not touch git for a non-approved proposal")
	}
}

func TestApplyShaMismatch(t *testing.T) {
	db := applyDB(t)
	seedApprovedProposal(t, db, 1, "tech-lead", coreAgentPath, "body\n", coreDiff)
	// Registry content drifted since the proposal → base_sha256 no longer matches.
	if _, err := db.Exec(`UPDATE agent_versions SET content='DIFFERENT' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	f := baseExec("/repo", "/tmp/wt3")
	svc := &Service{DB: db, Repo: "/repo", Exec: f}
	if err := svc.Apply(context.Background(), 1); err != nil {
		t.Fatalf("Apply returns nil (outcome on the row): %v", err)
	}
	status, _, errCol := applyRow(t, db, 1)
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if errCol == nil || !strings.Contains(*errCol, "agent changed since proposal") {
		t.Errorf("error = %v, want the sha-mismatch message", deref(errCol))
	}
	if f.ranSig("git fetch") {
		t.Error("sha mismatch must fail before any git op")
	}
}

func TestApplyGuardrailFailures(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*fakeExec)
		wantGate string
	}{
		{
			name: "flavor",
			mutate: func(f *fakeExec) {
				f.runResp["bash scan"] = struct {
					out string
					err error
				}{out: "files: 2   occurrences: 3   (target: 0)\n"}
			},
			wantGate: "scan-flavor",
		},
		{
			name: "frontmatter",
			mutate: func(f *fakeExec) {
				f.files[filepath.Join("/tmp/wtg", "plugins/core/agents/tech-lead.md")] =
					[]byte("no frontmatter here\n")
			},
			wantGate: "frontmatter",
		},
		{
			name: "numstat",
			mutate: func(f *fakeExec) {
				f.runResp["git diff"] = struct {
					out string
					err error
				}{out: "80\t60\tplugins/core/agents/tech-lead.md\n"}
			},
			wantGate: "changed lines",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := applyDB(t)
			seedApprovedProposal(t, db, 1, "tech-lead", coreAgentPath, "body\n", coreDiff)
			f := baseExec("/repo", "/tmp/wtg")
			c.mutate(f)
			svc := &Service{DB: db, Repo: "/repo", Exec: f}
			if err := svc.Apply(context.Background(), 1); err != nil {
				t.Fatalf("Apply returns nil on guardrail failure: %v", err)
			}
			status, _, errCol := applyRow(t, db, 1)
			if status != "failed" {
				t.Fatalf("status = %q, want failed", status)
			}
			if errCol == nil || !strings.Contains(*errCol, c.wantGate) {
				t.Errorf("error = %v, want gate %q named", deref(errCol), c.wantGate)
			}
			if len(f.removed) == 0 {
				t.Error("worktree not pruned on guardrail failure")
			}
			// No PR on a guardrail failure.
			if f.ranSig("gh pr") {
				t.Error("gh pr create ran despite a failed guardrail")
			}
		})
	}
}

// gh missing/unauthenticated leaves the proposal approved + stores the error,
// so the dashboard can re-run Apply.
func TestApplyGhErrorStaysApproved(t *testing.T) {
	db := applyDB(t)
	seedApprovedProposal(t, db, 1, "tech-lead", coreAgentPath, "body\n", coreDiff)
	f := baseExec("/repo", "/tmp/wt4")
	f.runResp["gh pr"] = struct {
		out string
		err error
	}{out: "gh: command not found", err: &fakeNotExist{"gh"}}
	svc := &Service{DB: db, Repo: "/repo", Exec: f}
	if err := svc.Apply(context.Background(), 1); err != nil {
		t.Fatalf("Apply returns nil (outcome on row): %v", err)
	}
	status, prURL, errCol := applyRow(t, db, 1)
	if status != "approved" {
		t.Fatalf("status = %q, want approved (idempotent re-run)", status)
	}
	if prURL != nil {
		t.Errorf("pr_url set despite gh failure: %v", prURL)
	}
	if errCol == nil || *errCol == "" {
		t.Error("gh failure did not store an error")
	}
	if len(f.removed) == 0 {
		t.Error("worktree not removed after gh failure")
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
