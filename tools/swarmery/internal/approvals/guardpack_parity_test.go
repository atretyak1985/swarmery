package approvals

// Parity between the daemon's built-in prod-deploy list and guard-pack's copy
// of it (plugins/guard-pack/hooks/patterns/prod.json), plus the verbatim
// mirror of that file the pack's tests carry (plugins/guard-pack/hooks/
// testkit.ts — `claude plugin test` gives a test no file system, so the JSON
// is pasted there). TestProdDeployDefaultsMatchHookList already pins core's
// prod-deploy-patterns.txt to the same Go list, so all four copies agree
// through DefaultProdDeployPatterns. Like that test, the files live outside
// this Go module and are read from the repository checkout (repoRoot).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	guardPackProdRel    = "plugins/guard-pack/hooks/patterns/prod.json"
	guardPackTestkitRel = "plugins/guard-pack/hooks/testkit.ts"
)

func TestGuardPackProdPatternsParity(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, guardPackProdRel))
	if err != nil {
		t.Fatalf("read %s: %v", guardPackProdRel, err)
	}
	var pack []string
	if err := json.Unmarshal(raw, &pack); err != nil {
		t.Fatalf("%s is not a JSON array of strings: %v", guardPackProdRel, err)
	}
	if !reflect.DeepEqual(pack, DefaultProdDeployPatterns) {
		t.Fatalf("guard-pack's prod-deploy patterns drifted — edit both lists together (same content, same order)\n"+
			"  %s:\n    %s\n  approvals.DefaultProdDeployPatterns (prodguard.go):\n    %s",
			guardPackProdRel, strings.Join(pack, "\n    "), strings.Join(DefaultProdDeployPatterns, "\n    "))
	}
}

func TestGuardPackTestkitProdMirrorParity(t *testing.T) {
	root := repoRoot(t)
	assertTestkitMirror(t, root, guardPackTestkitRel, "hooks/patterns/prod.json", guardPackProdRel)
}

// assertTestkitMirror decodes the String.raw literal between testkit.ts's
// `// BEGIN mirror: <name>` and `// END mirror: <name>` markers and fails
// unless it equals the JSON file at fileRel. Both sides are decoded, so
// whitespace never counts as drift; content and order do.
func assertTestkitMirror(t *testing.T, root, testkitRel, name, fileRel string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, testkitRel))
	if err != nil {
		t.Fatalf("read %s: %v", testkitRel, err)
	}
	body := mirrorLiteral(t, string(src), testkitRel, name)
	var mirror, file any
	if err := json.Unmarshal([]byte(body), &mirror); err != nil {
		t.Fatalf("%s: mirror of %s is not JSON: %v", testkitRel, name, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, fileRel))
	if err != nil {
		t.Fatalf("read %s: %v", fileRel, err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("%s is not JSON: %v", fileRel, err)
	}
	if !reflect.DeepEqual(mirror, file) {
		t.Fatalf("%s's mirror of %s drifted from the file — paste the file's JSON between the markers\n"+
			"  mirror:\n%s\n  file:\n%s", testkitRel, name, body, raw)
	}
}

// mirrorLiteral returns the text of the one String.raw template literal in a
// marked region. A `${` inside it would be interpolated by TypeScript, so the
// literal would no longer be the file's bytes — that fails too.
func mirrorLiteral(t *testing.T, src, rel, name string) string {
	t.Helper()
	begin, end := "// BEGIN mirror: "+name, "// END mirror: "+name
	i, j := strings.Index(src, begin), strings.Index(src, end)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("%s: markers %q … %q not found in order", rel, begin, end)
	}
	region := src[i+len(begin) : j]
	const open = "String.raw`"
	o := strings.Index(region, open)
	c := strings.LastIndex(region, "`")
	if o < 0 || c < o+len(open) {
		t.Fatalf("%s: no String.raw`…` literal between the %s markers", rel, name)
	}
	body := region[o+len(open) : c]
	if strings.Contains(body, "`") || strings.Contains(body, "${") {
		t.Fatalf("%s: the %s mirror holds a backtick or ${ — String.raw would not reproduce the file", rel, name)
	}
	return body
}
