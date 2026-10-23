package lessons

// Lesson candidates from repeated review findings (verify-not-trust phase 8,
// D3/D38). A finding TYPE that recurs in one code AREA across two or more
// phases becomes ONE surprise_lessons candidate with source = 'review' — a
// candidate, never a rule: Accept (review.go) stays the only path to 'active',
// and nothing here edits an agent, a skill or a plugin file.
//
// GROUPING. review_findings rows (wsingest's projection of every phase doc's
// `kind: review` blocks, migration 0123) are placed by surprise.AreaOf(file) —
// the file's directory. Two findings share a group when their type is equal and
// their areas overlap by surprise.AreaOverlap's segment rule at most one
// directory level apart (surprise.AreaApart ≤ maxAreaApart): a file at a module
// root (tools/app/README.md) is two levels above tools/app/internal/api, so it
// cannot pull every finding beneath it into one group. Groups are ANCHORED:
// findings are taken in (type, area, id) order — a directory sorts before its
// subdirectories — and each joins the first group whose anchor area is near its
// own, so the relation never chains sideways through a shared parent. A group is
// a repeat when its findings come from ≥ MinReviewPhases phase docs (any plans).
//
// ONE CANDIDATE PER GROUP. A review lesson remembers its group (review_type,
// review_area). A later tick that finds the group again extends that row's
// evidence_json instead of inserting — while it is a candidate or active, or
// through the lesson the operator merged it into. A dismissed one is muted
// (lesson_mutes, written by Dismiss): for ReviewMuteDays nothing re-proposes it,
// and after that only a finding first seen after the dismissal does.
//
// THE DRAFT. The guidance sentence comes from the cheap model the surprise path
// uses (DraftReview), under the same validation and fabrication check. A failed
// or empty draft still inserts the candidate, with DraftPendingGuidance, so the
// operator sees the repeat; a later extension of the group retries the draft. A
// placeholder lesson is never injected (loadActive skips it).
//
// Best-effort throughout: the proposer logs and carries on, and nothing here can
// fail or delay a review.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Lesson sources (surprise_lessons.source, migration 0129).
const (
	SourceSurprise = "surprise"
	SourceReview   = "review"
)

const (
	// DraftPendingGuidance is a review candidate's guidance until the model drafts
	// one (or the operator writes it). Never injected.
	DraftPendingGuidance = "(draft pending)"
	// MinReviewPhases is how many phase docs a group must span to be a repeat.
	MinReviewPhases = 2
	// ReviewMuteDays is how long dismissing a review candidate mutes its group.
	ReviewMuteDays = 30
	// maxAreaApart bounds how far apart two areas of one group may lie.
	maxAreaApart = 1
	// defaultKickDelay is how long Kick waits: the review block a finished review
	// appended reaches review_findings on the next workspace scan (wsingest
	// rescans every 60 s), not at once.
	defaultKickDelay = 2 * time.Minute
	// reviewTickTimeout bounds one tick, model drafts included.
	reviewTickTimeout = 15 * time.Minute
	// maxRootGlobs caps the per-file globs of a group at the repo root.
	maxRootGlobs = maxAreaGlobs
	// noLessonReason is a draft's DraftErr when the model returned no lesson.
	noLessonReason = "the model returned no valid lesson"
)

// ReviewFinding is one review_findings row as grouping and drafting read it.
type ReviewFinding struct {
	ID        int64 // review_findings.id — cited as FindingRef(ID)
	TaskID    int64
	PhaseID   int64 // 0 when the doc resolved to no phase at the last scan
	PhaseName string
	PlanID    string
	DocPath   string
	Round     string
	FindingID string // the block's own id (R1-2)
	Type      string
	File      string
	Line      int64
	Criterion string
	Evidence  string
	Note      string
	CreatedAt string
	Area      string // surprise.AreaOf(File)
}

// Group is one (type, area) set of findings.
type Group struct {
	Type       string
	Area       string // the anchor finding's area
	FindingIDs []int64
	PhaseIDs   []int64  // distinct, non-zero
	Docs       []string // distinct phase docs — the phases the group spans
	Findings   []ReviewFinding
}

// Phases is how many phases (docs) the group spans.
func (g Group) Phases() int { return len(g.Docs) }

// EvidenceIDs are the group's evidence ids, "finding:<id>", in id order.
func (g Group) EvidenceIDs() []string {
	out := make([]string, 0, len(g.FindingIDs))
	for _, id := range g.FindingIDs {
		out = append(out, FindingRef(id))
	}
	return out
}

// AreaGlobs is where the group's lesson applies: everything under its area, or
// — for findings on files at the repo root — those files.
func (g Group) AreaGlobs() []string {
	if g.Area != "." {
		return []string{g.Area + "/**"}
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range g.Findings {
		p := surprise.NormArea(f.File)
		if p == "" || p == "." || seen[p] || strings.ContainsAny(p, " \t,") {
			continue
		}
		seen[p] = true
		if out = append(out, p); len(out) == maxRootGlobs {
			break
		}
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

func (g Group) newestPhaseID() int64 {
	for i := len(g.Findings) - 1; i >= 0; i-- {
		if g.Findings[i].PhaseID > 0 {
			return g.Findings[i].PhaseID
		}
	}
	return 0
}

// hasFindingAfter reports whether any finding was first seen after t.
func (g Group) hasFindingAfter(t time.Time) bool {
	for _, f := range g.Findings {
		if at, err := time.Parse(time.RFC3339, f.CreatedAt); err == nil && at.After(t) {
			return true
		}
	}
	return false
}

// near is the grouping relation between two areas.
func near(x, y string) bool {
	d, ok := surprise.AreaApart(x, y)
	return ok && d <= maxAreaApart
}

// loadFindings reads every typed finding that names a file.
func loadFindings(db *sql.DB) ([]ReviewFinding, error) {
	rows, err := db.Query(`
		SELECT f.id, f.workspace_task_id, COALESCE(f.phase_id, 0), COALESCE(e.name, ''), COALESCE(t.external_id, ''),
		       f.doc_path, f.round, f.finding_id, f.type, f.file, COALESCE(f.line, 0), f.criterion, f.evidence,
		       f.note, f.created_at
		  FROM review_findings f
		  LEFT JOIN epic_phases e ON e.id = f.phase_id
		  LEFT JOIN tasks t ON t.id = f.workspace_task_id
		 WHERE f.type IS NOT NULL AND f.file <> ''
		 ORDER BY f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewFinding
	for rows.Next() {
		var f ReviewFinding
		if err := rows.Scan(&f.ID, &f.TaskID, &f.PhaseID, &f.PhaseName, &f.PlanID, &f.DocPath, &f.Round,
			&f.FindingID, &f.Type, &f.File, &f.Line, &f.Criterion, &f.Evidence, &f.Note, &f.CreatedAt); err != nil {
			return nil, err
		}
		if f.Area = surprise.AreaOf(f.File); f.Area != "" {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}

// GroupFindings groups the review findings by (type, area) and returns the
// repeats — groups spanning ≥ MinReviewPhases phases — ordered by type, then
// area. Each group's findings are in id order.
func GroupFindings(db *sql.DB) ([]Group, error) {
	fs, err := loadFindings(db)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Type != fs[j].Type {
			return fs[i].Type < fs[j].Type
		}
		if fs[i].Area != fs[j].Area {
			return fs[i].Area < fs[j].Area
		}
		return fs[i].ID < fs[j].ID
	})
	var all []*Group
	for _, f := range fs {
		var g *Group
		for _, c := range all {
			if c.Type == f.Type && near(c.Area, f.Area) {
				g = c
				break
			}
		}
		if g == nil {
			g = &Group{Type: f.Type, Area: f.Area}
			all = append(all, g)
		}
		g.Findings = append(g.Findings, f)
	}
	var out []Group
	for _, g := range all {
		sort.Slice(g.Findings, func(i, j int) bool { return g.Findings[i].ID < g.Findings[j].ID })
		docs, phases := map[string]bool{}, map[int64]bool{}
		for _, f := range g.Findings {
			g.FindingIDs = append(g.FindingIDs, f.ID)
			if !docs[f.DocPath] {
				docs[f.DocPath] = true
				g.Docs = append(g.Docs, f.DocPath)
			}
			if f.PhaseID > 0 && !phases[f.PhaseID] {
				phases[f.PhaseID] = true
				g.PhaseIDs = append(g.PhaseIDs, f.PhaseID)
			}
		}
		if g.Phases() >= MinReviewPhases {
			out = append(out, *g)
		}
	}
	return out, nil
}

// ReviewProposal actions.
const (
	ProposalInserted  = "inserted"  // a new candidate row
	ProposalExtended  = "extended"  // new finding ids folded into the group's lesson
	ProposalUnchanged = "unchanged" // the group's lesson already cites every finding
	ProposalMuted     = "muted"     // the operator dismissed this group (lesson_mutes)
)

// ReviewProposal reports one ProposeFromReview call.
type ReviewProposal struct {
	Action   string
	LessonID int64
	// Drafted is true when the model wrote the guidance this call stored.
	Drafted bool
	// DraftErr says why a draft was attempted and not stored ("" otherwise).
	DraftErr string
	Rejected []Rejection
}

// ReviewProposer turns repeated review findings into lesson candidates.
type ReviewProposer struct {
	DB *sql.DB
	// Runner drafts the guidance; nil ⇒ every candidate keeps DraftPendingGuidance.
	Runner Runner
	// Model is recorded on a drafted lesson.
	Model string
	// Enabled gates Tick and Kick (SWARMERY_LESSONS — the same switch as the
	// surprise path). ProposeFromReview itself is ungated.
	Enabled bool
	// Now is the clock (test seam; nil ⇒ time.Now).
	Now func() time.Time
	// KickDelay overrides defaultKickDelay when > 0.
	KickDelay time.Duration
	// AfterFunc schedules Kick's tick (test seam; nil ⇒ time.AfterFunc).
	AfterFunc func(d time.Duration, f func())

	tickMu  sync.Mutex // one tick at a time: two would insert one group twice
	kickMu  sync.Mutex
	pending bool // a kicked tick is scheduled and has not started
}

// NewReviewProposer builds a proposer with the production runner.
func NewReviewProposer(db *sql.DB, cfg Config) *ReviewProposer {
	return &ReviewProposer{DB: db, Runner: ClaudeRunner{Model: cfg.Model}, Model: cfg.Model, Enabled: cfg.Enabled}
}

func (p *ReviewProposer) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// reviewLessonRow is the group's lesson, when it has one.
type reviewLessonRow struct {
	id       int64
	absorbed bool // merged into something the proposer must not extend
}

// existing finds the group's lesson: a review lesson of the same type whose
// area is near the group's, active before candidate before merged. A merged one
// forwards to its target while that is a candidate or active; otherwise (merged
// into a retro identity, or a target since retired) the group stays absorbed.
func (p *ReviewProposer) existing(g Group) (*reviewLessonRow, error) {
	rows, err := p.DB.Query(`SELECT id, status, review_area, merged_into_id FROM surprise_lessons
		WHERE source = 'review' AND review_type = ? AND status IN ('candidate', 'active', 'merged')
		ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'candidate' THEN 1 ELSE 2 END, id`, g.Type)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id     int64
		status string
		into   sql.NullInt64
	}
	var hit *cand
	for rows.Next() {
		var (
			c    cand
			area string
		)
		if err := rows.Scan(&c.id, &c.status, &area, &c.into); err != nil {
			rows.Close()
			return nil, err
		}
		if hit == nil && near(area, g.Area) {
			hit = &c
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || hit == nil {
		return nil, err
	}
	if hit.status != StatusMerged {
		return &reviewLessonRow{id: hit.id}, nil
	}
	if hit.into.Valid {
		var st string
		err := p.DB.QueryRow(`SELECT status FROM surprise_lessons WHERE id = ?`, hit.into.Int64).Scan(&st)
		switch {
		case err == nil && (st == StatusCandidate || st == StatusActive):
			return &reviewLessonRow{id: hit.into.Int64}, nil
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	return &reviewLessonRow{id: hit.id, absorbed: true}, nil
}

// muted reports whether the operator's dismissal still holds for the group: a
// mute on the same type whose area is near the group's, either within its 30
// days, or expired with no finding first seen since it was set.
func (p *ReviewProposer) muted(g Group, now time.Time) (bool, error) {
	rows, err := p.DB.Query(`SELECT area, muted_at, muted_until FROM lesson_mutes WHERE type = ?`, g.Type)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var area, at, until string
		if err := rows.Scan(&area, &at, &until); err != nil {
			return false, err
		}
		if !near(area, g.Area) {
			continue
		}
		if u, err := time.Parse(time.RFC3339, until); err != nil || now.Before(u) {
			return true, nil // an unreadable mute errs on the operator's side
		}
		if a, err := time.Parse(time.RFC3339, at); err != nil || !g.hasFindingAfter(a) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// draft asks the model for the group's lesson.
func (p *ReviewProposer) draft(ctx context.Context, g Group) (c Candidate, ok bool, rejected []Rejection, why string) {
	if p.Runner == nil {
		return c, false, nil, "no runner configured"
	}
	c, ok, rejected, err := DraftReview(ctx, p.Runner, NewReviewInput(g))
	switch {
	case err != nil:
		return c, false, rejected, err.Error()
	case !ok:
		return c, false, rejected, noLessonReason
	}
	return c, true, rejected, ""
}

// ProposeFromReview writes the group's candidate, or folds the group's new
// findings into the lesson it already has. One candidate per group: see the
// file comment for the identity, the mute and the draft.
func (p *ReviewProposer) ProposeFromReview(ctx context.Context, g Group) (ReviewProposal, error) {
	now := p.now().UTC()
	ex, err := p.existing(g)
	if err != nil {
		return ReviewProposal{}, err
	}
	if ex != nil {
		if ex.absorbed {
			return ReviewProposal{Action: ProposalUnchanged, LessonID: ex.id}, nil
		}
		return p.extend(ctx, g, ex.id, now)
	}
	muted, err := p.muted(g, now)
	if err != nil {
		return ReviewProposal{}, err
	}
	if muted {
		return ReviewProposal{Action: ProposalMuted}, nil
	}
	return p.insert(ctx, g, now)
}

// insert writes a new review candidate. The status is the literal 'candidate',
// as for every generated lesson (activeguard_test.go).
func (p *ReviewProposer) insert(ctx context.Context, g Group, now time.Time) (ReviewProposal, error) {
	out := ReviewProposal{Action: ProposalInserted}
	title := capRunes(fmt.Sprintf("Repeated %s findings in %s", g.Type, g.Area), maxTitleRunes)
	guidance, model := DraftPendingGuidance, ""
	cause := fmt.Sprintf("Reviewers raised %s findings in %s in %d phases.", g.Type, g.Area, g.Phases())
	c, ok, rejected, why := p.draft(ctx, g)
	out.Rejected, out.DraftErr = rejected, why
	switch {
	case ok:
		title, guidance, cause, model, out.Drafted = c.Title, c.Guidance, c.Cause, p.Model, true
	case why == noLessonReason && len(rejected) == 0:
		// The model looked and declined: say so, so the operator knows the
		// placeholder is a judgement, not a pending job.
		cause += " The draft found no thread they share: read the findings and write the guidance, or dismiss to mute this area for 30 days."
	}
	norm := wsingest.NormalizeLessonTitle(title)
	key := "review:" + g.Type + ":" + g.Area
	ev, _ := json.Marshal(g.EvidenceIDs())
	ts := stamp(now)
	var seq int
	if err := p.DB.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM surprise_lessons WHERE source_phase_run = ?`, key).
		Scan(&seq); err != nil {
		return out, err
	}
	res, err := p.DB.Exec(`INSERT INTO surprise_lessons
		(source_phase_run, phase_id, seq, title, norm_title, guidance, area_globs, evidence_json, cause,
		 status, recurrences, model, created_at, updated_at, source, review_type, review_area)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'candidate', ?, ?, ?, ?, 'review', ?, ?)`,
		key, g.newestPhaseID(), seq, title, norm, guidance, strings.Join(g.AreaGlobs(), ","), string(ev), cause,
		g.Phases(), model, ts, ts, g.Type, g.Area)
	if err != nil {
		return out, err
	}
	out.LessonID, _ = res.LastInsertId()
	return out, nil
}

// extend folds the group's findings into its lesson's evidence. A candidate
// still waiting for its draft gets one more attempt when the group grew.
func (p *ReviewProposer) extend(ctx context.Context, g Group, id int64, now time.Time) (ReviewProposal, error) {
	out := ReviewProposal{Action: ProposalUnchanged, LessonID: id}
	var evJSON, guidance, status, source string
	var recurrences int
	if err := p.DB.QueryRow(`SELECT evidence_json, guidance, status, source, recurrences FROM surprise_lessons WHERE id = ?`, id).
		Scan(&evJSON, &guidance, &status, &source, &recurrences); err != nil {
		return out, err
	}
	var ev []string
	_ = json.Unmarshal([]byte(evJSON), &ev)
	merged := union(ev, g.EvidenceIDs())
	grew := len(merged) > len(ev)
	if !grew && (source != SourceReview || recurrences >= g.Phases()) {
		return out, nil
	}
	ts := stamp(now)
	eb, _ := json.Marshal(merged)
	if _, err := p.DB.Exec(`UPDATE surprise_lessons SET evidence_json = ?,
		recurrences = CASE WHEN source = 'review' AND recurrences < ? THEN ? ELSE recurrences END, updated_at = ?
		WHERE id = ? AND status IN ('candidate', 'active')`, string(eb), g.Phases(), g.Phases(), ts, id); err != nil {
		return out, err
	}
	out.Action = ProposalExtended
	if !grew || status != StatusCandidate || guidance != DraftPendingGuidance {
		return out, nil
	}
	c, ok, rejected, why := p.draft(ctx, g)
	out.Rejected, out.DraftErr = rejected, why
	if !ok {
		return out, nil
	}
	norm := wsingest.NormalizeLessonTitle(c.Title)
	if _, err := p.DB.Exec(`UPDATE surprise_lessons SET title = ?, norm_title = ?, guidance = ?, cause = ?, model = ?,
		updated_at = ? WHERE id = ? AND status = 'candidate' AND guidance = ?`,
		c.Title, norm, c.Guidance, c.Cause, p.Model, ts, id, DraftPendingGuidance); err != nil {
		return out, err
	}
	out.Drafted = true
	return out, nil
}

// TickOutcome reports one Tick.
type TickOutcome struct {
	Groups, Inserted, Extended, Muted, Drafted int
}

// Tick groups every review finding and proposes each repeat. Serialized: one
// tick at a time. A group that fails is logged and skipped.
func (p *ReviewProposer) Tick(ctx context.Context) (TickOutcome, error) {
	var out TickOutcome
	if p == nil || !p.Enabled || p.DB == nil {
		return out, nil
	}
	p.tickMu.Lock()
	defer p.tickMu.Unlock()
	groups, err := GroupFindings(p.DB)
	if err != nil {
		return out, err
	}
	out.Groups = len(groups)
	for _, g := range groups {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		pr, err := p.ProposeFromReview(ctx, g)
		if err != nil {
			log.Printf("warning: lessons: review group %s in %s: %v", g.Type, g.Area, err)
			continue
		}
		switch pr.Action {
		case ProposalInserted:
			out.Inserted++
		case ProposalExtended:
			out.Extended++
		case ProposalMuted:
			out.Muted++
		}
		if pr.Drafted {
			out.Drafted++
		}
		if pr.DraftErr != "" {
			log.Printf("lessons: review group %s in %s: lesson %d kept %q: %s (%d rejected)",
				g.Type, g.Area, pr.LessonID, DraftPendingGuidance, pr.DraftErr, len(pr.Rejected))
		}
	}
	return out, nil
}

// RunLogged is Tick for the daemon: bounded, logged, never panicking.
func (p *ReviewProposer) RunLogged() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("error: lessons: review tick panicked: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), reviewTickTimeout)
	defer cancel()
	out, err := p.Tick(ctx)
	switch {
	case err != nil:
		log.Printf("warning: lessons: review tick: %v", err)
	case out.Inserted+out.Extended > 0:
		log.Printf("lessons: review tick: %d repeated group(s), %d new candidate(s), %d extended, %d muted, %d drafted",
			out.Groups, out.Inserted, out.Extended, out.Muted, out.Drafted)
	}
}

// Kick schedules one tick after the kick delay; kicks that arrive while one is
// pending fold into it. phaserun calls it after every review it records.
func (p *ReviewProposer) Kick() {
	if p == nil || !p.Enabled {
		return
	}
	p.kickMu.Lock()
	if p.pending {
		p.kickMu.Unlock()
		return
	}
	p.pending = true
	p.kickMu.Unlock()
	delay := p.KickDelay
	if delay <= 0 {
		delay = defaultKickDelay
	}
	after := p.AfterFunc
	if after == nil {
		after = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	after(delay, func() {
		p.kickMu.Lock()
		p.pending = false
		p.kickMu.Unlock()
		p.RunLogged()
	})
}
