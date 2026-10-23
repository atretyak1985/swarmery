-- Skill-audit fixture telemetry (internal/sysscan/skillaudit_test.go).
-- Timestamps are relative to 'now' so the 30-day window never goes stale.
INSERT INTO sessions (id, project_id, session_uuid, started_at) VALUES
  (1, 1, 'sa-alpha-1', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 day')),
  (2, 1, 'sa-alpha-2', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day')),
  (3, 2, 'sa-beta-1',  strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-1 day')),
  (4, 1, 'sa-alpha-old', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-40 day'));

INSERT INTO events (session_id, ts, type, agent_id, skill_id, payload) VALUES
  -- alpha session 1: three skills (Skill tool x2 + one slash command)
  (1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 day'), 'skill_use', NULL, NULL,
      '{"input":{"skill":"core:workspace-plans"},"result":{"success":true}}'),
  (1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 day'), 'skill_use', NULL, NULL,
      '{"input":{"skill":"pdf-export"},"result":{"success":true}}'),
  (1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 day'), 'user_prompt', NULL, NULL,
      '{"content":"<command-message>graphify</command-message>\n<command-name>/graphify</command-name>\n<command-args>.</command-args>"}'),
  -- a repeat of the same skill in the same session counts once
  (1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 day'), 'skill_use', NULL, NULL,
      '{"input":{"skill":"workspace-plans"},"result":{"success":true}}'),
  (1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 day'), 'subagent_start', NULL, NULL,
      '{"subagent_type":"core:tech-lead"}'),
  -- alpha session 2: three skills, one of them not in the registry
  (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day'), 'skill_use', NULL, NULL,
      '{"input":{"skill":"core:workspace-plans"}}'),
  (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day'), 'skill_use', NULL, NULL,
      '{"input":{"skill":"pdf-exporter"}}'),
  (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day'), 'skill_use', NULL, NULL,
      '{"input":{"skill":"superpowers:brainstorming"}}'),
  -- a slash command that is NOT a skill (no registry row): not a skill load
  (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day'), 'user_prompt', NULL, NULL,
      '{"content":"<command-name>/clear</command-name>"}'),
  -- prose that quotes a command tag is not an invocation
  (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day'), 'user_prompt', NULL, NULL,
      '{"content":"Write a handoff. Do not type <command-name>/never-used</command-name> again."}'),
  -- the FK-only paths
  (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day'), 'skill_use', NULL, 2, NULL),
  (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day'), 'subagent_start', 3, NULL, NULL),
  -- a malformed payload must not fail the audit
  (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-3 day'), 'skill_use', NULL, NULL, '{not json'),
  -- beta session: one skill
  (3, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-1 day'), 'skill_use', NULL, NULL,
      '{"input":{"skill":"core:workspace-plans"}}'),
  -- 40 days ago: outside the window
  (4, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-40 day'), 'skill_use', NULL, NULL,
      '{"input":{"skill":"old-helper"}}'),
  (4, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-40 day'), 'subagent_start', NULL, NULL,
      '{"subagent_type":"legacy-bot"}');
