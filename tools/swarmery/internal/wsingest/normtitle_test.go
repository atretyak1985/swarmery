package wsingest

import "testing"

// TestNormalizeLessonTitle pins the fold's contract case by case. Every row is
// a wording the retro corpus actually produces (or a shape that must NOT be
// folded together), so a future tweak that looks harmless fails here loudly.
func TestNormalizeLessonTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   \t  ", ""},
		{"punctuation only", "--- !!! ???", ""},
		{"stop-words only", "The a an of to in for and", ""},
		{"ordinal label stripped", "Lesson 3: Sync cache before build", "sync cache before build"},
		{"different ordinal, same identity", "### lesson 11 - sync cache before build", "sync cache before build"},
		{"case and punctuation folded", "Sync-Cache Before Build!", "sync cache before build"},
		{"stop-words dropped", "sync the cache before the build", "sync cache before build"},
		{"word 'lesson' is not an ordinal", "Lessons from the migration", "lessons from migration"},
		{"'lesson:' without an ordinal is kept", "lesson: be careful", "lesson be careful"},
		{"digits survive", "bump plugin.json to 3.5.0", "bump plugin json 3 5 0"},
		{"unicode is preserved", "Урок: Ніколи не запускати prettier у web/", "урок ніколи не запускати prettier у web"},
		{"unicode case is folded", "НІКОЛИ не запускати Prettier", "ніколи не запускати prettier"},
		{"internal whitespace collapsed", "  sync\t\tcache   before \n build ", "sync cache before build"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeLessonTitle(c.in); got != c.want {
				t.Fatalf("NormalizeLessonTitle(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestNormalizeLessonTitleCap proves the 80-rune cap counts RUNES, not bytes —
// a Cyrillic title is 2 bytes per rune, so a byte cap would cut it in half and
// two different long lessons could collide on the truncated prefix.
func TestNormalizeLessonTitleCap(t *testing.T) {
	long := ""
	for i := 0; i < 40; i++ {
		long += "слово "
	}
	got := NormalizeLessonTitle(long)
	if n := len([]rune(got)); n > normTitleMaxRunes {
		t.Fatalf("cap not applied: %d runes (max %d)", n, normTitleMaxRunes)
	}
	if n := len([]rune(got)); n < normTitleMaxRunes-6 {
		t.Fatalf("cap cut too aggressively: %d runes", n)
	}
	if got[len(got)-1] == ' ' {
		t.Fatalf("cap left a trailing space: %q", got)
	}
}

// TestNormalizeLessonTitleDeterministic guards the one property every reader
// depends on: the same input always folds to the same key, so a row written by
// the insert path and a row written by the backfill can never disagree.
func TestNormalizeLessonTitleDeterministic(t *testing.T) {
	const in = "Lesson 2: ALWAYS run sync-cache, then build (the plugin cache is what runs)"
	first := NormalizeLessonTitle(in)
	if first == "" {
		t.Fatal("expected a non-empty key")
	}
	for i := 0; i < 5; i++ {
		if got := NormalizeLessonTitle(in); got != first {
			t.Fatalf("pass %d: %q != %q", i, got, first)
		}
	}
}
