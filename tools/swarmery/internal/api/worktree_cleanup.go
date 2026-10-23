// Worktree cleanup (internal/wtcleanup): the needs-you source for the
// janitor's keep-unmerged worktrees whose phase or card is closed and whose
// content is proven to be in main, and the one action on them.
//
//	GET  /api/needs-you                     one worktree_cleanup item per repository
//	POST /api/worktree-cleanup/{id}/apply   {"paths": [...]} → remove those, salvage first
//
// The item is DERIVED from the open rows of worktree_cleanup_proposals, which
// the daily worktree-cleanup routine writes (`swarmery wtcleanup propose`);
// this file never computes a proof. Only rows with a proof are listed; the
// proof-less ones are a count ("without proof: K"), and apply refuses them.
// Removal goes through wtjanitor.Service.ForceUnmerged — the janitor's only
// path to removing a keep-unmerged worktree, and it is reachable from here and
// from `swarmery wtcleanup apply`, nowhere else.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtcleanup"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
)

// needsYouWorktreeCleanup ranks last among same-instant blockers: nothing is
// stuck on it, it only reclaims disk and branch clutter.
const needsYouWorktreeCleanup = "worktree_cleanup"

// cleanupApplyMaxPaths bounds one apply: a card never lists more.
const cleanupApplyMaxPaths = 500

// needsYouCleanup is a worktree_cleanup item's payload. Mirrors
// NeedsYouCleanup in web/src/api/types.ts.
type needsYouCleanup struct {
	// ID is the proposal the card's apply addresses (the repository's oldest
	// open one; any open proposal of the repository would do).
	ID       int64  `json:"id"`
	RepoRoot string `json:"repoRoot"`
	// Candidates are the open proposals WITH a proof, path order.
	Candidates []cleanupCandidateDTO `json:"candidates"`
	// WithoutProof counts the open proposals whose content is not proven in
	// main: closed owners, counted, never offered for deletion.
	WithoutProof int `json:"withoutProof"`
}

// cleanupCandidateDTO is one listed worktree.
type cleanupCandidateDTO struct {
	ID     int64  `json:"id"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	// Owner labels the phase ("<plan> · Phase <seq>: <name>") or the card
	// ("<external-id>: <title>") the worktree belonged to.
	Owner   string `json:"owner"`
	PhaseID *int64 `json:"phaseId"`
	TaskID  *int64 `json:"taskId"`
	// PlanExternalID / PhaseSeq address the phase on the Plans page; empty / 0
	// for a card's worktree.
	PlanExternalID string `json:"planExternalId,omitempty"`
	PhaseSeq       int    `json:"phaseSeq,omitempty"`
	ProofKind      string `json:"proofKind"` // landing | content-diff
	ProofDetail    string `json:"proofDetail"`
	// Error is the last failed apply of this path (it stays listed).
	Error string `json:"error,omitempty"`
}

// queryNeedsYouWorktreeCleanup builds one item per repository with at least
// one open proposal that carries a proof. blockingSince is the oldest of those
// proposals' first sighting.
func queryNeedsYouWorktreeCleanup(db *sql.DB, scope string, projArgs []any) ([]needsYouItem, error) {
	q := `SELECT w.id, w.repo_root, COALESCE(p.slug, ''), w.path, w.branch, w.phase_id, w.task_id,
		       w.proof_kind, w.proof_detail, w.proposed_at, COALESCE(w.error, ''),
		       COALESCE(e.seq, 0), COALESCE(e.name, ''), COALESCE(t.external_id, ''), COALESCE(t.title, ''),
		       COALESCE(t.source, '')
		FROM worktree_cleanup_proposals w
		LEFT JOIN projects p ON p.id = w.project_id
		LEFT JOIN epic_phases e ON e.id = w.phase_id
		LEFT JOIN tasks t ON t.id = w.task_id
		WHERE w.decided IS NULL` + scope + `
		ORDER BY w.repo_root, w.path`
	rows, err := db.Query(q, projArgs...)
	if err != nil {
		return nil, fmt.Errorf("needs-you: worktree cleanup: %w", err)
	}
	defer rows.Close()

	var order []string
	items := map[string]*needsYouItem{}
	for rows.Next() {
		var c cleanupCandidateDTO
		var repo, slug, proposedAt, phaseName, extID, title, source string
		var phaseID, taskID sql.NullInt64
		if err := rows.Scan(&c.ID, &repo, &slug, &c.Path, &c.Branch, &phaseID, &taskID,
			&c.ProofKind, &c.ProofDetail, &proposedAt, &c.Error,
			&c.PhaseSeq, &phaseName, &extID, &title, &source); err != nil {
			return nil, err
		}
		it, ok := items[repo]
		if !ok {
			it = &needsYouItem{Kind: needsYouWorktreeCleanup, ProjectSlug: slug,
				SessionName: filepath.Base(repo), Cleanup: &needsYouCleanup{RepoRoot: repo, Candidates: []cleanupCandidateDTO{}}}
			items[repo] = it
			order = append(order, repo)
		}
		if c.ProofKind == wtcleanup.ProofNone {
			it.Cleanup.WithoutProof++
			continue
		}
		if phaseID.Valid {
			c.PhaseID = &phaseID.Int64
			c.PlanExternalID = extID
		}
		if taskID.Valid {
			c.TaskID = &taskID.Int64
		}
		c.Owner = cleanupOwner(phaseID.Valid, c.PhaseSeq, phaseName, extID, title, source)
		if it.Cleanup.ID == 0 || c.ID < it.Cleanup.ID {
			it.Cleanup.ID = c.ID
		}
		if it.BlockingSince == "" || proposedAt < it.BlockingSince {
			it.BlockingSince = proposedAt
		}
		it.Cleanup.Candidates = append(it.Cleanup.Candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []needsYouItem{}
	for _, repo := range order {
		it := items[repo]
		if len(it.Cleanup.Candidates) == 0 {
			continue // only proof-less worktrees: nothing the operator can act on
		}
		it.Preview = cleanupPreview(len(it.Cleanup.Candidates), it.Cleanup.WithoutProof)
		it.at = parseNeedsYouTS(it.BlockingSince)
		out = append(out, *it)
	}
	return out, nil
}

// cleanupOwner labels the run or card behind a worktree.
func cleanupOwner(isPhase bool, seq int, phaseName, extID, title, source string) string {
	switch {
	case isPhase:
		head := "Phase " + strconv.Itoa(seq) + ": " + phaseName
		if title == "" {
			return head
		}
		return title + " · " + head
	case source == "queue":
		return strings.TrimSpace(extID + ": " + title)
	case title != "":
		return title + " (plan run)"
	default:
		return extID
	}
}

// cleanupPreview is the row's one-liner (the card renders its own, localized).
func cleanupPreview(withProof, withoutProof int) string {
	head := strconv.Itoa(withProof) + " finished worktrees with their content in main"
	if withProof == 1 {
		head = "1 finished worktree with its content in main"
	}
	if withoutProof > 0 {
		head += " · " + strconv.Itoa(withoutProof) + " without proof"
	}
	return head
}

/* ---------- POST /api/worktree-cleanup/{id}/apply ---------- */

type cleanupApplyRequest struct {
	Paths []string `json:"paths"`
}

func (h *Handler) applyWorktreeCleanup(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeClientErr(w, http.StatusBadRequest, "invalid proposal id")
		return
	}
	var req cleanupApplyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(req.Paths) == 0 {
		writeClientErr(w, http.StatusBadRequest, "paths: select at least one worktree")
		return
	}
	if len(req.Paths) > cleanupApplyMaxPaths {
		writeClientErr(w, http.StatusBadRequest, fmt.Sprintf("paths: at most %d per apply", cleanupApplyMaxPaths))
		return
	}
	res, err := wtcleanup.Apply(h.DB, h.cleanupGit(), h.cleanupForcer(), id, req.Paths, time.Now())
	if errors.Is(err, wtcleanup.ErrNotFound) {
		writeClientErr(w, http.StatusNotFound, "no such worktree cleanup proposal")
		return
	}
	writeJSON(w, res, err)
}

// cleanupForcer is the janitor service apply removes through: the daemon's own
// worktree root and liveness, the same remover the sweep uses.
func (h *Handler) cleanupForcer() wtcleanup.Forcer {
	if h.wtCleanupForce != nil {
		return h.wtCleanupForce
	}
	mgr := h.Wt
	if mgr == nil {
		mgr = &worktree.Manager{Git: worktree.ExecGit{}}
	}
	root, _ := mgr.RootDir()
	return &wtjanitor.Service{
		DB: h.DB, Git: wtjanitor.RepoGit{},
		Live:       wtjanitor.ProcLiveness{Proc: procwatch.OsProvider{}, DB: h.DB},
		DaemonRoot: root, Remover: wtjanitor.NewRealRemover(mgr),
	}
}

func (h *Handler) cleanupGit() wtcleanup.Git {
	if h.wtCleanupGit != nil {
		return h.wtCleanupGit
	}
	return wtcleanup.ExecGit{}
}
