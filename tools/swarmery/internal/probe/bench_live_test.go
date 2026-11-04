package probe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// BenchLiveEnv opts into TestBenchLiveOneTask: a real, token-spending bench
// probe of one task for base and variant. Never set in CI.
const benchLiveEnv = "SWARMERY_PROBE_BENCH_LIVE"

// TestBenchLiveOneTask runs the production bench step (ScriptBenchRunner,
// ClaudeJudge, GitSource at this checkout's HEAD) for a small proposal against
// core's test-writer, whose only tagged task is b05-read-the-docs, and logs the
// rows it recorded. It is the phase-6 "one real run" evidence, kept repeatable:
//
//	SWARMERY_PROBE_BENCH_LIVE=1 go test ./internal/probe -run TestBenchLiveOneTask -v -timeout 60m
func TestBenchLiveOneTask(t *testing.T) {
	if os.Getenv(benchLiveEnv) != "1" {
		t.Skipf("set %s=1 to spend tokens on a real one-task bench probe", benchLiveEnv)
	}
	const target = "plugins/core/agents/test-writer.md"
	root := gitOut(t, ".", "rev-parse", "--show-toplevel")
	base := gitOut(t, root, "show", "HEAD:"+target) + "\n"
	variant := strings.TrimRight(base, "\n") +
		"\n\nBefore you write a test, state in one sentence the behavior it pins and the input that would break it.\n"

	db := openDB(t)
	res, err := db.Exec(`INSERT INTO agent_change_proposals
		(agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES ('test-writer', ?, 'agent', ?, ?, ?, 'live bench probe', 'proposed', ?)`,
		target, target, sha(base), unifiedDiff(t, target, base, variant),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	s := NewService(db, root)
	s.Source = GitSource{Repo: root, Ref: "HEAD"}
	p, err := s.LoadProposal(id)
	if err != nil {
		t.Fatal(err)
	}
	spent, err := s.RunBench(context.Background(), "live-bench", p)
	rows := mustRows(t, db, id)
	out, _ := json.MarshalIndent(rows, "", "  ")
	t.Logf("spent $%.4f (cap $%.2f)\n%s", spent, s.CapUSD(SourceBench), out)
	if err != nil {
		t.Fatalf("RunBench: %v", err)
	}
	if len(rows) != 1 || rows[0].Source != SourceBench || rows[0].TaskID != "b05-read-the-docs" {
		t.Fatalf("want one bench row for b05-read-the-docs, got %+v", rows)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimRight(string(out), "\n")
}

// unifiedDiff is `git diff` of target from base to variant, with a/ b/ paths
// rooted at the repo, as the proposal generator stores it.
func unifiedDiff(t *testing.T, target, base, variant string) string {
	t.Helper()
	dir := t.TempDir()
	for side, body := range map[string]string{"a": base, "b": variant} {
		p := filepath.Join(dir, side, filepath.FromSlash(target))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "diff", "--no-index", "--no-prefix", "a/"+target, "b/"+target)
	cmd.Dir = dir
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		t.Fatalf("git diff --no-index: %v", err)
	}
	return string(out)
}
