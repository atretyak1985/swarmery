package improve

import (
	"os"
	"testing"
)

// TestMain is defense-in-depth against any OTHER test in this package reaching
// a real claude binary through claudebin.Resolve's home-relative candidates
// (~/.local/bin/claude, ~/.claude/local/claude, …). It does NOT close the
// machine-wide gap (/opt/homebrew/bin, /usr/local/bin) — those sit in
// claudebin's own private search list, unreachable from here — so the
// resolveClaudeBin seam in runner.go is what makes TestClaudeRunnerMissingBinary
// itself fully hermetic; this TestMain only protects tests that forget to stub
// PATH (or resolveClaudeBin) at all.
//
// runner_account_test.go's own t.Setenv("HOME", …) calls still work unchanged:
// they override/restore HOME per-test, composing fine with this baseline.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "improve-test-home-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	os.Setenv("HOME", dir)
	os.Unsetenv("SWARMERY_CLAUDE_BIN")
	os.Exit(m.Run())
}
