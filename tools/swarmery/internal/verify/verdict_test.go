package verify

import (
	"strings"
	"testing"
)

// The parser is the crown jewel (FN-8004). This matrix is exhaustive per the
// phase-6 spec: verdict-at-end, mid-fence decoy, reasons above/below, missing,
// mixed case, multiple verdicts, garbled value. The verification fail-safe
// (garbled/missing → INCONCLUSIVE, the OPPOSITE of the merge reviewer's reject)
// is asserted explicitly.

func TestParseVerdict_Matrix(t *testing.T) {
	cases := []struct {
		name        string
		text        string
		wantVerdict Verdict
		reasonHas   string // substring the reasons must contain ("" = don't check)
	}{
		{
			name:        "verdict at end, reasons above (bullets)",
			text:        "Looked at the diff.\n- criterion 1 met\n- criterion 2 met\nVERDICT: PASS",
			wantVerdict: VerdictPass,
			reasonHas:   "criterion 1 met",
		},
		{
			name:        "reasons below the verdict line",
			text:        "VERDICT: FAIL\n- the endpoint returns 500\n- test TestX fails",
			wantVerdict: VerdictFail,
			reasonHas:   "returns 500",
		},
		{
			name: "mid-fence VERDICT decoy is ignored; real verdict outside wins",
			text: "Here is the test output:\n```\nsome log line\nVERDICT: PASS\nmore log\n```\n" +
				"That PASS above is echoed test text, not my conclusion.\n- criteria not actually met\nVERDICT: FAIL",
			wantVerdict: VerdictFail,
			reasonHas:   "not actually met",
		},
		{
			name:        "only a fenced verdict → no real verdict → inconclusive (fail-safe)",
			text:        "Running tests:\n```\nVERDICT: PASS\n```\n(that was inside a code block)",
			wantVerdict: VerdictInconclusive,
		},
		{
			name:        "missing verdict entirely → inconclusive",
			text:        "I read the code and ran the tests but I'm not summarizing with a verdict line.",
			wantVerdict: VerdictInconclusive,
		},
		{
			name:        "mixed case + emphasis wrappers",
			text:        "- ok\n**Verdict: Pass**",
			wantVerdict: VerdictPass,
		},
		{
			name:        "blockquote prefix + inconclusive",
			text:        "> verdict: inconclusive\nCould not install deps.",
			wantVerdict: VerdictInconclusive,
		},
		{
			name:        "multiple real verdicts → last one (closest to end) wins",
			text:        "VERDICT: PASS\n...reconsidered...\nVERDICT: FAIL",
			wantVerdict: VerdictFail,
		},
		{
			name:        "verdict with trailing reasons on same line reads the token",
			text:        "- missing error handling\nVERDICT: FAIL — see the reasons above",
			wantVerdict: VerdictFail,
			reasonHas:   "missing error handling",
		},
		{
			name:        "garbled verdict value → inconclusive (fail-safe, not fail)",
			text:        "VERDICT: MAYBE\nnot sure",
			wantVerdict: VerdictInconclusive,
		},
		{
			name:        "tilde fence also shields a decoy",
			text:        "~~~\nVERDICT: FAIL\n~~~\nreal answer follows\nVERDICT: PASS",
			wantVerdict: VerdictPass,
		},
		{
			name:        "prose fallback when no bullets present",
			text:        "The feature works as specified and all tests pass.\nVERDICT: PASS",
			wantVerdict: VerdictPass,
			reasonHas:   "works as specified",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reasons := ParseVerdict(tc.text)
			if got != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q\n--- text ---\n%s", got, tc.wantVerdict, tc.text)
			}
			if tc.reasonHas != "" && !strings.Contains(reasons, tc.reasonHas) {
				t.Fatalf("reasons %q missing %q", reasons, tc.reasonHas)
			}
		})
	}
}

func TestParseVerdict_ReasonsTruncated(t *testing.T) {
	big := strings.Repeat("x", verdictReasonsCap+500)
	_, reasons := ParseVerdict(big + "\nVERDICT: PASS")
	if len(reasons) > verdictReasonsCap {
		t.Fatalf("reasons not truncated: len=%d cap=%d", len(reasons), verdictReasonsCap)
	}
}

func TestParseVerdict_EmptyInput(t *testing.T) {
	got, _ := ParseVerdict("")
	if got != VerdictInconclusive {
		t.Fatalf("empty input verdict = %q, want inconclusive", got)
	}
}

func TestIsBullet(t *testing.T) {
	yes := []string{"- a", "* b", "• c", "1. d", "2) e", "10. f"}
	no := []string{"prose", "-nospace", "a. not a number", ""}
	for _, s := range yes {
		if !isBullet(s) {
			t.Errorf("isBullet(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isBullet(s) {
			t.Errorf("isBullet(%q) = true, want false", s)
		}
	}
}
