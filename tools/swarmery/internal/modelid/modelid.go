// Package modelid reads a Claude model id the way a human does: which FAMILY
// it belongs to and which GENERATION of that family it is.
//
// It exists because three unrelated places compared model ids as opaque strings
// and each was wrong in its own way:
//
//   - the hooks called every `opus` → `claude-opus-5-5` dispatch a fallback,
//   - modeleval's `WHERE model = ?` never matched `claude-opus-5-5[1m]`,
//   - the phase API reported "the model this run used" as the FIRST model of the
//     session, which is silently false for any session a safeguard moved.
//
// The shell half of the same rules lives in plugins/core/hooks/lib/model-tier.sh
// (hooks cannot call Go); the two are kept deliberately small and identical in
// behaviour, and scripts/tests/model-tier.test.sh pins the shell side against
// config/pricing.json.
package modelid

import (
	"strconv"
	"strings"
)

// Base strips the trailing context-window marker from a model id:
// `claude-opus-5-5[1m]` → `claude-opus-5-5`.
//
// The marker is a REQUEST property (which context window was purchased), not a
// different model, and every id-keyed lookup in this codebase wants it gone.
// cost.resolveKey already does this for pricing, where getting it wrong billed
// Opus 5.5 at Opus 5 rates through the `claude-opus-5-` prefix; modeleval did
// not, so a session run with a 1M window simply never matched its own model.
func Base(id string) string {
	if i := strings.IndexByte(id, '['); i > 0 {
		return strings.TrimSpace(id[:i])
	}
	return strings.TrimSpace(id)
}

// families are matched as substrings, longest-lived naming first. Substring
// rather than prefix because the word moved: `claude-opus-5-5` puts it second,
// `claude-3-5-sonnet-20241022` puts it third, and a vendor-prefixed
// `anthropic/claude-opus-5-5` puts it later still.
var families = []string{"opus", "sonnet", "haiku", "fable", "mythos"}

// Family returns the model family ("opus", "sonnet", …) or "" when the id
// belongs to none this package knows. "" is a real answer and callers must
// treat it as "no comparison possible", never as a family of its own.
func Family(id string) string {
	low := strings.ToLower(Base(id))
	for _, f := range families {
		if strings.Contains(low, f) {
			return f
		}
	}
	return ""
}

// Generation returns major*10+minor (5.5 → 55, 5 → 50, 4-1 → 41), or 0 when the
// id carries no version — which is what a bare alias like `opus` looks like.
//
// Zero is "unknown", never "oldest": comparing against it would make every alias
// look like an ancient model.
func Generation(id string) int {
	low := strings.ToLower(Base(id))
	fam := Family(low)
	if fam == "" {
		return 0
	}
	i := strings.Index(low, fam)
	// Digits AFTER the family word (`opus-5-5`), then digits BEFORE it
	// (`3-5-sonnet`) for the pre-Opus-5 naming order.
	if g := leadingVersion(low[i+len(fam):]); g > 0 {
		return g
	}
	return trailingVersion(low[:i])
}

// leadingVersion reads `-5-5-anything` / `-5` at the START of s.
func leadingVersion(s string) int {
	parts := splitNumeric(s)
	if len(parts) == 0 {
		return 0
	}
	return scale(parts[0], parts[1:])
}

// trailingVersion reads `claude-3-5-` at the END of s.
func trailingVersion(s string) int {
	parts := splitNumeric(reverseFields(s))
	if len(parts) == 0 {
		return 0
	}
	// Reversed, so `3-5` came back as [5 3].
	if len(parts) >= 2 {
		return scale(parts[1], parts[:1])
	}
	return scale(parts[0], nil)
}

// splitNumeric returns the leading run of dash-separated numeric fields.
func splitNumeric(s string) []string {
	var out []string
	for _, f := range strings.Split(strings.Trim(s, "-"), "-") {
		if f == "" {
			continue
		}
		if _, err := strconv.Atoi(f); err != nil {
			break
		}
		out = append(out, f)
	}
	return out
}

func reverseFields(s string) string {
	f := strings.Split(strings.Trim(s, "-"), "-")
	for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
		f[i], f[j] = f[j], f[i]
	}
	return strings.Join(f, "-")
}

// scale turns a major field plus an optional minor into the comparable integer.
// A date suffix (`20251001`) is NOT a minor version and is rejected by length:
// minors are one digit in every id this repo has ever priced.
func scale(major string, rest []string) int {
	m, err := strconv.Atoi(major)
	if err != nil || m <= 0 || len(major) > 2 {
		return 0
	}
	minor := 0
	if len(rest) > 0 && len(rest[0]) == 1 {
		minor, _ = strconv.Atoi(rest[0])
	}
	return m*10 + minor
}

// FamilyTier ranks families by capability: higher is stronger, 0 unknown.
//
// fable and mythos sit level with opus rather than above it even though they
// price higher (config/pricing.json: $10/$50 against opus 5.5's $4/$20). This
// comparison decides whether to CLAIM A FALLBACK, and "the frontier tier" is the
// only distinction that claim needs; ordering within that tier would turn every
// opus↔fable routing choice into a reported downgrade.
func FamilyTier(family string) int {
	switch family {
	case "opus", "fable", "mythos":
		return 3
	case "sonnet":
		return 2
	case "haiku":
		return 1
	}
	return 0
}

// SameTier reports whether two ids name the same family and the same
// generation — i.e. whether calling them "the same model" is fair. The alias
// `opus` is NOT the same tier as `claude-opus-5-5` by this test (its generation
// is unknown); use Family when that is the question you mean.
func SameTier(a, b string) bool {
	fa, fb := Family(a), Family(b)
	return fa != "" && fa == fb && Generation(a) == Generation(b)
}
