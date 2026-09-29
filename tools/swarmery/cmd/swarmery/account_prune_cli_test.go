package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/accountprune"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct/accttest"
)

// pruneFixture is an admitted estate "est" at <base>/estate with its own two
// files, an untracked-in-no-repo sub-repo copy, and a git repository whose
// COMMITTED settings.json carries a redundant key — the refusal's proof, since
// the live machine has no eligible tracked file.
func pruneFixture(t *testing.T) (root, tracked string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SWARMERY_SECRETS_DIR", filepath.Join(base, "secrets"))
	root = filepath.Join(base, "estate")
	put := func(rel, body string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	put(".claude/settings.json", `{"pluginConfigs":{"a@m":{"options":{"k":"v"}}},"extraKnownMarketplaces":{"mk":{"source":{"source":"github","repo":"o/r"}}}}`)
	put(".claude/settings.local.json", `{"swarmery":{"claudeAccount":"default","estate":"est"}}`)
	put("plain/.claude/settings.local.json", `{"pluginConfigs":{"a@m":{"options":{"k":"v"}}},"enabledPlugins":{"a@m":true}}`)
	put("other/.claude/settings.json", `{"extraKnownMarketplaces":{"foreign":{"source":{"source":"github","repo":"x/y"}}}}`)
	put("perms/.claude/settings.json", `{"permissions":{"allow":[]}}`)
	tracked = put("repo/.claude/settings.json", `{"pluginConfigs":{"a@m":{"options":{"k":"v"}}}}`)
	gitCLI(t, filepath.Join(root, "repo"), "init", "-q", ".")
	gitCLI(t, filepath.Join(root, "repo"), "add", "-f", ".claude/settings.json")
	gitCLI(t, filepath.Join(root, "repo"), "commit", "-q", "-m", "fixture")
	accttest.AdmitEstate(t, "est", root)
	return root, tracked
}

// The dry run's golden text: one line per listed file, each ending in its git
// status, only the eligible ones carrying the word; the permissions-only file
// is not listed. A dry run with an eligible TRACKED file already refuses.
func TestAccountPruneDryRunGolden(t *testing.T) {
	root, tracked := pruneFixture(t)
	var out, errOut bytes.Buffer
	err := accountPrune([]string{"--path", root, "--dry-run", "--include-tracked"}, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(out.String(), root, "<root>")
	got = regexp.MustCompile(` +`).ReplaceAllString(got, " ")
	want := strings.Join([]string{
		"skip <root>/.claude/settings.json estate source NO-REPO",
		"skip <root>/.claude/settings.local.json estate source NO-REPO",
		"skip <root>/other/.claude/settings.json nothing redundant (keep extraKnownMarketplaces[foreign]) NO-REPO",
		"eligible <root>/plain/.claude/settings.local.json remove pluginConfigs NO-REPO",
		"eligible <root>/repo/.claude/settings.json remove pluginConfigs TRACKED",
	}, "\n") + "\n"
	if got != want {
		t.Errorf("dry run:\n%s\nwant:\n%s", got, want)
	}
	if errOut.String() != "2 files would change\n" {
		t.Errorf("stderr = %q", errOut.String())
	}
	if !strings.Contains(read(t, tracked), "pluginConfigs") {
		t.Error("a dry run wrote")
	}

	// Without --include-tracked even the dry run refuses (exit 1, not usage).
	out.Reset()
	err = accountPrune([]string{"--path", root, "--dry-run"}, &out, &errOut)
	if !accountprune.IsTrackedRefusal(err) || isUsage(err) {
		t.Errorf("dry run err = %v, want the tracked refusal", err)
	}
}

// THE REFUSAL, on a git init fixture: without --include-tracked the run fails
// (main exits 1 for a non-usage error) naming the tracked path and writes
// NOTHING — not even the untracked eligible file; with it, both are written
// and a second run changes zero files.
func TestAccountPruneRefusesTracked(t *testing.T) {
	root, tracked := pruneFixture(t)
	plain := filepath.Join(root, "plain", ".claude", "settings.local.json")
	before := map[string]string{tracked: read(t, tracked), plain: read(t, plain)}

	var out, errOut bytes.Buffer
	err := accountPrune([]string{"--path", root}, &out, &errOut)
	if err == nil || isUsage(err) || !accountprune.IsTrackedRefusal(err) || !strings.Contains(err.Error(), tracked) {
		t.Fatalf("err = %v, want a non-usage refusal naming %s", err, tracked)
	}
	for p, b := range before {
		if read(t, p) != b {
			t.Errorf("%s written despite the refusal", p)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".swarmery")); !os.IsNotExist(err) {
		t.Error("the refusal created a quarantine")
	}

	out.Reset()
	if err := accountPrune([]string{"--path", root, "--include-tracked"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "2 files changed\n") || strings.Count(out.String(), "pre-image ") != 2 {
		t.Errorf("apply output:\n%s", out.String())
	}
	for p := range before {
		if strings.Contains(read(t, p), "pluginConfigs") {
			t.Errorf("%s still carries pluginConfigs", p)
		}
	}
	if !strings.Contains(read(t, plain), "enabledPlugins") {
		t.Error("enabledPlugins was pruned")
	}

	out.Reset()
	if err := accountPrune([]string{"--path", root}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "0 files changed\n") {
		t.Errorf("second run:\n%s", out.String())
	}
}

// --json carries every target with its status and reason; --help is a usage
// error whose text names each flag on its own line.
func TestAccountPruneJSONAndUsage(t *testing.T) {
	root, _ := pruneFixture(t)
	var out, errOut bytes.Buffer
	if err := accountPrune([]string{"--path", root, "--dry-run", "--json", "--include-tracked"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Targets []accountprune.Target `json:"targets"`
		Result  accountprune.Result   `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	sources := 0
	for _, tg := range doc.Targets {
		if tg.Reason == accountprune.ReasonEstateSource {
			sources++
		}
		if tg.Status == "" {
			t.Errorf("%s has no status", tg.Path)
		}
	}
	if sources != 2 || len(doc.Targets) != 5 || !doc.Result.DryRun || len(doc.Result.Changed) != 2 {
		t.Errorf("json = %+v", doc)
	}

	err := accountPrune([]string{"--help"}, &out, &errOut)
	if !isUsage(err) {
		t.Fatalf("--help = %v, want a usage error", err)
	}
	n := 0
	for _, l := range strings.Split(err.Error(), "\n") {
		if strings.Contains(l, "--include-tracked") || strings.Contains(l, "--dry-run") {
			n++
		}
	}
	if n < 2 {
		t.Errorf("usage names the flags on %d line(s)", n)
	}
	if err := accountPrune([]string{"stray"}, &out, &errOut); !isUsage(err) {
		t.Errorf("stray arg = %v", err)
	}
	if err := accountPrune([]string{"--path", t.TempDir()}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "no estate") {
		t.Errorf("estate-less path = %v", err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
