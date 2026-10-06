package approvals

// Parity between the daemon's built-in prod-deploy list and the PreToolUse
// hook's canonical list (plugins/core/hooks/lib/prod-deploy-patterns.txt),
// plus matcher agreement over the shared fixture both sides consume
// (scripts/tests/fixtures/prod-deploy-patterns/cases.tsv). The files live
// outside this Go module, so go:embed cannot reach them — they are read from
// the repository checkout at test time.

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	hookPatternsRel = "plugins/core/hooks/lib/prod-deploy-patterns.txt"
	casesFixtureRel = "scripts/tests/fixtures/prod-deploy-patterns/cases.tsv"
)

// repoRoot walks up from the test's working directory (the package dir) to the
// repository root, identified by .claude-plugin/marketplace.json. It skips —
// loudly — only when the module is built outside the repository.
func repoRoot(t *testing.T) string {
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
				"out-of-tree build, prod-deploy parity cannot be checked")
		}
		dir = parent
	}
}

// readDataLines returns the file's lines that are neither blank nor comments
// (first non-blank character '#'). trim also trims each kept line.
func readDataLines(t *testing.T, path string, trim bool) []fixtureLine {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []fixtureLine
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trim {
			line = trimmed
		}
		out = append(out, fixtureLine{n: n, text: line})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return out
}

type fixtureLine struct {
	n    int
	text string
}

// decodeCaseCommand applies the fixture's escapes left to right with no
// overlap: `\\` -> `\`, `\n` -> newline, `\t` -> tab; any other backslash is
// literal (mirrors decode() in scripts/tests/prod-deploy-guard.test.sh).
func decodeCaseCommand(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestProdDeployDefaultsMatchHookList(t *testing.T) {
	path := filepath.Join(repoRoot(t), hookPatternsRel)
	var hook []string
	for _, l := range readDataLines(t, path, true) {
		hook = append(hook, l.text)
	}
	if len(hook) == 0 {
		t.Fatalf("%s has no patterns", hookPatternsRel)
	}
	if !reflect.DeepEqual(hook, DefaultProdDeployPatterns) {
		t.Fatalf("prod-deploy pattern lists drifted — edit both files together (same content, same order)\n"+
			"  %s:\n    %s\n  approvals.DefaultProdDeployPatterns (prodguard.go):\n    %s",
			hookPatternsRel, strings.Join(hook, "\n    "), strings.Join(DefaultProdDeployPatterns, "\n    "))
	}
}

func TestProdDeployDefaultsParse(t *testing.T) {
	for _, s := range DefaultProdDeployPatterns {
		if _, err := ParseRulePattern(s); err != nil {
			t.Errorf("default prod-deploy pattern %q does not parse: %v", s, err)
		}
	}
}

func TestProdDeployCasesParity(t *testing.T) {
	path := filepath.Join(repoRoot(t), casesFixtureRel)
	g, err := NewProdGuard(nil)
	if err != nil {
		t.Fatalf("NewProdGuard(nil): %v", err)
	}
	rows := readDataLines(t, path, false)
	if len(rows) == 0 {
		t.Fatalf("%s has no rows", casesFixtureRel)
	}
	for _, r := range rows {
		raw, expect, ok := strings.Cut(r.text, "\t")
		if !ok || strings.Contains(expect, "\t") || (expect != "ask" && expect != "none") {
			t.Errorf("%s:%d: malformed row %q (want command<TAB>ask|none)", casesFixtureRel, r.n, r.text)
			continue
		}
		cmd := decodeCaseCommand(raw)
		got := g.Match("", "Bash", bashInput(cmd))
		if got != (expect == "ask") {
			t.Errorf("%s:%d: command %q: Go matcher says match=%v, fixture expects %s",
				casesFixtureRel, r.n, cmd, got, expect)
		}
	}
	t.Logf("checked %d fixture rows", len(rows))
}
