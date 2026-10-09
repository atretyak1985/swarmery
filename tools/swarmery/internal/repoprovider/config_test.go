package repoprovider

import (
	"os"
	"path/filepath"
	"testing"
)

func writeClaudeFile(t *testing.T, project, name, body string) {
	t.Helper()
	dir := filepath.Join(project, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigMergesLocalPerKey(t *testing.T) {
	p := t.TempDir()
	writeClaudeFile(t, p, "project.json", `{"AGENT_PROJECT":"x","vcs":{"provider":"auto","baseBranch":"main","forkRemote":"fork","allowPushToBase":false}}`)
	writeClaudeFile(t, p, "settings.local.json", `{"enabledPlugins":{},"swarmery":{"vcs":{"provider":"gitlab","allowPushToBase":true}}}`)
	got := LoadConfig(p)
	want := Config{Provider: "gitlab", BaseBranch: "main", ForkRemote: "fork", AllowPushToBase: true}
	if got != want {
		t.Fatalf("LoadConfig = %+v, want %+v", got, want)
	}
	if got.ExplicitKind() != KindGitLab {
		t.Fatalf("ExplicitKind = %q", got.ExplicitKind())
	}
}

func TestLoadConfigLocalFalseOverridesProjectTrue(t *testing.T) {
	p := t.TempDir()
	writeClaudeFile(t, p, "project.json", `{"vcs":{"allowPushToBase":true,"provider":"github"}}`)
	writeClaudeFile(t, p, "settings.local.json", `{"swarmery":{"vcs":{"allowPushToBase":false}}}`)
	got := LoadConfig(p)
	if got.AllowPushToBase || got.Provider != "github" {
		t.Fatalf("LoadConfig = %+v", got)
	}
}

func TestLoadConfigMissingAndMalformed(t *testing.T) {
	if got := LoadConfig(t.TempDir()); got != (Config{}) {
		t.Fatalf("empty project = %+v", got)
	}
	p := t.TempDir()
	writeClaudeFile(t, p, "project.json", `{"vcs":"not-an-object"}`)
	writeClaudeFile(t, p, "settings.local.json", `{not json`)
	if got := LoadConfig(p); got != (Config{}) {
		t.Fatalf("malformed = %+v", got)
	}
	q := t.TempDir()
	writeClaudeFile(t, q, "project.json", `{"vcs":{"baseBranch":" develop "}}`)
	writeClaudeFile(t, q, "settings.local.json", `{"swarmery":{"vcs":[1,2]}}`)
	if got := LoadConfig(q); got != (Config{BaseBranch: "develop"}) {
		t.Fatalf("bad local vcs = %+v", got)
	}
	r := t.TempDir()
	writeClaudeFile(t, r, "settings.local.json", `{"swarmery":{}}`)
	if got := LoadConfig(r); got != (Config{}) {
		t.Fatalf("no local vcs = %+v", got)
	}
}

func TestExplicitKind(t *testing.T) {
	for in, want := range map[string]Kind{"github": KindGitHub, "GITLAB": KindGitLab, "auto": "", "": "", "bitbucket": ""} {
		if got := (Config{Provider: in}).ExplicitKind(); got != want {
			t.Errorf("ExplicitKind(%q) = %q, want %q", in, got, want)
		}
	}
}
