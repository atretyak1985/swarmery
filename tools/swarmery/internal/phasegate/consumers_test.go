package phasegate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is tools/swarmery — two levels up from internal/phasegate.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// The point of this file: the defect was never one call site. Completion was
// derived in several places from the same two columns, so patching one would have
// left the others answering the old way. These tests make the sweep enforceable —
// a new completion decision that skips the gate, or an exemption whose reason has
// expired, turns the build red.

// Every consumer that claims to be gated must actually call Check; every one that
// claims an exemption must not.
func TestConsumersMatchTheCode(t *testing.T) {
	root := repoRoot(t)
	for _, c := range Consumers {
		t.Run(c.Path+":"+c.Symbol, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(root, c.Path))
			if err != nil {
				t.Fatalf("listed consumer is unreadable — drop the row or fix the path: %v", err)
			}
			body := funcBody(string(src), c.Symbol)
			if body == "" {
				t.Fatalf("%s is not in %s any more — the row is stale", c.Symbol, c.Path)
			}
			calls := strings.Contains(body, "phasegate.Check(") || strings.Contains(body, "Check(Input{")
			switch {
			case c.Gated && c.Via != "":
				// A forwarding consumer: it must take the verdict through Via, and
				// SOMETHING in its own file must produce that verdict — otherwise the
				// parameter is decorative and nobody computes it.
				if !strings.Contains(body, c.Via) {
					t.Errorf("%s claims to receive the gate through %q, which it never mentions", c.Symbol, c.Via)
				}
				if !strings.Contains(string(src), "phasegate.Check(") && !strings.Contains(string(src), c.Via+"(") {
					t.Errorf("nothing in %s computes %q — %s forwards a verdict no one produces",
						c.Path, c.Via, c.Symbol)
				}
			case c.Gated && !calls:
				t.Errorf("%s claims to be gated but never calls phasegate.Check", c.Symbol)
			case !c.Gated && calls:
				t.Errorf("%s is listed as exempt but calls phasegate.Check — drop the exemption", c.Symbol)
			}
			if !c.Gated && strings.TrimSpace(c.Why) == "" {
				t.Errorf("%s is exempt with no reason recorded", c.Symbol)
			}
		})
	}
}

// A completion decision that is not in Consumers is the bug this list exists to
// prevent. The heuristic is the shape of the old derivation: any file outside
// phasegate that compares a done count against a total, or calls
// phasediag.CriteriaMet, is deciding completion and owes the list a row.
func TestNoUnlistedCompletionDecision(t *testing.T) {
	root := repoRoot(t)
	listed := map[string]bool{}
	for _, c := range Consumers {
		listed[c.Path] = true
	}
	// `done == total` / `done >= total` in any spelling, plus the shared helper.
	suspects := regexp.MustCompile(
		`checkboxes_?[Dd]one\s*[=>]=\s*checkboxes_?[Tt]otal|` +
			`CriteriaDone\s*[=>]=\s*CriteriaTotal|` +
			`phasediag\.CriteriaMet\(`)

	var problems []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, "internal/phasegate") || strings.HasPrefix(rel, "internal/phasediag") {
			return nil // the gate itself and the derivation it wraps
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if suspects.Match(src) && !listed[rel] {
			problems = append(problems, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("%s decides phase completion but is not in phasegate.Consumers — "+
			"route it through Check, or add a row with the reason it is exempt", p)
	}
}

// Every gated consumer must hand the gate the CLOSURE inputs too, or the
// Completion Report and lesson conditions silently do not apply on that path.
// depSatisfied is the one deliberate exception, and it has to say so in the
// code: the closure conditions gate what is RECORDED as complete, not what may
// START, and blocking the DAG on a predecessor's unwritten paperwork is the
// deadlock this plan warns against.
func TestGatedConsumersFeedTheClosureInputs(t *testing.T) {
	root := repoRoot(t)
	// path:symbol → the reason it legitimately passes ClosureRequired: false.
	closureExempt := map[string]string{
		"internal/phaserun/service.go:depSatisfied": "the DAG gate must not stall on a predecessor's paperwork",
	}
	for _, c := range Consumers {
		if !c.Gated || c.Via != "" {
			continue // forwarding consumers are covered by their producer
		}
		key := c.Path + ":" + c.Symbol
		src, err := os.ReadFile(filepath.Join(root, c.Path))
		if err != nil {
			t.Fatalf("%s: %v", c.Path, err)
		}
		body := funcBody(string(src), c.Symbol)
		feeds := strings.Contains(body, "CompletionReport:") && strings.Contains(body, "Ran:")
		opted := strings.Contains(body, "ClosureRequired: false")
		reason, isExempt := closureExempt[key]
		switch {
		case isExempt && !opted:
			t.Errorf("%s is listed as closure-exempt (%s) but does not pass ClosureRequired: false", key, reason)
		case !isExempt && !feeds:
			t.Errorf("%s calls the gate without the closure inputs — the report and lesson "+
				"conditions do not apply on this path, and nothing said so", key)
		}
	}
}

// funcBody extracts the source of a top-level func (method or plain) by name,
// brace-counting from its opening line. Good enough for this sweep and free of a
// go/ast dependency in a leaf package.
func funcBody(src, symbol string) string {
	re := regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?` + regexp.QuoteMeta(symbol) + `\b`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	depth, started := 0, false
	for i := loc[0]; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return src[loc[0] : i+1]
			}
		}
	}
	return src[loc[0]:]
}

// Every gated consumer must hand the gate Dispatched. Its zero value silently
// drops the verdict condition (Input.VerificationOwed), so a path that forgot it
// would read a dispatched, ungraded phase as complete — and unblock its
// dependents — with nothing to say so.
func TestGatedConsumersFeedDispatched(t *testing.T) {
	root := repoRoot(t)
	for _, c := range Consumers {
		if !c.Gated || c.Via != "" {
			continue // forwarding consumers are covered by their producer
		}
		src, err := os.ReadFile(filepath.Join(root, c.Path))
		if err != nil {
			t.Fatalf("%s: %v", c.Path, err)
		}
		if !strings.Contains(funcBody(string(src), c.Symbol), "Dispatched:") {
			t.Errorf("%s:%s calls the gate without Dispatched — a verdict it is owed would be waived", c.Path, c.Symbol)
		}
	}
}
