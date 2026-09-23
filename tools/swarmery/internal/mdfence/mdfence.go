// Package mdfence holds the ONE markdown fence-aware line walker this daemon
// reads documents with.
//
// It was lifted out of internal/wsingest (where it still backs CountCheckboxes,
// UntickedCheckboxes and tickAllCheckboxes) because internal/runcore needs the
// same rule to find a run's `PHASE BLOCKED:` sentinel and cannot import wsingest:
// wsingest already imports runcore, so the dependency only goes one way. A second
// copy in runcore would be a second answer to "is this line the document's own
// text or a quoted example", and the codebase has already paid for that once — a
// checklist quoted inside a ``` fence left a shipped phase stuck at 7/11 for ever.
package mdfence

import "strings"

// ForEachLine calls fn for every line that is not inside a fenced code block.
// Fence tracking follows CommonMark's rule that a fence closes only on a marker
// of the SAME character and at least the opening length, so a ```` block quoting
// ``` markdown — exactly how a phase doc shows a generated template — stays one
// block instead of toggling twice.
//
// i is the 0-based index of the line in the ORIGINAL text, so a caller can report
// a line number even though fenced lines were skipped.
func ForEachLine(text string, fn func(i int, line string)) {
	var fenceChar byte
	fenceLen := 0
	for i, line := range strings.Split(text, "\n") {
		if c, n := marker(line); n > 0 {
			switch {
			case fenceLen == 0:
				fenceChar, fenceLen = c, n
			case c == fenceChar && n >= fenceLen:
				fenceChar, fenceLen = 0, 0
			}
			continue // the fence line itself is never content
		}
		if fenceLen == 0 {
			fn(i, line)
		}
	}
}

// EndsOpen reports whether text finishes with a fence still open — i.e. the
// document opened a code block and never closed it.
//
// It exists because an unclosed fence makes ForEachLine skip everything after
// it, which is the right call for a checklist (a half-pasted block is not the
// document's own text) and the wrong one for a run's ending line: a truncated
// build log that opens ``` and never closes it would hide the `PHASE BLOCKED:`
// the executor wrote underneath, and the daemon would stamp the run green over
// an explicit report that it is stuck. Callers who cannot afford that ask this
// and fall back to a raw scan.
func EndsOpen(text string) bool {
	var fenceChar byte
	fenceLen := 0
	for _, line := range strings.Split(text, "\n") {
		if c, n := marker(line); n > 0 {
			switch {
			case fenceLen == 0:
				fenceChar, fenceLen = c, n
			case c == fenceChar && n >= fenceLen:
				fenceChar, fenceLen = 0, 0
			}
		}
	}
	return fenceLen > 0
}

// marker reports a line's fence character and run length, or (0, 0) when the
// line does not open or close a fence. Up to three leading spaces are allowed, as
// in CommonMark.
func marker(line string) (byte, int) {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 || s == "" {
		return 0, 0
	}
	c := s[0]
	if c != '`' && c != '~' {
		return 0, 0
	}
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return c, n
}
