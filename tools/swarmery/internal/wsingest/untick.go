// Reopen (phase-run outcomes plan, phase 1): a finished phase whose defect got
// past its gates is sent back by UNTICKING the criteria it failed. Only the md
// file is written (md = truth), atomically: the plan-dir watcher folds the new
// counts into epic_phases like any other doc edit.

package wsingest

import (
	"os"
	"strings"
)

// TickedCriteriaLabels returns the label of every TICKED acceptance criterion, in
// document order — the mirror of UntickedCheckboxes, through the same line
// walker and the same criterionLabel, so a label it returns is exactly one
// UntickCriteria will match.
func TickedCriteriaLabels(text string) []string {
	var out []string
	forEachLineOutsideFences(text, func(_ int, line string) {
		loc := checkboxRe.FindStringSubmatchIndex(line)
		if loc == nil || !strings.EqualFold(line[loc[2]:loc[3]], "x") {
			return
		}
		if label := criterionLabel(line[loc[1]:]); label != "" {
			out = append(out, label)
		}
	})
	return out
}

// UntickCriteria unticks every ticked criterion of the doc at docPath whose
// label (criterionLabel, the normalisation UntickedCheckboxes uses) equals one
// of labels, and returns how many lines it flipped. Nothing is written when
// nothing matched.
func UntickCriteria(docPath string, labels []string) (n int, err error) {
	matched, err := UntickCriteriaMatched(docPath, labels)
	return len(matched), err
}

// UntickCriteriaMatched is UntickCriteria returning the labels of the lines it
// flipped, in document order — what the reopen ledger records.
func UntickCriteriaMatched(docPath string, labels []string) ([]string, error) {
	st, err := os.Stat(docPath)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return nil, err
	}
	out, matched := untickCriteria(string(body), labels)
	if len(matched) == 0 {
		return nil, nil
	}
	if err := writeFileAtomic(docPath, []byte(out), st.Mode().Perm()); err != nil {
		return nil, err
	}
	return matched, nil
}

// untickCriteria flips the matching ticked lines outside fences. Pure;
// unit-tested.
func untickCriteria(text string, labels []string) (string, []string) {
	want := map[string]bool{}
	for _, l := range labels {
		if norm := criterionLabel(l); norm != "" {
			want[norm] = true
		}
	}
	if len(want) == 0 {
		return text, nil
	}
	lines := strings.Split(text, "\n")
	var matched []string
	forEachLineOutsideFences(text, func(i int, line string) {
		loc := checkboxRe.FindStringSubmatchIndex(line)
		if loc == nil || !strings.EqualFold(line[loc[2]:loc[3]], "x") {
			return
		}
		label := criterionLabel(line[loc[1]:])
		if !want[label] {
			return
		}
		lines[i] = line[:loc[2]] + " " + line[loc[3]:]
		matched = append(matched, label)
	})
	return strings.Join(lines, "\n"), matched
}
