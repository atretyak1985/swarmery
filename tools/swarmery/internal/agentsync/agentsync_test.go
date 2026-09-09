package agentsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upstreamAgent mirrors the shape of a real pack agent closely enough to prove
// "exactly one key changed": several keys before and after isolation, a nested
// block whose own source_sha must NOT be mistaken for the top-level one, and a
// body with blank lines and markdown.
const upstreamAgent = `---
name: implementation-agent
description: Execute code changes as a leaf executor.
model: opus
effort: high
isolation: worktree
skills:
  - code-standards
  - code-search
docs:
  status: draft
  source_sha: cbf1cd62868f
  updated: 2026-09-01
---

# Role

You implement approved plans.

| Input | Mode |
|---|---|
| step_file | Leaf |
`

const marketplaceName = "swarmery"

// fixture builds a throwaway machine: a marketplace clone holding one pack with
// one agent, and a project directory. Both are returned as absolute paths.
func fixture(t *testing.T, agents map[string]string) (claudeDir, projectDir, packRoot string) {
	t.Helper()
	root := t.TempDir()
	claudeDir = filepath.Join(root, "claude")
	projectDir = filepath.Join(root, "project")
	mktRoot := filepath.Join(claudeDir, "plugins", "marketplaces", marketplaceName)
	packRoot = filepath.Join(mktRoot, "plugins", "core")

	mustMkdir(t, filepath.Join(mktRoot, ".claude-plugin"))
	mustWrite(t, filepath.Join(mktRoot, ".claude-plugin", "marketplace.json"), `{
	  "metadata": {"version": "3.0.0"},
	  "plugins": [
	    {"name": "core", "description": "core", "source": "./plugins/core"},
	    {"name": "web-pack", "description": "web", "source": "./plugins/web-pack"}
	  ]
	}`)
	mustMkdir(t, filepath.Join(packRoot, "agents"))
	for name, body := range agents {
		mustWrite(t, filepath.Join(packRoot, "agents", name+".md"), body)
	}
	mustMkdir(t, projectDir)
	return claudeDir, projectDir, packRoot
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSettings(t *testing.T, projectDir, body string) {
	t.Helper()
	mustWrite(t, filepath.Join(projectDir, ".claude", "settings.json"), body)
}

func fixedNow() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) }

func opts(claudeDir, projectDir string) Options {
	return Options{
		ProjectDir: projectDir, ClaudeDir: claudeDir,
		Marketplace: marketplaceName, Now: fixedNow,
	}
}

// (а) Generation changes exactly the isolation key and nothing else.
func TestRenderChangesOnlyIsolation(t *testing.T) {
	claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": upstreamAgent})
	writeSettings(t, projectDir, `{"swarmery":{"agents":{"implementation-agent":{"isolation":"none"}}}}`)

	plans, err := Plans(opts(claudeDir, projectDir))
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1", len(plans))
	}
	got := string(plans[0].Content)

	upFront, upBody, err := splitFrontmatter([]byte(upstreamAgent))
	if err != nil {
		t.Fatal(err)
	}
	genFront, genBody, err := splitFrontmatter([]byte(got))
	if err != nil {
		t.Fatal(err)
	}

	// The provenance block is the only addition allowed in the frontmatter.
	stamped := []string{
		keyGeneratedBy + ": " + generatorID,
		keySource + ": plugins/core/agents/implementation-agent.md",
		keySourceSHA + ": " + ShortSHA([]byte(upstreamAgent)),
		keyGeneratedAt + ": 2026-09-07",
	}
	if len(genFront) != len(upFront)+len(stamped) {
		t.Fatalf("frontmatter grew by %d lines, want %d\n%q", len(genFront)-len(upFront), len(stamped), genFront)
	}
	for i, want := range stamped {
		if got := genFront[len(upFront)+i]; got != want {
			t.Errorf("stamp line %d = %q, want %q", i, got, want)
		}
	}
	for i := range upFront {
		if upFront[i] == "isolation: worktree" {
			if genFront[i] != "isolation: none" {
				t.Errorf("isolation line = %q, want %q", genFront[i], "isolation: none")
			}
			continue
		}
		if genFront[i] != upFront[i] {
			t.Errorf("frontmatter line %d changed: %q -> %q", i, upFront[i], genFront[i])
		}
	}
	// The nested docs.source_sha must survive untouched: a top-level lookup that
	// leaked into nested keys would have rewritten it.
	if !strings.Contains(got, "  source_sha: cbf1cd62868f") {
		t.Error("nested docs.source_sha was rewritten")
	}
	// The body is the upstream body verbatim, behind exactly one warning line.
	// upstreamAgent's body already opens with a blank line, so the separator is
	// a single newline — the generated file must not grow a second blank line.
	wantBody := warningLine("plugins/core/agents/implementation-agent.md") + "\n" + upBody
	if !strings.HasPrefix(upBody, "\n") {
		t.Fatal("fixture no longer opens its body with a blank line; the assertion below is testing nothing")
	}
	if genBody != wantBody {
		t.Errorf("body changed beyond the warning line:\ngot  %q\nwant %q", genBody, wantBody)
	}
	if n := strings.Count(got, "ЗГЕНЕРОВАНО"); n != 1 {
		t.Errorf("warning marker appears %d times, want 1", n)
	}
}

// (б) An unknown isolation value is an explained error, never a silent no-op.
func TestInvalidIsolationIsExplainedError(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		want     []string
	}{
		{
			name:     "unknown value",
			settings: `{"swarmery":{"agents":{"implementation-agent":{"isolation":"sandbox"}}}}`,
			want:     []string{"isolation", `"sandbox"`, "none", "worktree"},
		},
		{
			name:     "empty value",
			settings: `{"swarmery":{"agents":{"implementation-agent":{"isolation":""}}}}`,
			want:     []string{"isolation", "none", "worktree"},
		},
		{
			name:     "no isolation key",
			settings: `{"swarmery":{"agents":{"implementation-agent":{}}}}`,
			want:     []string{"implementation-agent", "isolation"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": upstreamAgent})
			writeSettings(t, projectDir, tc.settings)

			_, err := Plans(opts(claudeDir, projectDir))
			if err == nil {
				t.Fatal("Plans succeeded, want an error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not explain %q", err, want)
				}
			}
			if _, statErr := os.Stat(OverridePath(projectDir, "implementation-agent")); statErr == nil {
				t.Error("a file was generated despite the invalid override")
			}
		})
	}
}

// (в) --check exits 1 when source_sha drifted and 0 when it matches.
func TestCheckExitCodeOnSHADrift(t *testing.T) {
	claudeDir, projectDir, packRoot := fixture(t, map[string]string{"implementation-agent": upstreamAgent})
	writeSettings(t, projectDir, `{"swarmery":{"agents":{"implementation-agent":{"isolation":"none"}}}}`)

	plans, err := Plans(opts(claudeDir, projectDir))
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if err := Apply(plans); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	args := []string{"sync", "--project", projectDir, "--check", "--claude-dir", claudeDir, "--marketplace", marketplaceName}
	if code := Cmd(args); code != 0 {
		t.Fatalf("check right after sync exited %d, want 0", code)
	}
	if drifts, err := Check(opts(claudeDir, projectDir)); err != nil || len(drifts) != 0 {
		t.Fatalf("Check right after sync: %v %v", drifts, err)
	}

	// Upstream moves on.
	mustWrite(t, filepath.Join(packRoot, "agents", "implementation-agent.md"),
		upstreamAgent+"\nA new upstream paragraph.\n")

	if code := Cmd(args); code != 1 {
		t.Fatalf("check after upstream drift exited %d, want 1", code)
	}
	drifts, err := Check(opts(claudeDir, projectDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(drifts) != 1 || !strings.Contains(drifts[0].Reason, "upstream moved on") {
		t.Fatalf("drifts = %+v, want one 'upstream moved on'", drifts)
	}

	// Regenerating clears it.
	if code := Cmd([]string{"sync", "--project", projectDir, "--claude-dir", claudeDir, "--marketplace", marketplaceName}); code != 0 {
		t.Fatalf("sync exited %d, want 0", code)
	}
	if code := Cmd(args); code != 0 {
		t.Fatalf("check after regeneration exited %d, want 0", code)
	}
}

// (г) No swarmery.agents block generates nothing at all — the state every other
// project on the machine is in.
func TestNoOverridesGeneratesNoFile(t *testing.T) {
	cases := map[string]string{
		"no settings.json at all": "",
		"no swarmery block":       `{"env":{"AGENT_PROJECT":"x"},"permissions":{"allow":[]}}`,
		"no agents block":         `{"swarmery":{"claudeAccount":"default"}}`,
		"empty agents block":      `{"swarmery":{"claudeAccount":"default","agents":{}}}`,
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": upstreamAgent})
			if settings != "" {
				writeSettings(t, projectDir, settings)
			}
			plans, err := Plans(opts(claudeDir, projectDir))
			if err != nil {
				t.Fatalf("Plans: %v", err)
			}
			if len(plans) != 0 {
				t.Fatalf("got %d plans, want 0", len(plans))
			}
			if err := Apply(plans); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(projectDir, ".claude", "agents")); !os.IsNotExist(err) {
				t.Errorf(".claude/agents was created: %v", err)
			}
			code := Cmd([]string{"sync", "--project", projectDir, "--claude-dir", claudeDir, "--marketplace", marketplaceName})
			if code != 0 {
				t.Errorf("sync exited %d, want 0", code)
			}
			if _, err := os.Stat(OverridePath(projectDir, "implementation-agent")); !os.IsNotExist(err) {
				t.Errorf("an override file was generated: %v", err)
			}
		})
	}
}

// A second sync on an unchanged upstream must not rewrite the file — otherwise
// every project's diff churns daily on generated_at alone.
func TestSyncIsIdempotentAcrossDays(t *testing.T) {
	claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": upstreamAgent})
	writeSettings(t, projectDir, `{"swarmery":{"agents":{"implementation-agent":{"isolation":"none"}}}}`)

	first := opts(claudeDir, projectDir)
	plans, err := Plans(first)
	if err != nil {
		t.Fatal(err)
	}
	if plans[0].Action != ActionCreate {
		t.Fatalf("first action = %s, want %s", plans[0].Action, ActionCreate)
	}
	if err := Apply(plans); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(plans[0].Path)
	if err != nil {
		t.Fatal(err)
	}

	later := first
	later.Now = func() time.Time { return fixedNow().AddDate(0, 0, 40) }
	plans2, err := Plans(later)
	if err != nil {
		t.Fatal(err)
	}
	if plans2[0].Action != ActionUnchanged {
		t.Fatalf("second action = %s, want %s", plans2[0].Action, ActionUnchanged)
	}
	if err := Apply(plans2); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(plans[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("an unchanged upstream rewrote the generated file")
	}
}

// A handwritten file in .claude/agents/ is a defect the ADR asks --check to
// name, not a fork to tolerate silently.
func TestCheckFlagsHandwrittenFork(t *testing.T) {
	claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": upstreamAgent})
	writeSettings(t, projectDir, `{"swarmery":{"agents":{"implementation-agent":{"isolation":"none"}}}}`)
	mustWrite(t, OverridePath(projectDir, "implementation-agent"),
		strings.Replace(upstreamAgent, "isolation: worktree", "isolation: none", 1))

	drifts, err := Check(opts(claudeDir, projectDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(drifts) != 1 || !strings.Contains(drifts[0].Reason, "handwritten fork") {
		t.Fatalf("drifts = %+v, want one 'handwritten fork'", drifts)
	}
	code := Cmd([]string{"sync", "--project", projectDir, "--check", "--claude-dir", claudeDir, "--marketplace", marketplaceName})
	if code != 1 {
		t.Fatalf("check exited %d, want 1", code)
	}
}

// Declaring an override before generating it is drift too: the missing file is
// exactly the state a fresh clone of the pilot project is in.
func TestCheckFlagsMissingGeneratedFile(t *testing.T) {
	claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": upstreamAgent})
	writeSettings(t, projectDir, `{"swarmery":{"agents":{"implementation-agent":{"isolation":"none"}}}}`)

	drifts, err := Check(opts(claudeDir, projectDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(drifts) != 1 || !strings.Contains(drifts[0].Reason, "not generated yet") {
		t.Fatalf("drifts = %+v, want one 'not generated yet'", drifts)
	}
}

// An agent named in settings.json but shipped by no installed pack must fail
// loudly: silently skipping it would leave the project believing it is narrowed.
func TestUnknownAgentIsAnError(t *testing.T) {
	claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": upstreamAgent})
	writeSettings(t, projectDir, `{"swarmery":{"agents":{"ghost-agent":{"isolation":"none"}}}}`)

	_, err := Plans(opts(claudeDir, projectDir))
	if err == nil || !strings.Contains(err.Error(), "ghost-agent") {
		t.Fatalf("err = %v, want one naming ghost-agent", err)
	}
	if code := Cmd([]string{"sync", "--project", projectDir, "--claude-dir", claudeDir, "--marketplace", marketplaceName}); code != 2 {
		t.Fatalf("sync exited %d, want 2", code)
	}
}

// An upstream whose frontmatter `name:` disagrees with its filename would
// generate an override that overrides nothing — Claude Code keys on `name:`.
func TestUpstreamNameMismatchIsAnError(t *testing.T) {
	renamed := strings.Replace(upstreamAgent, "name: implementation-agent", "name: something-else", 1)
	claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": renamed})
	writeSettings(t, projectDir, `{"swarmery":{"agents":{"implementation-agent":{"isolation":"none"}}}}`)

	_, err := Plans(opts(claudeDir, projectDir))
	if err == nil || !strings.Contains(err.Error(), "something-else") {
		t.Fatalf("err = %v, want one naming the mismatched frontmatter name", err)
	}
}

// An upstream that never declared isolation still has to end up narrowed: the
// key is appended rather than silently skipped.
func TestIsolationAppendedWhenUpstreamOmitsIt(t *testing.T) {
	bare := strings.Replace(upstreamAgent, "isolation: worktree\n", "", 1)
	claudeDir, projectDir, _ := fixture(t, map[string]string{"implementation-agent": bare})
	writeSettings(t, projectDir, `{"swarmery":{"agents":{"implementation-agent":{"isolation":"worktree"}}}}`)

	plans, err := Plans(opts(claudeDir, projectDir))
	if err != nil {
		t.Fatal(err)
	}
	front, _, err := splitFrontmatter(plans[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := lookupKey(front, isolationKey); !ok || got != "worktree" {
		t.Fatalf("isolation = %q (present=%v), want worktree", got, ok)
	}
}
