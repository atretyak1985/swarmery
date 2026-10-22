-- Skill-audit fixture registry (internal/sysscan/skillaudit_test.go).
-- Two projects; agents and skills in the shapes the scanner writes: plugin rows
-- carry the composite "<plugin>:<name>" name, local rows the bare name.
INSERT INTO projects (id, path, slug, name, first_seen) VALUES
  (1, '/fixture/alpha', 'alpha', 'alpha', '2026-07-13T00:00:00Z'),
  (2, '/fixture/beta',  'beta',  'beta',  '2026-07-13T00:00:00Z');

INSERT INTO agents (id, name, scope, project_id, file_path, origin, plugin_name, description, deleted) VALUES
  (1, 'core:tech-lead', 'global', NULL, '/plugins/core/agents/tech-lead.md', 'plugin', 'core', 'Orchestrate development work.', 0),
  (2, 'legacy-bot',     'project', 1,   '/fixture/alpha/.claude/agents/legacy-bot.md', 'local', NULL, 'Old deploy bot.', 0),
  (3, 'fk-agent',       'global', NULL, '/fixture/agents/fk-agent.md', 'local', NULL, 'Attributed through events.agent_id only.', 0),
  (4, 'gone-agent',     'global', NULL, '/fixture/agents/gone-agent.md', 'local', NULL, 'Deleted from disk.', 1);

INSERT INTO skills (id, name, scope, project_id, dir_path, description, origin, plugin_name, deleted) VALUES
  -- used through the Skill tool, under its plugin-qualified name
  (1, 'core:workspace-plans', 'global', NULL, '/plugins/core/skills/workspace-plans',
      'Write, revise or execute a workspace plan: the plan README and phase docs the dashboard ingests.', 'plugin', 'core', 0),
  -- used only through events.skill_id (the FK path still counts)
  (2, 'core:run-plan', 'global', NULL, '/plugins/core/skills/run-plan',
      'Executes an existing planner plan by dispatching per-phase implement and review loops.', 'plugin', 'core', 0),
  -- used only through a slash-command prompt
  (3, 'graphify', 'global', NULL, '/skills/graphify',
      'Turn a codebase into a persistent knowledge graph with community detection.', 'local', NULL, 0),
  -- last used 40 days ago: outside the window
  (4, 'old-helper', 'project', 1, '/fixture/alpha/.claude/skills/old-helper',
      'Format release notes for the changelog from merged pull requests.', 'local', NULL, 0),
  -- never used; only quoted inside a prose prompt, which is not an invocation
  (5, 'never-used', 'global', NULL, '/skills/never-used',
      'Draw architecture diagrams as mermaid from a repository scan.', 'local', NULL, 0),
  -- the near-identical pair: one shared-word difference
  (6, 'pdf-export', 'global', NULL, '/skills/pdf-export',
      'Export the session report to a PDF file with charts, tables and a cover page.', 'local', NULL, 0),
  (7, 'pdf-exporter', 'global', NULL, '/skills/pdf-exporter',
      'Export a session report to a PDF file with charts, tables and the cover page.', 'local', NULL, 0),
  -- a project copy of pdf-export: same folded name, so neither a self-pair nor
  -- a second pdf-export/pdf-exporter pair
  (8, 'pdf-export', 'project', 2, '/fixture/beta/.claude/skills/pdf-export',
      'Export the session report to a PDF file with charts, tables and a cover page.', 'local', NULL, 0),
  -- deleted rows are never audited
  (9, 'pdf-exporting', 'global', NULL, '/skills/pdf-exporting',
      'Export the session report to a PDF file with charts, tables and a cover page.', 'local', NULL, 1);
