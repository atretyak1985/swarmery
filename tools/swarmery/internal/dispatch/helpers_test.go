package dispatch

import (
	"regexp"
	"testing"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDFormatAndUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		u := newUUID()
		if !uuidRE.MatchString(u) {
			t.Fatalf("newUUID() = %q, not a v4 UUID", u)
		}
		if seen[u] {
			t.Fatalf("newUUID() collision on %q", u)
		}
		seen[u] = true
	}
}

func TestDecodeStringList(t *testing.T) {
	cases := []struct {
		in   string
		want int // len
		err  bool
	}{
		{"", 0, false},
		{"  ", 0, false},
		{"[]", 0, false},
		{`["a","b"]`, 2, false},
		{"null", 0, false},
		{"{bad", 0, true},
	}
	for _, tc := range cases {
		got, err := decodeStringList(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("decodeStringList(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("decodeStringList(%q) error: %v", tc.in, err)
		}
		if len(got) != tc.want {
			t.Errorf("decodeStringList(%q) len = %d, want %d", tc.in, len(got), tc.want)
		}
	}
}
