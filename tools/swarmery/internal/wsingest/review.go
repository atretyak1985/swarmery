// The independent code review of a phase run (migration 0106). A phase doc opts in
// with a `**Review:** on` header line; after its run settles, internal/phaserun
// spawns a read-only reviewer, and every verdict — PASS and FAIL alike — is written
// back INTO the doc (md = truth) as a `kind: review` block under `## Review`
// (D42, migration 0122); a FAIL's block is what the fix re-run reads first
// (phaserun.ReviewFixNote).

package wsingest

import (
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
)

// ReviewOn / ReviewOff are the values epic_phases.review_mode stores.
const (
	ReviewOn  = "on"
	ReviewOff = "off"
)

// docReviewRe matches the header line `**Review:** on`. It sits beside docVerifyRe
// (epics.go) and is scanned under the same header bound.
var docReviewRe = regexp.MustCompile(`(?i)^\s*\*\*Review:\*\*\s*(.+?)\s*$`)

// ParseDocReview reports whether the phase doc's header block opts into the review
// stage. Accepted spellings for on: on, yes, true; for off: off, no, false, none.
//
// Bounded by docStatusHeaderLines and stopping at the first `## ` section, exactly
// like ParseDocVerify: a `**Review:**` line quoted inside an agent prompt further
// down describes someone else's phase. Absent and unrecognized both mean off — a
// reviewer nobody asked for costs a session on every run.
func ParseDocReview(text string) (on bool) {
	lines := strings.Split(text, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		m := docReviewRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch v := strings.ToLower(strings.TrimSpace(m[1])); v {
		case ReviewOn, "yes", "true":
			return true
		case ReviewOff, "no", "false", "none":
			return false
		default:
			log.Printf("warn: wsingest: unrecognized **Review:** %q in a phase doc header — review stays off", v)
			return false
		}
	}
	return false
}

// ReviewFindingsMax bounds what AppendReviewFindings writes into the doc for one
// round. The doc is handed whole to the fix run's prompt (an argv), so it cannot
// grow without bound; the full reviewer output stays in phase_reviews.findings.
const ReviewFindingsMax = 32 << 10 // 32 KB

// reviewFieldMax bounds one finding's note or evidence inside a written block.
const reviewFieldMax = 2 << 10

// ErrFindingsEmpty is AppendReviewFindings' refusal of a FAIL round with neither
// a block nor any prose; the doc is left untouched.
var ErrFindingsEmpty = errors.New("review findings are empty")

// reviewSectionHeading is the section every `kind: review` block lives under (D42).
const reviewSectionHeading = "## Review"

var (
	reviewSectionRe   = regexp.MustCompile(`^##\s+Review\s*$`)
	forecastHeadingRe = regexp.MustCompile(`(?i)^##\s+Forecast\s*$`)
)

// ReviewBlock is one review round as the daemon's review stage hands it to
// AppendReviewFindings.
type ReviewBlock struct {
	// Review is the reviewer's own `kind: review` block (mdfence.ParseReviews over
	// its output); nil when it produced none that parses.
	Review *mdfence.Review
	// Verdict is the reviewer's VERDICT line — PASS | FAIL | INCONCLUSIVE. It is the
	// verdict of record: a block disagreeing with it is overwritten, not trusted.
	Verdict string
	// Prose is the reviewer's whole output. Written (quoted) only on the fallback
	// path, when Review is nil.
	Prose string
}

// reviewYAML is the written block, in the contract's key order
// (plugins/core/skills/workspace-plans/resources/plan-format.md, "## Review").
type reviewYAML struct {
	Kind           string        `yaml:"kind"`
	Round          int           `yaml:"round"`
	Verdict        string        `yaml:"verdict"`
	Complete       bool          `yaml:"complete"`
	ScopeLeft      []string      `yaml:"scope_left"`
	Findings       []findingYAML `yaml:"findings"`
	Resolves       []string      `yaml:"resolves"`
	IDs            []idYAML      `yaml:"ids"`
	Drift          []driftYAML   `yaml:"drift"`
	CheckedCallers []string      `yaml:"checked_callers"`
	Unparsable     bool          `yaml:"unparsable,omitempty"`
	Source         string        `yaml:"source"`
	WrittenAt      string        `yaml:"written_at"`
}

type findingYAML struct {
	ID         string `yaml:"id"`
	Type       string `yaml:"type"`
	File       string `yaml:"file,omitempty"`
	Line       int    `yaml:"line,omitempty"`
	Criterion  string `yaml:"criterion,omitempty"`
	TestCaught bool   `yaml:"test_caught"`
	Evidence   string `yaml:"evidence,omitempty"`
	Note       string `yaml:"note,omitempty"`
}

type idYAML struct {
	ID       string `yaml:"id"`
	Evidence string `yaml:"evidence"`
}

type driftYAML struct {
	Criterion string `yaml:"criterion,omitempty"`
	File      string `yaml:"file,omitempty"`
	Note      string `yaml:"note,omitempty"`
}

// capField trims one free-text value to reviewFieldMax. A ``` inside it would
// close the block's fence early in a markdown reader, so it is neutralised.
func capField(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "```", "'''"))
	if len(s) <= reviewFieldMax {
		return s
	}
	return strings.TrimSpace(s[:reviewFieldMax]) + " […]"
}

// RenderReviewBlock renders the round as a fenced ```yaml block. The daemon owns
// round, verdict, source and written_at; everything else is the reviewer's.
// Finding ids the reviewer left out are filled as R<round>-<n>.
func RenderReviewBlock(b ReviewBlock, round int, now time.Time) (string, error) {
	y := reviewYAML{
		Kind: mdfence.ReviewKind, Round: round, Verdict: strings.ToUpper(strings.TrimSpace(b.Verdict)),
		Complete: true, Source: ReviewSourceDaemon, WrittenAt: now.UTC().Format(time.RFC3339),
	}
	if r := b.Review; r != nil {
		y.Complete, y.ScopeLeft, y.Resolves, y.CheckedCallers = r.Complete, r.ScopeLeft, r.Resolves, r.CheckedCallers
		for i, f := range r.Findings {
			id := f.ID
			if strings.TrimSpace(id) == "" {
				id = fmt.Sprintf("R%d-%d", round, i+1)
			}
			y.Findings = append(y.Findings, findingYAML{
				ID: id, Type: f.Type, File: f.File, Line: f.Line, Criterion: f.Criterion,
				TestCaught: f.TestCaught, Evidence: capField(f.Evidence), Note: capField(f.Note),
			})
		}
		for _, e := range r.IDs {
			y.IDs = append(y.IDs, idYAML{ID: e.ID, Evidence: e.Evidence})
		}
		for _, d := range r.Drift {
			y.Drift = append(y.Drift, driftYAML{Criterion: d.Criterion, File: d.File, Note: capField(d.Note)})
		}
	} else {
		// The fallback: the reviewer wrote prose and no readable block. The round
		// is recorded — verdict and all — but as incomplete and unparsable, so a
		// later stage never counts it as a whole review.
		y.Complete, y.Unparsable = false, true
		y.ScopeLeft = []string{"unparsable: the reviewer produced no readable kind: review block; its own words follow the block"}
	}
	// nil lists render as `[]`, never `null`: the contract keeps every key.
	for _, l := range []*[]string{&y.ScopeLeft, &y.Resolves, &y.CheckedCallers} {
		if *l == nil {
			*l = []string{}
		}
	}
	if y.Findings == nil {
		y.Findings = []findingYAML{}
	}
	if y.IDs == nil {
		y.IDs = []idYAML{}
	}
	if y.Drift == nil {
		y.Drift = []driftYAML{}
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(y); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return "```yaml\n" + strings.TrimRight(buf.String(), "\n") + "\n```", nil
}

// AppendReviewFindings records one review round in the phase doc at docPath as a
// `kind: review` block (D42) under the doc's `## Review` section — appended after
// the section's earlier rounds, which are never rewritten. A doc without the
// section gets one, immediately before `## Forecast` (or, without a forecast,
// before `## Completion Report`, else at the end). Written atomically, keeping
// the file's mode.
//
// The block is the reviewer's own, re-rendered with the daemon's round, verdict,
// `source: daemon` and `written_at`, so it always parses (mdfence.ParseReviews).
// When the reviewer produced no readable block, the fallback is a minimal block
// marked `unparsable: true` / `complete: false` followed by the reviewer's prose,
// quoted so a `- [ ]` or a heading inside it stays inert, and cut at
// ReviewFindingsMax.
func AppendReviewFindings(docPath string, block ReviewBlock, round int, now time.Time) error {
	prose := strings.TrimSpace(block.Prose)
	// A FAIL with neither a block nor prose names nothing to fix; any other
	// verdict is still a round worth recording.
	if block.Review == nil && prose == "" && strings.EqualFold(strings.TrimSpace(block.Verdict), mdfence.ReviewFail) {
		return ErrFindingsEmpty
	}
	if round < 1 {
		round = 1
	}
	rendered, err := RenderReviewBlock(block, round, now)
	if err != nil {
		return err
	}
	if block.Review == nil && prose != "" {
		if len(prose) > ReviewFindingsMax {
			prose = strings.TrimSpace(prose[:ReviewFindingsMax]) + "\n\n[… findings truncated; the full text is in the review record]"
		}
		rendered += "\n\n" + quoteFeedback(prose)
	}
	st, err := os.Stat(docPath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	return writeFileAtomic(docPath, []byte(insertReviewBlock(string(body), rendered)), st.Mode().Perm())
}

// insertReviewBlock appends block to the end of the doc's `## Review` section
// (outside fences), creating the section when the doc has none. Pure; unit-tested.
func insertReviewBlock(text, block string) string {
	lines := strings.Split(text, "\n")
	section, end, forecast, report := -1, -1, -1, -1
	forEachLineOutsideFences(text, func(i int, line string) {
		switch {
		case section < 0 && reviewSectionRe.MatchString(line):
			section = i
		case section >= 0 && end < 0 && strings.HasPrefix(line, "## "):
			end = i
		}
		if forecast < 0 && forecastHeadingRe.MatchString(line) {
			forecast = i
		}
		if report < 0 && completionHeadingRe.MatchString(line) {
			report = i
		}
	})
	if section >= 0 {
		if end < 0 {
			return joinBlock(text, block) + "\n"
		}
		head := strings.Join(lines[:end], "\n")
		tail := strings.Join(lines[end:], "\n")
		return joinBlock(head, block) + "\n\n" + tail
	}
	newSection := reviewSectionHeading + "\n\n" + block
	at := forecast
	if at < 0 {
		at = report
	}
	if at < 0 {
		return joinBlock(text, newSection) + "\n"
	}
	head := strings.Join(lines[:at], "\n")
	tail := strings.Join(lines[at:], "\n")
	return joinBlock(head, newSection) + "\n\n" + tail
}

// NextReviewRound is the round the next review of the doc at docPath writes: one
// past both the highest numbered round already in the doc and min. Reading the
// doc keeps rounds unique when a later run of the phase is reviewed again after
// a PASS reset the daemon's fix counter. An unreadable doc yields min.
func NextReviewRound(docPath string, min int) int {
	next := min
	body, err := os.ReadFile(docPath)
	if err != nil {
		return next
	}
	rs, _ := mdfence.ParseReviews(string(body))
	for _, r := range rs {
		if n, err := strconv.Atoi(r.Round); err == nil && n+1 > next {
			next = n + 1
		}
	}
	return next
}
