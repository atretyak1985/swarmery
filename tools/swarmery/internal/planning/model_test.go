package planning

import (
	"errors"
	"testing"
)

// A model name is authored text — a `**Model:** Opus` header line, a picker
// value, an env knob — and its case carries no meaning. Refusing "Opus" while
// accepting "opus" turns an unambiguous declaration into a 409 the author has to
// debug by eye.
func TestResolveModel_IsCaseInsensitive(t *testing.T) {
	cases := map[string]string{
		"opus":            Models["opus"],
		"Opus":            Models["opus"],
		"OPUS":            Models["opus"],
		" Sonnet ":        Models["sonnet"],
		"fable":           Models["fable"],
		"CLAUDE-SONNET-5": Models["sonnet"], // a full ID, upper-cased
		"":                DefaultModel,
	}
	for in, want := range cases {
		got, err := ResolveModel(in)
		if err != nil {
			t.Errorf("ResolveModel(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ResolveModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveModel_UnknownStaysUnknown(t *testing.T) {
	for _, in := range []string{"gpt-9", "opus-pro", "claude-opus-5[1m]"} {
		if _, err := ResolveModel(in); !errors.Is(err, ErrUnknownModel) {
			t.Errorf("ResolveModel(%q) err = %v, want ErrUnknownModel", in, err)
		}
	}
}
