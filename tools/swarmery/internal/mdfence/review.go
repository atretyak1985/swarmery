package mdfence

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReviewKind is the `kind:` value of a review round's block.
const ReviewKind = "review"

// ReviewRoundPlan is the round of the advisory plan-branch review; it sorts after
// every numbered phase-review round.
const ReviewRoundPlan = "plan"

// Review verdicts — the same three words as the reviewer's `VERDICT:` line.
const (
	ReviewPass         = "PASS"
	ReviewFail         = "FAIL"
	ReviewInconclusive = "INCONCLUSIVE"
)

// FindingTypes is the closed taxonomy of a finding's `type`, one per reviewer
// lens (plugins/core/agents/code-reviewer.md). A value outside it is kept
// verbatim, not rejected: judging it is a lint's job, not the parser's.
var FindingTypes = []string{"correctness", "silent-failure", "contract", "plan-conformance", "quality"}

// Review is one round's `kind: review` block, written by either review engine
// (the daemon's headless reviewer or run-plan's orchestrator) under the phase
// doc's `## Review` section — one block per round (D42).
type Review struct {
	Round   string // "1", "2", … or "plan"
	Verdict string // PASS | FAIL | INCONCLUSIVE
	// Complete is false when the review stopped before covering its scope (a turn
	// limit, a resumed subagent); ScopeLeft then names what it did not read.
	// Absent means true: a reviewer that says nothing about it finished.
	Complete       bool
	ScopeLeft      []string
	Findings       []Finding
	Resolves       []string // finding ids of earlier rounds this round confirms fixed
	IDs            []IDEvidence
	Drift          []Drift
	CheckedCallers []string // call sites the reviewer read for a changed signature
	// Source is who wrote the block: "daemon" (the review stage), "interactive"
	// (run-plan's orchestrator) or "backfill" (an operator, after the fact). ""
	// when absent — a reviewer copying the contract never writes it; the engine
	// that appends the block does.
	Source string
	// WrittenAt is the block's `written_at:` as written; "" when absent.
	WrittenAt string
	// Unparsable marks the daemon's prose fallback: the reviewer produced no
	// readable block, so the daemon wrapped its prose in a minimal one
	// (complete: false). A later stage must treat such a round as incomplete.
	Unparsable bool
	Line       int // 1-based document line of the block's opening fence
}

// Finding is one merge-blocking problem a review round reports.
type Finding struct {
	ID         string // stable within the doc, e.g. "R1-1"; a later round's resolves cites it
	Type       string // one of FindingTypes, verbatim
	File       string
	Line       int // 0 when not given
	Criterion  string
	TestCaught bool // an existing test fails because of it
	Evidence   string
	Note       string
}

// IDEvidence is an identifier the change relies on (a function, config key, flag,
// migration number, …) and where the reviewer saw it defined — or "missing".
type IDEvidence struct {
	ID       string
	Evidence string
}

// Drift is a gap between what the phase doc says and what the change does: a
// ticked criterion that does not hold, or a change outside the phase's scope. The
// plan round (verify-not-trust phase 12) writes the same entry as `{id, where,
// how}` — the decision/criterion id, the file (and line) the code departs at, and
// how; those keys are read into Criterion, File and Note.
type Drift struct {
	Criterion string
	File      string
	Note      string
}

// ParseReviews returns every `kind: review` block of doc in document order, and
// one ErrUnparsable ParseError per block that cannot be read (invalid yaml, a
// missing or unknown `verdict`, a missing or malformed `round`, a field of the
// wrong shape). Unknown keys are ignored. A doc with no review block returns
// (nil, nil).
func ParseReviews(doc string) ([]Review, []ParseError) {
	var (
		out  []Review
		errs []ParseError
	)
	for _, b := range kindBlocks(doc, ReviewKind) {
		r, perr := decodeReview(b)
		if perr != nil {
			errs = append(errs, *perr)
			continue
		}
		out = append(out, r)
	}
	return out, errs
}

// Latest is the review of the highest round: numbered rounds by value, "plan"
// after every number; on a tie the later block wins. nil for no reviews.
func Latest(reviews []Review) *Review {
	var best *Review
	for i := range reviews {
		if best == nil || roundRank(reviews[i].Round) >= roundRank(best.Round) {
			best = &reviews[i]
		}
	}
	return best
}

// roundRank orders rounds; ParseReviews has already rejected anything else.
func roundRank(round string) int {
	if strings.EqualFold(round, ReviewRoundPlan) {
		return int(^uint(0) >> 1)
	}
	n, _ := strconv.Atoi(round)
	return n
}

func decodeReview(b Block) (Review, *ParseError) {
	m, perr := decodeMapping(b)
	if perr != nil {
		return Review{}, perr
	}
	keys := mappingKeys(m)
	bad := func(n *yaml.Node, why string) *ParseError {
		line := 1
		if n != nil {
			line = n.Line
		}
		return &ParseError{Kind: ErrUnparsable, Line: docLine(b, line), Reason: why}
	}
	r := Review{Line: b.Start + 1, Complete: true}

	round, ok := scalar(keys["round"])
	switch {
	case !ok || round == "":
		return Review{}, bad(keys["round"], "missing round")
	case strings.EqualFold(round, ReviewRoundPlan):
		r.Round = ReviewRoundPlan
	default:
		n, err := strconv.Atoi(round)
		if err != nil || n < 1 {
			return Review{}, bad(keys["round"], fmt.Sprintf("round %q is neither a positive number nor %q", round, ReviewRoundPlan))
		}
		r.Round = strconv.Itoa(n)
	}

	verdict, ok := scalar(keys["verdict"])
	if !ok || verdict == "" {
		return Review{}, bad(keys["verdict"], "missing verdict")
	}
	switch v := strings.ToUpper(verdict); v {
	case ReviewPass, ReviewFail, ReviewInconclusive:
		r.Verdict = v
	default:
		return Review{}, bad(keys["verdict"], fmt.Sprintf("verdict %q is not PASS, FAIL or INCONCLUSIVE", verdict))
	}

	if n := keys["complete"]; !isNull(n) {
		var c bool
		if n.Kind != yaml.ScalarNode || n.Decode(&c) != nil {
			return Review{}, bad(n, fmt.Sprintf("complete %q is not true or false", n.Value))
		}
		r.Complete = c
	}

	r.Source, _ = scalar(keys["source"])
	r.WrittenAt, _ = scalar(keys["written_at"])
	if n := keys["unparsable"]; !isNull(n) {
		if n.Kind != yaml.ScalarNode || n.Decode(&r.Unparsable) != nil {
			return Review{}, bad(n, fmt.Sprintf("unparsable %q is not true or false", n.Value))
		}
	}

	for _, l := range []struct {
		key string
		dst *[]string
	}{{"scope_left", &r.ScopeLeft}, {"resolves", &r.Resolves}, {"checked_callers", &r.CheckedCallers}} {
		if *l.dst, perr = stringList(b, l.key, keys[l.key]); perr != nil {
			return Review{}, perr
		}
	}

	items, perr := mapList(b, "findings", keys["findings"])
	if perr != nil {
		return Review{}, perr
	}
	for _, it := range items {
		f := Finding{
			ID: it.str("id"), Type: it.str("type"), File: it.str("file"),
			Criterion: it.str("criterion"), Evidence: it.str("evidence"), Note: it.str("note"),
		}
		if n := it.keys["line"]; !isNull(n) {
			if n.Kind != yaml.ScalarNode || n.Decode(&f.Line) != nil {
				return Review{}, bad(n, fmt.Sprintf("findings: line %q is not a number", n.Value))
			}
		}
		if n := it.keys["test_caught"]; !isNull(n) {
			if n.Kind != yaml.ScalarNode || n.Decode(&f.TestCaught) != nil {
				return Review{}, bad(n, fmt.Sprintf("findings: test_caught %q is not true or false", n.Value))
			}
		}
		r.Findings = append(r.Findings, f)
	}

	if items, perr = mapList(b, "ids", keys["ids"]); perr != nil {
		return Review{}, perr
	}
	for _, it := range items {
		r.IDs = append(r.IDs, IDEvidence{ID: it.str("id"), Evidence: it.str("evidence")})
	}

	if items, perr = mapList(b, "drift", keys["drift"]); perr != nil {
		return Review{}, perr
	}
	for _, it := range items {
		r.Drift = append(r.Drift, Drift{
			Criterion: firstNonEmpty(it.str("criterion"), it.str("id")),
			File:      firstNonEmpty(it.str("file"), it.str("where")),
			Note:      firstNonEmpty(it.str("note"), it.str("how")),
		})
	}
	return r, nil
}

// firstNonEmpty is the first non-empty value.
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// mapItem is one entry of a list of flat maps.
type mapItem struct{ keys map[string]*yaml.Node }

// str is the entry's scalar value for key; "" when absent or not a scalar.
func (it mapItem) str(key string) string {
	s, _ := scalar(it.keys[key])
	return s
}

// mapList reads a list of flat maps (findings, ids, drift). Null or absent is an
// empty list; anything that is not a list of maps is unparsable at its line.
func mapList(b Block, key string, n *yaml.Node) ([]mapItem, *ParseError) {
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, n.Line), Reason: key + ": must be a list of maps"}
	}
	out := make([]mapItem, 0, len(n.Content))
	for _, c := range n.Content {
		if c.Kind != yaml.MappingNode {
			return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, c.Line), Reason: key + ": every entry must be a map"}
		}
		out = append(out, mapItem{keys: mappingKeys(c)})
	}
	return out, nil
}
