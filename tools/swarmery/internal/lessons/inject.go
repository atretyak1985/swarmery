package lessons

// Injection (Opus 5.5 / learning-loop phase 15): hand a headless phase or plan
// run the ACTIVE lessons learned in the places it is about to work.
//
// WHERE LESSONS GO — AND WHERE THEY NEVER GO. Only into the prompt swarmery
// itself builds for a run it spawns (internal/phaserun, internal/planrun). Never
// into the always-loaded auto-memory (MEMORY.md is already over budget — advisor
// rule R10), and never through a hook: a hook would inject into EVERY session,
// interactive ones included, with no record of what was handed to whom.
// Interactive sessions pull lessons on demand instead (the core `area-lessons`
// skill reads GET /api/lessons).
//
// WHICH STORE FEEDS IT. surprise_lessons rows with status 'active' only — each
// was accepted by an operator (Accept is the only path to active) with its area
// globs in front of them. retro_lessons are NOT injected: every existing row
// defaults to area '*' (0084), wsingest rewrites them on every rescan (so no
// operator narrowing can stick), and injecting them would hand every retro
// lesson ever written to every run. A retro lesson reaches runs by being merged
// into, or re-learned as, a surprise lesson with real areas.
//
// SELECTION (15.1). A lesson is selected when one of its area globs overlaps
// the run's scope — for a phase run the PRIOR forecast's areas and files
// (phase_forecasts, kind 'prior'); for a plan run the union of the priors of the
// plan's phases. A glob g overlaps a scope entry e when
//
//  1. path.Match(g, e) — a file glob against a forecast file, or
//  2. surprise.AreaOverlap(g, e) — segment-prefix overlap in either direction,
//     after cutting g at its first glob segment and optionally stripping a
//     leading sub-module prefix (the phase-13 rule; `store` never matches
//     `restore`).
//
// A bare `*` / `**` glob is "everywhere" and matches any NON-EMPTY scope. A run
// with no forecast areas at all (no prior, a planning run) gets no lessons: a
// run we cannot place gets nothing rather than everything.
//
// RANKING. Effectiveness first (phase 16 fills the Effectiveness hook; until
// then every lesson is unscored), then recency (activated_at, newest first),
// then id. The block is cut at the budget: lessons are taken in rank order and
// the first one that would push the WHOLE injected text (separator and header
// included) past SWARMERY_LESSON_BUDGET_TOKENS stops the selection — a strict
// cut keeps "rank" meaning what it says. Tokens are estimated as bytes/4
// (rounded up), the same heuristic sysscan uses for CLAUDE.md budgets.
//
// ZERO LESSONS ⇒ the injected text is "" and the prompt is byte-identical to
// the prompt without injection. Every injected lesson is recorded in
// lesson_uses BEFORE the text is returned; a failed record injects nothing.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

// Budget knob.
const (
	// EnvBudget caps the injected block, in estimated tokens. "0" or "off"
	// disables injection.
	EnvBudget = "SWARMERY_LESSON_BUDGET_TOKENS"
	// DefaultBudgetTokens is the budget when EnvBudget is unset or unreadable.
	DefaultBudgetTokens = 600
)

// Run kinds as lesson_uses stores them.
const (
	KindPhaseRun = "phaserun"
	KindPlanRun  = "planrun"
)

// blockHeader opens the injected block. Calm and informational on purpose: a
// lesson is context from earlier runs, not an order.
const blockHeader = "Lessons from earlier runs in these areas (cite the id if you rely on one):"

// BudgetFromEnv reads EnvBudget: a non-negative integer, "off" ⇒ 0, anything
// else ⇒ DefaultBudgetTokens with a warning.
func BudgetFromEnv(getenv func(string) string) (int, []string) {
	v := strings.ToLower(strings.TrimSpace(getenv(EnvBudget)))
	switch v {
	case "":
		return DefaultBudgetTokens, nil
	case "off":
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return DefaultBudgetTokens, []string{fmt.Sprintf("%s: ignoring %q (want a token count ≥ 0 or off)", EnvBudget, v)}
	}
	return n, nil
}

// EstimateTokens is the bytes/4 heuristic, rounded up.
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

// Ref renders a lesson's citable id: "L-12".
func Ref(id int64) string { return "L-" + strconv.FormatInt(id, 10) }

// Scope is where a run is expected to work: forecast areas and files.
type Scope struct {
	Areas []string
	Files []string
}

func (s Scope) entries() []string {
	out := make([]string, 0, len(s.Areas)+len(s.Files))
	for _, e := range append(append([]string{}, s.Areas...), s.Files...) {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// Active is one active lesson as injection reads it.
type Active struct {
	ID          int64
	Title       string
	Guidance    string
	AreaGlobs   []string
	ActivatedAt string
	// Effectiveness is phase 16's score (higher is better); nil = unscored.
	Effectiveness *float64
}

// Line renders the lesson's line in the block.
func (a Active) Line() string {
	return "- [" + Ref(a.ID) + "] " + strings.Join(strings.Fields(a.Guidance), " ")
}

// Effectiveness is the ranking hook phase 16 fills: lesson id → score. The
// default scores nothing, so ranking falls through to recency.
var Effectiveness = func(db *sql.DB, ids []int64) (map[int64]float64, error) { return nil, nil }

// globHits reports whether one lesson glob overlaps one scope entry (rules 1–2
// in the file comment).
func globHits(glob, entry string) bool {
	g := strings.TrimPrefix(strings.TrimSpace(glob), "./")
	e := strings.TrimLeft(strings.TrimPrefix(strings.Trim(strings.TrimSpace(entry), "\"'`"), "./"), "/")
	if ok, _ := path.Match(g, e); ok {
		return true
	}
	return surprise.AreaOverlap(g, e)
}

// Matches reports whether any of globs overlaps the scope.
func Matches(globs []string, sc Scope) bool {
	entries := sc.entries()
	if len(entries) == 0 {
		return false
	}
	for _, g := range globs {
		g = strings.TrimSpace(g)
		switch g {
		case "":
			continue
		case "*", "**", "**/*":
			return true
		}
		for _, e := range entries {
			if globHits(g, e) {
				return true
			}
		}
	}
	return false
}

// Rank orders lessons in place: effectiveness (scored before unscored, higher
// first), then activated_at newest first, then id newest first.
func Rank(ls []Active) {
	sort.SliceStable(ls, func(i, j int) bool {
		a, b := ls[i], ls[j]
		if (a.Effectiveness != nil) != (b.Effectiveness != nil) {
			return a.Effectiveness != nil
		}
		if a.Effectiveness != nil && *a.Effectiveness != *b.Effectiveness {
			return *a.Effectiveness > *b.Effectiveness
		}
		if a.ActivatedAt != b.ActivatedAt {
			return a.ActivatedAt > b.ActivatedAt
		}
		return a.ID > b.ID
	})
}

// Render is the text appended to a prompt for these lessons: "" for none,
// otherwise a blank-line separator, the header and one line per lesson.
func Render(ls []Active) string {
	if len(ls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(blockHeader)
	for _, l := range ls {
		b.WriteString("\n")
		b.WriteString(l.Line())
	}
	return b.String()
}

// Cut takes lessons in rank order while the rendered text stays within budget
// tokens; the first lesson that does not fit ends the selection.
func Cut(ranked []Active, budget int) []Active {
	var out []Active
	for _, l := range ranked {
		next := append(append([]Active{}, out...), l)
		if EstimateTokens(Render(next)) > budget {
			break
		}
		out = next
	}
	return out
}

// Selection is what one run is handed.
type Selection struct {
	Lessons []Active
	Text    string // Render(Lessons)
	Tokens  int    // EstimateTokens(Text)
}

// loadActive reads every active surprise lesson.
func loadActive(db *sql.DB) ([]Active, error) {
	rows, err := db.Query(`SELECT id, title, guidance, area_globs, COALESCE(activated_at, updated_at)
		FROM surprise_lessons WHERE status = ?`, StatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Active
	for rows.Next() {
		var (
			a     Active
			globs string
		)
		if err := rows.Scan(&a.ID, &a.Title, &a.Guidance, &globs, &a.ActivatedAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Guidance) == "" {
			continue
		}
		a.AreaGlobs = splitGlobs(globs)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Select picks, ranks and cuts the active lessons for a scope.
func Select(db *sql.DB, sc Scope, budget int) (Selection, error) {
	if budget <= 0 || len(sc.entries()) == 0 {
		return Selection{}, nil
	}
	all, err := loadActive(db)
	if err != nil {
		return Selection{}, err
	}
	var hit []Active
	for _, a := range all {
		if Matches(a.AreaGlobs, sc) {
			hit = append(hit, a)
		}
	}
	if len(hit) == 0 {
		return Selection{}, nil
	}
	ids := make([]int64, len(hit))
	for i, a := range hit {
		ids[i] = a.ID
	}
	scores, err := Effectiveness(db, ids)
	if err != nil {
		log.Printf("warning: lessons: effectiveness unavailable, ranking by recency: %v", err)
	}
	for i := range hit {
		if v, ok := scores[hit[i].ID]; ok {
			v := v
			hit[i].Effectiveness = &v
		}
	}
	Rank(hit)
	cut := Cut(hit, budget)
	text := Render(cut)
	return Selection{Lessons: cut, Text: text, Tokens: EstimateTokens(text)}, nil
}

// priorScope reads the prior forecasts' areas and files for the given phases.
func priorScope(db *sql.DB, where string, arg int64) (Scope, error) {
	rows, err := db.Query(`SELECT f.areas_json, f.files_json FROM phase_forecasts f
		WHERE f.kind = 'prior' AND `+where+` ORDER BY f.id`, arg)
	if err != nil {
		return Scope{}, err
	}
	defer rows.Close()
	var sc Scope
	for rows.Next() {
		var areasJSON, filesJSON string
		if err := rows.Scan(&areasJSON, &filesJSON); err != nil {
			return Scope{}, err
		}
		var a, f []string
		_ = json.Unmarshal([]byte(areasJSON), &a)
		_ = json.Unmarshal([]byte(filesJSON), &f)
		sc.Areas = append(sc.Areas, a...)
		sc.Files = append(sc.Files, f...)
	}
	return sc, rows.Err()
}

// PhaseScope is a phase run's scope: its prior forecast.
func PhaseScope(db *sql.DB, phaseID int64) (Scope, error) {
	return priorScope(db, `f.phase_id = ?`, phaseID)
}

// PlanScope is a plan run's scope: the priors of every phase of the plan.
func PlanScope(db *sql.DB, taskID int64) (Scope, error) {
	return priorScope(db, `f.phase_id IN (SELECT id FROM epic_phases WHERE workspace_task_id = ?)`, taskID)
}

// RecordUses writes one lesson_uses row per injected lesson.
func RecordUses(db *sql.DB, kind string, phaseID, taskID int64, uuid string, sel Selection, now time.Time) error {
	if len(sel.Lessons) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	at := stamp(now)
	for i, l := range sel.Lessons {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO lesson_uses
			(session_uuid, run_kind, phase_id, task_id, lesson_id, rank, est_tokens, injected_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid, kind, phaseID, taskID, l.ID, i+1, EstimateTokens(l.Line()), at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// citeRe finds a cited lesson id: "[L-12]".
var citeRe = regexp.MustCompile(`\[L-(\d+)\]`)

// Cited returns the lesson ids cited in text.
func Cited(text string) map[int64]bool {
	out := map[int64]bool{}
	for _, m := range citeRe.FindAllStringSubmatch(text, -1) {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			out[id] = true
		}
	}
	return out
}

// CompletionReport returns the doc's `## Completion Report` section body ("" when
// absent): the lines after the heading up to the next level-1/2 heading.
func CompletionReport(doc string) string {
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "## Completion Report") {
			var out []string
			for _, r := range lines[i+1:] {
				if strings.HasPrefix(r, "# ") || strings.HasPrefix(r, "## ") {
					break
				}
				out = append(out, r)
			}
			return strings.Join(out, "\n")
		}
	}
	return ""
}

// transcriptText is the run's ingested assistant text that mentions a lesson id.
func transcriptText(db *sql.DB, uuid string) (string, error) {
	rows, err := db.Query(`SELECT t.text FROM turns t JOIN sessions s ON s.id = t.session_id
		WHERE s.session_uuid = ? AND t.role = 'assistant' AND t.text LIKE '%[L-%' ORDER BY t.seq`, uuid)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return "", err
		}
		b.WriteString(s.String)
		b.WriteString("\n")
	}
	return b.String(), rows.Err()
}

// MarkRelied flags the run's injected lessons that were cited in the transcript
// or the report. Only lessons the run was actually handed are marked; a cited id
// the run was never given is ignored. Returns how many rows changed.
func MarkRelied(db *sql.DB, uuid, transcript, report string, now time.Time) (int, error) {
	inT, inR := Cited(transcript), Cited(report)
	ids := map[int64]bool{}
	for id := range inT {
		ids[id] = true
	}
	for id := range inR {
		ids[id] = true
	}
	n := 0
	for id := range ids {
		var where []string
		if inT[id] {
			where = append(where, "transcript")
		}
		if inR[id] {
			where = append(where, "report")
		}
		// A report-only citation counts only when the report did not already cite
		// the id before this run started (prior_in_report, see snapshotReport).
		q := `UPDATE lesson_uses SET relied_on = 1, relied_where = ?, relied_at = ?
			WHERE session_uuid = ? AND lesson_id = ?`
		if !inT[id] {
			q += ` AND prior_in_report = 0`
		}
		res, err := db.Exec(q, strings.Join(where, ","), stamp(now), uuid, id)
		if err != nil {
			return n, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n += int(k)
		}
	}
	return n, nil
}

// Injector wires selection, recording and citation detection into the run
// engines. Every method is ADVISORY: it logs and degrades to "no lessons",
// never failing or blocking a run.
type Injector struct {
	DB     *sql.DB
	Budget int
	Now    func() time.Time
}

// NewInjector builds an injector with the given token budget.
func NewInjector(db *sql.DB, budget int) *Injector {
	return &Injector{DB: db, Budget: budget, Now: time.Now}
}

func (i *Injector) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

func (i *Injector) inject(kind string, phaseID, taskID int64, uuid string, sc Scope, scopeErr error) string {
	if scopeErr != nil {
		log.Printf("warning: lessons: %s phase=%d task=%d: read scope: %v", kind, phaseID, taskID, scopeErr)
		return ""
	}
	sel, err := Select(i.DB, sc, i.Budget)
	if err != nil {
		log.Printf("warning: lessons: %s phase=%d task=%d: select: %v", kind, phaseID, taskID, err)
		return ""
	}
	if len(sel.Lessons) == 0 {
		return ""
	}
	if err := RecordUses(i.DB, kind, phaseID, taskID, uuid, sel, i.now()); err != nil {
		// Unrecorded ⇒ not injected: every lesson a run carries has a row.
		log.Printf("warning: lessons: %s phase=%d task=%d: record uses (injecting none): %v", kind, phaseID, taskID, err)
		return ""
	}
	log.Printf("lessons: %s phase=%d task=%d uuid=%s injected %d lesson(s), ~%d tokens",
		kind, phaseID, taskID, uuid, len(sel.Lessons), sel.Tokens)
	return sel.Text
}

// ForPhase is phaserun.Service.InjectLessons: the text to append to a phase
// run's prompt ("" for none).
func (i *Injector) ForPhase(phaseID int64, uuid string) string {
	sc, err := PhaseScope(i.DB, phaseID)
	var taskID int64
	_ = i.DB.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, phaseID).Scan(&taskID)
	text := i.inject(KindPhaseRun, phaseID, taskID, uuid, sc, err)
	if text != "" {
		i.snapshotReport(phaseID, uuid)
	}
	return text
}

// ForPlan is planrun.Service.InjectLessons.
func (i *Injector) ForPlan(taskID int64, uuid string) string {
	sc, err := PlanScope(i.DB, taskID)
	return i.inject(KindPlanRun, 0, taskID, uuid, sc, err)
}

// snapshotReport marks the injected lessons the phase doc's Completion Report
// ALREADY cites — written by an earlier run — so a run that never touches the
// report is not credited with its predecessor's citations. Best-effort: on any
// error the run simply gets the old, more generous behaviour.
func (i *Injector) snapshotReport(phaseID int64, uuid string) {
	var docPath string
	if err := i.DB.QueryRow(`SELECT doc_path FROM epic_phases WHERE id = ?`, phaseID).Scan(&docPath); err != nil || docPath == "" {
		return
	}
	b, err := os.ReadFile(docPath)
	if err != nil {
		return
	}
	for id := range Cited(CompletionReport(string(b))) {
		if _, err := i.DB.Exec(`UPDATE lesson_uses SET prior_in_report = 1 WHERE session_uuid = ? AND lesson_id = ?`, uuid, id); err != nil {
			log.Printf("warning: lessons: uuid=%s snapshot report citations: %v", uuid, err)
			return
		}
	}
}

// AfterPhaseRun is phaserun.Service.LessonCitations: mark the lessons the run
// cited in its assistant text or in the returned doc's Completion Report.
func (i *Injector) AfterPhaseRun(phaseID int64, uuid, docPath string) {
	report := ""
	if docPath != "" {
		if b, err := os.ReadFile(docPath); err == nil {
			report = CompletionReport(string(b))
		}
	}
	i.afterRun(uuid, report)
}

// AfterPlanRun is planrun.Service.LessonCitations (transcript only: a plan run
// has no single report).
func (i *Injector) AfterPlanRun(taskID int64, uuid string) { i.afterRun(uuid, "") }

func (i *Injector) afterRun(uuid, report string) {
	var injected int
	if err := i.DB.QueryRow(`SELECT COUNT(*) FROM lesson_uses WHERE session_uuid = ?`, uuid).Scan(&injected); err != nil || injected == 0 {
		return
	}
	transcript, err := transcriptText(i.DB, uuid)
	if err != nil {
		log.Printf("warning: lessons: uuid=%s read transcript: %v", uuid, err)
	}
	n, err := MarkRelied(i.DB, uuid, transcript, report, i.now())
	if err != nil {
		log.Printf("warning: lessons: uuid=%s mark relied: %v", uuid, err)
		return
	}
	if n > 0 {
		log.Printf("lessons: uuid=%s relied on %d injected lesson(s)", uuid, n)
	}
}
