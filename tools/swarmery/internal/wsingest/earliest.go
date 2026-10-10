package wsingest

import (
	"regexp"
	"strings"
	"time"
)

// docEarliestRe matches a phase doc's date gate: `**Earliest:** 2026-11-03` in
// the header line (beside **Repo:** / **Depends on:**, separated by `·`), or a
// plain `Earliest: 2026-11-03`. The keyword must open the line or follow a
// separator, so prose like "the earliest: …" mid-sentence is not a gate.
var docEarliestRe = regexp.MustCompile(`(?:^|[\s·|>])\**Earliest:?\**:?\s*(\d{4}-\d{2}-\d{2})\b`)

// ParseEarliest returns the doc's `Earliest:` date (YYYY-MM-DD, verbatim) or ""
// when it declares none.
//
// Only the doc's HEADER is read — the lines before its first `## ` section,
// outside fences. A phase doc routinely MENTIONS an Earliest date in its body
// (a report quoting another phase's gate, an agent prompt that repeats it), and
// a gate read from prose would refuse a phase for a date that is not its own.
// Pure; unit-tested.
func ParseEarliest(text string) string {
	var out string
	inHeader := true
	forEachLineOutsideFences(text, func(_ int, line string) {
		if !inHeader || out != "" {
			return
		}
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			inHeader = false
			return
		}
		if m := docEarliestRe.FindStringSubmatch(line); m != nil {
			out = m[1]
		}
	})
	return out
}

// EarliestNotReached reports whether the doc's `Earliest:` date is still in the
// future at now (compared as a calendar day in now's location), returning the
// declared date. An absent or unparsable date never gates.
func EarliestNotReached(text string, now time.Time) (date string, gated bool) {
	date = ParseEarliest(text)
	if date == "" {
		return "", false
	}
	day, err := time.ParseInLocation("2006-01-02", date, now.Location())
	if err != nil {
		return date, false
	}
	return date, now.Before(day)
}
