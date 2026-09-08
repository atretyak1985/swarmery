package exploration

import "testing"

// TestClassifyBashShapes pins the shell shapes the metric stands on. The two
// that matter most are the edit-by-shell escapes at the bottom: `sed -i` and
// `find … -exec rm` are writers wearing a reader's name, and filing them under
// Explore would inflate the exploration share while hiding edit work.
func TestClassifyBashShapes(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want Kind
	}{
		{"cd hop into grep", "cd tools/swarmery && grep -rn foo internal/", Explore},
		{"env assignment prefix", "FOO=1 rg bar", Explore},
		{"subshell group", "(cat a; cat b)", Explore},
		{"env builtin prefix", "env RG_CONFIG=x rg --files", Explore},
		{"absolute path binary", "/usr/bin/grep -c func main.go", Explore},
		{"sed read-only range", "sed -n '1,20p' internal/api/analytics.go", Explore},
		{"find without exec", "find . -name '*.go'", Explore},
		{"pipeline head", "ls -la tools/ | head -20", Explore},
		{"wc over a glob", "wc -l internal/api/*.go", Explore},
		{"tree", "tree -L 2 internal", Explore},
		{"tail", "tail -n 50 /tmp/daemon.log", Explore},
		{"fd", "fd -e go exploration", Explore},
		{"go test", "go test ./...", Run},
		{"git diff", "git diff --stat", Run},
		{"npm build", "npm run build", Run},
		{"cd hop into a build", "cd tools/swarmery/web && npm run build", Run},
		{"sed in place is an edit", "sed -i '' 's/foo/bar/' internal/api/routes.go", Run},
		{"sed long in-place flag", "sed --in-place 's/foo/bar/' main.go", Run},
		{"sed clustered in-place flag", "sed -ri 's/a/b/' main.go", Run},
		{"find with exec rm", `find . -name '*.go' -exec rm {} \;`, Run},
		{"find with delete", "find . -name '*.tmp' -delete", Run},
		{"bare cd", "cd /tmp", Run},
		{"empty command", "", Run},
		{"make", "make test", Run},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify("Bash", c.cmd); got != c.want {
				t.Errorf("Classify(Bash, %q) = %q, want %q", c.cmd, got, c.want)
			}
		})
	}
}

// TestClassifyToolNames pins the non-Bash tools, including the rule that
// discovery through an MCP server is still discovery.
func TestClassifyToolNames(t *testing.T) {
	cases := []struct {
		tool string
		want Kind
	}{
		{"Read", Explore},
		{"Grep", Explore},
		{"Glob", Explore},
		{"Edit", Edit},
		{"Write", Edit},
		{"MultiEdit", Edit},
		{"NotebookEdit", Edit},
		{"mcp__serena__find_symbol", Explore},
		{"mcp__plugin_lsp-pack_serena__get_symbols_overview", Explore},
		{"mcp__graphify__query", Explore},
		{"mcp__graft__context", Explore},
		{"Task", Other},
		{"TodoWrite", Other},
		{"WebFetch", Other},
		{"", Other},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			if got := Classify(c.tool, ""); got != c.want {
				t.Errorf("Classify(%q, \"\") = %q, want %q", c.tool, got, c.want)
			}
		})
	}
}

// TestClassifyToolNameCasing — transcripts are not guaranteed to preserve the
// documented casing, and a miss would silently reclassify a whole tool as Other.
func TestClassifyToolNameCasing(t *testing.T) {
	if got := Classify(" bash ", "grep -rn foo ."); got != Explore {
		t.Errorf("padded/lowercased Bash = %q, want %q", got, Explore)
	}
	if got := Classify("READ", ""); got != Explore {
		t.Errorf("uppercased Read = %q, want %q", got, Explore)
	}
}

// TestTopKey covers the ranked-list label: Bash is broken out by the command
// it actually ran, everything else keeps its tool name.
func TestTopKey(t *testing.T) {
	cases := []struct {
		tool, cmd, want string
	}{
		{"Bash", "cd x && grep -rn foo", "grep"},
		{"Bash", "/usr/bin/rg foo", "rg"},
		{"Bash", "", "bash"},
		{"Read", "", "Read"},
		{"mcp__serena__find_symbol", "", "mcp__serena__find_symbol"},
		{"", "", "unknown"},
	}
	for _, c := range cases {
		if got := topKey(c.tool, c.cmd); got != c.want {
			t.Errorf("topKey(%q, %q) = %q, want %q", c.tool, c.cmd, got, c.want)
		}
	}
}
