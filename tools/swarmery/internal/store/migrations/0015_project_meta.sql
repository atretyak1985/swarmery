-- 0015: project meta — dashboard pinning + free-form tags.
-- pinned=1 floats a project to the top of the projects list and the global
-- scope switcher; tags is a JSON array of lowercase strings ('["billing"]')
-- written via PATCH /api/projects/{id}. Both are dashboard-only metadata —
-- ingest never touches them, so they survive rescans. Additive with defaults;
-- existing rows are unaffected.
ALTER TABLE projects ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN tags   TEXT    NOT NULL DEFAULT '[]';
