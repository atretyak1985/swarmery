package wsingest

import (
	"testing"
	"time"
)

func TestParseEarliest(t *testing.T) {
	cases := []struct {
		name, doc, want string
	}{
		{"header field", "# Phase 7\n**Repo:** `/r` · **Depends on:** 2 · **Earliest:** 2026-11-03\n\n## Goal\n", "2026-11-03"},
		{"plain line", "# P\nEarliest: 2026-10-01\n", "2026-10-01"},
		{"absent", "# P\n**Repo:** `/r`\n\n## Goal\n- [ ] a\n", ""},
		{"body prose is not the gate", "# P\n\n## Notes\n**Earliest:** 2026-11-03\n", ""},
		{"fenced header line is not the gate", "# P\n```\n**Earliest:** 2026-11-03\n```\n", ""},
		{"mid-word is not the gate", "# P\nthe latest-Earliest: 2026-11-03\n", ""},
	}
	for _, c := range cases {
		if got := ParseEarliest(c.doc); got != c.want {
			t.Errorf("%s: ParseEarliest = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestEarliestNotReached(t *testing.T) {
	doc := "# P\n**Earliest:** 2026-11-03\n"
	day := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if d, gated := EarliestNotReached(doc, day("2026-11-02 23:59")); !gated || d != "2026-11-03" {
		t.Errorf("day before: gated=%v date=%q, want gated", gated, d)
	}
	if _, gated := EarliestNotReached(doc, day("2026-11-03 00:00")); gated {
		t.Error("on the date: gated, want admitted")
	}
	if _, gated := EarliestNotReached("# P\n", day("2026-01-01 00:00")); gated {
		t.Error("no Earliest line: gated")
	}
	if _, gated := EarliestNotReached("# P\nEarliest: 2026-13-45\n", day("2026-01-01 00:00")); gated {
		t.Error("unparsable date: gated, want admitted")
	}
}
