-- 0068: terminal identity — which terminal tab owns this session, captured
-- once at SessionStart by the hookshim (`docs/hooks-protocol.md`), so any
-- client (notch widget, dashboard) can offer "focus that tab". All columns
-- are additive (NULL by default); existing rows unaffected. term_tty is
-- derived daemon-side from the bound PID, never from the shim's environment;
-- the other three come straight from the shim's terminal object and are NULL
-- together whenever a session has no terminal at all (a daemon-spawned
-- `claude -p` run, or a hook that never reached a live daemon).
ALTER TABLE sessions ADD COLUMN term_program   TEXT;
ALTER TABLE sessions ADD COLUMN term_focus_url TEXT;
ALTER TABLE sessions ADD COLUMN term_bundle_id TEXT;
ALTER TABLE sessions ADD COLUMN term_tty       TEXT;
