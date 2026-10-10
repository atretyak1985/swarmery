// Package landpoll is the change-request status poller (phase-landing plan,
// phase 7): it asks the code host for the state of every phase whose landing
// opened a PR/MR, stores the normalized repoprovider.ChangeStatus on the phase,
// and flips landing_state to `merged` when the change lands.
//
// The daemon runs RunOnce on a ticker; the manual refresh endpoint calls
// RefreshOne. Both read and write epic_phases' landing columns (migration
// 0103) and nothing else:
//
//   - pr_status / pr_checked_at   the last read, always (a failed read stamps
//     pr_checked_at too, so one broken PR cannot hold the head of the queue);
//   - landing_state / landed_at   pr_open → merged, once, when State is merged;
//   - landing_error               cleared by a successful read, stamped
//     "<code>: <redacted detail>" by a failed one (the same shape the land
//     handler writes, so the project's auth banner reads both).
//
// A change request closed without merging keeps landing_state pr_open with
// status state "closed": the operator may reopen it, and only they decide
// what a closed PR means for the phase. The poller never deletes a branch.
//
// The one criterion it does tick is the class landing closes (phase-run
// outcomes plan, phase 3, D3): the moment a phase flips to merged, every
// unticked `[LAND]` criterion in its doc is ticked (wsingest.TickCriteriaByClass,
// atomic); the plan-dir watcher sees the atomic rename and its rescan folds the
// new count in (the merge's own plan_updated event only refreshes the page —
// the scanner publishes a second one once the counts are in). Executable and `[MANUAL]` criteria are
// never touched — done and landed stay separate facts (D2).
//
// Imports: repoprovider, credstore, wsingest and database/sql only. The api-side effects
// (the plan_updated WS event, the project's auth-expired mark) arrive as funcs,
// and the repo a phase ran in arrives through RepoDir (production:
// phaserun.Service.RunRoot), so this package imports neither internal/api nor
// internal/phaserun.
package landpoll

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// DefaultMax is how many change requests one RunOnce reads when Poller.Max is
// unset: one `gh pr view` / `glab mr view` each, well under any host's rate limit.
const DefaultMax = 20

// Landing states the poller reads and writes (epic_phases.landing_state; the
// same wire values as internal/api's landing constants).
const (
	StatePROpen = "pr_open"
	StateMerged = "merged"
)

// landing_error codes. CodeNotAuthenticated matches the land handler's
// `not-authenticated` prefix, which the project's auth banner keys on.
const (
	CodeNotAuthenticated = "not-authenticated"
	CodeBinaryMissing    = "binary-missing"
	CodeNoRemote         = "no-remote"
	CodeProviderUnknown  = "provider-unknown"
	CodeStatusFailed     = "status-failed"
)

// Sentinels RefreshOne returns before any provider call.
var (
	// ErrPhaseNotFound: no epic_phases row has the id.
	ErrPhaseNotFound = errors.New("phase not found")
	// ErrNoChangeRequest: the phase has no change request to read — its
	// landing_state is neither pr_open nor merged, or it carries neither a URL
	// nor a number.
	ErrNoChangeRequest = errors.New("phase has no change request")
)

// Poller reads change-request status for landed phases. DB and Factory are
// required; every other field has a default.
type Poller struct {
	// DB is the daemon's store (migration 0103 applied).
	DB *sql.DB
	// Factory builds the provider for a kind (production: providers.Factory
	// over repoprovider.OSExec and credstore.Env).
	Factory func(kind repoprovider.Kind) (repoprovider.Provider, error)
	// Exec runs the local git reads repoprovider.Detect makes to find the
	// remote. nil ⇒ repoprovider.OSExec{}.
	Exec repoprovider.Exec
	// RepoDir resolves the repository the phase ran in (production:
	// (&phaserun.Service{DB: db}).RunRoot). nil ⇒ the project path.
	RepoDir func(phaseID int64) (string, error)
	// Clock stamps pr_checked_at and landed_at. nil ⇒ time.Now.
	Clock func() time.Time
	// Publish announces that a phase's landing changed, with the phase's
	// workspace task id (production: api's plan_updated publisher). nil ⇒ none.
	Publish func(taskID int64)
	// OnAuthExpired is told the project whose credentials the host rejected
	// (production: api.MarkVcsAuthExpired). nil ⇒ none.
	OnAuthExpired func(projectID int64)
	// Logf logs per-PR failures; every argument is already redacted. nil ⇒ none.
	Logf func(format string, args ...any)
	// Max caps the change requests one RunOnce reads. ≤0 ⇒ DefaultMax.
	Max int
}

// phaseRow is what one poll needs to know about a phase.
type phaseRow struct {
	phaseID, taskID, projectID int64
	projectPath                string
	docPath                    string
	landingState               string
	provider, url              string
	number                     int
	status, landingError       sql.NullString
}

// selectColumns is the SELECT list scanRow reads, over epic_phases e → tasks t
// → projects p (the same join the landing handlers use).
const selectColumns = `
	SELECT e.id, e.workspace_task_id, p.id, COALESCE(p.path, ''), COALESCE(e.doc_path, ''), e.landing_state,
	       COALESCE(e.pr_provider, ''), COALESCE(e.pr_url, ''), COALESCE(e.pr_number, 0),
	       e.pr_status, e.landing_error
	  FROM epic_phases e
	  JOIN tasks t ON t.id = e.workspace_task_id
	  JOIN projects p ON p.id = t.project_id`

type scanner interface{ Scan(dest ...any) error }

func scanRow(s scanner) (phaseRow, error) {
	var r phaseRow
	err := s.Scan(&r.phaseID, &r.taskID, &r.projectID, &r.projectPath, &r.docPath, &r.landingState,
		&r.provider, &r.url, &r.number, &r.status, &r.landingError)
	r.projectPath = strings.TrimSpace(r.projectPath)
	return r, err
}

// RunOnce reads the status of up to Max open change requests, the ones read
// longest ago first (never-read first), and stores what it learns. checked is
// how many it asked about; changed is how many phases' landing changed (and
// were published). A per-PR failure is stamped on that phase and logged, and
// the rest are still read; err is non-nil only when the tick itself failed (the
// store, or ctx ending).
func (p *Poller) RunOnce(ctx context.Context) (checked, changed int, err error) {
	limit := p.Max
	if limit <= 0 {
		limit = DefaultMax
	}
	// Read every row before writing any: the store runs on one connection, so
	// an UPDATE issued while the cursor is open would wait on itself.
	rows, err := p.DB.QueryContext(ctx, selectColumns+`
		 WHERE e.landing_state = ?
		 ORDER BY e.pr_checked_at IS NOT NULL, e.pr_checked_at, e.id
		 LIMIT ?`, StatePROpen, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("landpoll: select open change requests: %w", err)
	}
	var batch []phaseRow
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("landpoll: scan: %w", err)
		}
		batch = append(batch, r)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, fmt.Errorf("landpoll: select open change requests: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("landpoll: select open change requests: %w", err)
	}

	dets := map[string]detection{}
	for _, r := range batch {
		if err := ctx.Err(); err != nil {
			return checked, changed, err
		}
		checked++
		res, err := p.poll(ctx, r, dets)
		if err != nil {
			return checked, changed, err
		}
		if res.changed {
			changed++
		}
	}
	return checked, changed, nil
}

// RefreshOne reads one phase's change request now, for the manual refresh
// endpoint. It returns the stored status and the phase's landing state after
// the read. ErrPhaseNotFound / ErrNoChangeRequest come before any provider
// call; a provider failure is stamped on the phase (as RunOnce does) AND
// returned, wrapping the classified repoprovider error (errors.Is works on
// repoprovider.ErrNotAuthenticated etc.). A merged phase is re-read but never
// leaves merged.
func (p *Poller) RefreshOne(ctx context.Context, phaseID int64) (repoprovider.ChangeStatus, string, error) {
	r, err := scanRow(p.DB.QueryRowContext(ctx, selectColumns+` WHERE e.id = ?`, phaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return repoprovider.ChangeStatus{}, "", ErrPhaseNotFound
	}
	if err != nil {
		return repoprovider.ChangeStatus{}, "", fmt.Errorf("landpoll: load phase %d: %w", phaseID, err)
	}
	if r.landingState != StatePROpen && r.landingState != StateMerged {
		return repoprovider.ChangeStatus{}, r.landingState, ErrNoChangeRequest
	}
	if r.url == "" && r.number <= 0 {
		return repoprovider.ChangeStatus{}, r.landingState, ErrNoChangeRequest
	}
	res, err := p.poll(ctx, r, map[string]detection{})
	if err != nil {
		return repoprovider.ChangeStatus{}, r.landingState, err
	}
	if res.readErr != nil {
		return repoprovider.ChangeStatus{}, r.landingState, res.readErr
	}
	return res.status, res.state, nil
}

// pollResult is one poll's outcome. readErr is the provider-side failure that
// was stamped on the phase; the error poll itself returns is a store failure.
type pollResult struct {
	status  repoprovider.ChangeStatus
	state   string
	changed bool
	readErr error
}

// detection is a repo dir's Detect answer, cached for one tick.
type detection struct {
	det repoprovider.Detection
	err error
}

// poll reads r's change request and writes the outcome.
func (p *Poller) poll(ctx context.Context, r phaseRow, dets map[string]detection) (pollResult, error) {
	now := p.now()
	st, readErr := p.read(ctx, r, dets)
	if readErr != nil {
		changed, err := p.stampFailure(r, readErr, now)
		return pollResult{state: r.landingState, changed: changed, readErr: readErr}, err
	}
	st.CheckedAt = now
	return p.store(r, st, now)
}

// read resolves the phase's target and provider and asks for the status.
func (p *Poller) read(ctx context.Context, r phaseRow, dets map[string]detection) (repoprovider.ChangeStatus, error) {
	if r.url == "" && r.number <= 0 {
		return repoprovider.ChangeStatus{}, ErrNoChangeRequest
	}
	repoDir := r.projectPath
	if p.RepoDir != nil {
		dir, err := p.RepoDir(r.phaseID)
		if err != nil {
			return repoprovider.ChangeStatus{}, fmt.Errorf("resolve the phase's repo: %w", err)
		}
		repoDir = dir
	}
	if repoDir == "" {
		return repoprovider.ChangeStatus{}, errors.New("the project has no known path")
	}
	d, ok := dets[repoDir]
	if !ok {
		// cfg is the PROJECT's vcs config, never repoDir's (a multi-repo phase
		// runs in a sub-repo whose own .claude/ is not where vcs.provider lives).
		// No prober: the kind the change request was opened with is stored.
		d.det, d.err = repoprovider.Detect(ctx, p.exec(), repoDir, repoprovider.LoadConfig(r.projectPath), nil)
		dets[repoDir] = d
	}
	if d.err != nil {
		return repoprovider.ChangeStatus{}, d.err
	}
	kind := repoprovider.Kind(r.provider)
	if kind != repoprovider.KindGitHub && kind != repoprovider.KindGitLab {
		kind = d.det.Kind
	}
	prov, err := p.Factory(kind)
	if err != nil {
		return repoprovider.ChangeStatus{}, err
	}
	target := repoprovider.Target{RepoDir: repoDir, RemoteName: repoprovider.DefaultRemote, Remote: d.det.Remote}
	return prov.Status(ctx, target, repoprovider.ChangeRef{URL: r.url, Number: r.number, Provider: kind})
}

// store writes a successful read: always the status and its time, the merged
// flip when the change landed, and a cleared landing_error. The phase is
// published when anything the UI shows changed — not for a re-read that only
// moved checkedAt, which would refetch every open plan on every tick.
func (p *Poller) store(r phaseRow, st repoprovider.ChangeStatus, now time.Time) (pollResult, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return pollResult{}, fmt.Errorf("landpoll: encode status: %w", err)
	}
	ts := now.UTC().Format(time.RFC3339)
	res := pollResult{status: st, state: r.landingState}
	merging := r.landingState == StatePROpen && st.State == repoprovider.StateMerged

	var q string
	args := []any{string(raw), ts}
	if merging {
		// Guarded on pr_open: a return or a refresh racing this tick decides.
		q = `UPDATE epic_phases
		        SET pr_status = ?, pr_checked_at = ?, landing_error = NULL,
		            landing_state = ?, landed_at = ?
		      WHERE id = ? AND landing_state = ?`
		args = append(args, StateMerged, ts, r.phaseID, StatePROpen)
	} else {
		q = `UPDATE epic_phases SET pr_status = ?, pr_checked_at = ?, landing_error = NULL WHERE id = ?`
		args = append(args, r.phaseID)
	}
	out, err := p.DB.Exec(q, args...)
	if err != nil {
		return pollResult{}, fmt.Errorf("landpoll: store status of phase %d: %w", r.phaseID, err)
	}
	if n, _ := out.RowsAffected(); n == 0 {
		return res, nil // the phase moved under us; its new owner publishes
	}
	if merging {
		res.state = StateMerged
		p.tickLand(r)
	}
	res.changed = merging || r.landingError.Valid || statusChanged(r.status, st)
	if res.changed {
		p.publish(r.taskID)
	}
	return res, nil
}

// tickLand ticks the [LAND] criteria of a phase that just merged. Best effort:
// the merge is already stored, so a doc that cannot be written is logged and
// left for the operator (the Criteria tab ticks by hand), never retried here. A
// doc that is gone (an archived or renamed plan) has nothing to tick.
func (p *Poller) tickLand(r phaseRow) {
	if r.docPath == "" {
		return
	}
	n, err := wsingest.TickCriteriaByClass(r.docPath, wsingest.ClassLand)
	switch {
	case err != nil && errors.Is(err, fs.ErrNotExist):
	case err != nil:
		p.logf("landpoll: phase %d (task %d) merged, but ticking its [LAND] criteria failed: %v", r.phaseID, r.taskID, err)
	case n > 0:
		p.logf("landpoll: phase %d (task %d) merged: ticked %d [LAND] criteria", r.phaseID, r.taskID, n)
	}
}

// statusChanged reports whether st differs from the stored status in anything
// but checkedAt. No (or an unreadable) stored status counts as changed.
func statusChanged(stored sql.NullString, st repoprovider.ChangeStatus) bool {
	if !stored.Valid {
		return true
	}
	var prev repoprovider.ChangeStatus
	if json.Unmarshal([]byte(stored.String), &prev) != nil {
		return true
	}
	prev.CheckedAt, st.CheckedAt = time.Time{}, time.Time{}
	return prev != st
}

// stampFailure records a failed read on the phase: landing_error (redacted,
// code-prefixed) and pr_checked_at, so the phase rotates to the back of the
// queue. A rejected credential also marks the project's auth expired. The phase
// is published only when its landing_error text changed.
func (p *Poller) stampFailure(r phaseRow, readErr error, now time.Time) (bool, error) {
	code, detail := Classify(readErr)
	msg := code + ": " + detail
	p.logf("landpoll: phase %d (task %d): %s", r.phaseID, r.taskID, msg)
	if code == CodeNotAuthenticated && p.OnAuthExpired != nil {
		p.OnAuthExpired(r.projectID)
	}
	if _, err := p.DB.Exec(`UPDATE epic_phases SET landing_error = ?, pr_checked_at = ? WHERE id = ?`,
		msg, now.UTC().Format(time.RFC3339), r.phaseID); err != nil {
		return false, fmt.Errorf("landpoll: stamp failure on phase %d: %w", r.phaseID, err)
	}
	if r.landingError.Valid && r.landingError.String == msg {
		return false, nil
	}
	p.publish(r.taskID)
	return true, nil
}

// Classify maps a failed read onto its landing_error code and a redacted,
// bounded detail — the tool's own output for a classified provider error,
// the error's message otherwise.
func Classify(err error) (code, detail string) {
	switch {
	case errors.Is(err, repoprovider.ErrNotAuthenticated):
		code = CodeNotAuthenticated
	case errors.Is(err, repoprovider.ErrBinaryMissing):
		code = CodeBinaryMissing
	case errors.Is(err, repoprovider.ErrNoRemote):
		code = CodeNoRemote
	case errors.Is(err, repoprovider.ErrUnknownProvider):
		code = CodeProviderUnknown
	default:
		code = CodeStatusFailed
	}
	var pe *repoprovider.Error
	if errors.As(err, &pe) && pe.Detail != "" {
		return code, credstore.Redact(pe.Detail)
	}
	return code, credstore.RedactedTail("", err, repoprovider.OutputTail)
}

func (p *Poller) now() time.Time {
	if p.Clock != nil {
		return p.Clock()
	}
	return time.Now()
}

func (p *Poller) exec() repoprovider.Exec {
	if p.Exec != nil {
		return p.Exec
	}
	return repoprovider.OSExec{}
}

func (p *Poller) publish(taskID int64) {
	if p.Publish != nil {
		p.Publish(taskID)
	}
}

func (p *Poller) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}
