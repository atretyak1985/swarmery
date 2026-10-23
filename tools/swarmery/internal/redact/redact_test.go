package redact

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
)

func TestAllMasksEveryClass(t *testing.T) {
	for _, s := range redacttest.Samples() {
		got := All("x " + s.Value + " y")
		if strings.Contains(got, s.Needle) {
			t.Errorf("%s: All left the secret part in place", s.Class)
		}
		if !strings.Contains(got, Mark) {
			t.Errorf("%s: All left no mark", s.Class)
		}
		if c := Count(s.Value); c[s.Class] == 0 {
			t.Errorf("%s: Count = %v, want the class counted", s.Class, c)
		}
		if again := All(got); again != got {
			t.Errorf("%s: All is not idempotent", s.Class)
		}
		if c := Count(got); len(c) != 0 {
			t.Errorf("%s: Count after All = %v, want none", s.Class, c)
		}
	}
}

func TestLiteralSkipsContextualClasses(t *testing.T) {
	for _, s := range redacttest.Samples() {
		got := Literal(s.Value)
		contextual := s.Class == "bearer" || s.Class == "generic_key_value"
		if contextual && got != s.Value {
			t.Errorf("%s: Literal changed a contextual-only value", s.Class)
		}
		if !contextual && strings.Contains(got, s.Needle) {
			t.Errorf("%s: Literal left a literal token shape", s.Class)
		}
	}
	code := redacttest.SourceAssignment()
	if got := Literal(code); got != code {
		t.Errorf("Literal mangled source code: %q → %q", code, got)
	}
	if got := All(code); got == code {
		t.Errorf("All left the assignment alone — the contextual class is expected to match it")
	}
}

// The API's response redaction keeps exactly the 11 classes: a PEM block is
// a storage-only class.
func TestResponseKeepsTheElevenClasses(t *testing.T) {
	for _, s := range redacttest.Samples() {
		got := Response(s.Value)
		if s.Class == "private_key" {
			if got != s.Value {
				t.Errorf("Response masked a PEM block — response semantics must not change")
			}
			continue
		}
		if strings.Contains(got, s.Needle) {
			t.Errorf("%s: Response left the secret part in place", s.Class)
		}
	}
}

func TestPrivateKeyCutShortIsMaskedToTheEnd(t *testing.T) {
	pem := redacttest.Samples()[11].Value
	cut := pem[:len(pem)/2]
	got := All("head " + cut)
	if strings.Contains(got, redacttest.Samples()[11].Needle[:20]) {
		t.Errorf("a truncated key block kept its body: %q", got)
	}
	if !strings.HasPrefix(got, "head -----BEGIN ") {
		t.Errorf("text before the block changed: %q", got)
	}
}

func TestRuleNamesOrder(t *testing.T) {
	names := RuleNames()
	if len(names) != 11 {
		t.Fatalf("RuleNames = %d, want 11", len(names))
	}
	if names[0] != "anthropic_key" || names[10] != "url_userinfo" {
		t.Errorf("RuleNames order drifted: %v", names)
	}
	if len(literalRules) != 9 {
		t.Errorf("literalRules = %d, want 9 (classes 1–8 and 11)", len(literalRules))
	}
	want := []string{"private_key", "openai_key", "stripe_key", "auth_header", "cli_secret"}
	if c := CountClasses(); len(c) != 16 || strings.Join(c[11:], ",") != strings.Join(want, ",") {
		t.Errorf("CountClasses = %v, want the 11 classes then %v", c, want)
	}
}
