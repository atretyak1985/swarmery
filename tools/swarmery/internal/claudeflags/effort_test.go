package claudeflags

import (
	"strings"
	"testing"
)

// The precedence this package owns: the site's own knob, then the cross-site
// knob, then the engine's default. (The rungs ABOVE these — a request field, a
// phase doc's `**Effort:**` — belong to the engines, which is why they are
// tested there.)
func TestEffort_Precedence(t *testing.T) {
	const site = "SWARMERY_TESTENGINE_EFFORT"

	t.Run("default when nothing is set", func(t *testing.T) {
		t.Setenv(site, "")
		t.Setenv(EffortEnv, "")
		if got := Effort(site, "high"); got != "high" {
			t.Errorf("Effort = %q, want the engine default %q", got, "high")
		}
	})

	t.Run("cross-site knob beats the default", func(t *testing.T) {
		t.Setenv(site, "")
		t.Setenv(EffortEnv, "low")
		if got := Effort(site, "high"); got != "low" {
			t.Errorf("Effort = %q, want %q", got, "low")
		}
	})

	t.Run("site knob beats the cross-site knob", func(t *testing.T) {
		t.Setenv(site, "max")
		t.Setenv(EffortEnv, "low")
		if got := Effort(site, "high"); got != "max" {
			t.Errorf("Effort = %q, want %q", got, "max")
		}
	})

	t.Run("a site with no knob of its own still reads the cross-site one", func(t *testing.T) {
		t.Setenv(EffortEnv, "medium")
		if got := Effort("", "high"); got != "medium" {
			t.Errorf("Effort = %q, want %q", got, "medium")
		}
	})
}

// An operator's typo must not silently drop a pinned engine to the CLI's xhigh —
// that is the exact regression this package exists to prevent, arriving through
// the one path nobody reviews. It degrades to the engine default and logs.
func TestEffort_InvalidEnvFallsBackToTheDefaultRatherThanOmitting(t *testing.T) {
	const site = "SWARMERY_TESTENGINE_EFFORT"
	t.Setenv(site, "xxhigh")
	t.Setenv(EffortEnv, "")

	if got := Effort(site, "medium"); got != "medium" {
		t.Errorf("Effort = %q, want the engine default %q — a typo must never mean 'omit the flag'", got, "medium")
	}
}

// The escape hatch: an operator comparing a run against its pre-pinning cost has
// to be able to reproduce the old shape exactly, which means emitting no flag.
func TestEffort_OffOmitsTheFlag(t *testing.T) {
	const site = "SWARMERY_TESTENGINE_EFFORT"
	for _, spelling := range []string{OmitEffort, "none", "default", "OFF"} {
		t.Setenv(site, spelling)
		t.Setenv(EffortEnv, "")
		if got := Effort(site, "high"); got != "" {
			t.Errorf("Effort with %q = %q, want \"\" (omit the flag)", spelling, got)
		}
		if args := EffortArgs(site, "high"); args != nil {
			t.Errorf("EffortArgs with %q = %q, want nil", spelling, args)
		}
	}
}

func TestEffortArgs_Pair(t *testing.T) {
	const site = "SWARMERY_TESTENGINE_EFFORT"
	t.Setenv(site, "")
	t.Setenv(EffortEnv, "")

	got := EffortArgs(site, "low")
	want := []string{"--effort", "low"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("EffortArgs = %q, want %q", got, want)
	}
}

// A site that hands in a broken default would otherwise emit no flag and land on
// the CLI's xhigh — the failure this package exists to remove, reached through
// the one path that skips validation.
func TestEffort_UnusableEngineDefaultStillEmitsAFlag(t *testing.T) {
	const site = "SWARMERY_TESTENGINE_EFFORT"
	t.Setenv(site, "")
	t.Setenv(EffortEnv, "")

	for _, def := range []string{"", "ludicrous"} {
		if got := Effort(site, def); got != FallbackEffort {
			t.Errorf("Effort with default %q = %q, want %q", def, got, FallbackEffort)
		}
	}
}

func TestNormalizeEffort(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"low", "low", true},
		{"HIGH", "high", true},
		{"  xhigh  ", "xhigh", true},
		{"max", "max", true},
		{"", "", true},        // no opinion — fall through to the next rung
		{"off", "", true},     // explicit omission
		{"default", "", true}, // the word an operator reaches for meaning "the CLI's own"
		{"ultra", "", false},  // not a CLI choice: must be reported, not folded
		{"9", "", false},
	} {
		got, ok := NormalizeEffort(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeEffort(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// ValidEfforts is what error messages and the dashboard picker render, so a
// caller must not be able to reorder the source of truth under them.
func TestValidEfforts_IsACopy(t *testing.T) {
	a := ValidEfforts()
	a[0] = "tampered"
	if ValidEfforts()[0] != "low" {
		t.Fatal("ValidEfforts returned a shared slice — a caller can rewrite the closed set")
	}
}
