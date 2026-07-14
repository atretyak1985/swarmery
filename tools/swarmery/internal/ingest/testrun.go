package ingest

// Test-run detection: a Bash tool call that invokes a recognised test runner
// also emits a `test_run` event carrying parsed passed/failed/skipped counts.
// This is the only source of the "Quality" aggregate — Claude Code JSONL has
// no native test_run record, so we recognise the command and parse its output.
// Best-effort by design: counts are 0 (parsed=false) when a runner's summary
// line can't be matched, but the run is still recorded with its ok/error status.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// testRunnerRe matches a Bash command that invokes a test runner. Word
// boundaries keep "go test"/"pytest"/"npm test"/"npx vitest" in while keeping
// "latest"/"contest"/"attest" out.
var testRunnerRe = regexp.MustCompile(`(?i)(\bgo\s+test\b|\bgotestsum\b|\bpytest\b|\bpy\.test\b|\b(?:npm|pnpm|yarn|bun)\s+(?:run\s+)?test\b|\bnpx\s+(?:jest|vitest)\b|\bjest\b|\bvitest\b|\bcargo\s+test\b|\bdotnet\s+test\b|\brspec\b|\bphpunit\b|\b(?:mvn|gradlew?|gradle)\b[^&|;]*\btest\b)`)

func isTestCommand(cmd string) bool {
	return testRunnerRe.MatchString(cmd)
}

// testFramework labels the run for the payload (display only).
func testFramework(cmd string) string {
	lc := strings.ToLower(cmd)
	switch {
	case strings.Contains(lc, "pytest"), strings.Contains(lc, "py.test"):
		return "pytest"
	case strings.Contains(lc, "vitest"):
		return "vitest"
	case strings.Contains(lc, "jest"):
		return "jest"
	// cargo BEFORE go: "cargo test" contains the substring "go test".
	case strings.Contains(lc, "cargo test"):
		return "cargo"
	case strings.Contains(lc, "go test"), strings.Contains(lc, "gotestsum"):
		return "go"
	case strings.Contains(lc, "rspec"):
		return "rspec"
	case strings.Contains(lc, "phpunit"):
		return "phpunit"
	case strings.Contains(lc, "dotnet test"):
		return "dotnet"
	default:
		return "unknown"
	}
}

var (
	// "212 passed" / "3 skipped" / "2 failed" — pytest, jest, vitest summaries.
	passedRe  = regexp.MustCompile(`(?i)(\d+)\s+passed`)
	failedRe  = regexp.MustCompile(`(?i)(\d+)\s+failed`)
	skippedRe = regexp.MustCompile(`(?i)(\d+)\s+skipped`)
	// go test -v verdict lines (fallback when there is no numeric summary).
	goPassRe = regexp.MustCompile(`(?m)^\s*--- PASS:`)
	goFailRe = regexp.MustCompile(`(?m)^\s*--- FAIL:`)
	goSkipRe = regexp.MustCompile(`(?m)^\s*--- SKIP:`)
)

func lastNum(re *regexp.Regexp, s string) (int, bool) {
	m := re.FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseTestCounts extracts passed/failed/skipped from test output. It first
// tries the "<n> passed/failed/skipped" summaries (pytest/jest/vitest); if none
// are present it counts go test -v verdict lines. parsed=false when neither
// signal is found (all counts are then zero).
func parseTestCounts(text string) (passed, failed, skipped int, parsed bool) {
	p, okP := lastNum(passedRe, text)
	f, okF := lastNum(failedRe, text)
	s, okS := lastNum(skippedRe, text)
	if okP || okF || okS {
		return p, f, s, true
	}
	gp := len(goPassRe.FindAllStringIndex(text, -1))
	gf := len(goFailRe.FindAllStringIndex(text, -1))
	gs := len(goSkipRe.FindAllStringIndex(text, -1))
	if gp+gf+gs > 0 {
		return gp, gf, gs, true
	}
	return 0, 0, 0, false
}

// testResultText flattens a tool_result into searchable text. Bash results are
// usually a {stdout, stderr} object or a plain string; anything else yields "".
func testResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		var b strings.Builder
		for _, k := range []string{"stdout", "stderr", "output", "content", "result"} {
			if s, ok := t[k].(string); ok {
				b.WriteString(s)
				b.WriteByte('\n')
			}
		}
		return b.String()
	default:
		return ""
	}
}
