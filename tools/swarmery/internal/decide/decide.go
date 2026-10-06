// Package decide is the local decision classifier ("System One"): at a fork
// whose answer is yes/no, one-of-N or a 1–5 score, it asks a cheap classifier
// for a typed answer with a confidence, and keeps the big model for the work.
//
// THE RULE OF THIS PACKAGE: it works AROUND Claude runs, never inside them. It
// may inform what the control plane does after a run has ended (D1: continue,
// notify, stamp blocked), add analytics labels to finished sessions (D2), and
// label why a scored phase run diverged from its forecast (D3, analytics). It
// is never consulted by a hook, never prunes context, never picks a tool, a
// model or an effort. Every question defaults to `shadow` (decide and log, do
// not act), and with no backend configured every entry point is a no-op, so the
// daemon behaves exactly as it did before this package existed.
//
// Backends, in order: rules (an answer the caller already knows) → local (an
// OpenAI-compatible server on this machine or LAN, SWARMERY_DECIDE_URL) →
// claude (headless haiku, only when the question allows leaving the machine
// AND SWARMERY_DECIDE_CLAUDE is on — off by default). Every call is written to
// the decisions table (migration 0083) so accuracy is measurable.
package decide

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind is the shape of a question's answer.
type Kind string

const (
	// KindYesNo is the design's `noul` kind: the answer is "yes" or "no".
	KindYesNo Kind = "noul"
	// KindChoice: exactly one of Question.Options.
	KindChoice Kind = "choice"
	// KindScore: an integer 1..5, answered as its decimal string.
	KindScore Kind = "score"
)

// Mode is how far a question's answer is trusted.
type Mode string

const (
	// ModeOff: the question is never asked.
	ModeOff Mode = "off"
	// ModeShadow: asked and logged; the caller ignores the answer. The default.
	ModeShadow Mode = "shadow"
	// ModeActive: the caller acts on an answer at or above the threshold.
	ModeActive Mode = "active"
)

// ParseMode reads a mode, reporting ok=false for anything unknown.
func ParseMode(s string) (Mode, bool) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case ModeOff, ModeShadow, ModeActive:
		return m, true
	}
	return "", false
}

// Backend names, as stored in decisions.backend.
const (
	BackendRules  = "rules"
	BackendLocal  = "local"
	BackendClaude = "claude"
)

// Question is one typed question.
type Question struct {
	ID     string // stable question id, e.g. "d1.run_end"
	Kind   Kind
	Prompt string   // what is being asked, in plain words
	Opts   []string // the choices (KindChoice); derived for the other kinds
	// Input is the evidence, already truncated by the caller (the last assistant
	// message for D1, a compact digest for D2 — never a full transcript).
	Input string
	// AllowRemote permits the claude backend for this question. Even then the
	// backend is used only when it is enabled and local is unreachable.
	AllowRemote bool
	// RuleAnswer is an answer the caller's deterministic rules already know;
	// when set (and valid) the rules backend returns it with confidence 1.
	RuleAnswer string
	// RuleID names the rule that gave RuleAnswer (D2: "R0" … "R5"); it rides on
	// the answer so the eval can report each rule on its own. Not stored.
	RuleID string
	// Subject/SessionUUID/RuleValue are stored with the decision row.
	Subject     string
	SessionUUID string
	RuleValue   string
}

// Options is the closed answer set of the question.
func (q Question) Options() []string {
	switch q.Kind {
	case KindYesNo:
		return []string{"yes", "no"}
	case KindScore:
		return []string{"1", "2", "3", "4", "5"}
	}
	return q.Opts
}

// canonical maps a raw answer onto its option (case-insensitive), ok=false
// when it is not one of them.
func (q Question) canonical(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	for _, o := range q.Options() {
		if strings.EqualFold(o, raw) {
			return o, true
		}
	}
	return "", false
}

// Answer is a classifier's reply.
type Answer struct {
	Value string
	// Probs is the probability of each option the backend could estimate.
	Probs map[string]float64
	// Confidence is Probs[Value] when Calibrated; a self-report otherwise.
	Confidence float64
	// Calibrated is true only when the confidence came from token logprobs
	// (or the rules). An uncalibrated answer never clears an active threshold.
	Calibrated bool
	Backend    string
	// Rule is the id of the rule that answered (Question.RuleID); "" for any
	// other backend.
	Rule       string
	Latency    time.Duration
	DecisionID int64
}

// Backend answers questions.
type Backend interface {
	Name() string
	Ask(ctx context.Context, q Question) (Answer, error)
}

// ErrNotConfigured is returned by Decide when no backend can answer.
var ErrNotConfigured = errors.New("decide: no backend configured")

// Engine asks questions and records every call.
type Engine struct {
	DB     *sql.DB
	Local  Backend // nil ⇒ SWARMERY_DECIDE_URL unset
	Claude Backend // nil ⇒ nothing ever leaves the machine
	// DefaultModes maps a question FAMILY ("d1", "d2") to its env mode. A
	// decide_modes row for the question id overrides it; neither ⇒ shadow.
	DefaultModes map[string]Mode
	// Thresholds maps a question family to its active-mode confidence floor.
	Thresholds map[string]float64
	// QuestionThresholds maps a question ID to its own floor, which wins over
	// the family's (SWARMERY_DECIDE_THRESHOLDS). NeverThreshold switches a
	// question's model leg off: no confidence reaches it.
	QuestionThresholds map[string]float64
	// R5PhaseRunFeature switches on D2 rule R5 — a phase or plan run is
	// task_type `feature` (SWARMERY_DECIDE_R5). Off by default: the rule is a
	// fallback for a model that cannot reach the task_type floor on those runs,
	// and is removed by deleting this field and its branch in d2Rules.
	R5PhaseRunFeature bool
	// OnNeedsOperator is called when an ACTIVE D1 answer hands a run to the
	// operator. nil ⇒ the run event and the partial state are the only signal.
	OnNeedsOperator func(NeedsOperator)
	Now             func() time.Time
}

// NeedsOperator describes a run D1 stopped instead of continuing.
type NeedsOperator struct {
	Engine      string
	SubjectID   int64
	SessionUUID string
	Detail      string
}

// Configured reports whether a classifier backend the shipped questions can
// actually reach exists. The rules backend alone does not count, and neither
// does the claude backend on its own: it answers only questions that set
// AllowRemote, and neither D1 nor D2 does, so without a local URL the package
// stays inert instead of writing a "no backend" error row on every pass.
func (e *Engine) Configured() bool {
	return e != nil && e.Local != nil
}

func (e *Engine) now() time.Time {
	if e != nil && e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Family is the prefix of a question id before its first dot.
func Family(questionID string) string {
	if i := strings.IndexByte(questionID, '.'); i > 0 {
		return questionID[:i]
	}
	return questionID
}

// Mode is the question's effective mode.
func (e *Engine) Mode(questionID string) Mode {
	if e == nil {
		return ModeOff
	}
	var defaults map[string]Mode
	defaults = e.DefaultModes
	return ModeFor(e.DB, questionID, defaults)
}

// ModeFor resolves a question's mode: a decide_modes row, else the family's
// default, else shadow.
func ModeFor(db *sql.DB, questionID string, defaults map[string]Mode) Mode {
	if db != nil {
		var m string
		if err := db.QueryRow(`SELECT mode FROM decide_modes WHERE question_id=?`, questionID).Scan(&m); err == nil {
			if mode, ok := ParseMode(m); ok {
				return mode
			}
		}
	}
	if m, ok := defaults[Family(questionID)]; ok {
		return m
	}
	return ModeShadow
}

// SetMode stores the dashboard's mode switch for one question.
func SetMode(db *sql.DB, questionID string, mode Mode, now time.Time) error {
	if _, ok := ParseMode(string(mode)); !ok {
		return fmt.Errorf("decide: unknown mode %q", mode)
	}
	if !slices.Contains(KnownQuestions, questionID) {
		return fmt.Errorf("decide: unknown question %q", questionID)
	}
	_, err := db.Exec(`
		INSERT INTO decide_modes (question_id, mode, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(question_id) DO UPDATE SET mode=excluded.mode, updated_at=excluded.updated_at`,
		questionID, string(mode), now.UTC().Format(time.RFC3339))
	return err
}

// Threshold is the question's active-mode confidence floor: its own when one
// is configured, else its family's.
func (e *Engine) Threshold(questionID string) float64 {
	if e != nil {
		if t, ok := e.QuestionThresholds[questionID]; ok {
			return t
		}
		if t, ok := e.Thresholds[Family(questionID)]; ok {
			return t
		}
	}
	return DefaultThreshold
}

// DefaultThreshold is the floor for a family with no configured threshold.
const DefaultThreshold = 0.85

// NeverThreshold is the per-question floor no model confidence reaches: the
// value for a question whose model answers are not yet good enough to act on.
const NeverThreshold = 1.01

// clears reports whether an answer may be acted on for questionID: a rule's
// answer always (it is correct by construction, whatever the floor), a model's
// only when its confidence is calibrated and at or above the floor.
func (e *Engine) clears(questionID string, a Answer) bool {
	if a.Backend == BackendRules {
		return true
	}
	return a.Calibrated && a.Confidence >= e.Threshold(questionID)
}

// Decide asks q through the backends in order and records the call. The
// returned answer carries the decision row id (0 when recording failed).
func (e *Engine) Decide(ctx context.Context, q Question) (Answer, error) {
	if e == nil {
		return Answer{}, ErrNotConfigured
	}
	mode := e.Mode(q.ID)
	a, err := e.ask(ctx, q)
	a.DecisionID = e.record(q, a, mode, err)
	return a, err
}

func (e *Engine) ask(ctx context.Context, q Question) (Answer, error) {
	if v, ok := q.canonical(q.RuleAnswer); ok && q.RuleAnswer != "" {
		return Answer{Value: v, Probs: map[string]float64{v: 1}, Confidence: 1, Calibrated: true, Backend: BackendRules, Rule: q.RuleID}, nil
	}
	var errs []error
	for _, b := range []Backend{e.Local, e.Claude} {
		if b == nil {
			continue
		}
		if b.Name() == BackendClaude && !q.AllowRemote {
			continue
		}
		start := time.Now()
		a, err := b.Ask(ctx, q)
		a.Latency = time.Since(start)
		a.Backend = b.Name()
		if err == nil {
			// Every backend's answer is re-checked against the closed option
			// set: a label outside the taxonomy is a failed call, not data.
			v, ok := q.canonical(a.Value)
			if ok {
				a.Value = v
				return a, nil
			}
			err = fmt.Errorf("answer %q is not one of %v", a.Value, q.Options())
		}
		errs = append(errs, fmt.Errorf("%s: %w", b.Name(), err))
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		return Answer{}, ErrNotConfigured
	}
	return Answer{Backend: lastBackend(errs)}, errors.Join(errs...)
}

func lastBackend(errs []error) string {
	s := errs[len(errs)-1].Error()
	if i := strings.IndexByte(s, ':'); i > 0 {
		return s[:i]
	}
	return ""
}

// HashInput is the sha256 of the input, so a decision is traceable to its
// evidence without the evidence being stored.
func HashInput(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// record writes one decisions row. Best-effort: a failed write is logged, never
// returned — the decision explains a transition, it must not block one.
func (e *Engine) record(q Question, a Answer, mode Mode, callErr error) int64 {
	if e.DB == nil {
		return 0
	}
	probs, _ := json.Marshal(a.Probs)
	if a.Probs == nil {
		probs = []byte("{}")
	}
	var conf any
	if callErr == nil {
		conf = a.Confidence
	}
	errText := ""
	if callErr != nil {
		errText = truncate(callErr.Error(), 500)
	}
	res, err := e.DB.Exec(`
		INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, probs_json,
		                       confidence, calibrated, backend, latency_ms, mode, rule_value, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		q.ID, q.Subject, q.SessionUUID, HashInput(q.Input), a.Value, string(probs),
		conf, boolInt(a.Calibrated), a.Backend, a.Latency.Milliseconds(), string(mode), q.RuleValue,
		errText, e.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		log.Printf("error: decide: decision %s for %q not recorded: %v", q.ID, q.Subject, err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// MarkActed flags a decision whose answer changed what the control plane did.
func (e *Engine) MarkActed(id int64) {
	if e == nil || e.DB == nil || id == 0 {
		return
	}
	if _, err := e.DB.Exec(`UPDATE decisions SET acted=1 WHERE id=?`, id); err != nil {
		log.Printf("warning: decide: mark acted %d: %v", id, err)
	}
}

// Ground-truth sources: who wrote a decision's label
// (decisions.ground_truth_source). Only operator and observed labels feed a
// threshold; an agent label is reported beside them, never counted in them.
const (
	TruthOperator = "operator" // a person answered in the dashboard
	TruthAgent    = "agent"    // a triage run answered
	TruthObserved = "observed" // the daemon recorded what a run did (D1)
)

// ErrAlreadyLabelled: an agent label was refused because the decision already
// has ground truth.
var ErrAlreadyLabelled = errors.New("decide: decision already has ground truth")

// RecordGroundTruth stores what actually happened for one decision and who said so.
// An agent never overwrites a label; operator and observed labels always win.
// sql.ErrNoRows when the decision does not exist.
func RecordGroundTruth(db *sql.DB, id int64, truth, source string, now time.Time) error {
	if db == nil || id == 0 {
		return nil
	}
	truth = strings.TrimSpace(truth)
	if truth == "" {
		return errors.New("decide: empty ground truth")
	}
	switch source {
	case TruthOperator, TruthAgent, TruthObserved:
	default:
		return fmt.Errorf("decide: unknown ground-truth source %q", source)
	}
	q := `UPDATE decisions SET ground_truth=?, ground_truth_at=?, ground_truth_source=? WHERE id=?`
	if source == TruthAgent {
		q += ` AND ground_truth IS NULL`
	}
	res, err := db.Exec(q, truth, now.UTC().Format(time.RFC3339), source, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if source != TruthAgent {
		return sql.ErrNoRows
	}
	var one int
	switch err := db.QueryRow(`SELECT 1 FROM decisions WHERE id=?`, id).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return sql.ErrNoRows
	case err != nil:
		return err
	}
	return ErrAlreadyLabelled
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// truncate keeps at most n bytes of s, cut at a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	// Only the LAST rune can be split: find where it starts and drop it when its
	// bytes run past the cut. A rune that ends exactly at n is kept.
	i := len(cut) - 1
	for i > 0 && i > len(cut)-utf8.UTFMax && !utf8.RuneStart(cut[i]) {
		i--
	}
	if i >= 0 && !utf8.FullRuneInString(cut[i:]) {
		cut = cut[:i]
	}
	return cut
}

// tail keeps the LAST n bytes of s (the end of a reply is where it says how it
// ended), cut at a rune boundary.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[len(s)-n:]
	for len(cut) > 0 && cut[0]&0xC0 == 0x80 {
		cut = cut[1:]
	}
	return cut
}

// Config is the env-derived configuration.
type Config struct {
	URL   string
	Model string
	// NoSchema drops response_format from local requests
	// (SWARMERY_DECIDE_SCHEMA=off). Some servers mishandle a json_schema enum:
	// LM Studio's MLX builds end the reply at the first unique prefix of the
	// value (`"ref` for refactor), which fails every multi-token answer.
	NoSchema bool
	// UserSuffix is appended to every local question
	// (SWARMERY_DECIDE_USER_SUFFIX), e.g. Qwen3's `/no_think` soft switch: a
	// thinking model otherwise spends the reply budget on hidden reasoning.
	UserSuffix string
	// Timeout bounds one local call (SWARMERY_DECIDE_TIMEOUT, a Go duration;
	// 0 ⇒ LocalTimeout). A server that unloads an idle model (LM Studio's JIT
	// TTL) needs ~12 s to load it again, so the first call of every pass after an
	// idle stretch failed at 5 s.
	Timeout    time.Duration
	Claude     bool
	Modes      map[string]Mode
	Thresholds map[string]float64
	// QuestionThresholds are per-question floors (SWARMERY_DECIDE_THRESHOLDS,
	// `d2.outcome=0.95,d2.task_type=0.8`), each winning over its family's.
	QuestionThresholds map[string]float64
	// R5PhaseRunFeature switches D2 rule R5 on (SWARMERY_DECIDE_R5).
	R5PhaseRunFeature bool
}

// DefaultLocalModel is sent when SWARMERY_DECIDE_MODEL is unset; LM Studio
// and most OpenAI-compatible servers answer with whatever model is loaded.
const DefaultLocalModel = "local-model"

// ConfigFromEnv reads SWARMERY_DECIDE_* and returns warnings for values it
// had to ignore.
func ConfigFromEnv(getenv func(string) string) (Config, []string) {
	cfg := Config{
		URL:        strings.TrimSpace(getenv("SWARMERY_DECIDE_URL")),
		Model:      strings.TrimSpace(getenv("SWARMERY_DECIDE_MODEL")),
		UserSuffix: strings.TrimSpace(getenv("SWARMERY_DECIDE_USER_SUFFIX")),
		Modes:      map[string]Mode{"d1": ModeShadow, "d2": ModeShadow, "d3": ModeShadow},
		Thresholds: map[string]float64{"d1": 0.85, "d2": 0.6, "d3": 0.6},
	}
	if cfg.Model == "" {
		cfg.Model = DefaultLocalModel
	}
	var warn []string
	switch strings.ToLower(strings.TrimSpace(getenv("SWARMERY_DECIDE_SCHEMA"))) {
	case "0", "off", "false", "no":
		cfg.NoSchema = true
	case "", "1", "on", "true", "yes":
	default:
		warn = append(warn, "SWARMERY_DECIDE_SCHEMA: unknown value, the schema stays on")
	}
	if raw := strings.TrimSpace(getenv("SWARMERY_DECIDE_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 && d <= 5*time.Minute {
			cfg.Timeout = d
		} else {
			warn = append(warn, fmt.Sprintf("SWARMERY_DECIDE_TIMEOUT=%q: want a duration in (0, 5m], using %s", raw, LocalTimeout))
		}
	}
	switch strings.ToLower(strings.TrimSpace(getenv("SWARMERY_DECIDE_CLAUDE"))) {
	case "1", "on", "true", "yes":
		cfg.Claude = true
	case "", "0", "off", "false", "no":
	default:
		warn = append(warn, "SWARMERY_DECIDE_CLAUDE: unknown value, the claude backend stays off")
	}
	for _, fam := range []string{"d1", "d2", "d3"} {
		key := "SWARMERY_DECIDE_" + strings.ToUpper(fam)
		if raw := getenv(key); strings.TrimSpace(raw) != "" {
			if m, ok := ParseMode(raw); ok {
				cfg.Modes[fam] = m
			} else {
				warn = append(warn, fmt.Sprintf("%s=%q: want off|shadow|active, using shadow", key, raw))
			}
		}
		tkey := key + "_THRESHOLD"
		if raw := strings.TrimSpace(getenv(tkey)); raw != "" {
			if t, err := strconv.ParseFloat(raw, 64); err == nil && t > 0 && t <= 1 {
				cfg.Thresholds[fam] = t
			} else {
				warn = append(warn, fmt.Sprintf("%s=%q: want a number in (0,1], using %.2f", tkey, raw, cfg.Thresholds[fam]))
			}
		}
	}
	switch strings.ToLower(strings.TrimSpace(getenv("SWARMERY_DECIDE_R5"))) {
	case "1", "on", "true", "yes":
		cfg.R5PhaseRunFeature = true
	case "", "0", "off", "false", "no":
	default:
		warn = append(warn, "SWARMERY_DECIDE_R5: unknown value, rule R5 stays off")
	}
	var bad []string
	cfg.QuestionThresholds, bad = parseQuestionThresholds(getenv("SWARMERY_DECIDE_THRESHOLDS"))
	if len(bad) > 0 {
		warn = append(warn, fmt.Sprintf("SWARMERY_DECIDE_THRESHOLDS: ignored %s; want <question>=<number in (0,%.2f]> with a known question id",
			strings.Join(bad, ", "), NeverThreshold))
	}
	return cfg, warn
}

// parseQuestionThresholds reads `question=floor` pairs separated by commas. A
// pair with an unknown question id, no `=`, or a floor outside (0,
// NeverThreshold] is returned in bad and otherwise ignored; the good pairs
// still apply. nil when nothing valid was given.
func parseQuestionThresholds(raw string) (out map[string]float64, bad []string) {
	for _, pair := range strings.Split(raw, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		id, val, found := strings.Cut(pair, "=")
		id = strings.TrimSpace(id)
		t, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if !found || err != nil || !(t > 0 && t <= NeverThreshold) || !slices.Contains(KnownQuestions, id) {
			bad = append(bad, strconv.Quote(pair))
			continue
		}
		if out == nil {
			out = map[string]float64{}
		}
		out[id] = t
	}
	return out, bad
}

// String renders the config for the startup log (no secrets are involved).
func (c Config) String() string {
	local := "off"
	if c.URL != "" {
		local = c.URL + " model=" + c.Model
		if c.NoSchema {
			local += " schema=off"
		}
		if c.UserSuffix != "" {
			local += fmt.Sprintf(" suffix=%q", c.UserSuffix)
		}
		if c.Timeout > 0 {
			local += " timeout=" + c.Timeout.String()
		}
	}
	out := fmt.Sprintf("local=%s claude=%t d1=%s(%.2f) d2=%s(%.2f) d3=%s(%.2f)",
		local, c.Claude, c.Modes["d1"], c.Thresholds["d1"], c.Modes["d2"], c.Thresholds["d2"],
		c.Modes["d3"], c.Thresholds["d3"])
	if len(c.QuestionThresholds) > 0 {
		pairs := make([]string, 0, len(c.QuestionThresholds))
		for id, t := range c.QuestionThresholds {
			pairs = append(pairs, fmt.Sprintf("%s=%.2f", id, t))
		}
		slices.Sort(pairs)
		out += " thresholds=" + strings.Join(pairs, ",")
	}
	if c.R5PhaseRunFeature {
		out += " r5=on"
	}
	return out
}

// New builds the engine. Local exists only with a URL; Claude only when
// explicitly enabled.
func New(db *sql.DB, cfg Config) *Engine {
	e := &Engine{DB: db, DefaultModes: cfg.Modes, Thresholds: cfg.Thresholds,
		QuestionThresholds: cfg.QuestionThresholds, R5PhaseRunFeature: cfg.R5PhaseRunFeature}
	if cfg.URL != "" {
		e.Local = &Local{URL: cfg.URL, Model: cfg.Model, NoSchema: cfg.NoSchema, UserSuffix: cfg.UserSuffix, Timeout: cfg.Timeout}
	}
	if cfg.Claude {
		e.Claude = &Claude{}
	}
	return e
}
