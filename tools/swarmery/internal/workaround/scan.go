// Package workaround finds the places where an agent said it changed course —
// "instead", "for now", "pre-existing", "поки що" — in the assistant prose of a
// phase run, so the human and the reviewer read those sentences first (D23,
// SC-6). It is a hint, never a gate: nothing blocks on a hit.
//
// The words are guards.json's workaroundWords. A word matches as a WHOLE word,
// case-insensitively, in Latin and Cyrillic alike; an entry with spaces is a
// phrase whose words may be separated by any run of whitespace. Code is not
// prose: fenced blocks and `inline code` are masked before matching, so `skip`
// in a flag or `instead` in an identifier never counts.
package workaround

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Hit is one matched word with its sentence and the sentence on each side.
type Hit struct {
	// Word is the list entry that matched, as configured (not as written).
	Word string `json:"word"`
	// Sentence is the sentence holding the match, whitespace-collapsed.
	Sentence string `json:"sentence"`
	// Before and After are the neighbouring sentences ("" at either end).
	Before string `json:"before"`
	After  string `json:"after"`
	// TurnSeq is the turn the text came from; Scan leaves it 0 for the caller.
	TurnSeq int `json:"turnSeq"`
}

// contextMaxRunes bounds every stored sentence: a "sentence" can be a whole
// paragraph without punctuation, and the card shows three of them per hit.
const contextMaxRunes = 320

// Matcher is a compiled word list. Build it once per process (the list changes
// only on restart, D8) and share it: Scan is safe for concurrent use.
type Matcher struct {
	words []string
	res   []*regexp.Regexp
}

// Compile builds a Matcher. Blank entries are dropped; guards.Parse already
// refuses them, so this only protects hand-built lists.
func Compile(words []string) *Matcher {
	m := &Matcher{}
	for _, w := range words {
		fields := strings.Fields(w)
		if len(fields) == 0 {
			continue
		}
		parts := make([]string, len(fields))
		for i, f := range fields {
			parts[i] = regexp.QuoteMeta(f)
		}
		m.words = append(m.words, strings.Join(fields, " "))
		m.res = append(m.res, regexp.MustCompile(`(?i)`+strings.Join(parts, `\s+`)))
	}
	return m
}

// Empty reports whether the matcher has no words — a scan would find nothing.
func (m *Matcher) Empty() bool { return m == nil || len(m.res) == 0 }

// Scan finds every whole-word match of words in text. It is Compile(words).Scan
// for one-off callers; hot paths keep a Matcher.
func Scan(text string, words []string) []Hit { return Compile(words).Scan(text) }

// Scan returns the hits in text order, one per (word, sentence).
func (m *Matcher) Scan(text string) []Hit {
	if m.Empty() || text == "" {
		return nil
	}
	masked := maskCode(text)
	type match struct {
		word       string
		start, end int
	}
	var found []match
	for i, re := range m.res {
		for _, loc := range re.FindAllStringIndex(masked, -1) {
			if wholeWord(masked, loc[0], loc[1]) && !zeroCount(masked, loc[0], loc[1]) {
				found = append(found, match{m.words[i], loc[0], loc[1]})
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.SliceStable(found, func(a, b int) bool { return found[a].start < found[b].start })

	spans := sentenceSpans(text, masked)
	seen := map[[2]string]bool{}
	var hits []Hit
	for _, f := range found {
		i := spanAt(spans, f.start)
		if i < 0 {
			continue
		}
		h := Hit{Word: f.word, Sentence: clip(text[spans[i][0]:spans[i][1]])}
		if i > 0 {
			h.Before = clip(text[spans[i-1][0]:spans[i-1][1]])
		}
		if i+1 < len(spans) {
			h.After = clip(text[spans[i+1][0]:spans[i+1][1]])
		}
		key := [2]string{h.Word, h.Sentence}
		if seen[key] {
			continue
		}
		seen[key] = true
		hits = append(hits, h)
	}
	return hits
}

// wordRune is a rune that continues a word. A hyphen joins words too, so
// "pre-existing" is one word, `skip` does not match "skip-list", and `flaky`
// does not match "non-flaky".
func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_' || r == '-' || r == '\''
}

// wholeWord reports whether s[start:end] is bounded by non-word runes (or the
// ends of s) — \b for Latin and Cyrillic alike, which Go's ASCII-only \b is not.
func wholeWord(s string, start, end int) bool {
	if start > 0 {
		if r, _ := utf8.DecodeLastRuneInString(s[:start]); wordRune(r) {
			return false
		}
	}
	if end < len(s) {
		if r, _ := utf8.DecodeRuneInString(s[end:]); wordRune(r) {
			return false
		}
	}
	return true
}

// zeroCount reports a tally of nothing — "0 skipped", "0 SKIPPED", "skipped: 0",
// "flaky 0" — which says the opposite of a workaround. A non-zero tally ("1
// skipped") stays a hit: a skipped test is exactly what a reviewer should see.
func zeroCount(s string, start, end int) bool {
	before := strings.TrimRight(s[:start], " \t")
	if len(before) < len(s[:start]) && strings.HasSuffix(before, "0") {
		rest := before[:len(before)-1]
		if r, _ := utf8.DecodeLastRuneInString(rest); rest == "" || !(unicode.IsDigit(r) || r == '.' || r == ',') {
			return true
		}
	}
	after := strings.TrimLeft(s[end:], " \t:=")
	if len(after) < len(s[end:]) && strings.HasPrefix(after, "0") && !numberGoesOn(after[1:]) {
		return true
	}
	return false
}

// numberGoesOn reports whether rest continues a number: a digit, or a '.' / ','
// followed by a digit ("0.5", "0,5" — but not "0, next").
func numberGoesOn(rest string) bool {
	if rest == "" {
		return false
	}
	if c := rest[0]; c >= '0' && c <= '9' {
		return true
	}
	return (rest[0] == '.' || rest[0] == ',') && len(rest) > 1 && rest[1] >= '0' && rest[1] <= '9'
}

// maskCode blanks code — fenced blocks and `inline spans` — byte for byte, so
// offsets in the result are offsets in text. Newlines survive (they still end
// sentences); everything else inside code becomes a space.
func maskCode(text string) string {
	b := []byte(text)
	inFence := false
	lineStart := 0
	for lineStart <= len(b) {
		lineEnd := lineStart
		for lineEnd < len(b) && b[lineEnd] != '\n' {
			lineEnd++
		}
		line := strings.TrimLeft(string(b[lineStart:lineEnd]), " \t")
		fence := strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
		if inFence || fence {
			blank(b[lineStart:lineEnd])
			if fence {
				inFence = !inFence
			}
		} else {
			maskInline(b[lineStart:lineEnd])
		}
		lineStart = lineEnd + 1
	}
	return string(b)
}

// maskInline blanks every closed `…` span on one line, backticks included. An
// unclosed backtick is left alone — it is a stray character, not code.
func maskInline(line []byte) {
	for i := 0; i < len(line); i++ {
		if line[i] != '`' {
			continue
		}
		j := i + 1
		for j < len(line) && line[j] != '`' {
			j++
		}
		if j == len(line) {
			return
		}
		blank(line[i : j+1])
		i = j
	}
}

func blank(b []byte) {
	for i := range b {
		b[i] = ' '
	}
}

// sentenceSpans splits text into sentence byte ranges. Boundaries come from
// masked (so a '.' inside code never ends a sentence): a newline always ends
// one, and '.', '!', '?' or '…' end one when followed by whitespace or the end.
// "section.go", "92.1" and "e.g.x" therefore stay inside their sentence. Each
// span is trimmed on the ORIGINAL text, so a sentence opening with `code` keeps
// it; a span with no prose left once code is masked (a fenced line, a bare
// command) is dropped, so a hit's context is always prose.
func sentenceSpans(text, masked string) [][2]int {
	var spans [][2]int
	add := func(a, b int) {
		for a < b && isSpace(text[a]) {
			a++
		}
		for b > a && isSpace(text[b-1]) {
			b--
		}
		if a < b && strings.TrimFunc(masked[a:b], notProse) != "" {
			spans = append(spans, [2]int{a, b})
		}
	}
	start := 0
	for i := 0; i < len(masked); {
		r, size := utf8.DecodeRuneInString(masked[i:])
		next := i + size
		switch {
		case r == '\n':
			add(start, i)
			start = next
		case r == '.' || r == '!' || r == '?' || r == '…':
			if next == len(masked) || isSpace(masked[next]) {
				add(start, next)
				start = next
			}
		}
		i = next
	}
	add(start, len(masked))
	return spans
}

// notProse is true for runes that alone do not make a sentence: markdown list
// and emphasis marks, table pipes, punctuation, whitespace.
func notProse(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// spanAt returns the index of the span holding offset off, or -1 when off sits
// in a gap (it cannot for a real match: a match is prose, never trimmed away).
func spanAt(spans [][2]int, off int) int {
	i := sort.Search(len(spans), func(i int) bool { return spans[i][1] > off })
	if i < len(spans) && spans[i][0] <= off {
		return i
	}
	return -1
}

// clip collapses whitespace and bounds the length in runes.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= contextMaxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:contextMaxRunes-1]) + "…"
}
