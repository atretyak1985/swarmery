// Offline fixtures for the fusion-phase-18 /api/system/* Hub endpoints
// (VITE_MOCK=1) — mirror the DTOs frozen in ../api/types.ts so the System Hub
// develops without the daemon. Includes the two shapes the phase spec calls out:
// a lint-flagged hook (hook_no_timeout) and a project-override template (its
// badge flips to "project override"). Same plain-fixture + per-call-delay
// pattern as ./agentHub.ts. The copyTemplate write mutates in-memory state so a
// demo shows the badge flip + a repeat 409.

import type {
  CommandHub,
  HookHub,
  SkillHub,
  SystemHubSummary,
  SystemTemplate,
  SystemTemplateContent,
  SystemTemplateCopyResponse,
} from '../api/types';
import { TemplateCopyError } from '../api/systemHub';

const delay = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

function daysAgoISO(n: number): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - n);
  return d.toISOString();
}

function isoDay(n: number): string {
  return daysAgoISO(n).slice(0, 10);
}

/* ----- skills ----- */

function skillByDay(total: number): { day: string; count: number }[] {
  const out: { day: string; count: number }[] = [];
  for (let i = 29; i >= 0; i--) {
    out.push({ day: isoDay(i), count: Math.max(0, Math.round((total / 30) * (1 + Math.sin(i / 4)))) });
  }
  return out;
}

const SKILL_HUB: SkillHub = {
  id: 1,
  name: 'browser-verification',
  scope: 'global',
  projectSlug: null,
  origin: 'plugin',
  pluginName: 'core',
  model: null,
  description: 'Verify UI behavior in a live browser via Playwright MCP tools',
  path: '/plugins/core/skills/browser-verification',
  lintMax: null,
  dead: false,
  lastUsed: daysAgoISO(0),
  tasks30d: 12,
  usage: {
    windowDays: 30,
    invocations: 47,
    sessions: 18,
    projects: 3,
    errors: 2,
    lastUsed: daysAgoISO(0),
    approximate: false,
    byDay: skillByDay(47),
  },
  sessions: [
    { ts: daysAgoISO(0), sessionUuid: 'sess-h1', sessionTitle: 'Verify system hub UI', projectSlug: 'alpha', status: 'ok' },
    { ts: daysAgoISO(1), sessionUuid: 'sess-h2', sessionTitle: 'Regression sweep', projectSlug: 'beta', status: 'error' },
    { ts: daysAgoISO(2), sessionUuid: 'sess-h3', sessionTitle: 'Smoke test', projectSlug: 'alpha', status: 'ok' },
  ],
};

/* ----- hooks (one is lint-flagged: hook_no_timeout) ----- */

const HOOK_HUB: HookHub = {
  id: 1,
  scope: 'global',
  projectSlug: null,
  event: 'PreToolUse',
  matcher: 'Bash',
  command: 'swarmery hook pre-tool-use',
  timeout: null, // → hook_no_timeout lint below
  statusMessage: null,
  sourceFile: '/u/.claude/settings.json',
  seq: 0,
  enabled: true,
  managed: 'swarmery',
  contentHash: 'hook-hash-1',
  firingTelemetry: false,
  lint: [{ rule: 'hook_no_timeout', severity: 'warn', message: 'no timeout set — a hung command blocks the session' }],
};

/* ----- commands ----- */

const COMMAND_HUB: CommandHub = {
  id: 1,
  name: 'land',
  scope: 'global',
  projectSlug: null,
  origin: 'plugin',
  pluginName: 'core',
  description: 'End-of-session landing ritual',
  path: '/plugins/core/commands/land.md',
  frontmatter: 'description: End-of-session landing ritual',
  content: '# /land\n\nClose finished tasks, keep genuine WIP active, write a NEXT.md handoff.\n',
  usage: { windowDays: 30, invocations: 6, approximate: true },
};

/* ----- templates (a project override demonstrates the badge flip) ----- */

const BASE_TEMPLATES: SystemTemplate[] = [
  { name: 'adr-template', fileName: 'adr-template.md', path: '/plugins/core/templates/adr-template.md', resolution: 'core', source: 'plugin', pluginName: 'core', overridden: false },
  { name: 'pr-description-template', fileName: 'pr-description-template.md', path: '/plugins/core/templates/pr-description-template.md', resolution: 'core', source: 'plugin', pluginName: 'core', overridden: false },
  { name: 'commit-message-template', fileName: 'commit-message-template.md', path: '/plugins/core/templates/commit-message-template.md', resolution: 'core', source: 'plugin', pluginName: 'core', overridden: false },
  { name: 'web-scaffold', fileName: 'web-scaffold.md', path: '/plugins/web-pack/templates/web-scaffold.md', resolution: 'pack:web-pack', source: 'plugin', pluginName: 'web-pack', overridden: false },
];

// A project-local override of adr-template — the effective project view shadows
// the built-in and its badge reads "project override".
const PROJECT_OVERRIDES = new Map<string, SystemTemplate>([
  [
    'adr-template',
    { name: 'adr-template', fileName: 'adr-template.md', path: '/work/alpha/.claude/templates/adr-template.md', resolution: 'project override', source: 'project', pluginName: '', overridden: false },
  ],
]);

const TEMPLATE_CONTENT: Record<string, string> = {
  'adr-template': '# Architecture Decision Record\n\n**Status**: proposed\n**Context**: …\n**Decision**: …\n',
  'pr-description-template': '## Summary\n\n## Risk\n\n## Testing\n',
  'commit-message-template': 'type(scope): subject\n\nbody\n',
  'web-scaffold': '# Web feature scaffold\n',
};

/** The effective template list for a scope: fleet = built-ins; project = folds
 * the override in (badge "project override") and shadows the built-in. */
function effectiveTemplates(projectId?: string): SystemTemplate[] {
  if (projectId === undefined || projectId === '') {
    return BASE_TEMPLATES.map((t) => ({ ...t }));
  }
  const out: SystemTemplate[] = [];
  for (const [, ov] of PROJECT_OVERRIDES) out.push({ ...ov });
  for (const t of BASE_TEMPLATES) {
    if (PROJECT_OVERRIDES.has(t.name)) continue; // shadowed
    out.push({ ...t });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

export const mockSystemHubApi = {
  async summary(): Promise<SystemHubSummary> {
    await delay(90);
    return { agents: 24, skills: 34, hooks: 12, commands: 9, templates: BASE_TEMPLATES.length, insights: 3, lintFindings: 5 };
  },
  async skill(_id: number): Promise<SkillHub> {
    await delay(120);
    return SKILL_HUB;
  },
  async hook(_id: number): Promise<HookHub> {
    await delay(110);
    return HOOK_HUB;
  },
  async command(_id: number): Promise<CommandHub> {
    await delay(110);
    return COMMAND_HUB;
  },
  async templates(projectId?: string): Promise<SystemTemplate[]> {
    await delay(100);
    return effectiveTemplates(projectId);
  },
  async template(name: string, projectId?: string): Promise<SystemTemplateContent> {
    await delay(100);
    const base = effectiveTemplates(projectId).find((t) => t.name === name) ?? BASE_TEMPLATES[0];
    return { ...(base as SystemTemplate), content: TEMPLATE_CONTENT[name] ?? '# ' + name + '\n' };
  },
  async copyTemplate(name: string, _projectId: string): Promise<SystemTemplateCopyResponse> {
    await delay(140);
    if (PROJECT_OVERRIDES.has(name)) {
      // O_EXCL: already customised → 409, mirroring the daemon.
      throw new TemplateCopyError(409, `project template already exists: ${name}`);
    }
    const built = BASE_TEMPLATES.find((t) => t.name === name);
    if (built === undefined) throw new TemplateCopyError(404, `no template named ${name}`);
    const dest = `/work/alpha/.claude/templates/${name}.md`;
    PROJECT_OVERRIDES.set(name, {
      name, fileName: `${name}.md`, path: dest, resolution: 'project override', source: 'project', pluginName: '', overridden: false,
    });
    return { name, path: dest, hint: `edit ${dest} — project templates override built-ins` };
  },
};
