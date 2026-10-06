package approvals

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func bashInput(command string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": command})
	return b
}

func mustGuard(t *testing.T, extra ...string) *ProdGuard {
	t.Helper()
	g, err := NewProdGuard(extra)
	if err != nil {
		t.Fatalf("NewProdGuard(%v): %v", extra, err)
	}
	return g
}

// writeProjectConfig writes <dir>/.claude/project.json and returns dir.
func writeProjectConfig(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "project.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProdGuardDefaults(t *testing.T) {
	g := mustGuard(t)
	cases := []struct {
		command string
		want    bool
	}{
		{"acme-cli deploy --prod", true},
		{"./scripts/deploy.sh production", true},
		{"DEPLOY --PROD", true}, // case-insensitive
		{"npm run release:prod", true},
		{"terraform apply -auto-approve", true},
		{"cd infra && terraform apply -var-file=prod.tfvars", true},
		{"kubectl --context prod-eu apply -f x.yaml", true},
		{"helm upgrade api ./chart -f values-prod.yaml", true},
		{"git push origin main:prod", true},
		// Deliberately NOT matched (operator decision 2026-10-06).
		{"npm ci --production", false},
		{"npm install --omit=dev --production", false},
		{"terraform plan", false},
		{"kubectl get pods -n staging", false},
		{"git push origin main", false},
		{"ls -la", false},
	}
	for _, c := range cases {
		if got := g.Match("", "Bash", bashInput(c.command)); got != c.want {
			t.Errorf("Match(Bash %q) = %v, want %v", c.command, got, c.want)
		}
	}
}

func TestProdGuardDefaultsParse(t *testing.T) {
	for _, s := range DefaultProdDeployPatterns {
		if _, err := ParseRulePattern(s); err != nil {
			t.Errorf("default %q does not parse: %v", s, err)
		}
	}
}

func TestProdGuardExtras(t *testing.T) {
	g := mustGuard(t, SplitPatternList(" Bash(ship-it *) , ,Bash(*promote*live*)")...)
	for _, cmd := range []string{"ship-it now", "SHIP-IT now", "tool promote build-42 live"} {
		if !g.Match("", "Bash", bashInput(cmd)) {
			t.Errorf("extras: %q not matched", cmd)
		}
	}
	if g.Match("", "Bash", bashInput("ship it")) {
		t.Error("extras: 'ship it' matched Bash(ship-it *)")
	}
	// Defaults stay on next to extras.
	if !g.Match("", "Bash", bashInput("acme-cli deploy --prod")) {
		t.Error("defaults lost when extras are set")
	}
	if _, err := NewProdGuard([]string{"*"}); err == nil {
		t.Error("an invalid extra pattern must be a construction error")
	}
}

func TestProdGuardProjectFile(t *testing.T) {
	g := mustGuard(t)
	dir := writeProjectConfig(t, t.TempDir(),
		`{"name":"x","approvals":{"prodDeployPatterns":["Bash(acme-ctl ship*)"]}}`)
	if !g.Match(dir, "Bash", bashInput("acme-ctl ship web")) {
		t.Error("project pattern not matched")
	}
	if g.Match(t.TempDir(), "Bash", bashInput("acme-ctl ship web")) {
		t.Error("project pattern leaked into another project")
	}

	// Edited file is re-read (mtime/size change).
	writeProjectConfig(t, dir, `{"approvals":{"prodDeployPatterns":["Bash(acme-ctl roll-out*)"]}}`)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, ".claude", "project.json"), future, future); err != nil {
		t.Fatal(err)
	}
	if g.Match(dir, "Bash", bashInput("acme-ctl ship web")) {
		t.Error("stale project pattern still matched after the file changed")
	}
	if !g.Match(dir, "Bash", bashInput("acme-ctl roll-out web")) {
		t.Error("new project pattern not matched after the file changed")
	}
}

func TestProdGuardMalformedProjectFileKeepsDefaults(t *testing.T) {
	g := mustGuard(t, "Bash(ship-it *)")
	broken := writeProjectConfig(t, t.TempDir(), `{"approvals":{"prodDeployPatterns":[`)
	// The malformed file itself contributes nothing (and does not panic) …
	if ps := g.projectPatterns(broken); len(ps) != 0 {
		t.Errorf("malformed project file yielded patterns %+v", ps)
	}
	// … while the defaults and extras still apply for that project.
	for _, cmd := range []string{"acme-cli deploy --prod", "ship-it now"} {
		if !g.Match(broken, "Bash", bashInput(cmd)) {
			t.Errorf("malformed project file dropped the guard for %q", cmd)
		}
	}
	if g.Match(broken, "Bash", bashInput("ls -la")) {
		t.Error("malformed project file turned the guard into match-all")
	}
	// An invalid entry is skipped, valid neighbours still apply.
	mixed := writeProjectConfig(t, t.TempDir(),
		`{"approvals":{"prodDeployPatterns":["*", "Bash(acme-ctl ship*)"]}}`)
	if !g.Match(mixed, "Bash", bashInput("acme-ctl ship web")) {
		t.Error("valid project pattern dropped next to an invalid one")
	}
	// Wrong type for the key: malformed, defaults stay.
	wrong := writeProjectConfig(t, t.TempDir(), `{"approvals":{"prodDeployPatterns":"Bash(x)"}}`)
	if ps := g.projectPatterns(wrong); len(ps) != 0 {
		t.Errorf("wrong-typed project key yielded patterns %+v", ps)
	}
	if !g.Match(wrong, "Bash", bashInput("terraform apply")) {
		t.Error("wrong-typed project key dropped the defaults")
	}
}

func TestProdGuardNonBashTool(t *testing.T) {
	g := mustGuard(t)
	for _, tool := range []string{"Write", "Read", "WebFetch", "Task"} {
		in, _ := json.Marshal(map[string]string{
			"command": "deploy prod", "file_path": "/deploy/prod.yaml", "url": "https://deploy.example/prod",
		})
		if g.Match("", tool, in) {
			t.Errorf("Bash patterns matched tool %s", tool)
		}
	}
	// Tool part stays exact: a lower-case tool name is a different tool.
	if g.Match("", "bash", bashInput("deploy prod")) {
		t.Error("tool part matched case-insensitively")
	}
	// A nil guard matches nothing.
	var nilGuard *ProdGuard
	if nilGuard.Match("", "Bash", bashInput("deploy prod")) {
		t.Error("nil guard matched")
	}
}
