// Offline fixtures for the phase-4 /api/system/* read endpoints (VITE_MOCK=1)
// — mirror the step-05 DTO shapes frozen in ../api/types.ts so the System UI
// develops without the daemon. Same pattern as ./data.ts: plain fixture
// arrays + a mockSystemApi object with per-call delay and the scope/project
// query-param filtering the Go handlers implement.

import type {
  SystemCommand,
  SystemDiff,
  SystemHook,
  SystemItem,
  SystemItemDetail,
  SystemOverlays,
  SystemSummary,
  SystemVersion,
} from '../api/types';

const now = Date.now();
const iso = (msAgo: number): string => new Date(now - msAgo).toISOString();
const MIN = 60_000;
const DAY = 24 * 60 * MIN;

// --- GET /api/system/summary ---------------------------------------------------

export const mockSystemSummary: SystemSummary = {
  agents: 6,
  skills: 4,
  hooks: 12,
  commands: 5,
  overlays: 3,
  // Consistent with the item fixtures below: quality-checker error;
  // implementation-agent + release-notes + one hook_no_timeout warn;
  // legacy-deploy-bot agent_dead info.
  lint: { error: 1, warn: 3, info: 1 },
};

// --- GET /api/system/agents ------------------------------------------------------

export const mockSystemAgents: SystemItem[] = [
  {
    id: 1,
    name: 'tech-lead',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    model: 'claude-opus-4-8',
    description: 'Phase orchestrator: plans multi-repo changes and owns phase gates',
    path: '~/.claude/plugins/cache/swarmery/core/agents/tech-lead.md',
    lintMax: null,
    dead: false,
    lastUsed: iso(3 * 60 * MIN),
    tasks30d: 14,
  },
  {
    id: 2,
    name: 'implementation-agent',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    model: 'claude-opus-4-8',
    description: 'Phase 4 executor — writes code changes per the approved plan',
    path: '~/.claude/plugins/cache/swarmery/core/agents/implementation-agent.md',
    lintMax: 'warn',
    dead: false,
    lastUsed: iso(40 * MIN),
    tasks30d: 22,
  },
  {
    id: 3,
    name: 'commit-message',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    model: 'claude-fable-5',
    description: 'Writes conventional-commit messages from the staged diff',
    path: '~/.claude/plugins/cache/swarmery/core/agents/commit-message.md',
    lintMax: null,
    dead: false,
    lastUsed: iso(2 * DAY),
    tasks30d: 9,
  },
  {
    id: 4,
    name: 'quality-checker',
    scope: 'project',
    projectSlug: 'swarmery',
    origin: 'local',
    pluginName: null,
    model: 'claude-fable-5',
    description: null,
    path: '/Users/user/work/swarmery/.claude/agents/quality-checker.md',
    lintMax: 'error',
    dead: false,
    lastUsed: iso(5 * DAY),
    tasks30d: 3,
  },
  {
    id: 5,
    name: 'legacy-deploy-bot',
    scope: 'global',
    projectSlug: null,
    origin: 'local',
    pluginName: null,
    model: 'claude-fable-5',
    description: 'Deploy runbook automation (superseded by the deployment skill)',
    path: '~/.claude/agents/legacy-deploy-bot.md',
    lintMax: 'info',
    dead: true,
    lastUsed: null,
    tasks30d: 0,
  },
  {
    id: 6,
    name: 'orders-reviewer',
    scope: 'project',
    projectSlug: 'orders-api',
    origin: 'local',
    pluginName: null,
    model: 'claude-opus-4-8',
    description: 'Domain review of order-flow changes before merge',
    path: '/Users/user/work/orders-api/.claude/agents/orders-reviewer.md',
    lintMax: null,
    dead: false,
    lastUsed: iso(26 * 60 * MIN),
    tasks30d: 6,
  },
];

// --- GET /api/system/skills -------------------------------------------------------

export const mockSystemSkills: SystemItem[] = [
  {
    id: 101,
    name: 'code-standards',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    model: null,
    description: 'Reviews code against per-repo conventions with file:line citations',
    path: '~/.claude/plugins/cache/swarmery/core/skills/code-standards',
    lintMax: null,
    dead: false,
    lastUsed: iso(90 * MIN),
    tasks30d: 11,
  },
  {
    id: 102,
    name: 'api-integration',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    model: null,
    description: 'REST/ORM/WebSocket/SSE integration patterns for the main app',
    path: '~/.claude/plugins/cache/swarmery/core/skills/api-integration',
    lintMax: null,
    dead: false,
    lastUsed: iso(4 * DAY),
    tasks30d: 5,
  },
  {
    id: 103,
    name: 'release-notes',
    scope: 'project',
    projectSlug: 'orders-api',
    origin: 'local',
    pluginName: null,
    model: null,
    description: 'Drafts release notes from merged PR titles',
    path: '/Users/user/work/orders-api/.claude/skills/release-notes',
    lintMax: 'warn',
    dead: false,
    lastUsed: iso(9 * DAY),
    tasks30d: 2,
  },
  {
    id: 104,
    name: 'db-migrate',
    scope: 'project',
    projectSlug: 'swarmery',
    origin: 'local',
    pluginName: null,
    model: null,
    description: 'Guides SQLite migration authoring for the control plane',
    path: '/Users/user/work/swarmery/.claude/skills/db-migrate',
    lintMax: null,
    dead: false,
    lastUsed: iso(12 * 60 * MIN),
    tasks30d: 4,
  },
];

// --- Detail bodies (agents + skills share the builder) ---------------------------

const TECH_LEAD_BODY = `# Role

Tech Lead is the phase orchestrator. It owns the plan, hands work to the
implementation agent, and gates every phase transition.

## Process

1. **Review context** — read the phase-2 artifact before planning.
2. **Plan** — file list, order, acceptance criteria.
3. **Gate** — verify the Completion Report before advancing.

## Boundaries

- Never edits code itself — delegates to \`@implementation-agent\`.
- Escalates to the user when a plan assumption breaks.

| Phase | Owner |
|---|---|
| 3 — plan | tech-lead |
| 4 — implement | implementation-agent |
| 5 — verify | quality-checker |`;

const GENERIC_BODY = `# Purpose

Registered component of this machine's Claude Code setup. The scanner
versioned its content; older snapshots are listed under **Versions**.

## Notes

- Content is served redacted: secret-shaped values become \`•••\`.
- This is fixture prose for offline UI development (VITE_MOCK=1).`;

function frontmatterFor(item: SystemItem): string {
  const lines = [`name: ${item.name}`];
  if (item.description !== null) lines.push(`description: ${item.description}`);
  if (item.model !== null) lines.push(`model: ${item.model}`);
  lines.push(`identity: swarmery-${item.origin === 'plugin' ? (item.pluginName ?? 'core') : 'local'}`);
  if (item.name === 'quality-checker') lines.push('api_key: •••');
  return lines.join('\n');
}

function versionsFor(item: SystemItem): SystemVersion[] {
  const base = item.id * 10;
  const versions: SystemVersion[] = [
    {
      id: base + 3,
      createdAt: iso(2 * DAY),
      changeNote: 'scanner: content hash changed',
      contentHash: `f4a${String(item.id)}e91c0d27b3a5`,
    },
    {
      id: base + 2,
      createdAt: iso(11 * DAY),
      changeNote: 'tighten description + add boundaries section',
      contentHash: `8c1${String(item.id)}b02a9e64d7f1`,
    },
    {
      id: base + 1,
      createdAt: iso(27 * DAY),
      changeNote: 'first scan',
      contentHash: `a90${String(item.id)}c44f1b8e6d20`,
    },
  ];
  // Give rarely used items a single-entry history so both list shapes render.
  return item.tasks30d <= 2 ? versions.slice(0, 1) : versions;
}

function detailFor(item: SystemItem): SystemItemDetail {
  const versions = versionsFor(item);
  return {
    ...item,
    deleted: false,
    currentVersionId: versions[0]?.id ?? null,
    frontmatter: frontmatterFor(item),
    body: item.name === 'tech-lead' ? TECH_LEAD_BODY : GENERIC_BODY,
    versions,
  };
}

// --- GET .../{id}/diff?from=&to= ---------------------------------------------------

function diffFor(item: SystemItem, from: number, to: number): string {
  if (from === to) return '';
  return `--- ${item.name} @ v${String(from)}
+++ ${item.name} @ v${String(to)}
@@ -1,6 +1,7 @@
 ---
 name: ${item.name}
-description: (draft)
+description: ${item.description ?? 'registered component'}
 identity: swarmery-core
 ---
@@ -12,4 +13,6 @@ ## Process
-2. Plan the change.
+2. Plan — file list, order, acceptance criteria.
+3. Gate — verify the Completion Report before advancing.`;
}

// --- GET /api/system/hooks ----------------------------------------------------------

const GLOBAL_SETTINGS = '~/.claude/settings.json';
const SWARMERY_SETTINGS = '/Users/user/work/swarmery/.claude/settings.json';
const ORDERS_SETTINGS = '/Users/user/work/orders-api/.claude/settings.json';

export const mockSystemHooks: SystemHook[] = [
  {
    id: 1,
    scope: 'global',
    projectSlug: null,
    event: 'PreToolUse',
    matcher: 'Bash',
    command: 'swarmery hook pre-bash --token •••',
    timeout: 30,
    statusMessage: null,
    sourceFile: GLOBAL_SETTINGS,
    seq: 0,
    enabled: true,
    managed: 'swarmery',
  },
  {
    id: 2,
    scope: 'global',
    projectSlug: null,
    event: 'PreToolUse',
    matcher: 'Edit|Write',
    command: 'bash ~/.claude/hooks/guard-paths.sh',
    timeout: null,
    statusMessage: null,
    sourceFile: GLOBAL_SETTINGS,
    seq: 1,
    enabled: true,
    managed: null,
  },
  {
    id: 3,
    scope: 'project',
    projectSlug: 'swarmery',
    event: 'PreToolUse',
    matcher: 'Bash',
    command: 'bash plugins/core/hooks/post_bash_index_check.sh',
    timeout: 20,
    statusMessage: null,
    sourceFile: SWARMERY_SETTINGS,
    seq: 0,
    enabled: true,
    managed: 'swarmery',
  },
  {
    id: 4,
    scope: 'global',
    projectSlug: null,
    event: 'PostToolUse',
    matcher: 'Edit|Write',
    command: 'swarmery hook post-edit',
    timeout: 15,
    statusMessage: null,
    sourceFile: GLOBAL_SETTINGS,
    seq: 0,
    enabled: true,
    managed: 'swarmery',
  },
  {
    id: 5,
    scope: 'project',
    projectSlug: 'orders-api',
    event: 'PostToolUse',
    matcher: 'Write',
    command: 'npx prettier --write "$CLAUDE_FILE_PATHS"',
    timeout: 60,
    statusMessage: 'formatting…',
    sourceFile: ORDERS_SETTINGS,
    seq: 0,
    enabled: true,
    managed: null,
  },
  {
    id: 6,
    scope: 'global',
    projectSlug: null,
    event: 'UserPromptSubmit',
    matcher: null,
    command: 'swarmery hook prompt-heartbeat',
    timeout: 5,
    statusMessage: null,
    sourceFile: GLOBAL_SETTINGS,
    seq: 0,
    enabled: true,
    managed: 'swarmery',
  },
  {
    id: 7,
    scope: 'global',
    projectSlug: null,
    event: 'Stop',
    matcher: null,
    command: 'swarmery hook session-stop',
    timeout: 10,
    statusMessage: null,
    sourceFile: GLOBAL_SETTINGS,
    seq: 0,
    enabled: true,
    managed: 'swarmery',
  },
  {
    id: 8,
    scope: 'project',
    projectSlug: 'swarmery',
    event: 'Stop',
    matcher: null,
    command: 'bash .claude/hooks/completion-summary.sh',
    timeout: null,
    statusMessage: null,
    sourceFile: SWARMERY_SETTINGS,
    seq: 1,
    enabled: true,
    managed: null,
  },
  {
    id: 9,
    scope: 'global',
    projectSlug: null,
    event: 'SubagentStop',
    matcher: null,
    command: 'swarmery hook subagent-stop',
    timeout: 10,
    statusMessage: null,
    sourceFile: GLOBAL_SETTINGS,
    seq: 0,
    enabled: true,
    managed: 'swarmery',
  },
  {
    id: 10,
    scope: 'global',
    projectSlug: null,
    event: 'SessionStart',
    matcher: null,
    command: 'swarmery hook session-start --daemon http://localhost:7777',
    timeout: 10,
    statusMessage: null,
    sourceFile: GLOBAL_SETTINGS,
    seq: 0,
    enabled: true,
    managed: 'swarmery',
  },
  {
    id: 11,
    scope: 'project',
    projectSlug: 'orders-api',
    event: 'SessionStart',
    matcher: null,
    command: 'bash scripts/dev-env-check.sh',
    timeout: 30,
    statusMessage: null,
    sourceFile: ORDERS_SETTINGS,
    seq: 0,
    enabled: false,
    managed: null,
  },
  {
    id: 12,
    scope: 'global',
    projectSlug: null,
    event: 'SessionEnd',
    matcher: null,
    command: 'swarmery hook session-end',
    timeout: 10,
    statusMessage: null,
    sourceFile: GLOBAL_SETTINGS,
    seq: 0,
    enabled: true,
    managed: 'swarmery',
  },
];

// --- GET /api/system/commands ---------------------------------------------------------

export const mockSystemCommands: SystemCommand[] = [
  {
    id: 1,
    name: 'new-feature-branch',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    description: 'Branch-from-fresh-main boilerplate',
    path: '~/.claude/plugins/cache/swarmery/core/commands/new-feature-branch.md',
  },
  {
    id: 2,
    name: 'search',
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    description: 'Fast ripgrep code search across the project repositories',
    path: '~/.claude/plugins/cache/swarmery/core/commands/search.md',
  },
  {
    id: 3,
    name: 'deploy-status',
    scope: 'project',
    projectSlug: 'orders-api',
    origin: 'local',
    pluginName: null,
    description: 'Show the current canary rollout state',
    path: '/Users/user/work/orders-api/.claude/commands/deploy-status.md',
  },
  {
    id: 4,
    name: 'release',
    scope: 'project',
    projectSlug: 'swarmery',
    origin: 'local',
    pluginName: null,
    description: null,
    path: '/Users/user/work/swarmery/.claude/commands/release.md',
  },
  {
    id: 5,
    name: 'scratch',
    scope: 'global',
    projectSlug: null,
    origin: 'local',
    pluginName: null,
    description: 'Open a scratch worktree for one-off experiments',
    path: '~/.claude/commands/scratch.md',
  },
];

// --- GET /api/system/overlays -----------------------------------------------------------

export const mockSystemOverlays: SystemOverlays = {
  schemaPresent: true,
  overlays: [
    {
      dir: 'example',
      path: 'overlays/example/project.json',
      parseError: false,
      name: 'example',
      displayName: 'Example App',
      codePath: '/Users/user/work/example-app',
      mainApp: 'apps/web',
      repos: ['apps/web', 'services/api', 'infra'],
      enabledPacks: ['web-pack'],
    },
    {
      dir: 'orders-api',
      path: 'overlays/orders-api/project.json',
      parseError: false,
      name: 'orders-api',
      displayName: 'Orders API',
      codePath: '/Users/user/work/orders-api',
      mainApp: 'apps/portal',
      repos: ['apps/portal', 'device-edge'],
      enabledPacks: ['iot-pack', 'infra-pack'],
    },
    {
      dir: 'broken-legacy',
      path: 'overlays/broken-legacy/project.json',
      parseError: true,
      name: null,
      displayName: null,
      codePath: null,
      mainApp: null,
      repos: [],
      enabledPacks: [],
    },
  ],
};

// --- Mock API (the fetchers in ../api/system.ts dispatch here when MOCK) ----------

const delay = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

export interface SystemMockFilters {
  scope?: 'global' | 'project';
  project?: string;
}

function applyFilters<T extends { scope: 'global' | 'project'; projectSlug: string | null }>(
  rows: T[],
  filters: SystemMockFilters,
): T[] {
  return rows
    .filter((r) => {
      if (filters.scope !== undefined && r.scope !== filters.scope) return false;
      if (filters.project !== undefined && r.projectSlug !== filters.project) return false;
      return true;
    })
    .map((r) => ({ ...r }));
}

function itemsOf(kind: 'agents' | 'skills'): SystemItem[] {
  return kind === 'agents' ? mockSystemAgents : mockSystemSkills;
}

export const mockSystemApi = {
  async summary(): Promise<SystemSummary> {
    await delay(90);
    return { ...mockSystemSummary, lint: { ...mockSystemSummary.lint } };
  },

  async items(kind: 'agents' | 'skills', filters: SystemMockFilters = {}): Promise<SystemItem[]> {
    await delay(130);
    return applyFilters(itemsOf(kind), filters);
  },

  async itemDetail(kind: 'agents' | 'skills', id: number): Promise<SystemItemDetail> {
    await delay(150);
    const item = itemsOf(kind).find((i) => i.id === id);
    if (!item) throw new Error(`mock: system ${kind} ${String(id)} not found`);
    return detailFor(item);
  },

  async diff(kind: 'agents' | 'skills', id: number, from: number, to: number): Promise<SystemDiff> {
    await delay(120);
    const item = itemsOf(kind).find((i) => i.id === id);
    if (!item) throw new Error(`mock: system ${kind} ${String(id)} not found`);
    return { from, to, diff: diffFor(item, from, to) };
  },

  async hooks(filters: SystemMockFilters = {}): Promise<SystemHook[]> {
    await delay(120);
    return applyFilters(mockSystemHooks, filters);
  },

  async commands(filters: SystemMockFilters = {}): Promise<SystemCommand[]> {
    await delay(120);
    return applyFilters(mockSystemCommands, filters);
  },

  async overlays(): Promise<SystemOverlays> {
    await delay(100);
    return {
      schemaPresent: mockSystemOverlays.schemaPresent,
      overlays: mockSystemOverlays.overlays.map((o) => ({
        ...o,
        repos: [...o.repos],
        enabledPacks: [...o.enabledPacks],
      })),
    };
  },
};
