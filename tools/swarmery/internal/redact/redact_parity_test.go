package redact

// Parity between the daemon's response-layer redaction (redactRules) and
// guard-pack's port of it (plugins/guard-pack/hooks/patterns/secrets.json),
// plus the verbatim mirror of that file the pack's tests carry
// (plugins/guard-pack/hooks/testkit.ts — `claude plugin test` gives a test no
// file system). The files live outside this Go module and are read from the
// repository checkout.
//
// The port rule, checked both ways and in order:
//   - same rules, same order, same names (the <name> in [REDACTED:<name>]);
//   - JSON `pattern` is the Go pattern with a leading `(?i)` stripped, and
//     JSON `flags` is "i" exactly when the Go pattern had that `(?i)`;
//   - JSON `replace` ($1 = first capture group, {mark} = the mark, default
//     "{mark}") keeps the same capture groups as the Go replacement.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	guardPackSecretsRel = "plugins/guard-pack/hooks/patterns/secrets.json"
	guardPackTestkitRel = "plugins/guard-pack/hooks/testkit.ts"
)

type guardPackSecretRule struct {
	Name    string  `json:"name"`
	Pattern string  `json:"pattern"`
	Flags   *string `json:"flags"`
	Replace *string `json:"replace"`
}

// guardPackRepoRoot walks up from the package dir to the repository root,
// identified by .claude-plugin/marketplace.json, and skips — loudly — only
// when the module is built outside the repository.
func guardPackRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "marketplace.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("repository root (.claude-plugin/marketplace.json) not found above the package dir — " +
				"out-of-tree build, guard-pack redaction parity cannot be checked")
		}
		dir = parent
	}
}

func readGuardPackSecrets(t *testing.T, root string) []guardPackSecretRule {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, guardPackSecretsRel))
	if err != nil {
		t.Fatalf("read %s: %v", guardPackSecretsRel, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var rules []guardPackSecretRule
	if err := dec.Decode(&rules); err != nil {
		t.Fatalf("%s: %v", guardPackSecretsRel, err)
	}
	return rules
}

func TestGuardPackSecretsParity(t *testing.T) {
	pack := readGuardPackSecrets(t, guardPackRepoRoot(t))

	goNames := make([]string, len(redactRules))
	for i, r := range redactRules {
		goNames[i] = r.name
	}
	packNames := make([]string, len(pack))
	for i, r := range pack {
		packNames[i] = r.Name
	}
	if !reflect.DeepEqual(packNames, goNames) {
		t.Fatalf("rule names drifted — edit both lists together (same rules, same order)\n"+
			"  %s: %v\n  redactRules (redact.go): %v", guardPackSecretsRel, packNames, goNames)
	}

	seen := map[string]bool{}
	for i, g := range redactRules {
		p := pack[i]
		if g.name == "" || seen[g.name] {
			t.Errorf("redactRules[%d]: name %q is empty or repeated", i, g.name)
		}
		seen[g.name] = true

		goPat := g.re.String()
		wantPat, wantFlags := goPat, ""
		if strings.HasPrefix(goPat, "(?i)") {
			wantPat, wantFlags = strings.TrimPrefix(goPat, "(?i)"), "i"
		}
		if p.Pattern != wantPat {
			t.Errorf("%s: pattern drifted\n  json: %s\n  go:   %s (leading (?i) stripped)", g.name, p.Pattern, wantPat)
		}
		gotFlags := ""
		if p.Flags != nil {
			gotFlags = *p.Flags
		}
		if gotFlags != wantFlags {
			t.Errorf("%s: flags = %q, want %q (Go pattern %s)", g.name, gotFlags, wantFlags, goPat)
		}

		tmpl := "{mark}"
		if p.Replace != nil {
			tmpl = *p.Replace
		}
		if got := strings.NewReplacer("$1", "${1}", "{mark}", redactedMark).Replace(tmpl); got != g.repl {
			t.Errorf("%s: replace %q expands to %q, Go replacement is %q", g.name, tmpl, got, g.repl)
		}
	}
}

func TestGuardPackTestkitSecretsMirrorParity(t *testing.T) {
	root := guardPackRepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, guardPackTestkitRel))
	if err != nil {
		t.Fatalf("read %s: %v", guardPackTestkitRel, err)
	}
	const name = "hooks/patterns/secrets.json"
	begin, end := "// BEGIN mirror: "+name, "// END mirror: "+name
	s := string(src)
	i, j := strings.Index(s, begin), strings.Index(s, end)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("%s: markers %q … %q not found in order", guardPackTestkitRel, begin, end)
	}
	region := s[i+len(begin) : j]
	const open = "String.raw`"
	o, c := strings.Index(region, open), strings.LastIndex(region, "`")
	if o < 0 || c < o+len(open) {
		t.Fatalf("%s: no String.raw`…` literal between the %s markers", guardPackTestkitRel, name)
	}
	body := region[o+len(open) : c]
	if strings.Contains(body, "`") || strings.Contains(body, "${") {
		t.Fatalf("%s: the %s mirror holds a backtick or ${ — String.raw would not reproduce the file", guardPackTestkitRel, name)
	}

	var mirror, file any
	if err := json.Unmarshal([]byte(body), &mirror); err != nil {
		t.Fatalf("%s: mirror of %s is not JSON: %v", guardPackTestkitRel, name, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, guardPackSecretsRel))
	if err != nil {
		t.Fatalf("read %s: %v", guardPackSecretsRel, err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("%s is not JSON: %v", guardPackSecretsRel, err)
	}
	if !reflect.DeepEqual(mirror, file) {
		t.Fatalf("%s's mirror of %s drifted from the file — paste the file's JSON between the markers\n"+
			"  mirror:\n%s\n  file:\n%s", guardPackTestkitRel, name, body, raw)
	}
}
