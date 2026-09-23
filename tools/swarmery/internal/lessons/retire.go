package lessons

// Forgetting (Opus 5.5 / learning-loop phase 16.2): the retirement queue.
//
// A periodic daemon pass (Verifier.Run) re-measures every ACTIVE lesson and
// PROPOSES a retirement when one of four reasons holds:
//
//	ineffective  the area's median surprise did not drop after the lesson
//	             (a MEASURED drop ≤ 0 with ≥ MinRuns runs on each side; not
//	             enough data is never ineffective)
//	stale        the area's code churned past StaleChurn of its lines since
//	             activated_at (git numstat, baseline = the last commit before
//	             activation)
//	unused_60d   not injected into any run for UnusedDays (activation counts
//	             as the last use of a lesson never injected)
//	superseded   a newer active lesson carries the same identity (norm_title)
//
// PROPOSED, NOT SILENT. A proposal waits in the queue for the operator: confirm
// retires the lesson with retire_reason = the reason, keep closes the proposal
// and holds that reason off for KeepCooldown days. The ONE documented
// exception: a proposal left unanswered for AutoRetireDays (14 by default) is
// retired by the pass itself — same retire_reason, state 'auto_retired', and a
// log line — so an unattended queue cannot keep a useless lesson in every
// prompt forever.
//
// IDEMPOTENT. At most one open proposal per lesson (a partial unique index,
// 0086); a second pass in an unchanged world proposes nothing, retires nothing
// and rewrites only the effectiveness rows. A reason that DEFINITELY no longer
// holds withdraws its open proposal; one that cannot be evaluated (git
// unavailable, a repo that moved) leaves it where it is.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// Retirement reasons.
const (
	ReasonIneffective = "ineffective"
	ReasonStale       = "stale"
	ReasonUnused      = "unused_60d"
	ReasonSuperseded  = "superseded"
)

// Proposal states.
const (
	ProposalOpen      = "proposed"
	ProposalConfirmed = "confirmed"
	ProposalKept      = "kept"
	ProposalAuto      = "auto_retired"
	ProposalWithdrawn = "withdrawn"
)

// emptyTree is git's well-known empty tree: a numstat against it counts every
// line a path holds at HEAD.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Proposal is one lesson_retirements row with the lesson it concerns.
type Proposal struct {
	ID            int64             `json:"id"`
	LessonID      int64             `json:"lessonId"`
	Title         string            `json:"title"`
	Guidance      string            `json:"guidance"`
	AreaGlobs     []string          `json:"areaGlobs"`
	Reason        string            `json:"reason"`
	Detail        string            `json:"detail"`
	Evidence      map[string]any    `json:"evidence"`
	State         string            `json:"state"`
	ProposedAt    string            `json:"proposedAt"`
	DecidedAt     *string           `json:"decidedAt"`
	AutoRetireAt  *string           `json:"autoRetireAt"` // nil when auto-retire is off or the proposal is closed
	Effectiveness *EffectivenessRow `json:"effectiveness"`
}

// verdict is one reason's evaluation: fires, or definitely not, or unknown.
type verdict struct {
	fires, known bool
	detail       string
	evidence     map[string]any
}

// Verifier runs the verification pass.
type Verifier struct {
	DB      *sql.DB
	Git     worktree.Git // nil disables the stale check
	Resolve RepoResolver // nil disables the stale check
	Cfg     VerifyConfig
	Now     func() time.Time
}

// NewVerifier builds a verifier.
func NewVerifier(db *sql.DB, git worktree.Git, resolve RepoResolver, cfg VerifyConfig) *Verifier {
	return &Verifier{DB: db, Git: git, Resolve: resolve, Cfg: cfg, Now: time.Now}
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// VerifyStats counts one pass.
type VerifyStats struct {
	Measured, Proposed, Withdrawn, AutoRetired int
}

// activeLesson is an active lesson with what the reasons need.
type activeLesson struct {
	Active
	normTitle string
	phaseID   int64
}

func (v *Verifier) loadActive() ([]activeLesson, error) {
	rows, err := v.DB.Query(`SELECT id, title, guidance, area_globs, COALESCE(activated_at, updated_at),
		norm_title, phase_id FROM surprise_lessons WHERE status = ? ORDER BY id`, StatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activeLesson
	for rows.Next() {
		var (
			a     activeLesson
			globs string
		)
		if err := rows.Scan(&a.ID, &a.Title, &a.Guidance, &globs, &a.ActivatedAt, &a.normTitle, &a.phaseID); err != nil {
			return nil, err
		}
		a.AreaGlobs = splitGlobs(globs)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Run is one pass: measure, propose, withdraw, auto-retire.
func (v *Verifier) Run() (VerifyStats, error) {
	var st VerifyStats
	now := v.now()
	active, err := v.loadActive()
	if err != nil {
		return st, err
	}
	runs, err := LoadAreaRuns(v.DB)
	if err != nil {
		return st, err
	}
	for _, l := range active {
		eff, err := ComputeEffectiveness(v.DB, l.Active, runs, v.Cfg, now)
		if err != nil {
			return st, fmt.Errorf("lesson %s effectiveness: %w", Ref(l.ID), err)
		}
		if err := StoreEffectiveness(v.DB, eff); err != nil {
			return st, err
		}
		st.Measured++
		if err := v.review(l, eff, active, now, &st); err != nil {
			return st, fmt.Errorf("lesson %s: %w", Ref(l.ID), err)
		}
	}
	// A proposal whose lesson left 'active' some other way (the operator retired
	// it from the card) is moot.
	res, err := v.DB.Exec(`UPDATE lesson_retirements SET state = ?, decided_at = ?
		WHERE state = ? AND lesson_id NOT IN (SELECT id FROM surprise_lessons WHERE status = ?)`,
		ProposalWithdrawn, stamp(now), ProposalOpen, StatusActive)
	if err != nil {
		return st, err
	}
	n, _ := res.RowsAffected()
	st.Withdrawn += int(n)
	if err := v.autoRetire(now, &st); err != nil {
		return st, err
	}
	return st, nil
}

// RunLogged is Run for the daemon ticker: it logs and never fails.
func (v *Verifier) RunLogged() {
	st, err := v.Run()
	if err != nil {
		log.Printf("warning: lessons: verification pass: %v", err)
		return
	}
	if st.Proposed+st.Withdrawn+st.AutoRetired > 0 {
		log.Printf("lessons: verification pass: measured=%d proposed=%d withdrawn=%d auto-retired=%d",
			st.Measured, st.Proposed, st.Withdrawn, st.AutoRetired)
	}
}

// review evaluates the reasons for one lesson in priority order and keeps the
// queue in step with them.
func (v *Verifier) review(l activeLesson, eff EffectivenessRow, all []activeLesson, now time.Time, st *VerifyStats) error {
	var open struct {
		id     int64
		reason string
	}
	err := v.DB.QueryRow(`SELECT id, reason FROM lesson_retirements WHERE lesson_id = ? AND state = ?`,
		l.ID, ProposalOpen).Scan(&open.id, &open.reason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	verdicts := map[string]verdict{
		ReasonSuperseded:  superseded(l, all),
		ReasonIneffective: ineffective(eff),
		ReasonStale:       v.stale(l),
	}
	unused, err := v.unused(l, now)
	if err != nil {
		return err
	}
	verdicts[ReasonUnused] = unused
	if open.id != 0 {
		if vd := verdicts[open.reason]; vd.known && !vd.fires {
			if _, err := v.DB.Exec(`UPDATE lesson_retirements SET state = ?, decided_at = ? WHERE id = ? AND state = ?`,
				ProposalWithdrawn, stamp(now), open.id, ProposalOpen); err != nil {
				return err
			}
			st.Withdrawn++
		} else {
			return nil // one open proposal per lesson
		}
	}
	for _, reason := range []string{ReasonSuperseded, ReasonIneffective, ReasonStale, ReasonUnused} {
		vd := verdicts[reason]
		if !vd.fires {
			continue
		}
		kept, err := v.keptRecently(l.ID, reason, now)
		if err != nil {
			return err
		}
		if kept {
			continue
		}
		ev, _ := json.Marshal(vd.evidence)
		res, err := v.DB.Exec(`INSERT OR IGNORE INTO lesson_retirements
			(lesson_id, reason, detail, evidence_json, state, proposed_at) VALUES (?, ?, ?, ?, ?, ?)`,
			l.ID, reason, vd.detail, string(ev), ProposalOpen, stamp(now))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			st.Proposed++
			log.Printf("lessons: proposed retiring %s (%s): %s", Ref(l.ID), reason, vd.detail)
		}
		return nil
	}
	return nil
}

func (v *Verifier) keptRecently(id int64, reason string, now time.Time) (bool, error) {
	var n int
	err := v.DB.QueryRow(`SELECT COUNT(*) FROM lesson_retirements WHERE lesson_id = ? AND reason = ? AND state = ?
		AND decided_at >= ?`, id, reason, ProposalKept, stamp(now.AddDate(0, 0, -v.Cfg.KeepCooldown))).Scan(&n)
	return n > 0, err
}

func ineffective(eff EffectivenessRow) verdict {
	if eff.MedianDrop == nil {
		return verdict{}
	}
	ev := map[string]any{"beforeN": eff.BeforeN, "afterN": eff.AfterN, "medianBefore": *eff.MedianBefore,
		"medianAfter": *eff.MedianAfter, "medianDrop": *eff.MedianDrop}
	if *eff.MedianDrop > 0 {
		return verdict{known: true, evidence: ev}
	}
	return verdict{fires: true, known: true, evidence: ev, detail: fmt.Sprintf(
		"no measured drop: median surprise %.2f over %d runs before activation, %.2f over %d runs after",
		*eff.MedianBefore, eff.BeforeN, *eff.MedianAfter, eff.AfterN)}
}

func superseded(l activeLesson, all []activeLesson) verdict {
	if l.normTitle == "" {
		return verdict{known: true}
	}
	for _, o := range all {
		if o.ID == l.ID || o.normTitle != l.normTitle {
			continue
		}
		if o.ActivatedAt > l.ActivatedAt || (o.ActivatedAt == l.ActivatedAt && o.ID > l.ID) {
			return verdict{fires: true, known: true, evidence: map[string]any{"supersededBy": o.ID},
				detail: fmt.Sprintf("superseded by %s, a newer active lesson with the same identity", Ref(o.ID))}
		}
	}
	return verdict{known: true}
}

func (v *Verifier) unused(l activeLesson, now time.Time) (verdict, error) {
	var last sql.NullString
	if err := v.DB.QueryRow(`SELECT MAX(injected_at) FROM lesson_uses WHERE lesson_id = ?`, l.ID).Scan(&last); err != nil {
		return verdict{}, err
	}
	ref, source := l.ActivatedAt, "activated"
	if last.Valid && last.String != "" {
		ref, source = last.String, "last injected"
	}
	t, ok := parseTS(ref)
	if !ok {
		return verdict{}, nil
	}
	days := int(now.Sub(t).Hours() / 24)
	ev := map[string]any{"since": ref, "sinceKind": source, "days": days}
	if days < v.Cfg.UnusedDays {
		return verdict{known: true, evidence: ev}, nil
	}
	return verdict{fires: true, known: true, evidence: ev,
		detail: fmt.Sprintf("not injected into any run for %d days (%s %s)", days, source, ref)}, nil
}

// stale measures the area's churn since activation with git.
func (v *Verifier) stale(l activeLesson) verdict {
	if v.Git == nil || v.Resolve == nil {
		return verdict{}
	}
	var projectPath, repoCell string
	if err := v.DB.QueryRow(`SELECT p.path, COALESCE(e.repo, '') FROM epic_phases e
		JOIN tasks t ON t.id = e.workspace_task_id JOIN projects p ON p.id = t.project_id
		WHERE e.id = ?`, l.phaseID).Scan(&projectPath, &repoCell); err != nil {
		return verdict{}
	}
	root, err := v.Resolve(projectPath, repoCell)
	if err != nil {
		return verdict{}
	}
	changed, total, ok := AreaChurn(v.Git, root, l.AreaGlobs, l.ActivatedAt)
	if !ok {
		return verdict{}
	}
	return staleVerdict(changed, total, v.Cfg.StaleChurn)
}

// staleVerdict is the churn rule over measured numbers. Pure; unit-tested.
func staleVerdict(changed, total int, limit float64) verdict {
	if total == 0 && changed == 0 {
		return verdict{} // an area with no lines and no history: nothing to judge
	}
	share := 1.0
	if total > 0 {
		share = float64(changed) / float64(total)
	}
	ev := map[string]any{"changedLines": changed, "areaLines": total, "churnShare": share, "limit": limit}
	if share <= limit {
		return verdict{known: true, evidence: ev}
	}
	return verdict{fires: true, known: true, evidence: ev, detail: fmt.Sprintf(
		"the area changed since activation: %d changed lines against %d lines today (%.0f%% > %.0f%%)",
		changed, total, share*100, limit*100)}
}

// AreaChurn counts the lines changed in the lesson's area directories since the
// last commit before activatedAt, and the lines the area holds at HEAD. ok is
// false when git cannot answer (no commit before activation, a bad repo).
func AreaChurn(git worktree.Git, root string, globs []string, activatedAt string) (changed, total int, ok bool) {
	t, tok := parseTS(activatedAt)
	if !tok {
		return 0, 0, false
	}
	dirs := areaDirs(globs)
	if len(dirs) == 0 {
		return 0, 0, false
	}
	base, err := git.Run(root, "rev-list", "-1", "--before="+t.Format(time.RFC3339), "HEAD")
	base = strings.TrimSpace(base)
	if err != nil || base == "" || strings.ContainsAny(base, " \n") {
		return 0, 0, false
	}
	numstat := func(from string) (int, bool) {
		args := append([]string{"-c", "core.quotepath=false", "diff", "--numstat", "--no-renames", from, "HEAD", "--"}, dirs...)
		out, err := git.Run(root, args...)
		if err != nil {
			return 0, false
		}
		files, err := gitstat.ParseNumstat(out)
		if err != nil {
			return 0, false
		}
		a, r := gitstat.Totals(files)
		return a + r, true
	}
	changed, ok1 := numstat(base)
	total, ok2 := numstat(emptyTree)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return changed, total, true
}

// areaDirs turns globs into distinct repo-relative directories ("." for a bare
// "*"). A glob that leaves the repo is dropped.
func areaDirs(globs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range globs {
		d, err := AreaDir(g)
		if err != nil || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// autoRetire retires every lesson whose proposal waited out the window.
func (v *Verifier) autoRetire(now time.Time, st *VerifyStats) error {
	if v.Cfg.AutoRetireDays <= 0 {
		return nil
	}
	cutoff := stamp(now.AddDate(0, 0, -v.Cfg.AutoRetireDays))
	rows, err := v.DB.Query(`SELECT id, lesson_id, reason FROM lesson_retirements
		WHERE state = ? AND proposed_at <= ? ORDER BY proposed_at, id`, ProposalOpen, cutoff)
	if err != nil {
		return err
	}
	type due struct {
		id, lesson int64
		reason     string
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.lesson, &d.reason); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		if _, err := Retire(v.DB, d.lesson, d.reason, now); err != nil && !errors.Is(err, ErrState) {
			return err
		}
		if _, err := v.DB.Exec(`UPDATE lesson_retirements SET state = ?, decided_at = ? WHERE id = ? AND state = ?`,
			ProposalAuto, stamp(now), d.id, ProposalOpen); err != nil {
			return err
		}
		st.AutoRetired++
		log.Printf("lessons: auto-retired %s after %d days without an operator response (retire_reason=%s)",
			Ref(d.lesson), v.Cfg.AutoRetireDays, d.reason)
	}
	return nil
}

// ── the operator's queue ──

const selectProposal = `SELECT r.id, r.lesson_id, COALESCE(l.title, ''), COALESCE(l.guidance, ''),
	COALESCE(l.area_globs, ''), r.reason, r.detail, r.evidence_json, r.state, r.proposed_at, r.decided_at
	FROM lesson_retirements r LEFT JOIN surprise_lessons l ON l.id = r.lesson_id`

func (c VerifyConfig) scanProposal(scan func(...any) error) (Proposal, error) {
	var (
		p         Proposal
		globs, ev string
		decided   sql.NullString
	)
	if err := scan(&p.ID, &p.LessonID, &p.Title, &p.Guidance, &globs, &p.Reason, &p.Detail, &ev, &p.State,
		&p.ProposedAt, &decided); err != nil {
		return p, err
	}
	p.AreaGlobs, p.DecidedAt, p.Evidence = splitGlobs(globs), nullStr(decided), map[string]any{}
	_ = json.Unmarshal([]byte(ev), &p.Evidence)
	if p.State == ProposalOpen && c.AutoRetireDays > 0 {
		if t, ok := parseTS(p.ProposedAt); ok {
			s := stamp(t.AddDate(0, 0, c.AutoRetireDays))
			p.AutoRetireAt = &s
		}
	}
	return p, nil
}

// ListProposals returns proposals, newest first: the open queue when all is
// false, every proposal otherwise.
func ListProposals(db *sql.DB, cfg VerifyConfig, all bool) ([]Proposal, error) {
	q, args := selectProposal, []any{}
	if !all {
		q += ` WHERE r.state = ?`
		args = append(args, ProposalOpen)
	}
	rows, err := db.Query(q+` ORDER BY r.id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	out := []Proposal{}
	var ids []int64
	for rows.Next() {
		p, err := cfg.scanProposal(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
		ids = append(ids, p.LessonID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	effs, err := LoadEffectiveness(db, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if e, ok := effs[out[i].LessonID]; ok {
			e := e
			out[i].Effectiveness = &e
		}
	}
	return out, nil
}

// GetProposal reads one proposal.
func GetProposal(db *sql.DB, cfg VerifyConfig, id int64) (Proposal, error) {
	p, err := cfg.scanProposal(db.QueryRow(selectProposal+` WHERE r.id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// ConfirmRetirement is the operator's "retire it": the lesson is retired with
// the proposal's reason as retire_reason.
func ConfirmRetirement(db *sql.DB, cfg VerifyConfig, id int64, now time.Time) (Proposal, error) {
	p, err := GetProposal(db, cfg, id)
	if err != nil {
		return p, err
	}
	if p.State != ProposalOpen {
		return p, ErrState
	}
	if _, err := Retire(db, p.LessonID, p.Reason, now); err != nil {
		return p, err
	}
	return closeProposal(db, cfg, id, ProposalConfirmed, now)
}

// KeepLesson is the operator's "keep it": the proposal closes and its reason is
// held off for KeepCooldown days.
func KeepLesson(db *sql.DB, cfg VerifyConfig, id int64, now time.Time) (Proposal, error) {
	p, err := GetProposal(db, cfg, id)
	if err != nil {
		return p, err
	}
	if p.State != ProposalOpen {
		return p, ErrState
	}
	return closeProposal(db, cfg, id, ProposalKept, now)
}

func closeProposal(db *sql.DB, cfg VerifyConfig, id int64, state string, now time.Time) (Proposal, error) {
	res, err := db.Exec(`UPDATE lesson_retirements SET state = ?, decided_at = ? WHERE id = ? AND state = ?`,
		state, stamp(now), id, ProposalOpen)
	if err != nil {
		return Proposal{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Proposal{}, ErrState
	}
	return GetProposal(db, cfg, id)
}
