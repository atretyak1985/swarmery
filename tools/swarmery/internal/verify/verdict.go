package verify

import "strings"

// Verdict is the parsed outcome of a verifier session's final text.
type Verdict string

const (
	VerdictPass         Verdict = "pass"
	VerdictFail         Verdict = "fail"
	VerdictInconclusive Verdict = "inconclusive"
)

// verdictReasonsCap bounds how much reason text we persist (verify_detail is
// <=4KB in the schema; we truncate before storage).
const verdictReasonsCap = 4096

// ParseVerdict extracts the verdict + reason bullets from a verifier session's
// final assistant text, porting Fusion's FN-8004 rules (merger-ai-prompts.ts
// 43-159) with verification's fail-safe:
//
//   - Scan BACKWARD from the end for a line whose leading token (after markdown
//     emphasis wrappers) is `VERDICT:` followed by PASS | FAIL | INCONCLUSIVE.
//   - Track markdown code-fence depth so a `VERDICT:` line INSIDE a ``` fence
//     (e.g. echoed diff/test output) can never shadow the real conclusion.
//   - Collect reason bullets on BOTH sides of the verdict line (models put
//     reasons above OR below the verdict).
//   - Missing / garbled / unrecognized verdict token → INCONCLUSIVE. This is the
//     verification fail-safe and is the OPPOSITE of the merge reviewer's
//     bias-to-reject: an ambiguous parse must NOT be read as FAIL, because FAIL
//     spawns a fix task and parser ambiguity is not evidence the work is wrong
//     (DESIGN.md §4.6).
//
// The returned reasons string is already truncated to verdictReasonsCap.
func ParseVerdict(text string) (Verdict, string) {
	lines := strings.Split(text, "\n")

	// First pass: compute, for every line, whether it sits inside a code fence.
	// A fence toggles on a line whose trimmed content starts with ``` (``` or
	// ~~~). The fence-delimiter line itself is "inside" for our purpose (we never
	// want to read a verdict from a ``` line anyway).
	inFence := make([]bool, len(lines))
	fenceOpen := false
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		isFenceDelim := strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
		if isFenceDelim {
			inFence[i] = true
			fenceOpen = !fenceOpen
			continue
		}
		inFence[i] = fenceOpen
	}

	// Backward scan for the verdict line, skipping any inside a fence.
	verdictIdx := -1
	var verdict Verdict
	for i := len(lines) - 1; i >= 0; i-- {
		if inFence[i] {
			continue
		}
		if v, ok := verdictOnLine(lines[i]); ok {
			verdict = v
			verdictIdx = i
			break
		}
	}

	if verdictIdx == -1 {
		// No recognizable verdict line anywhere outside a fence → fail-safe.
		return VerdictInconclusive, truncate(collectReasons(lines, inFence, -1), verdictReasonsCap)
	}
	return verdict, truncate(collectReasons(lines, inFence, verdictIdx), verdictReasonsCap)
}

// verdictOnLine reports whether a single line IS a verdict line and, if so, its
// value. It tolerates leading markdown emphasis (`*`, `-`, `#`, `>`, backticks,
// spaces) before the `VERDICT:` token and matches case-insensitively. A line
// like "VERDICT: PASS" or "**VERDICT: FAIL**" or "> verdict: inconclusive"
// matches; prose merely mentioning the word "verdict" does not (the token must
// lead the trimmed line and be immediately followed by ':').
func verdictOnLine(raw string) (Verdict, bool) {
	line := strings.TrimSpace(raw)
	// Strip common markdown emphasis wrappers from both ends so
	// "**VERDICT: PASS**" reduces to "VERDICT: PASS".
	line = strings.Trim(line, "*_`> \t#-")
	upper := strings.ToUpper(line)
	if !strings.HasPrefix(upper, "VERDICT:") {
		return "", false
	}
	rest := strings.TrimSpace(upper[len("VERDICT:"):])
	// The value is the first whitespace-delimited token after the colon (so
	// "VERDICT: FAIL — reasons" still reads FAIL).
	if sp := strings.IndexAny(rest, " \t—-"); sp >= 0 {
		rest = rest[:sp]
	}
	switch rest {
	case "PASS":
		return VerdictPass, true
	case "FAIL":
		return VerdictFail, true
	case "INCONCLUSIVE":
		return VerdictInconclusive, true
	default:
		// A "VERDICT:" line with a garbled/empty value is itself ambiguous →
		// treat as NOT a verdict line so the backward scan keeps looking; if none
		// is found the caller falls back to INCONCLUSIVE.
		return "", false
	}
}

// collectReasons gathers bullet-style reason lines on both sides of the verdict
// line (or across the whole text when verdictIdx < 0), skipping fenced lines and
// the verdict line itself. A "bullet" is a line whose trimmed form starts with
// -, *, • or a "n." enumerator; if no bullets are found at all we fall back to
// the non-empty, non-fenced prose lines (bounded) so a verdict without bulleted
// reasons still carries context.
func collectReasons(lines []string, inFence []bool, verdictIdx int) string {
	var bullets []string
	var prose []string
	for i, raw := range lines {
		if i == verdictIdx || inFence[i] {
			continue
		}
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if isBullet(t) {
			bullets = append(bullets, t)
		} else {
			prose = append(prose, t)
		}
	}
	if len(bullets) > 0 {
		return strings.Join(bullets, "\n")
	}
	return strings.Join(prose, "\n")
}

// isBullet reports whether a trimmed line looks like a reason bullet.
func isBullet(t string) bool {
	if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "• ") {
		return true
	}
	// enumerator "1." / "2)" etc.
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	return i > 0 && i < len(t) && (t[i] == '.' || t[i] == ')')
}

// truncate caps s at n bytes (trimmed), keeping the TAIL when over-long — the
// conclusion/reasons nearest the verdict are the most useful to retain.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
