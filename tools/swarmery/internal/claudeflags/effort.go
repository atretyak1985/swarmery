package claudeflags

import (
	"log"
	"os"
	"strings"
)

// --effort is the second flag every headless spawn must decide explicitly, and
// it fails the same way --permission-mode does: invisibly, and in the direction
// that costs money.
//
// A `claude -p` run that passes no --effort does not get "the cheap default" —
// it gets the CLI's own, which is xhigh. On Opus 5.5 xhigh thinks the most per
// turn of any setting, so every unpinned spawn here has been paying maximum
// reasoning tokens for work that never asked for it: a config probe that reads
// two files, a classification pass over a 16KB digest, a handoff note. Nothing
// fails, nothing logs, the bill is just larger than the work.
//
// So this file is the mirror of permission.go: one resolution, one validator,
// one escape hatch, and a per-site knob so an operator can move a single engine
// without touching the code. The DEFAULT per site lives with the engine (its
// DefaultEffort const), because how hard a run should think is a property of
// what that engine does, not of flag plumbing.
//
// The precedence, top to bottom:
//
//	the engine's own request/doc rung (owned by the engine — see e.g.
//	  phaserun.resolveEffort, which reads a phase doc's `**Effort:**` header)
//	SWARMERY_<ENGINE>_EFFORT   this site's knob
//	SWARMERY_EFFORT            the cross-site knob
//	the engine's DefaultEffort
//
// Only the bottom three live here; the top rung is engine policy and stays with
// the engine, exactly as the model ladder does.

// EffortEnv is the cross-site override, consulted when a spawn site's own knob
// is unset. The twin of ModeEnv.
const EffortEnv = "SWARMERY_EFFORT"

// OmitEffort is the escape hatch: setting a knob to this value passes NO
// --effort flag at all, restoring pre-pin behaviour (the CLI's xhigh). Kept
// reachable deliberately, so an operator comparing a run against its
// pre-pinning cost can reproduce the old shape exactly.
const OmitEffort = "off"

// FallbackEffort is the last resort: what a site gets when its own default is
// missing or unusable. Chosen as the middle of the range rather than the CLI's
// xhigh — a site that forgot to pin itself should cost the average, not the
// maximum, and the table test in spawndefaults_test.go exists so no site
// actually reaches this.
const FallbackEffort = "medium"

// validEfforts is the closed set `claude --effort` accepts. An unknown value
// must not reach the CLI: it rejects the flag and the spawn dies before the run
// starts, which would turn an operator typo into a dead phase — the same
// failure mode validModes guards against for --permission-mode.
//
// Keyed lower-case; every canonical value is already lower-case, so folding the
// input is enough.
var validEfforts = map[string]string{
	"low":    "low",
	"medium": "medium",
	"high":   "high",
	"xhigh":  "xhigh",
	"max":    "max",
}

// ValidEfforts lists the accepted values in ascending depth, for error messages
// and for the dashboard's picker. Returned as a fresh slice so a caller cannot
// reorder the source of truth.
func ValidEfforts() []string {
	return []string{"low", "medium", "high", "xhigh", "max"}
}

// NormalizeEffort canonicalises one authored value — a picker choice, a phase
// doc's `**Effort:**` header, an env knob — and reports whether it is known.
//
// It is deliberately SEPARATE from Effort: the rungs above the env knob belong
// to engines that must fail LOUDLY on a bad value (a typo in a phase doc is a
// defect in the plan and should name the document), whereas Effort's own rungs
// degrade with a warning because an operator's env typo must not wedge the
// daemon. Same parse, two different contracts — so the parse lives in one place
// and each caller keeps its own judgement, exactly as wsingest.ParseModel and
// phaserun.resolveModel split that responsibility for --model.
//
// "" normalises to ("", true): "no opinion" is a legitimate answer at every
// rung and means "fall through to the next one".
func NormalizeEffort(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", true
	}
	switch strings.ToLower(v) {
	case OmitEffort, "none", "default":
		// Spelled out rather than passed through: none of the three is a CLI
		// choice, but they are the words an operator reaches for when they mean
		// "whatever claude does on its own", which is exactly omission.
		return "", true
	}
	if canonical, ok := validEfforts[strings.ToLower(v)]; ok {
		return canonical, true
	}
	return "", false
}

// Effort resolves the reasoning depth for one spawn site: its own knob, then
// the cross-site knob, then the engine default handed in here.
//
// siteEnv names the site's knob (e.g. SWARMERY_PHASERUN_EFFORT); "" means the
// site has no knob of its own. def is the engine's DefaultEffort.
//
// "" means omit the flag — reachable only through the OmitEffort escape hatch,
// never through a typo: an unrecognised env value logs and falls back to def,
// because silently dropping a pinned engine to the CLI's xhigh is the exact
// regression this package exists to prevent.
func Effort(siteEnv, def string) string {
	raw, from := "", ""
	if siteEnv != "" {
		if v := strings.TrimSpace(os.Getenv(siteEnv)); v != "" {
			raw, from = v, siteEnv
		}
	}
	if raw == "" {
		if v := strings.TrimSpace(os.Getenv(EffortEnv)); v != "" {
			raw, from = v, EffortEnv
		}
	}
	if raw == "" {
		return defaultEffort(def)
	}
	if canonical, ok := NormalizeEffort(raw); ok {
		return canonical
	}
	log.Printf("warning: claudeflags: ignoring invalid %s=%q; using %s (valid: %s, or %q to omit the flag)",
		from, raw, defaultEffort(def), strings.Join(ValidEfforts(), ", "), OmitEffort)
	return defaultEffort(def)
}

// EffortArgs returns the `--effort <value>` pair to append to a headless
// spawn's argv, or nil when the flag must be omitted. The shape sites that
// build argv as a flat []string want; sites that build a runcore.Spec want the
// VALUE and call Effort directly (the twin split of PermissionModeArgs/Mode).
func EffortArgs(siteEnv, def string) []string {
	e := Effort(siteEnv, def)
	if e == "" {
		return nil
	}
	return []string{"--effort", e}
}

// EffortArgsWith is EffortArgs with one rung above the env knobs: an effort a
// caller set EXPLICITLY on the run (a runner struct's Effort field, filled by an
// operator request or a test), which must win over both knobs and the default.
//
// It exists because that rung used to bypass validation entirely. The five
// stdout-only engines each read their own field and interpolated it into a fixed
// argv slot, so `Effort: "off"` spawned `--effort ""` — the CLI rejects an empty
// flag value, which turned the documented escape hatch into a dead engine — and
// `Effort: "bogus"` reached the CLI just as literally. Routing the field through
// NormalizeEffort gives it the same two guarantees the env path has: "off" omits
// the pair, and an unknown value never reaches the process.
//
// An unusable override is DEMOTED, not fatal: it logs and falls through to the
// site's knobs, the same degradation Effort applies to a bad env value, because
// wedging an engine over a typo is the outcome this package exists to prevent.
func EffortArgsWith(override, siteEnv, def string) []string {
	raw := strings.TrimSpace(override)
	if raw == "" {
		return EffortArgs(siteEnv, def)
	}
	if canonical, ok := NormalizeEffort(raw); ok {
		if canonical == "" {
			return nil // the "off" hatch: pass no --effort at all
		}
		return []string{"--effort", canonical}
	}
	log.Printf("warning: claudeflags: ignoring invalid effort override %q; falling back to %s (valid: %s, or %q to omit the flag)",
		raw, siteEnv, strings.Join(ValidEfforts(), ", "), OmitEffort)
	return EffortArgs(siteEnv, def)
}

// defaultEffort guards the engine default itself. A site that hands in an empty
// or misspelled default would otherwise emit no flag and quietly inherit the
// CLI's xhigh — the failure this package exists to remove, arriving through the
// one path that skips validation.
func defaultEffort(def string) string {
	if canonical, ok := NormalizeEffort(def); ok && canonical != "" {
		return canonical
	}
	if strings.TrimSpace(def) != "" {
		log.Printf("warning: claudeflags: spawn site declares an unusable default effort %q; using %s", def, FallbackEffort)
	}
	return FallbackEffort
}
