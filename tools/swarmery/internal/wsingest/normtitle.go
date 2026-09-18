package wsingest

// agent-memory phase 4 — cross-task identity for retro lessons.
//
// A retro lesson is free prose written by whoever ran the retro, so the same
// lesson arrives worded a dozen ways: "Lesson 3: Always sync-cache before
// build", "always sync cache before the build", "Sync-cache before build!".
// Rows are DELETE+reinserted per retro (artifacts.go applyRetro), so the row id
// cannot be that identity either — the whole set is replaced on every rescan.
//
// NormalizeLessonTitle is the fold that gives them one: deterministic, pure,
// and cheap enough to run on every insert. It is the ONLY definition of lesson
// identity — migration 0069's retro_lessons.norm_title, the grouped
// /api/retro/lessons?group=1 feed, and advisor R11 all read the column this
// writes, so none of them can disagree about what "the same lesson" means.
//
// It is deliberately NOT clever: no stemming, no edit distance, no synonyms. A
// fold that guesses would merge two different lessons under one heading and the
// operator would have no way to see that it happened.

import (
	"strings"
	"unicode"
)

// normTitleMaxRunes caps the folded key. Titles are h3 headings, so 80 runes is
// generous; the cap exists so one runaway heading cannot bloat a key that is
// indexed and read on every advisor pass.
const normTitleMaxRunes = 80

// normTitleStopWords are dropped from the fold. English function words only —
// they carry no identity and their presence drifts freely between retro authors
// ("sync cache before build" vs "sync the cache before the build"). Nothing
// domain-specific is listed: dropping a domain word would merge real lessons.
var normTitleStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true,
	"to": true, "in": true, "for": true, "and": true,
}

// NormalizeLessonTitle folds one lesson title into its cross-task identity:
// lowercase, drop a leading "Lesson N:" ordinal, punctuation to spaces, drop
// stop-words, collapse whitespace, cap at normTitleMaxRunes.
//
// Returns "" for a title that folds to nothing (empty, punctuation-only, or all
// stop-words). "" is never an identity: callers skip it rather than group by it,
// so two meaningless titles are never declared the same lesson.
//
// Unicode-safe throughout: the punctuation pass keeps anything
// unicode.IsLetter/IsDigit accepts, so a Cyrillic title folds to a Cyrillic key
// instead of collapsing to "".
func NormalizeLessonTitle(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = stripLessonOrdinal(s)

	// Punctuation becomes a space, never a deletion: "sync-cache" must fold to
	// two words, exactly like "sync cache", or the hyphenated wording would get
	// an identity of its own.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}

	words := make([]string, 0, 16)
	for _, w := range strings.Fields(b.String()) {
		if normTitleStopWords[w] {
			continue
		}
		words = append(words, w)
	}
	return capNormTitle(strings.Join(words, " "))
}

// stripLessonOrdinal removes a leading "lesson <n>" label with its separator —
// the shape the retro template emits (`### Lesson 3: ...`). The ordinal is the
// lesson's position in ONE doc, so keeping it would give the same lesson a
// different identity in every task that happened to number it differently.
//
// Only a leading, digit-bearing label is stripped: a title that genuinely starts
// with the word "lesson" ("lessons from the migration") is left alone, and so is
// "lesson: be careful" — with no ordinal there is nothing task-local to remove.
func stripLessonOrdinal(s string) string {
	// A caller that passes the raw heading (`### Lesson 3: ...`) rather than
	// artifacts.go's already-captured title still folds correctly.
	s = strings.TrimLeft(s, "# \t")
	rest := strings.TrimPrefix(s, "lesson")
	if rest == s {
		return s
	}
	rest = strings.TrimLeft(rest, " \t")
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return s // "lessons ..." / "lesson: ..." — a word, not an ordinal label
	}
	rest = strings.TrimLeft(rest[digits:], " \t:.-)")
	if rest == "" {
		return s // the whole title was the label — keep it rather than fold to ""
	}
	return rest
}

// capNormTitle truncates at a rune boundary and re-trims, so a cut landing
// mid-word cannot leave a trailing space in the key.
//
// The cut is a narrow false-merge path: two genuinely different lessons that
// share their first normTitleMaxRunes runes fold to one identity and are counted
// as a recurrence. Accepted deliberately — lesson titles that agree for 80 runes
// and then diverge are rare enough that the bounded key is worth more than the
// miss — but it is the first thing to look at if a group reads as unrelated.
func capNormTitle(s string) string {
	r := []rune(s)
	if len(r) <= normTitleMaxRunes {
		return s
	}
	return strings.TrimSpace(string(r[:normTitleMaxRunes]))
}

// BackfillNormTitles fills retro_lessons.norm_title for rows that predate
// migration 0069 (norm_title = ''), and returns how many rows it wrote.
//
// Idempotent by construction: it only ever selects rows whose key is still
// empty, so a second call after a complete pass writes nothing and returns 0.
// A row whose title folds to "" stays '' and is re-examined on the next start —
// that costs one bounded scan and keeps the "'' is not an identity" invariant
// true for every reader.
//
// Run() calls this once, before the first scan pass, so the first advisor tick
// after an upgrade already sees a fully folded table. It is deliberately NOT
// part of Scan(): a rescan every 60 s has no reason to re-walk history, and the
// insert path (applyRetro) has folded every new row since 0069 landed.
func (s *Scanner) BackfillNormTitles() (int, error) {
	rows, err := s.db.Query(`SELECT id, title FROM retro_lessons WHERE norm_title = ''`)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id   int64
		norm string
	}
	var todo []pending
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			rows.Close()
			return 0, err
		}
		if norm := NormalizeLessonTitle(title); norm != "" {
			todo = append(todo, pending{id: id, norm: norm})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(todo) == 0 {
		return 0, nil
	}

	// One transaction: a half-backfilled table would group a lesson with only
	// some of its own occurrences, and every count downstream would be quietly
	// wrong rather than visibly missing.
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`UPDATE retro_lessons SET norm_title = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	for _, p := range todo {
		if _, err := stmt.Exec(p.norm, p.id); err != nil {
			stmt.Close()
			tx.Rollback()
			return 0, err
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(todo), nil
}
