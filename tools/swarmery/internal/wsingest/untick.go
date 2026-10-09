// Reopen (phase-run outcomes plan, phase 1): a finished phase whose defect got
// past its gates is sent back by UNTICKING the criteria it failed. Only the md
// file is written (md = truth), atomically: the plan-dir watcher folds the new
// counts into epic_phases like any other doc edit.
//
// The write is split in two so a caller can order it against its own durable
// state: PrepareUntick resolves the labels without touching the file, and
// PendingUntick.Commit writes it. The reopen handler inserts its ledger row
// first and commits the doc only after that row is in — a failed insert then
// leaves the doc exactly as it was, and the operator can simply retry.

package wsingest

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrAmbiguousCriteria is returned when a requested label matches more than
// one ticked line of the doc — the caller cannot know which one the operator
// meant, so nothing is flipped.
var ErrAmbiguousCriteria = errors.New("criterion label matches more than one ticked line")

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

// PendingUntick is an untick that has been resolved against the doc but not
// yet written. Matched holds the labels Commit will flip, in document order;
// it is nil when no requested label is a ticked line.
type PendingUntick struct {
	docPath string
	out     []byte
	mode    os.FileMode
	Matched []string
}

// PrepareUntick reads the doc at docPath and resolves labels (criterionLabel,
// the normalisation UntickedCheckboxes uses) against its ticked lines outside
// fences, without writing anything. A label that matches more than one ticked
// line yields ErrAmbiguousCriteria (wrapped, naming the labels).
func PrepareUntick(docPath string, labels []string) (*PendingUntick, error) {
	st, err := os.Stat(docPath)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return nil, err
	}
	out, matched, ambiguous := untickCriteria(string(body), labels)
	if len(ambiguous) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrAmbiguousCriteria, strings.Join(ambiguous, "; "))
	}
	return &PendingUntick{docPath: docPath, out: []byte(out), mode: st.Mode().Perm(), Matched: matched}, nil
}

// Commit writes the flipped doc atomically (temp file + rename, mode kept).
// It is a no-op when nothing matched.
func (p *PendingUntick) Commit() error {
	if p == nil || len(p.Matched) == 0 {
		return nil
	}
	return writeFileAtomic(p.docPath, p.out, p.mode)
}

// UntickCriteria unticks every ticked criterion of the doc at docPath whose
// label equals one of labels, and returns how many lines it flipped. Nothing
// is written when nothing matched.
func UntickCriteria(docPath string, labels []string) (n int, err error) {
	matched, err := UntickCriteriaMatched(docPath, labels)
	return len(matched), err
}

// UntickCriteriaMatched is UntickCriteria returning the labels of the lines it
// flipped, in document order — PrepareUntick followed by Commit.
func UntickCriteriaMatched(docPath string, labels []string) ([]string, error) {
	p, err := PrepareUntick(docPath, labels)
	if err != nil {
		return nil, err
	}
	if err := p.Commit(); err != nil {
		return nil, err
	}
	return p.Matched, nil
}

// untickCriteria flips the matching ticked lines outside fences. Pure;
// unit-tested. When a requested label matches more than one ticked line the
// text is returned unchanged with those labels in ambiguous.
func untickCriteria(text string, labels []string) (out string, matched, ambiguous []string) {
	want := map[string]bool{}
	for _, l := range labels {
		if norm := criterionLabel(l); norm != "" {
			want[norm] = true
		}
	}
	if len(want) == 0 {
		return text, nil, nil
	}
	tickedLabel := func(line string) (string, bool) {
		loc := checkboxRe.FindStringSubmatchIndex(line)
		if loc == nil || !strings.EqualFold(line[loc[2]:loc[3]], "x") {
			return "", false
		}
		label := criterionLabel(line[loc[1]:])
		return label, want[label]
	}
	hits := map[string]int{}
	forEachLineOutsideFences(text, func(_ int, line string) {
		if label, ok := tickedLabel(line); ok {
			hits[label]++
		}
	})
	seen := map[string]bool{}
	for _, l := range labels {
		norm := criterionLabel(l)
		if hits[norm] > 1 && !seen[norm] {
			seen[norm] = true
			ambiguous = append(ambiguous, norm)
		}
	}
	if len(ambiguous) > 0 {
		return text, nil, ambiguous
	}
	lines := strings.Split(text, "\n")
	forEachLineOutsideFences(text, func(i int, line string) {
		label, ok := tickedLabel(line)
		if !ok {
			return
		}
		loc := checkboxRe.FindStringSubmatchIndex(line)
		lines[i] = line[:loc[2]] + " " + line[loc[3]:]
		matched = append(matched, label)
	})
	return strings.Join(lines, "\n"), matched, nil
}
