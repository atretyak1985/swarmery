// Package route scores how complex a unit of work looks before any model runs
// and turns that score into a tier and a pick: model, effort and — for board
// cards — playbook.
//
// The package is pure and deterministic: Decide reads only the Signals and the
// Policy it is handed, calls no model, touches no database or process, and
// returns the same Decision for the same input every time. The only I/O here
// is LoadPolicy reading an optional policy file.
//
// A Decision is a suggestion, never an override. It does not know whether the
// operator already chose a model, an effort or a playbook; callers own that
// precedence and must apply a Decision only where no explicit choice exists.
//
// review-heavy is never auto-selected: Decide only ever yields standard or
// plan-first, the same rule internal/dispatch's autoProfile follows, because
// escalating to strict verification on a heuristic would spend the verify
// budget on noise. It stays a human opt-in.
package route

import (
	"fmt"
	"strconv"
	"strings"
)

// Surface is where the work runs.
type Surface string

const (
	// SurfaceDispatch is a board card dispatched by internal/dispatch.
	SurfaceDispatch Surface = "dispatch"
	// SurfacePhaseRun is a plan phase run by internal/phaserun.
	SurfacePhaseRun Surface = "phaserun"
)

// Tier names, lowest to highest.
const (
	TierS  = "S"
	TierM  = "M"
	TierL  = "L"
	TierXL = "XL"
)

// Playbooks the router may pick. review-heavy is named only so it can be refused.
const (
	PlaybookStandard    = "standard"
	PlaybookPlanFirst   = "plan-first"
	PlaybookReviewHeavy = "review-heavy"
)

// maxScore is the top of the 0..100 score range.
const maxScore = 100

// Signals are the cheap, pre-run facts the score is built from.
type Signals struct {
	Surface      Surface
	PromptBytes  int      // card prompt length; 0 for phases
	FileScope    int      // declared files/globs; -1 = unknown
	Areas        int      // distinct dirs at depth 2; -1 = unknown
	RiskPaths    []string // matched risky paths (migrations, auth, api contract, lockfiles)
	Deps         int
	ForecastSize string  // phase prior size_band XS|S|M|L|XL; "" = none
	HistFailRate float64 // recent failure rate for the same project/areas
	HistSamples  int
}

// Decision is the router's answer for one unit of work.
type Decision struct {
	Score    int
	Tier     string   // S | M | L | XL
	Model    string   // alias: haiku | sonnet | opus
	Effort   string   // low | medium | high | xhigh | max
	Playbook string   // standard | plan-first ("" on phaserun)
	Reasons  []string // one line per signal, e.g. "file_scope=7 (+20)"; the (±N) parts sum to Score
}

// Score sums the policy's points for each signal and clamps the total to
// 0..100. Every signal contributes exactly one reason line ending in a signed
// "(+N)", zero-point ones included, so an operator can see what was considered
// as well as what counted; a clamp adds one more line ("(-N)" at the top). The
// signed numbers in the reasons always add up to the returned score.
func Score(s Signals, p Policy) (int, []string) {
	w := p.Weights
	total := 0
	reasons := make([]string, 0, 8)
	add := func(pts int, what string) {
		total += pts
		reasons = append(reasons, fmt.Sprintf("%s (%+d)", what, pts)) // "(+10)", "(+0)"; "(-10)" only from a hand-built policy
	}

	// A phase's own size forecast is a better signal than prompt length, so a
	// usable one replaces PromptBytes rather than adding to it.
	size := strings.ToUpper(strings.TrimSpace(s.ForecastSize))
	sizePts, sizeOK := w.ForecastSize.lookup(size)
	switch {
	case size == "":
		add(0, "forecast_size=none")
	case !sizeOK:
		add(0, fmt.Sprintf("forecast_size=%q unknown, ignored", s.ForecastSize))
	default:
		add(sizePts, "forecast_size="+size)
	}
	if sizeOK {
		add(0, fmt.Sprintf("prompt_bytes=%d ignored: forecast_size set", s.PromptBytes))
	} else {
		add(w.PromptBytes.points(s.PromptBytes), fmt.Sprintf("prompt_bytes=%d", s.PromptBytes))
	}

	add(w.FileScope.points(s.FileScope), countReason("file_scope", s.FileScope))
	add(w.Areas.points(s.Areas), countReason("areas", s.Areas))

	if len(s.RiskPaths) > 0 {
		add(w.RiskPaths, "risk_paths="+strings.Join(s.RiskPaths, ","))
	} else {
		add(0, "risk_paths=none")
	}

	if s.Deps > 0 {
		add(w.Deps, fmt.Sprintf("deps=%d", s.Deps))
	} else {
		add(0, fmt.Sprintf("deps=%d", s.Deps))
	}

	rate, minRate := fmtRate(s.HistFailRate), fmtRate(w.HistFailRateMin)
	switch {
	case s.HistSamples < w.HistMinSamples:
		add(0, fmt.Sprintf("history: n<%d, ignored (n=%d)", w.HistMinSamples, s.HistSamples))
	case s.HistFailRate >= w.HistFailRateMin:
		add(w.HistFail, fmt.Sprintf("history: fail_rate=%s ≥ %s (n=%d)", rate, minRate, s.HistSamples))
	default:
		add(0, fmt.Sprintf("history: fail_rate=%s < %s (n=%d)", rate, minRate, s.HistSamples))
	}

	switch {
	case total > maxScore:
		reasons = append(reasons, fmt.Sprintf("clamp: %d → %d (-%d)", total, maxScore, total-maxScore))
		total = maxScore
	case total < 0: // only reachable with a hand-built policy; LoadPolicy rejects negative points
		reasons = append(reasons, fmt.Sprintf("clamp: %d → 0 (+%d)", total, -total))
		total = 0
	}
	return total, reasons
}

// Decide scores the signals and returns the tier's pick. The playbook is set
// on the dispatch surface only, and is never review-heavy.
func Decide(s Signals, p Policy) Decision {
	score, reasons := Score(s, p)
	tier, pick := p.tierFor(score)
	d := Decision{
		Score:   score,
		Tier:    tier,
		Model:   pick.Model,
		Effort:  pick.Effort,
		Reasons: reasons,
	}
	if s.Surface == SurfaceDispatch {
		d.Playbook = pick.Playbook
		// LoadPolicy already refuses review-heavy; this guards a policy built by
		// hand, so the rule holds for every caller, not only file-loaded ones.
		if strings.EqualFold(strings.TrimSpace(d.Playbook), PlaybookReviewHeavy) {
			d.Playbook = PlaybookPlanFirst
		}
	}
	return d
}

// tierFor maps a score onto the cut-offs: S < M ≤ … < L ≤ … < XL ≤ score.
func (p Policy) tierFor(score int) (string, Pick) {
	switch c := p.Cutoffs; {
	case score >= c.XL:
		return TierXL, p.Tiers.XL
	case score >= c.L:
		return TierL, p.Tiers.L
	case score >= c.M:
		return TierM, p.Tiers.M
	default:
		return TierS, p.Tiers.S
	}
}

// points scores one integer signal against the ladder.
func (l Ladder) points(v int) int {
	if v < 0 {
		return l.Unknown
	}
	for _, b := range l.Bands {
		if v <= b.Max {
			return b.Points
		}
	}
	return l.Else
}

// lookup returns the points for an upper-cased size band, and whether it is one.
func (sp SizePoints) lookup(band string) (int, bool) {
	switch band {
	case "XS":
		return sp.XS, true
	case "S":
		return sp.S, true
	case "M":
		return sp.M, true
	case "L":
		return sp.L, true
	case "XL":
		return sp.XL, true
	}
	return 0, false
}

// countReason renders a count signal, spelling out the -1 "unknown" sentinel.
func countReason(name string, v int) string {
	if v < 0 {
		return fmt.Sprintf("%s=%d unknown", name, v)
	}
	return fmt.Sprintf("%s=%d", name, v)
}

// fmtRate prints a rate with the fewest digits that round-trip, so a 0.299
// never displays as a misleading "0.30".
func fmtRate(r float64) string {
	return strconv.FormatFloat(r, 'f', -1, 64)
}
