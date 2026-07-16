// Offline fixtures for the phase-4 /api/system/* endpoints (VITE_MOCK=1) —
// mirror the step-05 read DTOs plus the Stage 2 write surface (steps 09–12)
// frozen in ../api/types.ts so the System UI develops without the daemon.
// Same pattern as ./data.ts: plain fixture arrays + a mockSystemApi object
// with per-call delay and the scope/project query-param filtering the Go
// handlers implement. Write calls mutate in-memory state and reproduce the
// full 409/403/422 error contract — see the demo-trigger table below.

import type {
  AgentHistory,
  SystemCommand,
  SystemCreateAgentRequest,
  SystemCreateResponse,
  SystemDiff,
  SystemHook,
  SystemInsights,
  SystemItem,
  SystemItemDetail,
  SystemLintFinding,
  SystemOverlays,
  SystemRestoreResponse,
  SystemRollbackRequest,
  SystemSummary,
  SystemVersion,
  SystemWriteRequest,
  SystemWriteResponse,
} from '../api/types';
// Runtime-only use inside async methods — the module cycle with ../api/system
// (it imports mockSystemApi) is safe: the class binding is live by call time.
import { SystemWriteError } from '../api/system';
import { mockProjects } from './data';

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
  // Consistent with mockSystemInsights below: 1 promotion candidate + 1 stale
  // override.
  insights: { promotions: 1, staleOverrides: 1 },
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

// --- Stage 2 write state (step-12) --------------------------------------------
//
// Demo triggers — prove every error state without a live daemon:
//   · PUT content containing 'demo-conflict'  → 409 {disk_hash, base_hash, diff}
//   · PUT content containing 'demo-readonly'  → 403 readonly (global banner)
//   · PUT content without a leading --- block → 422 parse error
//   · any write on an origin=plugin item      → 403 plugin-managed
//   · save on a stale base_hash               → real 409 (open Edit twice, save both)
//   · create with an existing name            → 409 (+restore_id when soft-deleted)
//   · create with 'demo-readonly' description → 403 readonly (global banner)
//   · toggle/edit of a managed=swarmery hook  → 403 installer-managed
//   · hook edit: command w/ 'demo-readonly'   → 403 readonly (global banner)
//   · hook edit: command w/ 'demo-conflict'   → 409 hook conflict (refetch path)

interface MockItemState {
  frontmatter: string;
  body: string;
  versions: SystemVersion[];
  currentVersionId: number | null;
  deleted: boolean;
}

const itemState = new Map<string, MockItemState>();
let nextVersionId = 9000;
let nextAgentId = 900;
let hashSeq = 0;

const fakeHash = (): string => `w${String(hashSeq++).padStart(4, '0')}deadbeef42`;

function stateOf(kind: 'agents' | 'skills', item: SystemItem): MockItemState {
  const key = `${kind}:${String(item.id)}`;
  let st = itemState.get(key);
  if (st === undefined) {
    const versions = versionsFor(item);
    st = {
      frontmatter: frontmatterFor(item),
      body: item.name === 'tech-lead' ? TECH_LEAD_BODY : GENERIC_BODY,
      versions,
      currentVersionId: versions[0]?.id ?? null,
      deleted: false,
    };
    itemState.set(key, st);
  }
  return st;
}

function isDeleted(kind: 'agents' | 'skills', item: SystemItem): boolean {
  return itemState.get(`${kind}:${String(item.id)}`)?.deleted ?? false;
}

function detailFor(kind: 'agents' | 'skills', item: SystemItem): SystemItemDetail {
  const st = stateOf(kind, item);
  return {
    ...item,
    deleted: st.deleted,
    currentVersionId: st.currentVersionId,
    frontmatter: st.frontmatter,
    body: st.body,
    versions: st.versions.map((v) => ({ ...v })),
  };
}

function currentHashOf(st: MockItemState): string {
  return st.versions.find((v) => v.id === st.currentVersionId)?.contentHash ?? '';
}

/** Split raw md into frontmatter + body; null = unparseable (the 422 tier). */
function splitContent(content: string): { frontmatter: string; body: string } | null {
  if (!content.startsWith('---\n')) return null;
  const end = content.indexOf('\n---', 4);
  if (end === -1) return null;
  return {
    frontmatter: content.slice(4, end),
    body: content.slice(end + 4).replace(/^\n+/, ''),
  };
}

/** Deterministic ride-along lint (never blocks) — mirrors sysscan.LintContent. */
function lintOf(content: string): SystemLintFinding[] {
  const lint: SystemLintFinding[] = [];
  if (!/boundaries/i.test(content)) {
    lint.push({
      rule: 'no_boundaries',
      severity: 'warn',
      message: 'no Boundaries section — agents without limits drift out of scope',
    });
  }
  if (content.length < 400) {
    lint.push({
      rule: 'short_body',
      severity: 'info',
      message: 'body is under 400 chars — consider documenting process and outputs',
    });
  }
  return lint;
}

function throwConflict(baseHash: string, name: string): never {
  throw new SystemWriteError(
    409,
    {
      error: 'content changed on disk since base_hash',
      disk_hash: fakeHash(),
      base_hash: baseHash,
      diff: [
        `--- ${name} (your base)`,
        `+++ ${name} (on disk)`,
        '@@ -1,4 +1,4 @@',
        ' ---',
        ` name: ${name}`,
        '-description: the content your edit is based on',
        '+description: edited outside the dashboard while you were typing',
        ' ---',
      ].join('\n'),
    },
    'mock',
  );
}

function guardWritable(item: SystemItem, content?: string): void {
  if (item.origin === 'plugin') {
    throw new SystemWriteError(
      403,
      { error: "item is plugin-managed — edit it in the plugin's repo" },
      'mock',
    );
  }
  if (content !== undefined && content.includes('demo-readonly')) {
    throw new SystemWriteError(
      403,
      { error: 'system editor is in readonly mode (SWARMERY_SYSTEM_READONLY)' },
      'mock',
    );
  }
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

// Every hook write (toggle/edit) needs the row content_hash as base_hash —
// give each fixture a stable pseudo-hash; mock writes rotate it (the real
// nodeHash covers command/timeout/enabled, sysscan/hooknodes.go).
for (const h of mockSystemHooks) {
  h.contentHash = `hk${String(h.id).padStart(2, '0')}c0ffee42`;
}

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

// --- GET /api/system/insights ---------------------------------------------------

export const mockSystemInsights: SystemInsights = {
  promotionCandidates: [
    {
      kind: 'agent',
      name: 'release-notes',
      copies: [
        {
          itemId: 4,
          projectSlug: 'nova',
          scope: 'project',
          path: '/work/nova/.claude/agents/release-notes.md',
          contentHash: 'rn-nova',
        },
        {
          itemId: 9,
          projectSlug: 'atlas',
          scope: 'project',
          path: '/work/atlas/.claude/agents/release-notes.md',
          contentHash: 'rn-atlas',
        },
      ],
      similarity: 'diverged',
      diffStat: { added: 3, removed: 1 },
      diff: [
        '--- /work/nova/.claude/agents/release-notes.md',
        '+++ /work/atlas/.claude/agents/release-notes.md',
        '@@ -4,3 +4,5 @@',
        ' Collect merged PRs since the last tag.',
        '-Group by scope.',
        '+Group by conventional-commit scope.',
        '+Put breaking changes first.',
        '+Link every PR.',
      ].join('\n'),
      hint:
        "promote: de-flavor → move to a domain pack (2 projects) or core (all projects) → bump that plugin's semver → delete the donor copies (docs/EXTENDING.md)",
    },
  ],
  staleOverrides: [
    {
      kind: 'skill',
      name: 'code-quality',
      pluginName: 'core',
      local: {
        itemId: 12,
        projectSlug: 'nova',
        scope: 'project',
        path: '/work/nova/.claude/skills/code-quality',
        contentHash: 'cq-1',
      },
      plugin: {
        itemId: 3,
        projectSlug: null,
        scope: 'global',
        path: '/u/.claude/plugins/cache/swarmery/core/1.7.2/skills/code-quality',
        contentHash: 'cq-1',
      },
      identical: true,
      diffStat: null,
      diff: '',
      hint: 'identical to the plugin copy — the local override is pointless; delete the local file and rely on the plugin',
    },
  ],
  dead: [
    {
      kind: 'agent',
      id: 6,
      name: 'legacy-deploy-bot',
      scope: 'global',
      projectSlug: null,
      message: 'agent "legacy-deploy-bot": 0 event mentions in the last 30 days by available telemetry',
      hint: '0 telemetry mentions in 30 days (advisory — events.agent_id is only partially attributed); consider deleting or archiving',
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
    // Soft-deleted rows disappear from lists (the Go handlers filter deleted=0).
    return applyFilters(
      itemsOf(kind).filter((i) => !isDeleted(kind, i)),
      filters,
    );
  },

  async itemDetail(kind: 'agents' | 'skills', id: number): Promise<SystemItemDetail> {
    await delay(150);
    const item = itemsOf(kind).find((i) => i.id === id);
    if (!item) throw new Error(`mock: system ${kind} ${String(id)} not found`);
    return detailFor(kind, item);
  },

  async agentHistory(id: number, days = 90): Promise<AgentHistory> {
    await delay(140);
    const item = itemsOf('agents').find((i) => i.id === id);
    const name = item?.name ?? 'agent';
    return {
      agentName: name,
      windowDays: days,
      totals: { runs: 12, sessions: 9, projects: 2, okRuns: 11, errorRuns: 1, errorRate: 1 / 12 },
      duration: { avgMs: 184000, p50Ms: 150000, p95Ms: 420000, totalMs: 2208000 },
      byProject: [
        { slug: 'alpha', name: 'Alpha', runs: 8, avgMs: 165000, errorRate: 0, lastUsed: iso(DAY) },
        { slug: 'beta', name: 'Beta', runs: 4, avgMs: 220000, errorRate: 0.25, lastUsed: iso(4 * DAY) },
      ],
      byDay: [
        { day: '2026-07-10', runs: 3 },
        { day: '2026-07-11', runs: 5 },
        { day: '2026-07-12', runs: 4 },
      ],
      recentRuns: [
        { ts: iso(DAY), projectSlug: 'alpha', sessionUuid: 'aaaa-1', sessionTitle: 'Refactor auth', description: 'Orchestrate refactor', status: 'ok', durationMs: 152000 },
        { ts: iso(4 * DAY), projectSlug: 'beta', sessionUuid: 'bbbb-1', sessionTitle: 'Fix payments', description: 'Root-cause bug', status: 'error', durationMs: 421000 },
      ],
    };
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

  async insights(): Promise<SystemInsights> {
    await delay(110);
    return structuredClone(mockSystemInsights);
  },

  // --- Stage 2 write surface (step-12) — mutates the in-memory state above ----

  async putItem(
    kind: 'agents' | 'skills',
    id: number,
    req: SystemWriteRequest,
  ): Promise<SystemWriteResponse> {
    await delay(160);
    const item = itemsOf(kind).find((i) => i.id === id);
    if (!item) throw new SystemWriteError(404, { error: `${kind} not found` }, 'mock');
    guardWritable(item, req.content);
    const st = stateOf(kind, item);
    if (req.base_hash !== currentHashOf(st) || req.content.includes('demo-conflict')) {
      throwConflict(req.base_hash, item.name);
    }
    const split = splitContent(req.content);
    if (split === null) {
      throw new SystemWriteError(
        422,
        { error: 'frontmatter parse error: missing leading --- block' },
        'mock',
      );
    }
    st.frontmatter = split.frontmatter;
    st.body = split.body;
    const v: SystemVersion = {
      id: nextVersionId++,
      createdAt: new Date().toISOString(),
      changeNote: req.change_note !== undefined && req.change_note !== '' ? req.change_note : null,
      contentHash: fakeHash(),
    };
    st.versions.unshift(v);
    st.currentVersionId = v.id;
    return { version_id: v.id, lint: lintOf(req.content) };
  },

  async rollbackItem(
    kind: 'agents' | 'skills',
    id: number,
    req: SystemRollbackRequest,
  ): Promise<SystemWriteResponse> {
    await delay(160);
    const item = itemsOf(kind).find((i) => i.id === id);
    if (!item) throw new SystemWriteError(404, { error: `${kind} not found` }, 'mock');
    guardWritable(item);
    const st = stateOf(kind, item);
    const target = st.versions.find((v) => v.id === req.version_id);
    if (target === undefined) {
      throw new SystemWriteError(404, { error: 'version not found' }, 'mock');
    }
    if (req.base_hash !== currentHashOf(st)) throwConflict(req.base_hash, item.name);
    // Content-addressed history (system_write.go): restoring byte-identical
    // old content re-points current_version_id at the EXISTING row.
    st.currentVersionId = target.id;
    return { version_id: target.id, lint: [] };
  },

  async createAgent(req: SystemCreateAgentRequest): Promise<SystemCreateResponse> {
    await delay(170);
    if (req.description.includes('demo-readonly')) {
      throw new SystemWriteError(
        403,
        { error: 'system editor is in readonly mode (SWARMERY_SYSTEM_READONLY)' },
        'mock',
      );
    }
    const projectSlug =
      req.scope === 'project'
        ? (mockProjects.find((p) => p.id === req.project_id)?.slug ?? null)
        : null;
    const existing = mockSystemAgents.find(
      (a) => a.name === req.name && a.scope === req.scope && a.projectSlug === projectSlug,
    );
    if (existing !== undefined) {
      if (isDeleted('agents', existing)) {
        throw new SystemWriteError(
          409,
          {
            error: `agent "${req.name}" exists soft-deleted in this scope — restore it instead of creating a new one`,
            restore_id: existing.id,
          },
          'mock',
        );
      }
      throw new SystemWriteError(
        409,
        { error: `agent "${req.name}" already exists in this scope`, id: existing.id },
        'mock',
      );
    }
    const item: SystemItem = {
      id: nextAgentId++,
      name: req.name,
      scope: req.scope,
      projectSlug,
      origin: 'local',
      pluginName: null,
      model: req.model !== undefined && req.model !== '' ? req.model : null,
      description: req.description,
      path:
        req.scope === 'global'
          ? `~/.claude/agents/${req.name}.md`
          : `/Users/user/work/${projectSlug ?? 'project'}/.claude/agents/${req.name}.md`,
      lintMax: null,
      dead: false,
      lastUsed: null,
      tasks30d: 0,
    };
    mockSystemAgents.push(item);
    const v: SystemVersion = {
      id: nextVersionId++,
      createdAt: new Date().toISOString(),
      changeNote: 'created in dashboard',
      contentHash: fakeHash(),
    };
    const fmLines = [`name: ${req.name}`, `description: ${req.description}`];
    if (item.model !== null) fmLines.push(`model: ${item.model}`);
    if (req.tools !== undefined && req.tools.length > 0) fmLines.push(`tools: ${req.tools.join(', ')}`);
    const boundaries =
      req.boundaries !== undefined && req.boundaries.trim() !== ''
        ? req.boundaries
        : '- stay within the task scope';
    itemState.set(`agents:${String(item.id)}`, {
      frontmatter: fmLines.join('\n'),
      body: `# Role\n\n${req.description}\n\n## Boundaries\n\n${boundaries}`,
      versions: [v],
      currentVersionId: v.id,
      deleted: false,
    });
    const lint: SystemLintFinding[] =
      req.boundaries === undefined || req.boundaries.trim() === ''
        ? [{ rule: 'no_boundaries', severity: 'warn', message: 'boundaries left empty — the template stub applies' }]
        : [];
    return { id: item.id, version_id: v.id, lint };
  },

  async deleteAgent(id: number): Promise<{ deleted: boolean }> {
    await delay(140);
    const item = mockSystemAgents.find((a) => a.id === id);
    if (!item) throw new SystemWriteError(404, { error: 'agent not found' }, 'mock');
    guardWritable(item);
    stateOf('agents', item).deleted = true;
    return { deleted: true };
  },

  async restoreAgent(id: number): Promise<SystemRestoreResponse> {
    await delay(140);
    const item = mockSystemAgents.find((a) => a.id === id);
    if (!item) throw new SystemWriteError(404, { error: 'agent not found' }, 'mock');
    const st = stateOf('agents', item);
    if (!st.deleted) {
      throw new SystemWriteError(409, { error: 'agent is not deleted — nothing to restore' }, 'mock');
    }
    st.deleted = false;
    return { id, version_id: st.currentVersionId ?? 0 };
  },

  async toggleHook(id: number, enabled: boolean, baseHash: string): Promise<{ status: string }> {
    await delay(130);
    const hook = guardHookWrite(id, baseHash);
    if (hook.enabled !== enabled) {
      hook.enabled = enabled;
      hook.contentHash = fakeHash(); // nodeHash covers the enabled state
    }
    return { status: 'ok' };
  },

  async updateHook(
    id: number,
    command: string,
    timeout: number | null,
    baseHash: string,
  ): Promise<{ status: string }> {
    await delay(130);
    if (command.includes('demo-readonly')) {
      throw new SystemWriteError(
        403,
        { error: 'system editor is in readonly mode (SWARMERY_SYSTEM_READONLY)' },
        'mock',
      );
    }
    if (command.includes('demo-conflict')) {
      throw new SystemWriteError(
        409,
        {
          error: 'content changed on disk since base_hash',
          disk_hash: fakeHash(),
          base_hash: baseHash,
          diff: '--- your base\n+++ on disk\n- (your command)\n+ (entry changed outside the dashboard)',
        },
        'mock',
      );
    }
    const hook = guardHookWrite(id, baseHash);
    hook.command = command;
    hook.timeout = timeout;
    hook.contentHash = fakeHash();
    return { status: 'ok' };
  },
};

/** Shared hook-write guards: 404, managed=swarmery → 403, hash drift → 409. */
function guardHookWrite(id: number, baseHash: string): SystemHook {
  const hook = mockSystemHooks.find((h) => h.id === id);
  if (!hook) throw new SystemWriteError(404, { error: 'hook not found' }, 'mock');
  if (hook.managed === 'swarmery') {
    throw new SystemWriteError(
      403,
      { error: 'hook is managed by the swarmery installer — manage it via `swarmery hooks`' },
      'mock',
    );
  }
  if (baseHash !== hook.contentHash) {
    throw new SystemWriteError(
      409,
      {
        error: 'content changed on disk since base_hash',
        disk_hash: hook.contentHash ?? '',
        base_hash: baseHash,
        diff: `--- your base\n+++ on disk\n- command: ${hook.command}\n+ (entry changed outside the dashboard)`,
      },
      'mock',
    );
  }
  return hook;
}
