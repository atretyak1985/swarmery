-- 0074: give agent_change_proposals a TARGET (agent file or SKILL.md), and a
-- status for a proposal that knows its lesson but not yet its file.
--
-- agent-memory phase 5. The improve loop was born able to rewrite exactly one
-- kind of file — plugins/<pack>/agents/<name>.md — and the whole apply pipeline
-- reads that assumption out of a single column (agent_path). R11 (migration
-- 0072) emits target_kind='skill': a lesson the fleet re-learned in >= 3 tasks,
-- whose fix is an edit to the SKILL.md that should have carried it. Routing that
-- into the same human-gated diff pipeline needs the row to say WHICH KIND of
-- file it targets and WHERE that file is, because every downstream gate
-- (path scope, frontmatter, the pack semver bump) branches on it.
--
--   target_kind  'agent' | 'skill'   — the routing key, DEFAULT 'agent' so every
--                                      pre-0074 row keeps its meaning verbatim.
--   target_path  repo-relative path of the SKILL.md, '' for agent rows (which
--                carry their path in agent_path, untouched here).
--
-- WHY 0074 AND NOT 0070, which is what the phase doc reserved. migrate.go applies
-- any UNAPPLIED file in FILENAME order. Slot 0070 was taken by
-- 0070_retro_lessons_norm_title.sql and 0073 by 0073_fk_child_indexes.sql, so a
-- file numbered 0070 written today would run BEFORE 0071/0072/0073 on a fresh
-- database and AFTER them on an already-migrated one — two different schemas and
-- no error anywhere (migrate.go's version-collision check would in fact refuse to
-- start, which is the louder half of the same problem). A new migration always
-- takes the next number above the highest existing one.
--
-- WHY A REBUILD AND NOT TWO `ALTER TABLE ADD COLUMN`s. The two columns alone
-- would be an ALTER, but the status vocabulary has to widen with 'needs_target'
-- and SQLite cannot ALTER a CHECK constraint. One rebuild carries both.
--
-- FOREIGN KEYS. The 0071/0072 hazard is NOT in play here, but it is worth saying
-- why so the next rebuild does not have to re-derive it. That hazard was
-- `DROP TABLE recommendations` — the PARENT — firing
-- agent_change_proposals.recommendation_id's ON DELETE SET NULL and silently
-- NULLing every live proposal's link (PRAGMA foreign_keys is a no-op inside
-- migrate.go's transaction, and the DSN enables enforcement). Here the table
-- being dropped is the CHILD: no parent row is deleted, so no ON DELETE action
-- fires, and nothing in the schema REFERENCES agent_change_proposals, so the
-- RENAME rewrites no other table's FK clause. The INSERT below carries
-- recommendation_id across under live FK enforcement, which is safe precisely
-- because ON DELETE SET NULL guarantees there are no orphan ids to re-insert.
--
-- THE ONE-OPEN INVARIANT MOVES FROM AGENT TO TARGET. 0022's partial unique index
-- was on (agent); that is now wrong in both directions — a skill proposal and an
-- agent proposal can legitimately share a name, and two skill proposals for the
-- same SKILL.md must still collide. The replacement keys on
-- (target_kind, COALESCE(NULLIF(target_path,''), agent)): agent rows fall back to
-- the agent name (byte-identical behaviour to 0022), skill rows key on their file
-- path, and a skill row that has no path yet keys on the lesson identity stored in
-- `agent`. 'needs_target' is INSIDE the open set — the phase doc's index spec
-- predates the status, and leaving it out would let one unresolvable lesson
-- accumulate an unbounded pile of identical rows on the Retro page.

CREATE TABLE agent_change_proposals_new (
  id                INTEGER PRIMARY KEY,
  recommendation_id INTEGER REFERENCES recommendations(id) ON DELETE SET NULL,
  agent             TEXT NOT NULL,           -- agent registry key, or the lesson identity for a skill row
  agent_path        TEXT NOT NULL,           -- repo-relative agent file path ('' for skill rows)
  target_kind       TEXT NOT NULL DEFAULT 'agent'
                    CHECK (target_kind IN ('agent','skill')),
  target_path       TEXT NOT NULL DEFAULT '', -- repo-relative SKILL.md path ('' for agent rows / unresolved)
  base_sha256       TEXT NOT NULL,           -- sha256 of the target file content the diff was made against
  diff              TEXT NOT NULL,           -- unified diff
  rationale         TEXT NOT NULL,           -- model's per-hunk explanation
  status            TEXT NOT NULL DEFAULT 'proposed'
                    CHECK (status IN ('proposed','approved','applied','rejected','failed','needs_target')),
  error             TEXT,                    -- populated when status='failed' or 'needs_target'
  pr_url            TEXT,                    -- populated by the apply pipeline
  created_at        TEXT NOT NULL,
  decided_at        TEXT
);

INSERT INTO agent_change_proposals_new
  (id, recommendation_id, agent, agent_path, target_kind, target_path,
   base_sha256, diff, rationale, status, error, pr_url, created_at, decided_at)
SELECT id, recommendation_id, agent, agent_path, 'agent', '',
       base_sha256, diff, rationale, status, error, pr_url, created_at, decided_at
  FROM agent_change_proposals;

DROP TABLE agent_change_proposals;
ALTER TABLE agent_change_proposals_new RENAME TO agent_change_proposals;

CREATE INDEX idx_agent_proposals_agent ON agent_change_proposals(agent);
CREATE INDEX idx_agent_proposals_rec ON agent_change_proposals(recommendation_id);

-- At most one OPEN proposal per TARGET. See the note above for the key shape.
CREATE UNIQUE INDEX idx_agent_proposals_one_open
  ON agent_change_proposals(target_kind, COALESCE(NULLIF(target_path, ''), agent))
  WHERE status IN ('proposed', 'approved', 'needs_target');
