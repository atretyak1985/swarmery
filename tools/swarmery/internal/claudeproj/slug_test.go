package claudeproj

import "testing"

// TestSlug pins the encoder that the auto-memory readers and the worktree
// helpers all route through. The shapes below are the ones that actually occur
// on a daemon-driven machine; the ground truth behind them (39 real slug
// directories re-derived from the cwd their own transcripts record) lives in
// internal/worktree's TestProjectSlugMatchesRealClaudeProjectDirs.
func TestSlug(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"repo checkout", "/Volumes/Work/swarmery", "-Volumes-Work-swarmery"},
		{"dot-directory doubles the dash", "/Users/dev/.local/src/acme", "-Users-dev--local-src-acme"},
		{
			"daemon worktree of a plan run",
			"/Users/dev/.swarmery/worktrees/-Volumes-Work-swarmery/plan-530",
			"-Users-dev--swarmery-worktrees--Volumes-Work-swarmery-plan-530",
		},
		{"hyphenated project name", "/Volumes/Work/english-grammar", "-Volumes-Work-english-grammar"},
		{"root", "/", "-"},
		{"trailing separator is cleaned away", "/Volumes/Work/swarmery/", "-Volumes-Work-swarmery"},
		{"empty stays empty", "", ""},
		// Documented pass-through: only '/' and '.' are rewritten, because they
		// are the only rewrites any observed directory name shows. Do not
		// "improve" these without a real slug directory that disagrees.
		{"spaces pass through", "/Users/dev/My Projects/acme", "-Users-dev-My Projects-acme"},
		{"underscores pass through", "/Users/dev/src/my_app", "-Users-dev-src-my_app"},
		{"non-ASCII passes through", "/Users/dev/проєкти/акме", "-Users-dev-проєкти-акме"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Slug(tc.path); got != tc.want {
				t.Fatalf("Slug(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
