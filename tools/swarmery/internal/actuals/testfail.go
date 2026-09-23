package actuals

// "Unexpected test failure" (step 12.3): a failing test_run in a file or area
// the phase's forecast did not name — neither among its areas, nor its files,
// nor mentioned by one of its risks. A failure the forecast saw coming is the
// plan working; one it did not is a surprise phase 13 routes attention to.
//
// WHERE a failure happened is read, deterministically, from two places:
//
//  1. the test runner's own output, kept on the parent tool_call event — Go's
//     `FAIL\t<package>`, jest/vitest's `FAIL  <file>`, pytest's
//     `FAILED <file>::<test>`, and `<file>_test.go:<line>:` references. These
//     name where the failure actually is, so when any are present they are the
//     whole answer;
//  2. otherwise the path-like arguments of the command itself
//     (`go test ./internal/store`, `cd web && npx vitest src/x.test.ts`), which
//     name where the failure can be.
//
// A failure with no locus at all (a bare `make test` whose output was not kept)
// is NOT counted as unexpected: nothing places it outside the forecast, and
// calling it a surprise would be a judgement the evidence does not support.

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
)

// forecastScope is the part of a forecast a test failure is judged against.
type forecastScope struct {
	Areas []string
	Files []string
	Risks []string
}

// testRunEvent is one test_run event reduced to what the judgement needs.
type testRunEvent struct {
	Status        string // ok | error | …
	Payload       string // {"command":…, "failed":N, …}
	ParentPayload string // the Bash tool_call: {"input":{…}, "result":…}
}

// testRunPayload is the ingest-written shape (internal/ingest testrun).
type testRunPayload struct {
	Command string `json:"command"`
	Failed  int    `json:"failed"`
}

// failed reports whether the test run failed: a non-ok exit, or a parsed
// failure count above zero (some runners exit 0 on a failure they report).
func (e testRunEvent) failed() (testRunPayload, bool) {
	var p testRunPayload
	_ = json.Unmarshal([]byte(e.Payload), &p) // tolerant: a garbled payload still has a status
	return p, e.Status == "error" || p.Failed > 0
}

// countTestFailures returns (failing test runs, of which unexpected). With a nil
// scope (no forecast) nothing is judged unexpected; the caller stores NULL for
// that column rather than the 0 returned here.
func countTestFailures(evs []testRunEvent, scope *forecastScope) (failures, unexpected int) {
	for _, e := range evs {
		p, bad := e.failed()
		if !bad {
			continue
		}
		failures++
		if scope == nil {
			continue
		}
		loci := failureLoci(p.Command, resultText(e.ParentPayload))
		if len(loci) > 0 && !scope.expects(loci) {
			unexpected++
		}
	}
	return failures, unexpected
}

// resultText pulls the tool output out of a Bash tool_call payload: the result
// is a string (an error) or an object carrying stdout/stderr.
func resultText(payload string) string {
	if payload == "" {
		return ""
	}
	var v struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(payload), &v) != nil || len(v.Result) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Result, &s) == nil {
		return s
	}
	var m map[string]any
	if json.Unmarshal(v.Result, &m) != nil {
		return ""
	}
	var b strings.Builder
	for _, k := range []string{"stdout", "stderr", "output", "content"} {
		if s, ok := m[k].(string); ok {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

var (
	// `FAIL\tgithub.com/x/y/internal/store\t0.1s`, `FAIL  src/a.test.ts > suite`,
	// `--- FAIL: TestX` (no path — filtered by looksLikePath).
	// Horizontal whitespace only: Go ends a failing run with a bare `FAIL` line,
	// and `\s+` would run across the newline and capture the next line's token.
	failLineRe = regexp.MustCompile(`(?m)^[ \t]*(?:---[ \t]*)?FAIL(?:ED)?:?[ \t]+(\S+)`)
	// `    store_test.go:42: want 1, got 2`
	goTestFileRe = regexp.MustCompile(`(?m)(\S+_test\.go):\d+:`)
)

// failureLoci returns the places a failing test run points at. Output loci win
// over command loci — see the file comment.
func failureLoci(command, output string) []string {
	var out []string
	add := func(tok string) {
		if l := normLocus(tok); l != "" && looksLikePath(l) {
			out = append(out, l)
		}
	}
	for _, m := range failLineRe.FindAllStringSubmatch(output, -1) {
		tok := m[1]
		if i := strings.Index(tok, "::"); i > 0 { // pytest node id
			tok = tok[:i]
		}
		add(tok)
	}
	for _, m := range goTestFileRe.FindAllStringSubmatch(output, -1) {
		add(m[1])
	}
	if len(out) > 0 {
		return out
	}
	for _, tok := range strings.FieldsFunc(command, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '&' || r == '|' || r == '(' || r == ')'
	}) {
		// Flags, redirections, env assignments and expansions are not places.
		if strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "/dev/") ||
			strings.ContainsAny(tok, "<>=$*") {
			continue
		}
		add(tok)
	}
	return out
}

// normLocus strips the decoration a path argument carries: quotes, a leading
// `./`, a trailing `/...` (Go's package wildcard) or `/`.
func normLocus(tok string) string {
	t := strings.Trim(tok, `"'`+"`")
	t = strings.TrimSuffix(t, "/...")
	t = strings.TrimSuffix(t, "...")
	for strings.HasPrefix(t, "./") {
		t = t[2:]
	}
	t = strings.TrimSuffix(t, "/")
	if t == "." || t == ".." {
		return ""
	}
	return t
}

// testFileExts are extensions that make a slash-less token a test file.
var testFileExts = []string{".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".py", ".rs", ".rb", ".php", ".swift", ".kt", ".java"}

func looksLikePath(t string) bool {
	if strings.Contains(t, "/") {
		return true
	}
	ext := path.Ext(t)
	for _, e := range testFileExts {
		if ext == e {
			return true
		}
	}
	return false
}

// expects reports whether ANY of the loci lies inside what the forecast named.
//
// Area matching is by path SEGMENTS, in either spelling a forecast may use: an
// area written relative to the repo root (`tools/app/internal/store`), relative
// to a module (`internal/store`), or a Go import path's tail all match when the
// area's segments appear contiguously in the locus. A plain substring test would
// let area `store` match `restore/`.
func (s *forecastScope) expects(loci []string) bool {
	for _, l := range loci {
		for _, a := range s.Areas {
			if segContains(l, normLocus(a)) {
				return true
			}
		}
		for _, f := range s.Files {
			if fileMatches(l, f) {
				return true
			}
		}
		low := strings.ToLower(l)
		base := strings.ToLower(path.Base(l))
		for _, r := range s.Risks {
			rl := strings.ToLower(r)
			if strings.Contains(rl, low) || (len(base) >= 3 && strings.Contains(rl, base)) {
				return true
			}
		}
	}
	return false
}

// segContains reports whether needle's segments appear contiguously in hay's.
func segContains(hay, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains("/"+hay+"/", "/"+needle+"/")
}

// fileMatches matches a locus against one forecast `files` entry, which may be
// a plain path or a glob, written from the repo root or from a module root: the
// entry is tried against every segment-suffix of the locus.
func fileMatches(locus, entry string) bool {
	entry = normLocus(entry)
	if entry == "" {
		return false
	}
	if segContains(locus, entry) {
		return true
	}
	segs := strings.Split(locus, "/")
	for i := range segs {
		if ok, _ := path.Match(entry, strings.Join(segs[i:], "/")); ok {
			return true
		}
	}
	return false
}
