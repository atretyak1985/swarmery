package lessons

// Output validation — the lesson twin of internal/retroanalysis/validate.go.
//
// The rule that matters: a lesson with no cited cause is not a weaker lesson,
// it is no lesson. Advice about "how this codebase works" is easy to generate
// and hard to check, and an uncited sentence reads exactly like a cited one.
// So the whole OUTPUT fails on a malformed shape, and each LESSON fails on its
// own when its guidance, areas, cause or evidence break the contract — above
// all when it cites an id its input never offered.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Candidate is one lesson as the model returns it.
type Candidate struct {
	Title     string   `json:"title"`
	Guidance  string   `json:"guidance"`
	AreaGlobs []string `json:"area_globs"`
	Cause     string   `json:"cause"`
	Evidence  []string `json:"evidence"`
}

// Rejection is one lesson that failed validation, kept for the generation row.
type Rejection struct {
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Field limits.
const (
	maxTitleRunes    = 120
	maxGuidanceRunes = 280
	maxCauseRunes    = 400
	maxAreaGlobs     = 5
	maxEvidence      = 8
)

// ErrInvalid marks a validation failure (the API maps it to 400).
var ErrInvalid = errors.New("invalid lesson")

// ParseOutput decodes the model's stdout under the strict schema
// {"lessons": [Candidate, …]} — unknown keys fail, a missing "lessons" fails,
// more than MaxLessons fails. A fenced or prose-wrapped object is accepted: the
// object is taken from the first '{' to the last '}'.
func ParseOutput(raw string) ([]Candidate, error) {
	start, end := strings.IndexByte(raw, '{'), strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return nil, fmt.Errorf("the output carries no JSON object")
	}
	var out struct {
		Lessons *[]Candidate `json:"lessons"`
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw[start : end+1])))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("the output does not match the lesson schema: %w", err)
	}
	if out.Lessons == nil {
		return nil, fmt.Errorf(`the output has no "lessons" array`)
	}
	if n := len(*out.Lessons); n > MaxLessons {
		return nil, fmt.Errorf("the output returned %d lessons; at most %d are allowed", n, MaxLessons)
	}
	return *out.Lessons, nil
}

// Validate splits candidates into the valid ones (normalized: trimmed fields,
// evidence ids in bare "kind:id" form, de-duplicated) and the rejected ones.
func Validate(cs []Candidate, allowed map[string]bool) (ok []Candidate, rejected []Rejection) {
	for _, c := range cs {
		n, err := ValidateCandidate(c, allowed)
		if err != nil {
			rejected = append(rejected, Rejection{Title: capRunes(strings.TrimSpace(c.Title), maxTitleRunes), Reason: err.Error()})
			continue
		}
		ok = append(ok, n)
	}
	return ok, rejected
}

// ValidateCandidate checks one lesson. allowed is the set of "kind:id" pairs
// the input offered; it must be non-empty — a lesson can never be validated
// against "anything goes".
func ValidateCandidate(c Candidate, allowed map[string]bool) (Candidate, error) {
	n := Candidate{
		Title:    strings.TrimSpace(c.Title),
		Guidance: strings.TrimSpace(c.Guidance),
		Cause:    strings.TrimSpace(c.Cause),
	}
	if err := checkText(n.Title, n.Guidance); err != nil {
		return n, err
	}
	if n.Cause == "" {
		return n, fmt.Errorf("%w: no cited cause — a lesson must say what went differently and why", ErrInvalid)
	}
	if utf8.RuneCountInString(n.Cause) > maxCauseRunes {
		return n, fmt.Errorf("%w: cause is over %d characters", ErrInvalid, maxCauseRunes)
	}
	globs, err := NormalizeGlobs(c.AreaGlobs)
	if err != nil {
		return n, err
	}
	n.AreaGlobs = globs
	if len(c.Evidence) == 0 {
		return n, fmt.Errorf("%w: cites no evidence — every lesson must cite ids copied from its input", ErrInvalid)
	}
	if len(c.Evidence) > maxEvidence {
		return n, fmt.Errorf("%w: cites %d evidence ids, at most %d", ErrInvalid, len(c.Evidence), maxEvidence)
	}
	if len(allowed) == 0 {
		return n, fmt.Errorf("%w: no evidence was offered, so no citation can be checked", ErrInvalid)
	}
	seen := map[string]bool{}
	var foreign []string
	for _, e := range c.Evidence {
		id := NormalizeEvidenceID(e)
		if !allowed[id] {
			foreign = append(foreign, id)
			continue
		}
		if !seen[id] {
			seen[id] = true
			n.Evidence = append(n.Evidence, id)
		}
	}
	if len(foreign) > 0 {
		return n, fmt.Errorf("%w: cites evidence that is not in its input: %s — ids may only be copied, never invented",
			ErrInvalid, strings.Join(foreign, ", "))
	}
	return n, nil
}

// checkText validates the title and the one-sentence guidance; shared with the
// operator's Edit so an edited lesson obeys the same contract.
func checkText(title, guidance string) error {
	switch {
	case title == "":
		return fmt.Errorf("%w: empty title", ErrInvalid)
	case utf8.RuneCountInString(title) > maxTitleRunes:
		return fmt.Errorf("%w: title is over %d characters", ErrInvalid, maxTitleRunes)
	case guidance == "":
		return fmt.Errorf("%w: empty guidance", ErrInvalid)
	case utf8.RuneCountInString(guidance) > maxGuidanceRunes:
		return fmt.Errorf("%w: guidance is over %d characters — one sentence, not a paragraph", ErrInvalid, maxGuidanceRunes)
	case strings.ContainsAny(guidance, "\n\r"):
		return fmt.Errorf("%w: guidance must be one sentence on one line", ErrInvalid)
	}
	return nil
}

// NormalizeGlobs trims, de-duplicates and checks area globs: 1..maxAreaGlobs,
// relative, no whitespace, no "..", and syntactically valid for path.Match.
func NormalizeGlobs(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			continue
		}
		switch {
		case strings.ContainsAny(g, " \t\n,"):
			return nil, fmt.Errorf("%w: area glob %q contains whitespace or a comma", ErrInvalid, g)
		case strings.HasPrefix(g, "/") || strings.Contains(g, ".."):
			return nil, fmt.Errorf("%w: area glob %q must be relative to the repo root", ErrInvalid, g)
		}
		if _, err := path.Match(g, ""); err != nil {
			return nil, fmt.Errorf("%w: area glob %q is not a valid pattern", ErrInvalid, g)
		}
		seen[g] = true
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no area — name the glob(s) where the lesson applies", ErrInvalid)
	}
	if len(out) > maxAreaGlobs {
		return nil, fmt.Errorf("%w: %d area globs, at most %d", ErrInvalid, len(out), maxAreaGlobs)
	}
	return out, nil
}

var markerWrap = regexp.MustCompile(`^\[E:(.*)\]$`)

// NormalizeEvidenceID accepts an id as "kind:id" or as the "[E:kind:id]" marker
// the prompt shows, and returns the bare form.
func NormalizeEvidenceID(s string) string {
	s = strings.TrimSpace(s)
	if m := markerWrap.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}
