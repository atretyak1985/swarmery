-- 0131: decision evidence (verify-not-trust phase 12, D19). Every decision (`D-n`,
-- phase_decisions) and spec criterion (`SC-n`, spec_criteria) has to survive to
-- proof. One row per place an id is evidenced:
--
--   kind 'report' — a phase doc's `## Completion Report` cites the id (wsingest,
--                   re-derived on every scan of a changed plan);
--   kind 'pr'     — a landed change-request body cites it (reserved: landing does
--                   not store bodies yet, and the body's report IS the report);
--   kind 'review' — the plan-level review (`kind: review`, `round: plan`) maps the
--                   id to evidence or `missing` in its `ids:` list.
--
-- status: 'evidence' (the id is shown to hold), 'not-implemented' (a report line
-- `not implemented: D-n — <why>`: a deliberate omission, never "remaining"), or
-- 'missing' (the plan reviewer looked and found none). excerpt is the line that
-- says so — the reason for a not-implemented row.
--
-- doc_path is the doc the mention came from (the phase doc for report rows and
-- for an interactive plan review written into a phase doc's `## Review`; the plan
-- README for the daemon's plan review, which writes into no doc). Keyed by
-- doc_path, not phase id, for the same reason as review_findings (0123): a renamed
-- doc mints a new epic_phases id. No foreign keys.
--
-- Old plans are not rewritten: rows appear only for what their docs already say.
--
-- Rollback: DROP TABLE IF EXISTS decision_evidence;
CREATE TABLE IF NOT EXISTS decision_evidence (
  id                INTEGER PRIMARY KEY,
  workspace_task_id INTEGER NOT NULL,
  decision_id       TEXT NOT NULL,            -- 'D-2' | 'SC-4'
  phase_id          INTEGER,                  -- the phase the doc resolved to; NULL for a daemon plan review
  doc_path          TEXT NOT NULL DEFAULT '',
  kind              TEXT NOT NULL CHECK (kind IN ('report','pr','review')),
  status            TEXT NOT NULL DEFAULT 'evidence'
                    CHECK (status IN ('evidence','not-implemented','missing')),
  excerpt           TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  UNIQUE (workspace_task_id, decision_id, kind, doc_path)
);
CREATE INDEX IF NOT EXISTS idx_decision_evidence_task ON decision_evidence(workspace_task_id, decision_id);
