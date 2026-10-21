package modelid

import "testing"

func TestFamilyAndGeneration(t *testing.T) {
	cases := []struct {
		id     string
		family string
		gen    int
	}{
		{"claude-opus-5-5", "opus", 55},
		{"claude-opus-5-5[1m]", "opus", 55},
		{"claude-opus-5-5-fast", "opus", 55},
		{"claude-opus-5", "opus", 50},
		{"claude-opus-4-1", "opus", 41},
		{"claude-sonnet-5", "sonnet", 50},
		{"claude-sonnet-4-6", "sonnet", 46},
		{"claude-haiku-4-5-20251001", "haiku", 45},
		{"claude-fable-5-1", "fable", 51},
		{"claude-mythos-5", "mythos", 50},
		{"anthropic/claude-opus-5-5", "opus", 55},
		// Pre-Opus-5 word order, where the version comes BEFORE the family.
		{"claude-3-5-sonnet-20241022", "sonnet", 35},
		// A bare alias has a family and NO generation. Zero must read as
		// "unknown", never as "oldest".
		{"opus", "opus", 0},
		{"sonnet", "sonnet", 0},
		{"", "", 0},
		{"gpt-4o", "", 0},
	}
	for _, tc := range cases {
		if got := Family(tc.id); got != tc.family {
			t.Errorf("Family(%q) = %q, want %q", tc.id, got, tc.family)
		}
		if got := Generation(tc.id); got != tc.gen {
			t.Errorf("Generation(%q) = %d, want %d", tc.id, got, tc.gen)
		}
	}
}

// A date suffix is not a minor version. Reading `20251001` as one would put
// claude-haiku-4-5-20251001 generations ahead of every real model.
func TestGenerationIgnoresDateSuffixes(t *testing.T) {
	if got := Generation("claude-haiku-4-5-20251001"); got != 45 {
		t.Errorf("Generation = %d, want 45", got)
	}
	if got := Generation("claude-opus-5-20250101"); got != 50 {
		t.Errorf("Generation = %d, want 50", got)
	}
}

func TestBaseStripsContextWindowMarker(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5-5[1m]": "claude-opus-5-5",
		"claude-opus-5-5":     "claude-opus-5-5",
		" claude-opus-5 ":     "claude-opus-5",
		"[1m]":                "[1m]", // a leading bracket is not a marker
		"":                    "",
	}
	for in, want := range cases {
		if got := Base(in); got != want {
			t.Errorf("Base(%q) = %q, want %q", in, got, want)
		}
	}
}

// SameTier is what stops a context-window marker from being reported as a model
// change — the false positive that would put a "fell back" chip on healthy runs.
func TestSameTier(t *testing.T) {
	same := [][2]string{
		{"claude-opus-5-5", "claude-opus-5-5[1m]"},
		{"claude-opus-5-5", "claude-opus-5-5-fast"},
	}
	for _, p := range same {
		if !SameTier(p[0], p[1]) {
			t.Errorf("SameTier(%q, %q) = false, want true", p[0], p[1])
		}
	}
	differ := [][2]string{
		{"claude-opus-5-5", "claude-opus-5"},
		{"claude-opus-5-5", "claude-opus-4-1"},
		{"claude-opus-5-5", "claude-sonnet-5"},
		{"opus", "claude-opus-5-5"}, // an alias has no generation to match
		{"gpt-4o", "gpt-4o"},        // an unknown family is never "the same tier"
	}
	for _, p := range differ {
		if SameTier(p[0], p[1]) {
			t.Errorf("SameTier(%q, %q) = true, want false", p[0], p[1])
		}
	}
}

// fable and mythos rank ABOVE opus and level with each other. The shell half
// (plugins/core/hooks/lib/model-tier.sh, MODEL_TIER) must hold the same numbers;
// scripts/tests/model-tier.test.sh pins its side.
func TestFamilyTier(t *testing.T) {
	if FamilyTier("fable") != FamilyTier("mythos") {
		t.Error("fable and mythos must rank equal")
	}
	if !(FamilyTier("fable") > FamilyTier("opus") && FamilyTier("opus") > FamilyTier("sonnet") && FamilyTier("sonnet") > FamilyTier("haiku")) {
		t.Error("fable > opus > sonnet > haiku")
	}
	if FamilyTier("") != 0 || FamilyTier("gpt") != 0 {
		t.Error("an unknown family must rank 0, which every caller reads as no comparison")
	}
}

// Generations are compared only WITHIN a family: fable 5.1 against opus 5.5 is
// a tier question, and the lower generation number on the fable side means
// nothing. Two families on one tier are never ordered.
func TestIsFallbackAcrossFamilies(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"claude-fable-5-1", "claude-opus-5-5", true},
		{"claude-mythos-5", "claude-opus-5-5", true},
		{"claude-fable-5-1", "claude-sonnet-5", true},
		{"claude-opus-5-5", "claude-fable-5-1", false},
		{"claude-opus-5-5", "claude-mythos-5", false},
		{"claude-fable-5-1", "claude-mythos-5", false},
		{"claude-mythos-5", "claude-fable-5-1", false},
		{"claude-fable-5-1", "claude-fable-5", true},
		{"claude-fable-5", "claude-fable-5-1", false},
	}
	for _, tc := range cases {
		if got := IsFallback(tc.from, tc.to); got != tc.want {
			t.Errorf("IsFallback(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}
