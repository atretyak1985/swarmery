package credstore

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// redactPatternsFile is the shell side's copy of TokenPatterns, relative to this
// package directory (tools/swarmery/internal/repoprovider/credstore).
var redactPatternsFile = filepath.Join("..", "..", "..", "scripts", "lib", "redact-patterns.txt")

// readRedactPatterns returns the file's patterns: every line that is neither
// blank nor a "#" comment, verbatim.
func readRedactPatterns(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(redactPatternsFile)
	if err != nil {
		t.Fatalf("open %s: %v", redactPatternsFile, err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The shell scrubber (scripts/guard-trap.sh) and Redact must mask the same
// token shapes: a pattern added to one and not the other is a token that leaves
// the machine through the other path.
func TestRedactPatternsFileMatches(t *testing.T) {
	got := readRedactPatterns(t)
	if !reflect.DeepEqual(got, TokenPatterns) {
		t.Fatalf("%s = %q\nTokenPatterns = %q\nedit both together", redactPatternsFile, got, TokenPatterns)
	}
}

// Each pattern has to mean the same thing to `sed -E` as to RE2, so it must
// compile under Go's POSIX ERE restriction too, and mask a token of its shape.
func TestRedactPatternsArePOSIXAndMask(t *testing.T) {
	samples := []string{
		"ghp_" + strings.Repeat("a1", 18),
		"github_pat_" + strings.Repeat("b_", 15),
		"glpat-" + strings.Repeat("c-", 12),
	}
	for i, p := range TokenPatterns {
		re, err := regexp.CompilePOSIX(p)
		if err != nil {
			t.Errorf("pattern %q is not POSIX ERE: %v", p, err)
			continue
		}
		if i < len(samples) && !re.MatchString(samples[i]) {
			t.Errorf("pattern %q does not match a token of its shape", p)
		}
	}
	for _, s := range samples {
		if got := Redact("x " + s + " y"); strings.Contains(got, s) {
			t.Errorf("Redact left %q in place: %q", s, got)
		}
	}
}
