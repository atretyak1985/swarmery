package mcpcfg

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadConfiguredAllScopesAndDisables(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	root := filepath.Join(home, "work", "repo")
	other := filepath.Join(home, "work", "other")
	writeFile(t, filepath.Join(home, ".claude.json"), `{
	  "mcpServers": {"auggie": {"command": "auggie"}, "muted": {"command": "x"}},
	  "projects": {
	    "`+root+`": {
	      "mcpServers": {"playwright": {"command": "npx"}},
	      "disabledMcpServers": ["muted"],
	      "disabledMcpjsonServers": ["off-json"]
	    },
	    "`+other+`": {"mcpServers": {"not-mine": {"command": "y"}}}
	  }
	}`)
	writeFile(t, filepath.Join(root, ".mcp.json"),
		`{"mcpServers": {"repo-tools": {"command": "z"}, "off-json": {"command": "w"}}}`)

	got := ReadConfigured(claudeDir, root)
	want := []ConfiguredServer{
		{Name: "auggie", Scope: ScopeUser, Source: filepath.Join(home, ".claude.json")},
		{Name: "playwright", Scope: ScopeLocal, Source: filepath.Join(home, ".claude.json")},
		{Name: "repo-tools", Scope: ScopeProject, Source: filepath.Join(root, ".mcp.json")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadConfigured =\n%+v\nwant\n%+v", got, want)
	}
}

func TestReadConfiguredToleratesMissingAndMalformed(t *testing.T) {
	home := t.TempDir()
	if got := ReadConfigured(filepath.Join(home, ".claude"), filepath.Join(home, "repo")); len(got) != 0 {
		t.Fatalf("no files → %v, want empty", got)
	}
	writeFile(t, filepath.Join(home, ".claude.json"), `{not json`)
	writeFile(t, filepath.Join(home, "repo", ".mcp.json"), `[]`)
	if got := ReadConfigured(filepath.Join(home, ".claude"), filepath.Join(home, "repo")); len(got) != 0 {
		t.Fatalf("malformed files → %v, want empty", got)
	}
}

func TestGlobalConfigPathFollowsTheCLILayout(t *testing.T) {
	home := t.TempDir()
	stock := filepath.Join(home, ".claude")
	// A stray in-dir file must not shadow the stock sibling.
	writeFile(t, filepath.Join(stock, ".claude.json"), `{}`)
	if got, want := GlobalConfigPath(stock), filepath.Join(home, ".claude.json"); got != want {
		t.Fatalf("stock dir: %s, want %s", got, want)
	}
	relocated := filepath.Join(home, ".claude-acct-2")
	if got, want := GlobalConfigPath(relocated), filepath.Join(relocated, ".claude.json"); got != want {
		t.Fatalf("relocated dir: %s, want %s", got, want)
	}
}

func TestToolPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"playwright":             "mcp__playwright__",
		"claude-in-chrome":       "mcp__claude-in-chrome__",
		"claude.ai Atlassian":    "mcp__claude_ai_Atlassian__",
		"plugin:lsp-pack:serena": "mcp__plugin_lsp-pack_serena__",
		"ünï":                    "mcp___n___",
	} {
		if got := ToolPrefix(in); got != want {
			t.Errorf("ToolPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
