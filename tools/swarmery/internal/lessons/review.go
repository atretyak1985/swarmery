package lessons

// The operator's review queue (14.4). Every state change a lesson can make is
// here, each guarded by the state it may leave from:
//
//	candidate → active     Accept   (THE ONLY path to active — activeguard_test.go)
//	candidate → merged     Merge    (into another lesson; the target absorbs it)
//	candidate → dismissed  Dismiss
//	active    → retired    Retire
//
// Edit changes the words, the areas and the identity (norm_title), never the
// status. Every function is called from an operator endpoint behind
// requireLocalOrigin, with ONE documented exception (phase 16, retire.go): the
// verification pass calls Retire itself for a retirement proposal the operator
// left unanswered for the auto-retire window, recording the proposal's reason.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Review errors (the API maps them to 404 / 409 / 400).
var (
	ErrNotFound = errors.New("no such lesson")
	ErrState    = errors.New("the lesson is not in a state that allows this")
)

// Lesson is one surprise_lessons row as the dashboard reads it.
type Lesson struct {
	ID              int64    `json:"id"`
	PhaseID         int64    `json:"phaseId"`
	PhaseName       string   `json:"phaseName"`
	PlanID          string   `json:"planId"`
	SourcePhaseRun  string   `json:"sourcePhaseRun"`
	Title           string   `json:"title"`
	NormTitle       string   `json:"normTitle"`
	Guidance        string   `json:"guidance"`
	AreaGlobs       []string `json:"areaGlobs"`
	Evidence        []string `json:"evidence"`
	Cause           string   `json:"cause"`
	SourceParagraph string   `json:"sourceParagraph"`
	SurpriseIndex   *float64 `json:"surpriseIndex"`
	Status          string   `json:"status"`
	LinkedNormTitle string   `json:"linkedNormTitle"`
	MergedIntoID    *int64   `json:"mergedIntoId"`
	Recurrences     int      `json:"recurrences"`
	RecurrenceRuns  []string `json:"recurrenceRuns"`
	Model           string   `json:"model"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
	ActivatedAt     *string  `json:"activatedAt"`
	RetiredAt       *string  `json:"retiredAt"`
	RetireReason    *string  `json:"retireReason"`
	// PromotedBranch is the branch of the lesson's latest successful promotion
	// into a nested CLAUDE.md (15.4, lesson_promotions); "" when never promoted.
	PromotedBranch string `json:"promotedBranch"`
	// Effectiveness is the lesson's stored verification row (phase 16); nil
	// until the first verification pass measured it.
	Effectiveness *EffectivenessRow `json:"effectiveness"`
	// Matches are merge suggestions (candidates only): lessons whose identity
	// equals this one's, or overlaps it by at least half its words.
	Matches []Match `json:"matches"`
	// Source is the producer: SourceSurprise (a run far from its forecast) or
	// SourceReview (a repeated review finding, verify-not-trust phase 8).
	Source string `json:"source"`
	// ReviewType / ReviewArea are a review lesson's group identity; "" otherwise.
	ReviewType string `json:"reviewType"`
	ReviewArea string `json:"reviewArea"`
	// Findings resolves the lesson's "finding:<id>" evidence for the dashboard's
	// links; a finding whose row is gone (its block left the doc) is omitted.
	Findings []FindingLink `json:"findings"`
}

// FindingLink is one review finding a lesson cites as "finding:<id>".
type FindingLink struct {
	ID          int64  `json:"id"` // review_findings.id
	TaskID      int64  `json:"taskId"`
	ProjectSlug string `json:"projectSlug"`
	PlanID      string `json:"planId"`
	PhaseID     int64  `json:"phaseId"`
	PhaseSeq    int    `json:"phaseSeq"` // 0 when the doc resolved to no phase
	PhaseName   string `json:"phaseName"`
	Round       string `json:"round"`
	FindingID   string `json:"findingId"` // the block's own id (R1-2)
	Type        string `json:"type"`
	File        string `json:"file"`
	Line        int64  `json:"line"`
}

// Match is one merge suggestion.
type Match struct {
	Kind      string `json:"kind"` // retro | surprise
	LessonID  int64  `json:"lessonId,omitempty"`
	NormTitle string `json:"normTitle"`
	Title     string `json:"title"`
	Count     int    `json:"count"` // retro: lessons with this identity; surprise: recurrences
	Exact     bool   `json:"exact"`
}

// maxMatches caps the suggestions per candidate.
const maxMatches = 5

const selectLesson = `SELECT l.id, l.phase_id, COALESCE(e.name, ''), COALESCE(t.external_id, ''),
	l.source_phase_run, l.title, l.norm_title, l.guidance, l.area_globs, l.evidence_json, l.cause,
	l.source_paragraph, l.surprise_index, l.status, l.linked_norm_title, l.merged_into_id, l.recurrences,
	l.recurrence_runs_json, l.model, l.created_at, l.updated_at, l.activated_at, l.retired_at, l.retire_reason,
	COALESCE((SELECT p.branch FROM lesson_promotions p WHERE p.lesson_id = l.id AND p.error = ''
	          ORDER BY p.id DESC LIMIT 1), ''),
	l.source, l.review_type, l.review_area
	FROM surprise_lessons l
	LEFT JOIN epic_phases e ON e.id = l.phase_id
	LEFT JOIN tasks t ON t.id = e.workspace_task_id`

func scanLesson(scan func(...any) error) (Lesson, error) {
	var (
		l                             Lesson
		areas, ev, runs               string
		idx                           sql.NullFloat64
		merged                        sql.NullInt64
		activated, retired, retireWhy sql.NullString
	)
	if err := scan(&l.ID, &l.PhaseID, &l.PhaseName, &l.PlanID, &l.SourcePhaseRun, &l.Title, &l.NormTitle,
		&l.Guidance, &areas, &ev, &l.Cause, &l.SourceParagraph, &idx, &l.Status, &l.LinkedNormTitle,
		&merged, &l.Recurrences, &runs, &l.Model, &l.CreatedAt, &l.UpdatedAt, &activated, &retired, &retireWhy,
		&l.PromotedBranch, &l.Source, &l.ReviewType, &l.ReviewArea); err != nil {
		return l, err
	}
	l.AreaGlobs = splitGlobs(areas)
	l.Evidence, l.RecurrenceRuns, l.Matches, l.Findings = []string{}, []string{}, []Match{}, []FindingLink{}
	_ = json.Unmarshal([]byte(ev), &l.Evidence)
	_ = json.Unmarshal([]byte(runs), &l.RecurrenceRuns)
	if idx.Valid {
		v := idx.Float64
		l.SurpriseIndex = &v
	}
	if merged.Valid {
		v := merged.Int64
		l.MergedIntoID = &v
	}
	l.ActivatedAt, l.RetiredAt, l.RetireReason = nullStr(activated), nullStr(retired), nullStr(retireWhy)
	return l, nil
}

func nullStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func splitGlobs(s string) []string {
	out := []string{}
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// List returns lessons, newest first; status "" means every status. Candidates
// carry their merge suggestions.
func List(db *sql.DB, status string) ([]Lesson, error) {
	q, args := selectLesson, []any{}
	if status != "" {
		q += ` WHERE l.status = ?`
		args = append(args, status)
	}
	rows, err := db.Query(q+` ORDER BY l.id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	var out []Lesson
	for rows.Next() {
		l, err := scanLesson(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachMatches(db, out); err != nil {
		return nil, err
	}
	if err := attachEffectiveness(db, out); err != nil {
		return nil, err
	}
	if err := attachFindings(db, out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Lesson{}
	}
	return out, nil
}

// Get reads one lesson (with suggestions when it is a candidate).
func Get(db *sql.DB, id int64) (Lesson, error) {
	l, err := scanLesson(db.QueryRow(selectLesson+` WHERE l.id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	one := []Lesson{l}
	if err = attachMatches(db, one); err == nil {
		if err = attachEffectiveness(db, one); err == nil {
			err = attachFindings(db, one)
		}
	}
	return one[0], err
}

// findingPrefix marks a review finding's evidence id ("finding:12").
const findingPrefix = "finding:"

// attachFindings resolves every "finding:<id>" evidence id of ls into a
// FindingLink — whichever producer wrote the lesson, since a review candidate
// merged into a surprise lesson carries its findings along.
func attachFindings(db *sql.DB, ls []Lesson) error {
	var ids []any
	for _, l := range ls {
		for _, e := range l.Evidence {
			if rest, ok := strings.CutPrefix(e, findingPrefix); ok {
				if id, err := strconv.ParseInt(rest, 10, 64); err == nil {
					ids = append(ids, id)
				}
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	byID := map[int64]FindingLink{}
	// Batched under SQLite's default bound on host parameters.
	for start := 0; start < len(ids); start += 500 {
		batch := ids[start:min(start+500, len(ids))]
		rows, err := db.Query(`SELECT f.id, f.workspace_task_id, COALESCE(p.slug, ''), COALESCE(t.external_id, ''),
			COALESCE(f.phase_id, 0), COALESCE(e.seq, 0), COALESCE(e.name, ''), f.round, f.finding_id,
			COALESCE(f.type, ''), f.file, COALESCE(f.line, 0)
			  FROM review_findings f
			  LEFT JOIN epic_phases e ON e.id = f.phase_id
			  LEFT JOIN tasks t ON t.id = f.workspace_task_id
			  LEFT JOIN projects p ON p.id = t.project_id
			 WHERE f.id IN (?`+strings.Repeat(", ?", len(batch)-1)+`)`, batch...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var f FindingLink
			if err := rows.Scan(&f.ID, &f.TaskID, &f.ProjectSlug, &f.PlanID, &f.PhaseID, &f.PhaseSeq, &f.PhaseName,
				&f.Round, &f.FindingID, &f.Type, &f.File, &f.Line); err != nil {
				rows.Close()
				return err
			}
			byID[f.ID] = f
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for i := range ls {
		for _, e := range ls[i].Evidence {
			if rest, ok := strings.CutPrefix(e, findingPrefix); ok {
				if id, err := strconv.ParseInt(rest, 10, 64); err == nil {
					if f, ok := byID[id]; ok {
						ls[i].Findings = append(ls[i].Findings, f)
					}
				}
			}
		}
	}
	return nil
}

type identity struct {
	kind  string
	id    int64
	norm  string
	title string
	count int
}

// attachMatches fills Matches on every candidate in ls: exact norm_title
// matches first, then word-overlap (Jaccard ≥ 0.5) ones.
func attachMatches(db *sql.DB, ls []Lesson) error {
	need := false
	for _, l := range ls {
		need = need || l.Status == StatusCandidate
	}
	if !need {
		return nil
	}
	var pool []identity
	rows, err := db.Query(`SELECT norm_title, MAX(title), COUNT(*) FROM retro_lessons
		WHERE norm_title <> '' GROUP BY norm_title`)
	if err != nil {
		return err
	}
	for rows.Next() {
		it := identity{kind: "retro"}
		if err := rows.Scan(&it.norm, &it.title, &it.count); err != nil {
			rows.Close()
			return err
		}
		pool = append(pool, it)
	}
	rows.Close()
	rows, err = db.Query(`SELECT id, norm_title, title, recurrences FROM surprise_lessons
		WHERE status IN ('candidate', 'active') AND norm_title <> ''`)
	if err != nil {
		return err
	}
	for rows.Next() {
		it := identity{kind: "surprise"}
		if err := rows.Scan(&it.id, &it.norm, &it.title, &it.count); err != nil {
			rows.Close()
			return err
		}
		pool = append(pool, it)
	}
	rows.Close()
	for i := range ls {
		if ls[i].Status == StatusCandidate {
			ls[i].Matches = matchesFor(ls[i], pool)
		}
	}
	return nil
}

func matchesFor(l Lesson, pool []identity) []Match {
	type scored struct {
		m Match
		s float64
	}
	var got []scored
	words := wordSet(l.NormTitle)
	for _, it := range pool {
		if it.kind == "surprise" && it.id == l.ID {
			continue
		}
		s := 1.0
		if it.norm != l.NormTitle {
			if s = jaccard(words, wordSet(it.norm)); s < 0.5 {
				continue
			}
		}
		got = append(got, scored{Match{Kind: it.kind, LessonID: it.id, NormTitle: it.norm, Title: it.title,
			Count: it.count, Exact: it.norm == l.NormTitle}, s})
	}
	sort.SliceStable(got, func(i, j int) bool { return got[i].s > got[j].s })
	out := []Match{}
	for i := 0; i < len(got) && i < maxMatches; i++ {
		out = append(out, got[i].m)
	}
	return out
}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		out[w] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// transition applies one guarded state change and reports ErrNotFound /
// ErrState precisely.
func transition(db *sql.DB, id int64, res sql.Result, err error) (Lesson, error) {
	if err != nil {
		return Lesson{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := Get(db, id); gerr != nil {
			return Lesson{}, gerr
		}
		return Lesson{}, ErrState
	}
	return Get(db, id)
}

func stamp(now time.Time) string { return now.UTC().Format(time.RFC3339) }

// Accept makes a candidate active. It is the ONLY function in the daemon that
// writes status = 'active' (activeguard_test.go enforces this), and it is only
// reachable from the operator endpoint POST /api/lessons/{id}/accept.
func Accept(db *sql.DB, id int64, now time.Time) (Lesson, error) {
	ts := stamp(now)
	res, err := db.Exec(`UPDATE surprise_lessons SET status = 'active', activated_at = ?, updated_at = ?
		WHERE id = ? AND status = 'candidate'`, ts, ts, id)
	return transition(db, id, res, err)
}

// Dismiss drops a candidate, keeping the row (and the reason) for calibration.
// Dismissing a review candidate also mutes its (type, area) for ReviewMuteDays
// (lesson_mutes), in the same transaction: the operator's "not this one" holds
// for the group, not just for this row.
func Dismiss(db *sql.DB, id int64, reason string, now time.Time) (Lesson, error) {
	ts := stamp(now)
	tx, err := db.Begin()
	if err != nil {
		return Lesson{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE surprise_lessons SET status = 'dismissed', retire_reason = ?, retired_at = ?,
		updated_at = ? WHERE id = ? AND status = 'candidate'`, strings.TrimSpace(reason), ts, ts, id)
	if err != nil {
		return Lesson{}, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		if err := muteReviewGroup(tx, id, now); err != nil {
			return Lesson{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Lesson{}, err
	}
	return transition(db, id, res, nil)
}

// muteReviewGroup records a review lesson's dismissal as a ReviewMuteDays mute on
// its (type, area); a surprise lesson has no group and writes nothing. A second
// dismissal of the same group restarts the mute.
func muteReviewGroup(tx *sql.Tx, id int64, now time.Time) error {
	var source, typ, area string
	if err := tx.QueryRow(`SELECT source, review_type, review_area FROM surprise_lessons WHERE id = ?`, id).
		Scan(&source, &typ, &area); err != nil {
		return err
	}
	if source != SourceReview || typ == "" || area == "" {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO lesson_mutes (type, area, muted_at, muted_until, lesson_id) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(type, area) DO UPDATE SET muted_at = excluded.muted_at, muted_until = excluded.muted_until,
			lesson_id = excluded.lesson_id`,
		typ, area, stamp(now), stamp(now.AddDate(0, 0, ReviewMuteDays)), id)
	return err
}

// Retire takes an active lesson out of circulation.
func Retire(db *sql.DB, id int64, reason string, now time.Time) (Lesson, error) {
	ts := stamp(now)
	res, err := db.Exec(`UPDATE surprise_lessons SET status = 'retired', retire_reason = ?, retired_at = ?,
		updated_at = ? WHERE id = ? AND status = 'active'`, strings.TrimSpace(reason), ts, ts, id)
	return transition(db, id, res, err)
}

// EditInput is an operator edit; nil fields are left unchanged.
type EditInput struct {
	Title     *string   `json:"title"`
	Guidance  *string   `json:"guidance"`
	AreaGlobs *[]string `json:"areaGlobs"`
}

// Edit rewrites a candidate's or an active lesson's words and areas under the
// same contract generation enforces. It never touches the status.
func Edit(db *sql.DB, id int64, in EditInput, now time.Time) (Lesson, error) {
	cur, err := Get(db, id)
	if err != nil {
		return cur, err
	}
	if cur.Status != StatusCandidate && cur.Status != StatusActive {
		return cur, ErrState
	}
	title, guidance, globs := cur.Title, cur.Guidance, cur.AreaGlobs
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
	}
	if in.Guidance != nil {
		guidance = strings.TrimSpace(*in.Guidance)
	}
	if err := checkText(title, guidance); err != nil {
		return cur, err
	}
	if in.AreaGlobs != nil {
		if globs, err = NormalizeGlobs(*in.AreaGlobs); err != nil {
			return cur, err
		}
	}
	norm := wsingest.NormalizeLessonTitle(title)
	if norm == "" {
		return cur, fmt.Errorf("%w: the title folds to an empty identity", ErrInvalid)
	}
	res, err := db.Exec(`UPDATE surprise_lessons SET title = ?, norm_title = ?, guidance = ?, area_globs = ?,
		updated_at = ? WHERE id = ? AND status IN ('candidate', 'active')`,
		title, norm, guidance, strings.Join(globs, ","), stamp(now), id)
	return transition(db, id, res, err)
}

// MergeTarget names the lesson a candidate is merged into: another surprise
// lesson by id, or a retro lesson identity by norm_title.
type MergeTarget struct {
	LessonID  int64  `json:"lessonId"`
	NormTitle string `json:"normTitle"`
}

// Merge folds a candidate into an existing lesson. A surprise-lesson target
// absorbs the candidate's run and evidence as one more recurrence and keeps its
// own status; a retro target is recorded as the candidate's linked identity.
func Merge(db *sql.DB, id int64, target MergeTarget, now time.Time) (Lesson, error) {
	cur, err := Get(db, id)
	if err != nil {
		return cur, err
	}
	if cur.Status != StatusCandidate {
		return cur, ErrState
	}
	ts := stamp(now)
	switch {
	case target.LessonID > 0:
		if target.LessonID == id {
			return cur, fmt.Errorf("%w: a lesson cannot be merged into itself", ErrInvalid)
		}
		tgt, err := Get(db, target.LessonID)
		if errors.Is(err, ErrNotFound) {
			return cur, fmt.Errorf("%w: merge target %d does not exist", ErrInvalid, target.LessonID)
		}
		if err != nil {
			return cur, err
		}
		if tgt.Status != StatusCandidate && tgt.Status != StatusActive {
			return cur, fmt.Errorf("%w: merge target %d is %s", ErrInvalid, tgt.ID, tgt.Status)
		}
		// Every run the candidate had already absorbed moves with it, not only its
		// source run: a merge must not shrink the recurrence record.
		runs := cur.RecurrenceRuns
		if len(runs) == 0 {
			runs = []string{cur.SourcePhaseRun}
		}
		for i, r := range runs {
			ev := cur.Evidence
			if i > 0 {
				ev = nil
			}
			if err := addRecurrence(db, tgt.ID, r, ev, ts); err != nil {
				return cur, err
			}
		}
		res, err := db.Exec(`UPDATE surprise_lessons SET status = 'merged', merged_into_id = ?, updated_at = ?
			WHERE id = ? AND status = 'candidate'`, tgt.ID, ts, id)
		return transition(db, id, res, err)
	case strings.TrimSpace(target.NormTitle) != "":
		norm := strings.TrimSpace(target.NormTitle)
		var found string
		err := db.QueryRow(`SELECT norm_title FROM retro_lessons WHERE norm_title = ? LIMIT 1`, norm).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return cur, fmt.Errorf("%w: no retro lesson has the identity %q", ErrInvalid, norm)
		}
		if err != nil {
			return cur, err
		}
		res, err := db.Exec(`UPDATE surprise_lessons SET status = 'merged', linked_norm_title = ?, updated_at = ?
			WHERE id = ? AND status = 'candidate'`, found, ts, id)
		return transition(db, id, res, err)
	}
	return cur, fmt.Errorf("%w: name a lessonId or a normTitle to merge into", ErrInvalid)
}
