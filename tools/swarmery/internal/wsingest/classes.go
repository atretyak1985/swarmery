// Criterion classes (phase-run outcomes plan, phase 3, decision D3): an
// acceptance criterion whose label starts with `[LAND]` is closed by landing the
// work (push / PR / merge — the operator or the land action), one starting with
// `[MANUAL]` only by a human (production, a console, a hand check). Neither is
// the phase run's to tick, and a run that has ticked everything else is done.
//
// The marker is read ONLY at the very start of the label, after `- [ ] `, and
// only outside fenced code blocks — the same line walker CountCheckboxes uses, so
// "how many criteria are open" and "how many of them are [LAND]" are answers
// about the same set of lines.

package wsingest

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Criterion classes, as written in the doc (`- [ ] [LAND] push the branch`).
const (
	ClassLand   = "LAND"
	ClassManual = "MANUAL"
)

// criterionClassRe matches a class marker at the start of a criterion's label
// (the text after the `- [ ] ` checkbox marker, leading space trimmed).
var criterionClassRe = regexp.MustCompile(`^\[(LAND|MANUAL)\]\s+`)

// ErrNoCriterionAtLine is returned by MarkCriterionClass when the requested line
// is not an acceptance-criteria checkbox outside a fence.
var ErrNoCriterionAtLine = errors.New("no acceptance criterion at that line")

// ErrUnknownCriterionClass is returned for a class other than LAND / MANUAL.
var ErrUnknownCriterionClass = errors.New("unknown criterion class (want LAND or MANUAL)")

// CriteriaCounts is what a phase doc says about its acceptance criteria: how
// many are ticked, how many there are, and how many of the UNTICKED ones carry
// each class marker. Done/Total are exactly CountCheckboxes' numbers.
type CriteriaCounts struct {
	Done       int
	Total      int
	LandOpen   int
	ManualOpen int
}

// Executable is the total a phase run is measured against: every criterion
// except the open [LAND] / [MANUAL] ones. Ticked classed criteria stay in it
// (they are also in Done), so Done >= Executable() exactly when every criterion
// the executor can close is closed.
func (c CriteriaCounts) Executable() int {
	return c.Total - c.LandOpen - c.ManualOpen
}

// criterionClass returns the class marker at the start of a criterion's label
// rest (the text after the checkbox marker), or "".
func criterionClass(rest string) string {
	m := criterionClassRe.FindStringSubmatch(strings.TrimLeft(rest, " \t"))
	if m == nil {
		return ""
	}
	return m[1]
}

// CountCriteria counts the doc's acceptance criteria with their open classes.
// Pure; unit-tested.
func CountCriteria(text string) CriteriaCounts {
	var c CriteriaCounts
	forEachLineOutsideFences(text, func(_ int, line string) {
		loc := checkboxRe.FindStringSubmatchIndex(line)
		if loc == nil {
			return
		}
		c.Total++
		if strings.EqualFold(line[loc[2]:loc[3]], "x") {
			c.Done++
			return
		}
		switch criterionClass(line[loc[1]:]) {
		case ClassLand:
			c.LandOpen++
		case ClassManual:
			c.ManualOpen++
		}
	})
	return c
}

// UntickedExecutable is UntickedCheckboxes without the [LAND] / [MANUAL]
// criteria: the labels a phase run may still be asked to close. A continuation
// built from the full list would send the executor off to push a branch.
func UntickedExecutable(text string) []string {
	var out []string
	forEachLineOutsideFences(text, func(_ int, line string) {
		loc := checkboxRe.FindStringSubmatchIndex(line)
		if loc == nil || strings.EqualFold(line[loc[2]:loc[3]], "x") {
			return
		}
		if criterionClass(line[loc[1]:]) != "" {
			return
		}
		if label := criterionLabel(line[loc[1]:]); label != "" {
			out = append(out, label)
		}
	})
	return out
}

// TickCriteriaByClass ticks every unticked criterion of the given class in the
// doc at docPath and returns how many lines it flipped. Nothing is written when
// nothing matched; the write is atomic (temp file + rename, mode kept).
func TickCriteriaByClass(docPath, class string) (int, error) {
	if class != ClassLand && class != ClassManual {
		return 0, fmt.Errorf("%w: %q", ErrUnknownCriterionClass, class)
	}
	st, err := os.Stat(docPath)
	if err != nil {
		return 0, err
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return 0, err
	}
	out, n := tickClass(string(body), class)
	if n == 0 {
		return 0, nil
	}
	if err := writeFileAtomic(docPath, []byte(out), st.Mode().Perm()); err != nil {
		return 0, err
	}
	return n, nil
}

// tickClass flips every unticked criterion of class outside fences. Pure.
func tickClass(text, class string) (string, int) {
	lines := strings.Split(text, "\n")
	n := 0
	forEachLineOutsideFences(text, func(i int, line string) {
		loc := checkboxRe.FindStringSubmatchIndex(line)
		if loc == nil || line[loc[2]:loc[3]] != " " {
			return
		}
		if criterionClass(line[loc[1]:]) != class {
			return
		}
		lines[i] = line[:loc[2]] + "x" + line[loc[3]:]
		n++
	})
	return strings.Join(lines, "\n"), n
}

// MarkCriterionClass prefixes the criterion on 1-based line `line` of the doc at
// docPath with the class marker (`- [ ] push` → `- [ ] [LAND] push`). A
// criterion that already carries a marker has it replaced; one that already
// carries this class is left alone (no write). The line must be a checkbox
// outside a fence, else ErrNoCriterionAtLine. Atomic like TickCriteriaByClass.
func MarkCriterionClass(docPath string, line int, class string) error {
	if class != ClassLand && class != ClassManual {
		return fmt.Errorf("%w: %q", ErrUnknownCriterionClass, class)
	}
	st, err := os.Stat(docPath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	out, changed, err := markClass(string(body), line, class)
	if err != nil || !changed {
		return err
	}
	return writeFileAtomic(docPath, []byte(out), st.Mode().Perm())
}

// markClass is MarkCriterionClass on text. Pure; unit-tested.
func markClass(text string, line int, class string) (out string, changed bool, err error) {
	lines := strings.Split(text, "\n")
	idx := line - 1
	found := false
	forEachLineOutsideFences(text, func(i int, l string) {
		if i != idx {
			return
		}
		loc := checkboxRe.FindStringSubmatchIndex(l)
		if loc == nil {
			return
		}
		found = true
		rest := strings.TrimLeft(l[loc[1]:], " \t")
		if m := criterionClassRe.FindStringSubmatch(rest); m != nil {
			if m[1] == class {
				return
			}
			rest = rest[len(m[0]):]
		}
		lines[i] = l[:loc[1]] + "[" + class + "] " + rest
		changed = true
	})
	if !found {
		return text, false, fmt.Errorf("%w: line %d", ErrNoCriterionAtLine, line)
	}
	return strings.Join(lines, "\n"), changed, nil
}
