-- 0126: worktree cleanup proposals (internal/wtcleanup) — the janitor's
-- keep-unmerged pile, narrowed to worktrees whose phase or board card is closed
-- and annotated with a PROOF that the branch's content is already in main.
--
-- One row per (repository, worktree path) per proposal. `swarmery wtcleanup
-- propose` refreshes the UNDECIDED rows of a repository in place (ids stay
-- stable, proposed_at keeps the first sighting) and drops the undecided rows
-- that stopped being candidates; decided rows are history and never rewritten.
-- GET /api/needs-you derives one `worktree_cleanup` item per repository from the
-- undecided rows; POST /api/worktree-cleanup/{id}/apply decides them.
--
-- proof_kind 'none' rows are COUNTED (the item's "without proof: K"), never
-- listed for deletion, and apply refuses them.
--
-- decided: NULL while open, then removed | gone (the path disappeared between
-- propose and apply). A failed removal leaves the row undecided with `error`
-- set, so the operator can retry it from the same card.

CREATE TABLE worktree_cleanup_proposals (
    id             INTEGER PRIMARY KEY,
    repo_root      TEXT NOT NULL,                    -- projects.path of the repository
    project_id     INTEGER REFERENCES projects(id),  -- for the needs-you ?project= scope
    path           TEXT NOT NULL,                    -- the worktree checkout
    branch         TEXT NOT NULL DEFAULT '',         -- '' when detached
    phase_id       INTEGER,                          -- epic_phases.id when a phase run owns it
    task_id        INTEGER,                          -- tasks.id: the board card, or the plan of the phase
    proof_kind     TEXT NOT NULL,                    -- landing | content-diff | none
    proof_detail   TEXT NOT NULL DEFAULT '',
    proposed_at    TEXT NOT NULL,                    -- ISO-8601 UTC, first proposal of this path
    decided        TEXT,                             -- NULL | removed | gone
    decided_at     TEXT,
    salvage_branch TEXT,                             -- the salvage ref written before removal
    error          TEXT                              -- last apply failure; the row stays open
);

-- At most one OPEN proposal per checkout; history rows are unconstrained.
CREATE UNIQUE INDEX idx_wtcleanup_open ON worktree_cleanup_proposals(repo_root, path) WHERE decided IS NULL;
CREATE INDEX idx_wtcleanup_decided ON worktree_cleanup_proposals(decided, repo_root);
