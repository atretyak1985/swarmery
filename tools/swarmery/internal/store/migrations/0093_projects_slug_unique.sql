-- 0093: projects.slug becomes UNIQUE.
--
-- projects.path was UNIQUE from 0001; projects.slug never was. But slug is the
-- name everything else addresses a project BY — the dashboard label, the
-- analytics grouping key, and (until the fix that accompanies this migration)
-- the directory segment dispatch built a micro-plan path from.
--
-- Two rows sharing one slug is therefore not a cosmetic duplicate: every
-- by-slug resolution silently returns whichever row the query planner reaches
-- first. In a live store this happened for real: a dispatched card minted its
-- micro-plan under a path rebuilt from the registry slug, the workspace scanner
-- indexed that tree as a project of its own, and the store ended up with two
-- rows both answering to "-home-<user>-Lab-<project>" — one the real checkout,
-- one the workspace dir. Lookups became a coin flip, with no error anywhere.
--
-- Duplicates are resolved before the index is built, lowest id keeping the
-- name: it is the oldest row and the one other tables already reference. The
-- losers keep their name plus a "-dup<id>" suffix rather than being deleted —
-- a slug clash is not evidence that a row is junk, and id makes each rename
-- distinct from every other rename by construction.
--
-- Going forward the invariant is held at the write end too, because this index
-- turns a clash from a silent wrong answer into a failed INSERT:
--   * ingest.FreeSlug suffixes a derived slug that is already taken, so
--     discovering a new cwd can never fail on a name collision.
--   * wsingest.taskProjectID mints its registry row through the same
--     FreeSlug, so a workspace named after an existing slug keeps its cards
--     instead of failing every scan on this index.
--   * ingest.SetOnboardedSlug already refuses a taken slug (SlugConflict).
--
-- IF NOT EXISTS: a dev machine that already carried this fix locally, under an
-- earlier ad hoc migration number, before it landed here — a fresh install has
-- no such index yet, an already-fixed one does, and this must succeed either
-- way.

UPDATE projects
   SET slug = slug || '-dup' || id
 WHERE id NOT IN (SELECT MIN(id) FROM projects GROUP BY slug);

CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_slug ON projects(slug);
