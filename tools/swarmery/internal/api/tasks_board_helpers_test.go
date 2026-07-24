package api

import (
	"reflect"
	"testing"
)

func TestNormalizePriority(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", 5, false}, // default → normal
		{"normal", 5, false},
		{"urgent", 1, false},
		{"HIGH", 3, false},    // case-insensitive
		{"  low  ", 7, false}, // trimmed
		{"medium", 0, true},   // unknown token
		{"5", 0, true},        // raw ints are not accepted tokens
	}
	for _, c := range cases {
		got, err := normalizePriority(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizePriority(%q): want error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizePriority(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizePriority(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPriorityFromIntRoundTrip(t *testing.T) {
	// Every accepted token must round-trip token → int → token.
	for _, tok := range []string{"urgent", "high", "normal", "low"} {
		v, err := normalizePriority(tok)
		if err != nil {
			t.Fatalf("normalizePriority(%q): %v", tok, err)
		}
		if back := priorityFromInt(v); back != tok {
			t.Errorf("round-trip %q → %d → %q", tok, v, back)
		}
	}
	// Legacy raw default 5 (workspace rows) maps to normal.
	if got := priorityFromInt(5); got != "normal" {
		t.Errorf("priorityFromInt(5) = %q, want normal", got)
	}
}

func TestValidColumn(t *testing.T) {
	for _, ok := range []string{"triage", "todo", "in_progress", "in_review", "done", "archived"} {
		if !validColumn(ok) {
			t.Errorf("validColumn(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "backlog", "Todo", "in-progress", "review"} {
		if validColumn(bad) {
			t.Errorf("validColumn(%q) = true, want false", bad)
		}
	}
}

func TestLegalTransition(t *testing.T) {
	// any → archived always allowed.
	for _, from := range []string{"triage", "todo", "in_progress", "in_review", "done"} {
		if err := legalTransition(from, "archived"); err != nil {
			t.Errorf("legalTransition(%q, archived) = %v, want nil", from, err)
		}
	}
	// done → in_progress rejected.
	if err := legalTransition("done", "in_progress"); err == nil {
		t.Error("legalTransition(done, in_progress) = nil, want error")
	}
	// Everything else permissive (spot checks).
	for _, c := range [][2]string{
		{"triage", "todo"},
		{"todo", "in_progress"},
		{"in_progress", "in_review"},
		{"in_review", "done"},
		{"in_review", "todo"}, // moving back is allowed
		{"done", "in_review"}, // only done→in_progress is special
	} {
		if err := legalTransition(c[0], c[1]); err != nil {
			t.Errorf("legalTransition(%q, %q) = %v, want nil", c[0], c[1], err)
		}
	}
}

func TestStringListRoundTrip(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{"src/api"},
		{"src/api", "web/src/**", "internal/store/migrations/*.sql"},
	}
	for _, in := range cases {
		stored, err := marshalStringList(in)
		if err != nil {
			t.Fatalf("marshalStringList(%v): %v", in, err)
		}
		got, err := unmarshalStringList(stored)
		if err != nil {
			t.Fatalf("unmarshalStringList(%q): %v", stored, err)
		}
		want := in
		if want == nil {
			want = []string{} // nil normalizes to empty slice on the way back
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round-trip %v → %q → %v, want %v", in, stored, got, want)
		}
	}
	// Legacy NULL/empty storage decodes to empty slice, not nil.
	got, err := unmarshalStringList("")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("unmarshalStringList(\"\") = %v, %v; want empty slice", got, err)
	}
}

func TestNewBoardExternalID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := newBoardExternalID()
		if err != nil {
			t.Fatalf("newBoardExternalID: %v", err)
		}
		if len(id) != 8 || id[:2] != "T-" {
			t.Fatalf("id %q: want T- + 6 chars", id)
		}
		for _, r := range id[2:] {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z')) {
				t.Fatalf("id %q: non-base36 char %q", id, r)
			}
		}
		seen[id] = true
	}
	// Not a strict guarantee, but 100 draws from 36^6 should not collide.
	if len(seen) < 99 {
		t.Errorf("unexpected collisions: %d unique of 100", len(seen))
	}
}
