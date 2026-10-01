package phaserun

// The blocked re-run guard.
//
// A run that ends `blocked` found a reason it cannot do the phase: a dependency's
// code is not on its base, a premise of the doc does not match the repository, a
// date gate has not opened. Running it again while none of that has changed spends
// a full run to be told the same thing — and on 2026-09-27 that is what happened,
// several times over, to the same phases.
//
// So the moment a run settles blocked, stamp() records a FINGERPRINT of everything
// a re-run would see differently if the situation had moved. Start compares it to
// the fingerprint of the situation as it stands, and refuses when they are equal —
// for a while (the cooldown), and unless the operator says to run it anyway
// (`force`). Nothing here decides whether the phase CAN run; it only declines to
// repeat an experiment whose inputs are byte-identical to the one that failed.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// blockedCooldownEnv bounds how long an unchanged blocked phase is refused. A Go
// duration; "0" disables the guard entirely.
const blockedCooldownEnv = "SWARMERY_BLOCKED_RERUN_COOLDOWN"

// defaultBlockedCooldown is a day on purpose. Some blocks lift with nothing in the
// repo or the doc changing — a phase gated on a date (an `Earliest:` line), a
// dependency in another plan — and a daily routine has to be able to try again.
// Inside the day the phase stays refused; the lapse is what lets the retry through.
const defaultBlockedCooldown = 24 * time.Hour

// ErrBlockedUnchanged: the phase's last run ended blocked and nothing a re-run
// would see has changed since (409). Returned as a *BlockedUnchangedError, which
// errors.Is-matches this sentinel.
var ErrBlockedUnchanged = errors.New("phase is blocked and nothing has changed since")

// BlockedUnchangedError carries what the operator needs to decide: why the run
// blocked, since when, and when the refusal lapses on its own.
type BlockedUnchangedError struct {
	// Reason is the blocked run's own one-line reason (epic_phases.run_error).
	Reason string
	// Since is when that run ended (epic_phases.run_ended_at, RFC 3339).
	Since string
	// RetryAfter is Since plus the cooldown (RFC 3339): from then on the phase
	// re-runs without `force`, changed or not.
	RetryAfter string
}

func (e *BlockedUnchangedError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "no reason was recorded"
	}
	return fmt.Sprintf("this phase has been blocked since %s and nothing has changed since then "+
		"(same base commit, same dependency branches, same ticked criteria, same phase doc) — "+
		"a re-run would hit the same block: %s. Change one of them, wait until %s, "+
		"or force the re-run (Run anyway in the dashboard, `\"force\": true` on the run request).",
		e.Since, reason, e.RetryAfter)
}

func (e *BlockedUnchangedError) Is(target error) bool { return target == ErrBlockedUnchanged }

// blockedCooldown reads SWARMERY_BLOCKED_RERUN_COOLDOWN. Unset ⇒ 24h; "0" ⇒ 0,
// which disables the guard; an unusable value ⇒ the default with a warning, the
// same posture every other duration knob here takes — an operator's typo must not
// silently switch a guard off.
func blockedCooldown() time.Duration {
	raw := strings.TrimSpace(os.Getenv(blockedCooldownEnv))
	if raw == "" {
		return defaultBlockedCooldown
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Printf("warning: phaserun: ignoring invalid %s=%q: %v", blockedCooldownEnv, raw, err)
		return defaultBlockedCooldown
	}
	return d
}

// blockedFingerprint hashes the four things a re-run of a blocked phase would see
// differently if its situation had changed:
//
//   - base: the commit the next run would start from — the dependency tip when the
//     run would be stacked, the repo's branch tip otherwise. A merge landing on the
//     base branch moves it.
//   - ticked: the criteria ticked in the doc.
//   - depTips: the tips of the dependency run branches (sorted by resolveBase). A
//     dependency that gained a commit is a change even when the start point is not.
//   - doc: the phase doc's body WITHOUT its `## Forecast` and `## Completion
//     Report` sections. A blocked run rewrites both — it adds a posterior, and it
//     writes how far it got and what stopped it — so hashing them would make every
//     blocked run look like a change to itself and the guard would never fire.
//
// Each part is length-prefixed, so no two different inputs can concatenate to the
// same bytes.
func blockedFingerprint(base string, ticked int, depTips []string, doc string) string {
	h := sha256.New()
	part := func(label, value string) {
		fmt.Fprintf(h, "%s:%d:%s\n", label, len(value), value)
	}
	part("base", base)
	part("ticked", fmt.Sprintf("%d", ticked))
	part("deps", strings.Join(depTips, ","))
	part("doc", stableDocBody(doc))
	return hex.EncodeToString(h.Sum(nil))
}

// stableDocBody is the phase doc with its `## Forecast` and `## Completion Report`
// sections removed — heading line through to the next `## ` heading (or EOF).
//
// Headings are found with mdfence.ForEachLine, the daemon's one fence-aware line
// walker, so a `## Completion Report` QUOTED inside a fenced block — every phase
// doc's copy-paste agent prompt mentions both headings — is document text and
// stays in the hash. A fenced block inside a removed section goes with its
// section, as it should: the forecast IS a fenced yaml block.
//
// Trailing whitespace is dropped per line and at the end, so an editor that strips
// it (or a report stub appended with a different number of blank lines) is not a
// change to the phase.
func stableDocBody(doc string) string {
	lines := strings.Split(doc, "\n")
	// volatile[i] marks a heading that OPENS a removed section; heading[i] any
	// level-2 heading, which is what closes one.
	volatile := make(map[int]bool)
	heading := make(map[int]bool)
	mdfence.ForEachLine(doc, func(i int, line string) {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "## ") {
			return
		}
		heading[i] = true
		title := strings.TrimSpace(t[3:])
		if strings.EqualFold(title, "Forecast") || strings.EqualFold(title, "Completion Report") {
			volatile[i] = true
		}
	})

	kept := make([]string, 0, len(lines))
	skipping := false
	for i, line := range lines {
		if heading[i] {
			skipping = volatile[i]
		}
		if skipping {
			continue
		}
		kept = append(kept, strings.TrimRight(line, " \t\r"))
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

// currentFingerprint computes the fingerprint of the phase's situation RIGHT NOW,
// from the doc body the caller already holds and the base it already resolved.
//
// The tick count comes from the doc, through the same parser that defines
// epic_phases.checkboxes_done, and NOT from that column: wsingest rescans on a
// debounce, so at the instant a run ends the column can still hold the pre-tick
// count (stamp() documents the same window). A fingerprint stamped from the
// lagging column would differ from the one computed a second later for no reason
// but the rescan, and an unchanged phase would read as changed.
func currentFingerprint(base baseResolution, doc string) string {
	ticked, _ := wsingest.CountCheckboxes(doc)
	return blockedFingerprint(base.fingerprintBase(), ticked, base.DepTips, doc)
}

// fingerprintForStamp computes the fingerprint to record when a run settles
// blocked, or "" when it cannot be computed — and "" is stored as NULL, which
// never refuses anything. Failing open is the point: the guard exists to save a
// run, never to strand a phase because its own bookkeeping could not be read.
//
// It re-loads the row and re-resolves the base rather than reusing what Start
// saw: hours have passed, and the fingerprint has to describe the situation the
// NEXT Start will compare against, which is the one that exists now.
//
// A base that does not resolve into a single start point (the dependency branches
// diverged while the run was going) still fingerprints: resolveBase hands back
// the base tip and the dependency tips alongside that error, and those are what
// the hash needs.
func (s *Service) fingerprintForStamp(phaseID int64, docPath string) string {
	if docPath == "" {
		return ""
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d blocked fingerprint skipped, doc %q unreadable: %v", phaseID, docPath, err)
		return ""
	}
	info, err := s.loadPhase(phaseID)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d blocked fingerprint skipped: %v", phaseID, err)
		return ""
	}
	if info.ProjectPath == "" {
		return ""
	}
	info.RepoRoot, err = s.runRoot(info)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d blocked fingerprint skipped, run repository unresolved: %v", phaseID, err)
		return ""
	}
	base, err := s.resolveRunBase(info)
	var unmerged *DepsUnmergedError
	if err != nil && !errors.As(err, &unmerged) {
		log.Printf("warning: phaserun: phase=%d blocked fingerprint skipped, run base unresolved: %v", phaseID, err)
		return ""
	}
	return currentFingerprint(base, string(body))
}

// checkBlockedUnchanged is the guard itself: nil admits the run, a
// *BlockedUnchangedError refuses it. It admits whenever it cannot PROVE the
// refusal — every condition below must hold for the run to be turned away.
//
//   - the last run ended blocked, and a fingerprint was recorded for it;
//   - the request did not ask to run anyway;
//   - the guard is on (cooldown > 0) and the block is younger than the cooldown —
//     an end time that does not parse has no provable age, so it admits;
//   - the situation's fingerprint is the recorded one.
func (s *Service) checkBlockedUnchanged(info phaseInfo, base baseResolution, doc string, force bool) error {
	if force || info.RunState != "blocked" || info.RunBlockedFingerprint == "" {
		return nil
	}
	cooldown := blockedCooldown()
	if cooldown <= 0 {
		return nil
	}
	since, err := time.Parse(time.RFC3339, info.RunEndedAt)
	if err != nil {
		return nil
	}
	retryAfter := since.Add(cooldown)
	// The service clock, not the wall clock: run_ended_at was stamped from it.
	if !s.clock().Before(retryAfter) {
		return nil
	}
	if currentFingerprint(base, doc) != info.RunBlockedFingerprint {
		return nil
	}
	return &BlockedUnchangedError{
		Reason:     info.RunError,
		Since:      info.RunEndedAt,
		RetryAfter: retryAfter.UTC().Format(time.RFC3339),
	}
}
