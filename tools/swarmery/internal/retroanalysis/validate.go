package retroanalysis

// Output validation for a system analysis.
//
// The one rule worth stating plainly: an analysis with no citation is a
// FAILURE, not a weaker success. Prose about an agent system is easy to write
// and hard to check, and an uncited paragraph reads exactly like a cited one.
// Storing it as 'proposed' would put unverifiable advice in front of an
// operator wearing the same badge as evidence — so it becomes 'failed' with a
// reason instead.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RequiredSections are the H2 headings the agent contract fixes, in order.
// The third is the one that travels onward as a planning idea.
var RequiredSections = []string{"## Що болить", "## Чому", "## Що я б змінив"}

// ChangeSection is the heading whose body becomes the Planning Mode idea.
const ChangeSection = "## Що я б змінив"

// maxChangeSectionLen bounds the change section in BYTES. It sits far under
// maxPlanningIdeaLen (enforced with len() in internal/api/planning.go) with
// room for the window/scope preamble the handoff prepends; the tighter cap here
// is editorial — a change proposal is a paragraph or three, not a spec. Bytes,
// not runes: the gate downstream measures bytes, and this text is Cyrillic.
const maxChangeSectionLen = 6000

// markerRe matches one citation marker. The kind vocabulary is the twin of
// internal/retrodigest's — keep the two in lockstep; a kind the digest can
// emit but this rejects would fail every honest analysis.
var markerRe = regexp.MustCompile(`\[E:(agent|rec|error_group|session|task|lesson):([^\]\s][^\]]*)\]`)

// Validate checks one analysis against the output contract and returns how
// many distinct citations it carries.
//
// allowed, when non-empty, is the set of "kind:id" pairs the digest actually
// offered; a marker outside it is a fabrication and fails the analysis. Pass
// nil to skip that check (the section and citation-count rules still apply).
func Validate(md string, allowed map[string]bool) (int, error) {
	body := strings.TrimSpace(md)
	if body == "" {
		return 0, fmt.Errorf("the analysis is empty")
	}
	// Sections must all be present, and in the contracted order — an analysis
	// that answers "what to change" before "why" is a different document.
	at := make([]int, 0, len(RequiredSections))
	for _, want := range RequiredSections {
		i := sectionIndex(body, want)
		if i < 0 {
			return 0, fmt.Errorf("the analysis is missing the required section %q", want)
		}
		at = append(at, i)
	}
	for i := 1; i < len(at); i++ {
		if at[i] < at[i-1] {
			return 0, fmt.Errorf("section %q appears before %q; the contract fixes their order",
				RequiredSections[i], RequiredSections[i-1])
		}
	}

	if n := len(changeSection(body)); n > maxChangeSectionLen {
		return 0, fmt.Errorf(
			"the %q section is %d bytes, over the %d-byte budget — it has to fit a planning idea",
			ChangeSection, n, maxChangeSectionLen)
	}

	found := markerRe.FindAllStringSubmatch(body, -1)
	if len(found) == 0 {
		return 0, fmt.Errorf(
			"the analysis cites no evidence: every claim must end in an [E:kind:id] marker copied from the digest")
	}
	uniq := map[string]bool{}
	var unknown []string
	for _, m := range found {
		key := m[1] + ":" + m[2]
		uniq[key] = true
		if len(allowed) > 0 && !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		unknown = dedupe(unknown)
		return 0, fmt.Errorf(
			"the analysis cites evidence that is not in the digest: %s — identifiers may only be copied, never invented",
			strings.Join(unknown, ", "))
	}
	return len(uniq), nil
}

// ChangeIdea returns the BODY of the change section — the text handed to
// Planning Mode — with the heading stripped. Empty when the section is absent.
func ChangeIdea(md string) string {
	sec := changeSection(strings.TrimSpace(md))
	if sec == "" {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(sec, ChangeSection))
}

// AllowedCitations extracts the "kind:id" set a digest offers, so an analysis
// can be checked against the evidence it was actually given.
func AllowedCitations(digest string) map[string]bool {
	out := map[string]bool{}
	for _, m := range markerRe.FindAllStringSubmatch(digest, -1) {
		out[m[1]+":"+m[2]] = true
	}
	return out
}

// sectionIndex finds a heading at the start of a line, so a heading quoted
// inside a sentence does not count as the section itself.
func sectionIndex(body, heading string) int {
	if strings.HasPrefix(body, heading) {
		return 0
	}
	if i := strings.Index(body, "\n"+heading); i >= 0 {
		return i + 1
	}
	return -1
}

// changeSection returns the change section including its heading, up to the
// next H2 or the end of the document.
func changeSection(body string) string {
	start := sectionIndex(body, ChangeSection)
	if start < 0 {
		return ""
	}
	rest := body[start+len(ChangeSection):]
	if i := strings.Index(rest, "\n## "); i >= 0 {
		return body[start : start+len(ChangeSection)+i]
	}
	return body[start:]
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
