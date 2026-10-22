package claudeflags_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
)

// TestLiveArgvDump is the argv source of scripts/tests/live-headless-defaults.sh,
// and is skipped unless that script sets SWARMERY_LIVE_ARGV_DIR. It drives the
// REAL planner and verifier runners against a `claude` stub that records the
// argv they build, so the live check replays exactly the permission flags the
// daemon would pass — not a hand-copied list that could drift from the code.
//
// Inputs (env): SWARMERY_LIVE_ARGV_DIR (output dir), SWARMERY_LIVE_REPO (the
// scratch repo, the runs' cwd), SWARMERY_LIVE_WS (the scratch workspace root),
// SWARMERY_LIVE_DOC (a phase doc carrying a Verify block). Outputs:
// planner.argv and verifier.argv, one argument per line.
func TestLiveArgvDump(t *testing.T) {
	out := os.Getenv("SWARMERY_LIVE_ARGV_DIR")
	if out == "" {
		t.Skip("set by scripts/tests/live-headless-defaults.sh only")
	}
	repo, ws, docPath := os.Getenv("SWARMERY_LIVE_REPO"), os.Getenv("SWARMERY_LIVE_WS"), os.Getenv("SWARMERY_LIVE_DOC")
	for _, k := range []string{claudeflags.ModeEnv, "SWARMERY_PLANNING_PERMISSION_MODE", "SWARMERY_VERIFY_PERMISSION_MODE", claudeflags.AgentDenyEnv} {
		t.Setenv(k, "")
	}
	stub := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n: > \"$ARGV_FILE\"\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> \"$ARGV_FILE\"; done\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", stub)

	t.Setenv("ARGV_FILE", filepath.Join(out, "planner.argv"))
	if _, err := (planning.ClaudeRunner{Timeout: 30 * time.Second}).Start(context.Background(), planning.RunSpec{
		Prompt: "p", SessionUUID: "live-plan", Cwd: repo, WritableDirs: []string{ws},
	}); err != nil {
		t.Fatalf("planner spawn: %v", err)
	}

	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	cmds, ok := verify.ParseVerifyCommands(string(doc))
	if !ok {
		t.Fatalf("no usable Verify block in %s", docPath)
	}
	t.Setenv("ARGV_FILE", filepath.Join(out, "verifier.argv"))
	// The same two lists verify.Service.VerifyTarget builds for a target whose
	// contract declares its checks.
	if _, err := (verify.ClaudeRunner{Timeout: 30 * time.Second}).Run(context.Background(), verify.RunSpec{
		Prompt: "p", SessionUUID: "live-verify", Cwd: repo,
		AllowedTools:    verify.VerifierAllowList(cmds, repo, repo),
		DisallowedTools: verify.DaemonDenyPatterns(7794),
	}); err != nil {
		t.Fatalf("verifier spawn: %v", err)
	}
}
