package accountprune

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// secretSurface is claudeacct's credential-store API. The prune has no reason
// to touch any of it: it compares settings files and nothing else.
var secretSurface = map[string]bool{
	"SecretEnvFor":        true,
	"SecretEnvForAccount": true,
	"SecretEnvForStore":   true,
	"SecretsDir":          true,
	"SecretsPath":         true,
	"StoreStatus":         true,
	"CredentialCount":     true,
	"SpawnEnvResolved":    true,
}

// assignment is what a leaked `NAME=value` line looks like.
var assignment = regexp.MustCompile(`^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*=`)

// SC-11 must not become a secret-printing path: the package references no
// part of the credential-store API, and nothing it renders — text or JSON —
// carries an assignment line or any settings VALUE.
func TestNoSecretOutput(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "claudeacct" && secretSurface[sel.Sel.Name] {
					t.Errorf("%s: references claudeacct.%s (the credential-store surface)", fset.Position(sel.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}

	// A fixture whose settings VALUES are sentinels, some shaped like
	// assignments; a store in the estate holding a credential sentinel.
	const sentinel = "zzq-prune-sentinel"
	fx := newEstate(t)
	store := filepath.Join(os.Getenv("SWARMERY_SECRETS_DIR"), "est.env")
	fh, err := os.OpenFile(store, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("TOKEN=" + sentinel + "-cred\n"); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	write(t, filepath.Join(fx.root, "a", ".claude", "settings.json"),
		`{"pluginConfigs":{"b@m":{"options":{}}},"env":{"TOKEN":"`+sentinel+`-env"}}`)
	write(t, filepath.Join(fx.root, "b", ".claude", "settings.json"),
		`{"pluginConfigs":{"a@m":{"options":{"k":"KEY=`+sentinel+`-val"}}}}`)

	targets := mustPlan(t, fx.root)
	var out bytes.Buffer
	if err := RenderTargets(&out, targets); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(targets, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := RenderResult(&out, res); err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(struct {
		T []Target
		R Result
	}{targets, res})
	out.Write(append(js, '\n'))

	for _, line := range strings.Split(out.String(), "\n") {
		if assignment.MatchString(line) {
			t.Errorf("assignment-shaped line: %q", line)
		}
	}
	if strings.Contains(out.String(), sentinel) {
		t.Errorf("a settings or credential VALUE reached the output:\n%s", out.String())
	}
}
