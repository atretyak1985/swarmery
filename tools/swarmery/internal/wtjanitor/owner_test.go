package wtjanitor

import (
	"os"
	"path/filepath"
	"testing"
)

// The janitor may only ever touch the two kinds of worktree doc.go names: the
// harness's isolated-subagent checkouts and the daemon's own task checkouts.
// An operator's feature worktree is neither, wherever it lives — on
// 2026-09-28 one under <repo>/.claude/worktrees was removed five times as
// "redundant" because its branch was pushed.
func TestAgentOwned(t *testing.T) {
	const repo = "/repo"
	const daemon = "/home/u/.swarmery/worktrees"
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"harness subagent worktree", "/repo/.claude/worktrees/agent-a5522fbe9cf1e1c0c", true},
		{"daemon task worktree", "/home/u/.swarmery/worktrees/swarmery/plan-308", true},
		{"operator feature worktree under .claude/worktrees", "/repo/.claude/worktrees/account-switch-estate", false},
		{"agent- name whose id is not hex", "/repo/.claude/worktrees/agent-notes", false},
		{"bare agent- name", "/repo/.claude/worktrees/agent-", false},
		{"agent- name outside .claude/worktrees", "/repo/agent-a1b2", false},
		{"agent- dir of another repository", "/other/.claude/worktrees/agent-a1b2", false},
		{"a directory below an agent worktree", "/repo/.claude/worktrees/agent-a1b2/sub", false},
		{"the daemon root itself", "/home/u/.swarmery/worktrees", false},
		{"a sibling sharing the daemon root's prefix", "/home/u/.swarmery/worktrees-old/x", false},
		{"a worktree anywhere else", "/home/u/src/scratch", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentOwned(repo, daemon, c.path); got != c.want {
				t.Errorf("agentOwned(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// With no daemon root known, only the harness rule applies: a path that merely
// looks like a daemon checkout is not owned.
func TestAgentOwned_NoDaemonRootOwnsOnlyHarnessWorktrees(t *testing.T) {
	if agentOwned("/repo", "", "/home/u/.swarmery/worktrees/swarmery/plan-308") {
		t.Error("a daemon-shaped path was owned with no daemon root configured")
	}
	if !agentOwned("/repo", "", "/repo/.claude/worktrees/agent-ab12") {
		t.Error("a harness worktree was not owned with no daemon root configured")
	}
}

// git prints the path it stored, which on macOS is often the resolved form
// (/private/var/…) of a repository the database knows by its symlinked form
// (/var/…). Ownership must survive that.
func TestAgentOwned_ResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	agentDir := filepath.Join(real, ".claude", "worktrees", "agent-ab12")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if !agentOwned(link, "", agentDir) {
		t.Errorf("agentOwned(%q via %q) = false, want true", agentDir, link)
	}
	if !agentOwned(real, "", filepath.Join(link, ".claude", "worktrees", "agent-ab12")) {
		t.Error("the symlinked spelling of an agent worktree was not owned")
	}
}
