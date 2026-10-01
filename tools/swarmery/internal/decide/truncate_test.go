package decide

import (
	"testing"
	"unicode/utf8"
)

// truncate keeps at most n bytes and never splits a rune: a rune that fits
// whole is kept, one that would be cut is dropped whole.
func TestTruncateKeepsEveryRuneThatFits(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"aé!", 3, "aé"}, // é (2 bytes) ends exactly at the cap
		{"aé!", 2, "a"},  // é would be split
		{"日本語", 6, "日本"}, // two 3-byte runes fit exactly
		{"日本語", 7, "日本"}, // the third would be split
		{"日本語", 5, "日"},  // the second would be split
		{"🙂ok", 4, "🙂"},  // a 4-byte rune that fits
		{"🙂ok", 3, ""},   // one that does not
		{"plain ascii", 5, "plain"},
		{"short", 10, "short"},
	} {
		got := truncate(tc.s, tc.n)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
		if len(got) > tc.n || !utf8.ValidString(got) {
			t.Errorf("truncate(%q, %d) = %q: over the cap or not valid UTF-8", tc.s, tc.n, got)
		}
	}
}
