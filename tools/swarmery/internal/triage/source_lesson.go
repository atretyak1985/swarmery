package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
)

// lessonInstruction is what the judge is asked about a lesson candidate.
const lessonInstruction = "accept when the sentence is a general rule a future run in these areas can act on. " +
	"not-useful when it names one plan, phase or ticket, restates the obvious, or cannot be acted on."

// lessonCandidate is the surprise_lessons status a lesson waits in for review.
const lessonCandidate = "candidate"

// LessonSource offers lesson candidates (surprise_lessons, status candidate)
// to a run. Its kind is suggest-only: a verdict waits for the operator, whose
// accept goes through the lessons review functions, never through Apply.
// Lessons are fleet-wide, so they are collected for every scope.
type LessonSource struct{ DB *sql.DB }

// Kind is the policy key of lesson verdicts.
func (LessonSource) Kind() string { return "lesson" }

// Collect lists every lesson candidate, newest first; limit > 0 caps the list.
func (s LessonSource) Collect(_ context.Context, _ Scope, limit int) ([]Item, error) {
	ls, err := lessons.List(s.DB, lessonCandidate)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(ls))
	for _, l := range ls {
		ref := strconv.FormatInt(l.ID, 10)
		items = append(items, Item{
			Kind:         "lesson",
			Key:          ref,
			Title:        l.Title,
			WaitingSince: l.CreatedAt,
			Instruction:  lessonInstruction,
			Evidence:     lessonEvidence(l),
			Parts:        []Part{{Ref: ref, Label: l.Title, Allowed: []string{"accept", "not-useful"}}},
		})
	}
	return capItems(items, limit), nil
}

// lessonEvidence is what the judge is shown about one candidate.
func lessonEvidence(l lessons.Lesson) string {
	var b strings.Builder
	line(&b, "Title", l.Title)
	line(&b, "Guidance", l.Guidance)
	if strings.TrimSpace(l.Cause) != "" {
		line(&b, "Cause", l.Cause)
	} else {
		line(&b, "Source paragraph", l.SourceParagraph)
	}
	areas := "*"
	if len(l.AreaGlobs) > 0 {
		areas = strings.Join(l.AreaGlobs, ", ")
	}
	line(&b, "Areas", areas)
	line(&b, "Recurrences", strconv.Itoa(l.Recurrences))
	if l.SurpriseIndex != nil {
		line(&b, "Off-plan (surprise) index", fmtNum(*l.SurpriseIndex))
	}
	return strings.TrimRight(b.String(), "\n")
}

// Apply is never reached: the policy makes lesson verdicts suggestions only.
func (LessonSource) Apply(context.Context, Item, Part, string, string, json.RawMessage) (Applied, error) {
	return Applied{}, errSuggestOnly("lesson")
}

// Undo is never reached: nothing is ever applied for a lesson.
func (LessonSource) Undo(context.Context, Verdict) error { return errSuggestOnly("lesson") }

// Open reports whether the lesson still waits for review (status candidate).
// An unknown or malformed ref is not open.
func (s LessonSource) Open(ctx context.Context, ref string) (bool, error) {
	return rowStateIs(ctx, s.DB, `SELECT status FROM surprise_lessons WHERE id=?`, ref, lessonCandidate)
}

// ── helpers shared by the suggest-only sources ──

// errSuggestOnly is the backstop error of a suggest-only source's Apply/Undo.
func errSuggestOnly(kind string) error {
	return fmt.Errorf("triage: %s verdicts are suggestions and are never applied", kind)
}

// capItems keeps the first limit items; limit <= 0 keeps them all.
func capItems(items []Item, limit int) []Item {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

// rowStateIs reads one text column by a decimal id ref and compares it to want.
// A malformed ref or a missing row is false with no error.
func rowStateIs(ctx context.Context, db *sql.DB, query, ref, want string) (bool, error) {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return false, nil
	}
	var got string
	err = db.QueryRowContext(ctx, query, id).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == want, nil
}

// line writes "label: value\n" when value is not blank.
func line(b *strings.Builder, label, value string) {
	if v := strings.TrimSpace(value); v != "" {
		fmt.Fprintf(b, "%s: %s\n", label, v)
	}
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
