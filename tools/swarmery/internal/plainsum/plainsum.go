// Package plainsum retells plan markdown in plain words: what a phase is for
// and what it closes, what a whole plan is about, and what a finished phase or
// plan actually delivered. The source docs are written for executors — paths,
// SC ids, agent prompts — so the Plans page pairs them with a short version a
// person can read in half a minute.
//
// The markdown stays the source of truth: a retelling is a cache entry keyed
// by the sha256 of the exact source text (plus kind, language and
// promptVersion), generated once by a headless `claude` run and served from
// the plain_summaries table after that. An edited doc misses the cache and is
// retold again on the next view.
package plainsum

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Kind is what is being retold.
type Kind string

const (
	// KindPhase is one phase doc: what the phase is for and what it changes.
	KindPhase Kind = "phase"
	// KindPhaseReport is one phase's Completion Report: what was delivered.
	KindPhaseReport Kind = "phase-report"
	// KindPlan is the plan README (+ spec): what the plan is and why.
	KindPlan Kind = "plan"
	// KindPlanReport is the finished plan's summary and phase reports.
	KindPlanReport Kind = "plan-report"
)

// ParseKind validates a wire value.
func ParseKind(s string) (Kind, bool) {
	switch k := Kind(s); k {
	case KindPhase, KindPhaseReport, KindPlan, KindPlanReport:
		return k, true
	}
	return "", false
}

// ParseLang narrows a UI locale to a language the prompt knows; anything that
// is not Ukrainian is retold in English.
func ParseLang(s string) string {
	if strings.HasPrefix(strings.ToLower(s), "uk") {
		return "uk"
	}
	return "en"
}

// promptVersion is part of the cache key: bump it when Prompt changes so old
// retellings are not served under the new instructions.
const promptVersion = "1"

// maxSource bounds the text sent to the model. Plan docs are rarely near it; a
// plan-report over many phases can be, and its tail matters least.
const maxSource = 120_000

// ErrEmpty means there is nothing to retell (an empty source).
var ErrEmpty = errors.New("nothing to retell")

// authoredHeadingRe matches the planner-written section's heading. The
// canonical name is "In plain words" (core's plan format); the Ukrainian name
// is accepted for docs written by hand in that language.
var (
	authoredHeadingRe = regexp.MustCompile(`(?mi)^##\s+(?:In plain words|Простими словами)\s*$`)
	nextHeadingRe     = regexp.MustCompile(`(?m)^##\s`)
)

// Authored returns the body of the doc's own `## In plain words` section (to
// the next `## ` heading or EOF), trimmed; "" when the doc has none. When a
// planner wrote it, the dashboard shows it as-is and no model run is needed.
func Authored(doc string) string {
	loc := authoredHeadingRe.FindStringIndex(doc)
	if loc == nil {
		return ""
	}
	body := doc[loc[1]:]
	if next := nextHeadingRe.FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}
	return strings.TrimSpace(body)
}

// Key is the cache key of one retelling.
func Key(kind Kind, lang, source string) string {
	sum := sha256.Sum256([]byte(promptVersion + "\x00" + string(kind) + "\x00" + lang + "\x00" + source))
	return hex.EncodeToString(sum[:])
}

// Runner turns a prompt into the model's text answer.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// Service caches and generates retellings. The zero value is not usable: DB
// and Runner are required.
type Service struct {
	DB     *sql.DB
	Runner Runner
	// Model is recorded beside each row; informational only.
	Model string

	mu       sync.Mutex
	inflight map[string]*flight
}

type flight struct {
	done chan struct{}
	text string
	err  error
}

// Cached returns the stored retelling for key, if any.
func (s *Service) Cached(key string) (string, bool, error) {
	var text string
	err := s.DB.QueryRow(`SELECT text FROM plain_summaries WHERE key = ?`, key).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return text, true, nil
}

// Generate returns the retelling of source, from the cache when present,
// otherwise by running the model once — concurrent callers for the same key
// share that one run.
func (s *Service) Generate(ctx context.Context, kind Kind, lang, source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", ErrEmpty
	}
	key := Key(kind, lang, source)
	if text, ok, err := s.Cached(key); err != nil || ok {
		return text, err
	}

	s.mu.Lock()
	if s.inflight == nil {
		s.inflight = map[string]*flight{}
	}
	if f, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		select {
		case <-f.done:
			return f.text, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	s.inflight[key] = f
	s.mu.Unlock()

	// The run is detached from the request: a closed tab must not waste a
	// half-finished generation the next viewer would have reused.
	f.text, f.err = s.run(kind, lang, source, key)
	s.mu.Lock()
	delete(s.inflight, key)
	s.mu.Unlock()
	close(f.done)
	return f.text, f.err
}

func (s *Service) run(kind Kind, lang, source, key string) (string, error) {
	out, err := s.Runner.Run(context.Background(), Prompt(kind, lang, source))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(out)
	if text == "" {
		return "", errors.New("the model returned an empty retelling")
	}
	_, err = s.DB.Exec(
		`INSERT INTO plain_summaries (key, kind, lang, text, model, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET text = excluded.text, model = excluded.model, created_at = excluded.created_at`,
		key, string(kind), lang, text, s.Model, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("store retelling: %w", err)
	}
	return text, nil
}

// Prompt builds the instruction for one retelling. Pure; unit-tested.
func Prompt(kind Kind, lang, source string) string {
	if len(source) > maxSource {
		source = source[:maxSource] + "\n\n[…truncated]"
	}
	var task string
	switch kind {
	case KindPhase:
		task = `The document below is one phase of a software work plan, written for the engineer or agent who will carry it out.
Explain this phase to a person who did not write it and does not know the codebase:
- what this phase is for and why it is needed (what problem or gap it closes);
- what will be different once it is done — in terms of what a user or the team gets, not file names;
- if it matters, what it depends on or what it unblocks.`
	case KindPhaseReport:
		task = `The text below is the completion report an engineer or agent wrote after finishing one phase of a software work plan.
Retell it for a person who was not involved:
- what was actually done and what it gives in practice;
- whether anything was left out, postponed or turned out differently than planned;
- anything the reader should know or do next, if the report says so.`
	case KindPlan:
		task = `The document below is the overview of a software work plan (and possibly its specification), written for the engineers or agents who will carry it out.
Explain the plan to a person who did not write it and does not know the codebase:
- what this plan is about and why it exists — the problem it solves;
- what will be different when the whole plan is done;
- how the work is split up, in one or two sentences (no phase-by-phase list of file names);
- the main risks or open questions, if any are stated.`
	case KindPlanReport:
		task = `The text below is the final summary and the per-phase completion reports of a finished software work plan.
Retell the outcome for a person who was not involved:
- what the plan delivered overall and what it gives in practice;
- what was left out, postponed or changed along the way;
- what remains to be done, if anything.`
	}
	language := "English"
	if lang == "uk" {
		language = "Ukrainian"
	}
	return task + `

Rules:
- Write in ` + language + `, in plain, natural language — the way you would explain it to a smart colleague from another team.
- No jargon, no file paths, no code, no identifiers like SC-3 or function names. If a technical term is unavoidable, explain it in a few words.
- Be concise: one short opening paragraph, then at most a few short paragraphs or bullet points. Under 200 words.
- Say only what the text supports; do not invent details.
- Output only the explanation in Markdown, with no heading and no preamble.

<document>
` + source + `
</document>
`
}
