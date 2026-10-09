package wsingest

import (
	"strings"
)

// Section returns the body of the doc's level-2 section `## <heading>` — every
// line after the heading up to the next level-1 or level-2 heading (or EOF),
// trimmed. The heading is matched case-insensitively on its trimmed text, so
// Section(doc, "Goal") finds `## Goal`. "" when the section is absent or empty.
// Pure; unit-tested.
//
// Fence-aware, on the same walker as CountCheckboxes and tickAllCheckboxes: a
// `## Something` line quoted inside a ``` fence is an illustration — a template, an
// agent prompt — and neither opens nor closes a section. Fenced lines that fall
// INSIDE the section are part of its body and are returned verbatim, fence
// markers included; only the boundaries are decided outside fences. Deeper
// headings (`### …`) belong to the section they sit in.
func Section(doc, heading string) string {
	want := strings.TrimSpace(heading)
	if want == "" {
		return ""
	}
	lines := strings.Split(doc, "\n")
	start, end := -1, -1
	forEachLineOutsideFences(doc, func(i int, line string) {
		if end >= 0 {
			return
		}
		level, title, ok := atxHeading(line)
		if !ok || level > 2 {
			return
		}
		if start < 0 {
			if level == 2 && strings.EqualFold(title, want) {
				start = i + 1
			}
			return
		}
		end = i
	})
	if start < 0 {
		return ""
	}
	if end < 0 {
		end = len(lines)
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

// atxHeading parses a CommonMark ATX heading line: up to three leading spaces, a
// run of 1–6 '#', then a space or end of line. It returns the level and the
// title with an optional closing '#' sequence removed.
func atxHeading(line string) (level int, title string, ok bool) {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 {
		return 0, "", false
	}
	for level < len(s) && s[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	rest := s[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false // "#hashtag", not a heading
	}
	rest = strings.TrimSpace(rest)
	// Optional closing sequence: "## Goal ##" — only when separated by a space.
	if trimmed := strings.TrimRight(rest, "#"); trimmed != rest &&
		(trimmed == "" || strings.HasSuffix(trimmed, " ") || strings.HasSuffix(trimmed, "\t")) {
		rest = strings.TrimSpace(trimmed)
	}
	return level, rest, true
}

// TickedCheckboxes returns every TICKED acceptance-criteria checkbox line, in
// document order, verbatim apart from leading indentation (`- [x] label`).
//
// The complement of UntickedCheckboxes and on the same walker as CountCheckboxes,
// so "which criteria are ticked" is an answer about exactly the lines the done
// count counts — a checklist quoted inside a ``` fence is never reported as
// verified work. Used to render a phase's "How to verify" list into its change
// request body.
func TickedCheckboxes(text string) []string {
	var out []string
	forEachLineOutsideFences(text, func(_ int, line string) {
		loc := checkboxRe.FindStringSubmatchIndex(line)
		if loc == nil {
			return
		}
		if !strings.EqualFold(line[loc[2]:loc[3]], "x") {
			return
		}
		out = append(out, strings.TrimSpace(line))
	})
	return out
}
