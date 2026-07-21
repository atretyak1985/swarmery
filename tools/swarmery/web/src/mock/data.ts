// Offline fixture data for VITE_MOCK=1 — mirrors the shapes the Go daemon
// serves (frozen contract in ../api/types.ts) and the stories in
// testdata/fixtures/*.jsonl (simple, tool-heavy, and subagent sessions).

import type {
  DocDetail,
  DocMeta,
  Event,
  FileChange,
  HealthResponse,
  PermissionRequest,
  Project,
  ProjectComponents,
  ProjectDetail,
  ProjectHealth,
  Session,
  SessionDetail,
  SessionsResponse,
  StatsOverview,
  StatsSeriesPoint,
  StatsToday,
  TaskDetail,
  TaskSummary,
  Turn,
} from '../api/types';
import type {
  AnalyticsDimension,
  AnalyticsMetric,
  BreakdownResp,
  DurationsResp,
  ErrorsResp,
  MatrixResp,
  RetroAgentsResp,
  RetroFrictionResp,
  SkillsResp,
  TimeseriesResp,
  ToolsResp,
} from '../api/types';
import { addDays, isoDay, parseDay } from '../lib/format';
import { mockApprovalsList, mockResolveApproval } from './approvals';
import {
  mockBreakdown,
  mockDurations,
  mockErrorGroups,
  mockMatrix,
  mockSkillStats,
  mockTimeseries,
  mockToolStats,
} from './analytics';

interface AnalyticsRangeArg {
  from?: string;
  to?: string;
}

const now = Date.now();
const iso = (msAgo: number): string => new Date(now - msAgo).toISOString();
const MIN = 60_000;

const swarmeryMarketplace = 'atretyak1985/swarmery';

export const mockProjects: Project[] = [
  {
    id: 1,
    path: '/Users/user/work/orders-api',
    slug: 'orders-api',
    name: 'Orders API',
    firstSeen: iso(30 * 24 * 60 * MIN),
    lastActivity: iso(2 * MIN),
    archived: false,
    pinned: true,
    tags: ['backend', 'billing'],
    sessions: 41,
    tokens: 4_820_000,
    costUsd: 18.42,
    plugin: { managed: true, packs: ['iot-pack'], marketplace: swarmeryMarketplace, underOnboardRoot: true },
  },
  {
    id: 2,
    path: '/Users/user/work/example-app',
    slug: 'example-app',
    name: 'Example App',
    firstSeen: iso(21 * 24 * 60 * MIN),
    lastActivity: iso(6 * MIN),
    archived: false,
    pinned: false,
    tags: ['frontend'],
    sessions: 27,
    tokens: 2_310_000,
    costUsd: 9.07,
    plugin: { managed: true, packs: [], marketplace: swarmeryMarketplace, underOnboardRoot: true },
  },
  {
    id: 3,
    path: '/Users/user/work/swarmery',
    slug: 'swarmery',
    name: 'Swarmery',
    firstSeen: iso(9 * 24 * 60 * MIN),
    lastActivity: iso(4 * MIN),
    archived: false,
    pinned: false,
    tags: [],
    sessions: 18,
    tokens: 1_540_000,
    costUsd: 6.13,
    plugin: { managed: true, packs: [], marketplace: swarmeryMarketplace, underOnboardRoot: false },
  },
  {
    id: 4,
    path: '/Users/user/work/docs-site',
    slug: 'docs-site',
    name: null,
    firstSeen: iso(60 * 24 * 60 * MIN),
    lastActivity: iso(26 * 60 * MIN),
    archived: false,
    pinned: false,
    tags: ['frontend'],
    sessions: 6,
    tokens: null,
    costUsd: null,
    plugin: null,
  },
];

export const mockSessions: Session[] = [
  {
    id: 1,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'Orders API',
    sessionUuid: 'a3f2b8c1-4d5e-4f60-8a71-b2c3d4e5f601',
    model: 'claude-fable-5',
    gitBranch: 'feat/templates-v2',
    cwd: '/Users/user/work/orders-api',
    status: 'active',
    startedAt: iso(18 * MIN),
    endedAt: null,
    title: 'Migrate email templates to the provider v2 API',
    source: 'jsonl',
    tokens: 412_000,
    costUsd: 0.84,
    taskId: 1,
    taskExternalId: '2026-07-10-email-templates-v2',
    taskLinkSource: 'explicit',
    taskConfidence: null,
  },
  {
    id: 2,
    projectId: 2,
    projectSlug: 'example-app',
    projectName: 'Example App',
    sessionUuid: 'e1f2a3b4-c5d6-4e7f-8091-a2b3c4d5e6f7',
    model: 'claude-fable-5',
    gitBranch: 'main',
    cwd: '/Users/user/work/example-app',
    status: 'active',
    startedAt: iso(6 * MIN),
    endedAt: null,
    title: 'Analyze the agent system and summarize orchestration',
    source: 'jsonl',
    tokens: 141_000,
    costUsd: 0.34,
  },
  {
    id: 3,
    projectId: 3,
    projectSlug: 'swarmery',
    projectName: 'Swarmery',
    sessionUuid: '9c11d2e3-f4a5-4b6c-8d7e-90f1a2b3c4d5',
    model: 'claude-fable-5',
    gitBranch: 'feat/swarmery-ingest',
    cwd: '/Users/user/work/swarmery',
    status: 'waiting_approval',
    startedAt: iso(52 * MIN),
    endedAt: null,
    title: 'Agent A: fsnotify watcher + offsets',
    source: 'both',
    tokens: 96_000,
    costUsd: 0.41,
    taskId: 2,
    taskExternalId: '2026-07-08-swarmery-control-plane',
    taskLinkSource: 'heuristic',
    taskConfidence: 0.92,
  },
  {
    id: 4,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'Orders API',
    sessionUuid: 'b4c5d6e7-f8a9-4b0c-8d1e-2f3a4b5c6d7e',
    model: 'claude-fable-5',
    gitBranch: 'main',
    cwd: '/Users/user/work/orders-api',
    status: 'idle',
    startedAt: iso(95 * MIN),
    endedAt: null,
    title: 'Investigate flaky pagination test',
    source: 'jsonl',
    tokens: 60_000,
    costUsd: 0.11,
  },
  {
    id: 5,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'Orders API',
    sessionUuid: 'c5d6e7f8-a9b0-4c1d-8e2f-3a4b5c6d7e8f',
    model: 'claude-fable-5',
    gitBranch: 'fix/vendor-pagination',
    cwd: '/Users/user/work/orders-api',
    status: 'completed',
    startedAt: iso(3 * 60 * MIN),
    endedAt: iso(139 * MIN),
    title: 'Fix pagination in the vendor portal',
    source: 'jsonl',
    tokens: 220_000,
    costUsd: 0.52,
    taskId: 3,
    taskExternalId: '2026-07-09-vendor-pagination-fix',
    taskLinkSource: 'heuristic',
    taskConfidence: 0.74,
  },
  {
    id: 6,
    projectId: 4,
    projectSlug: 'docs-site',
    projectName: null,
    sessionUuid: 'd6e7f8a9-b0c1-4d2e-8f3a-4b5c6d7e8f90',
    model: 'claude-fable-5',
    gitBranch: 'main',
    cwd: '/Users/user/work/docs-site',
    status: 'completed',
    startedAt: iso(6 * 60 * MIN),
    endedAt: iso(5 * 60 * MIN),
    title: 'Regenerate API reference pages',
    source: 'jsonl',
    tokens: 88_000,
    costUsd: 0.19,
  },
  {
    id: 7,
    projectId: 2,
    projectSlug: 'example-app',
    projectName: 'Example App',
    sessionUuid: 'e7f8a9b0-c1d2-4e3f-8a4b-5c6d7e8f9012',
    model: 'claude-fable-5',
    gitBranch: 'chore/deps',
    cwd: '/Users/user/work/example-app',
    status: 'killed',
    startedAt: iso(26 * 60 * MIN),
    endedAt: iso(25 * 60 * MIN),
    title: 'Bulk dependency upgrade',
    source: 'hook',
    tokens: null,
    costUsd: null,
  },
];

export const mockStatsToday: StatsToday = {
  sessions: 14,
  active: 2,
  tokens_in: 1_240_000,
  tokens_out: 860_000,
  cost_usd: 4.87,
  errors: 3,
  tests_passed: 212,
  tests_failed: 0,
  tests_skipped: 0,
};

// --- /api/health --------------------------------------------------------------

export const mockHealth: HealthResponse = {
  status: 'ok',
  version: '0.3.0',
  db_size_bytes: 18_874_368,
  watching: true,
};

// --- /api/stats/overview?day= --------------------------------------------------
// Deterministic per-day pseudo-values so the day navigator shows believable,
// stable variation; "today" mirrors mockStatsToday.

interface DayFacts {
  sessions: number;
  tokens: number;
  cost: number;
  errors: number;
}

/** offset = days before today (0 = today). Stable across reloads within a day. */
function dayFacts(offset: number): DayFacts {
  if (offset === 0) return { sessions: 14, tokens: 2_100_000, cost: 4.87, errors: 3 };
  const r = (salt: number): number => {
    const x = Math.sin(offset * 12.9898 + salt * 78.233) * 43758.5453;
    return x - Math.floor(x);
  };
  return {
    sessions: 2 + Math.floor(r(1) * 9),
    tokens: 150_000 + Math.floor(r(2) * 900_000),
    cost: Math.round((3 + r(3) * 42) * 100) / 100,
    errors: Math.floor(r(4) * 20),
  };
}

function offsetOf(day: string): number {
  const today = parseDay(isoDay());
  const target = parseDay(day);
  const diff = Math.round((today.getTime() - target.getTime()) / 86_400_000);
  return Math.min(Math.max(diff, 0), 60);
}

export function mockStatsOverview(day: string): StatsOverview {
  const offset = offsetOf(day);
  const requested = addDays(isoDay(), -offset); // normalized day key
  const facts = dayFacts(offset);
  const isToday = offset === 0;

  const series: StatsSeriesPoint[] = [];
  for (let i = 13; i >= 0; i -= 1) {
    const o = offset + i;
    const f = dayFacts(o);
    series.push({
      day: addDays(requested, -i),
      sessions: f.sessions,
      tokens: f.tokens,
      cost_usd: f.cost,
      errors: f.errors,
    });
  }

  // Split errors/cost/sessions across the fixture projects & models, keeping
  // the rail rows consistent with the headline numbers.
  const slugs = mockProjects.map((p) => ({ slug: p.slug, name: p.name }));
  const errorRows: StatsOverview['errors_by_project'] = [];
  let errorsLeft = facts.errors;
  for (const [i, p] of slugs.entries()) {
    if (errorsLeft <= 0) break;
    const take = i === slugs.length - 1 ? errorsLeft : Math.ceil(errorsLeft / 2);
    errorRows.push({ slug: p.slug, name: p.name, errors: take });
    errorsLeft -= take;
  }

  const fableShare = Math.round(facts.cost * 0.77 * 100) / 100;
  const opusShare = Math.round((facts.cost - fableShare) * 100) / 100;
  const costRows: StatsOverview['cost_by_model'] = [{ model: 'claude-fable-5', cost_usd: fableShare }];
  if (opusShare > 0) costRows.push({ model: 'claude-opus-4-8', cost_usd: opusShare });

  const projectRows: StatsOverview['projects'] = [];
  let sessionsLeft = facts.sessions;
  for (const [i, p] of slugs.entries()) {
    if (sessionsLeft <= 0) break;
    const take = i === slugs.length - 1 ? sessionsLeft : Math.ceil(sessionsLeft / 2);
    projectRows.push({ slug: p.slug, name: p.name, sessions: take });
    sessionsLeft -= take;
  }

  return {
    day: requested,
    sessions: facts.sessions,
    active: isToday ? 2 : 0,
    waiting_approval: isToday ? 1 : 0,
    tokens_in: Math.round(facts.tokens * 0.6),
    tokens_out: facts.tokens - Math.round(facts.tokens * 0.6),
    cost_usd: facts.cost,
    errors: facts.errors,
    series,
    errors_by_project: errorRows,
    cost_by_model: costRows,
    projects: projectRows,
    // Test signal only for "today" so past days demo the degraded Quality tile.
    ...(isToday ? { tests_passed: 212, tests_failed: 0, tests_skipped: 0 } : {}),
  };
}

// --- /api/docs -----------------------------------------------------------------
// Trimmed versions of the real swarmery/docs/*.md so the Docs screen demos
// headings, lists, a pipe table, and a code fence offline.

const onboardingMd = `# Onboarding a project onto swarmery

## The one-command way

From the new project's root:

\`\`\`bash
bash <swarmery-repo>/scripts/init.sh <project-slug> [pack ...]
# e.g.  bash /path/to/swarmery/scripts/init.sh my-shop web-pack
\`\`\`

It scaffolds \`.claude/settings.json\` (marketplace + core + chosen packs + env + safety denies), a \`.claude/project.json\` skeleton (fill the TODOs), and the workspace namespace. Then start a fresh session and accept the trust prompt. Idempotent — existing files are never overwritten.

**Packs:** \`uav-pack\` (drones/telemetry) · \`iot-pack\` (devices/BLE) · \`web-pack\` (SEO/i18n/CRO) · \`infra-pack\` (k8s/Helm/GitOps).

## The payoff test (prove porting is dead)

1. Bump \`plugins/core\` minor version; push.
2. In each consumer: \`/plugin update\`.
3. Confirm the change lands in every project with **zero per-project file copying**.

This is the whole reason swarmery exists — verify it explicitly once ≥2 consumers are live.`;

const extendingMd = `# Extending swarmery: where project-specific things live

swarmery is **one global system + a thin native overlay per project** — never a separate "sub-agent-system" inside a project. On a name collision the project-local component wins (native base + overlay).

## Decision tree for every new skill / agent / command / template / script

| The thing is… | It goes to… |
|---|---|
| useful to any project | \`plugins/core\` (bump semver → consumers adopt via \`/plugin update\`) |
| useful to ≥2 projects of one domain | the domain pack (\`uav-pack\` / \`iot-pack\` / \`web-pack\`) |
| unique to one project | **the project's own** \`.claude/{agents,skills,commands,templates}/\` |
| configuration, not logic | the project's \`.claude/project.json\` |

## The graduation rule (flow goes UP only)

New things are born **project-local**. When a **second** project needs the same thing, promote it to a pack; when every project needs it, promote to core. Never copy downward — copying framework files into projects recreates the fork-and-sync rot this repo exists to eliminate.

Promotion checklist:

1. De-flavor it (see \`docs/NEUTRALITY.md\`) — values move to \`project.json\` reads.
2. Move the file into the pack/core; bump that plugin's semver.
3. Delete the project-local copy in the consumer that donated it.`;

const neutralityMd = `# Vendor neutrality policy

\`plugins/**\` must contain **zero** project-specific tokens — no company/product names, no internal repo names, no environment aliases, no cloud regions. Per-project flavor lives in each consumer's \`.claude/project.json\` (schema: \`overlays/_schema/project.schema.json\`; sample: \`overlays/example/\`) and is read at **runtime** by core agents, skills, and hooks.

## Rules

- **Brand tokens** (project/company identity, internal infra names) — forbidden everywhere in \`plugins/**\`.
- **Domain vocabulary** (e.g. drones for \`uav-pack\`, wearables for \`iot-pack\`) — legitimate *inside its own domain pack*, forbidden in \`core\`.
- Scripts/hooks read \`\${CLAUDE_PROJECT_DIR}/.claude/project.json\`; never default to a hard-coded path.
- Prose examples use neutral placeholders (\`apps/<mainApp>\`, \`<device>\`, \`<envAlias>\`) or neutral example domains (\`orders/line-items\`, \`pipelines/job_runs\`).
- Frontmatter identity is \`swarmery-core\`.

## Checking

\`scripts/scan-flavor.sh\` greps \`plugins/**\` for your token patterns:

\`\`\`bash
# Put your (private) token regexes next to the repo or in the env:
echo 'mycompany|my-app|my-env-alias' > .flavor-tokens          # brand family (gitignored)
echo 'my-domain-noun' > .flavor-tokens-domain                  # domain family (gitignored)
bash scripts/scan-flavor.sh                                    # target: 0 occurrences
\`\`\`

Without those files the script falls back to a small example pattern — replace it with your own. Consumers should run this as a CI ratchet: the count must never increase.`;

export const mockDocs: DocDetail[] = [
  { slug: 'onboarding', title: 'Onboarding', file: 'ONBOARDING.md', markdown: onboardingMd },
  { slug: 'extending', title: 'Extending', file: 'EXTENDING.md', markdown: extendingMd },
  { slug: 'neutrality', title: 'Neutrality', file: 'NEUTRALITY.md', markdown: neutralityMd },
];

// --- Session 1 detail: the subagent showcase (mirrors subagent-session.jsonl)

const s1AssistantText = `I'll port both templates to the provider v2 API. Here's the plan:

## What changed in v2

- **Single template document** per template — locales render from one doc instead of a part per locale
- \`CreateTemplate\` is replaced by \`CreateEmailTemplate\` with a typed request
- Old v1 template names remain valid as *aliases* until the next release

I delegated the port to the **backend-tests** agent. The first test run failed:

\`\`\`
FAIL: TestRenderOrderConfirmation — missing field .VendorName (exit 1)
\`\`\`

The v2 request schema requires \`VendorName\` on order events; the fixture predates the field. After adding it to \`providerv2.go\` the suite is green:

1. \`internal/mail/providerv2.go\` — new v2 client wrapper (+70 −1)
2. \`internal/mail/templates.go\` — locale loop replaced with one template doc (+18 −41)
3. \`internal/mail/render_test.go\` — vendor-name assertion (+12 −3)

Backwards compatibility is preserved: old event payloads without \`VendorName\` fall back to the account display name, so nothing breaks for queued v1 events.

Ready for the full suite whenever you are.`;

const s1Turns: Turn[] = [
  {
    id: 11,
    seq: 1,
    role: 'user',
    messageId: null,
    model: null,
    startedAt: iso(18 * MIN),
    endedAt: iso(18 * MIN),
    tokensIn: null,
    tokensOut: null,
    tokensCacheRead: null,
    tokensCacheWrite: null,
    costUsd: null,
    text: 'Port the order-confirmation and vendor-notify templates to the provider v2 API. Keep backwards compatibility with old event payloads.',
  },
  {
    id: 12,
    seq: 2,
    role: 'assistant',
    model: 'claude-fable-5',
    messageId: 'msg_01AAAAAAAAAAAAAAAAAAAA0001',
    startedAt: iso(18 * MIN - 4000),
    endedAt: iso(9 * MIN),
    tokensIn: 284_000,
    tokensOut: 41_000,
    tokensCacheRead: 220_000,
    tokensCacheWrite: 18_000,
    costUsd: 0.61,
    text: s1AssistantText,
  },
  {
    id: 13,
    seq: 3,
    role: 'user',
    messageId: null,
    model: null,
    startedAt: iso(8 * MIN),
    endedAt: iso(8 * MIN),
    tokensIn: null,
    tokensOut: null,
    tokensCacheRead: null,
    tokensCacheWrite: null,
    costUsd: null,
    text: 'Looks good — run the full suite and commit.',
  },
  {
    id: 14,
    seq: 4,
    role: 'assistant',
    model: 'claude-fable-5',
    messageId: 'msg_01AAAAAAAAAAAAAAAAAAAA0002',
    startedAt: iso(8 * MIN - 3000),
    endedAt: null,
    tokensIn: 74_000,
    tokensOut: 13_000,
    tokensCacheRead: 61_000,
    tokensCacheWrite: 4_000,
    costUsd: 0.23,
    // No prose yet (running turn) — the Chat tab shows only the tool one-liner.
    text: null,
  },
];

const s1Events: Event[] = [
  {
    id: 100,
    turnId: 11,
    ts: iso(18 * MIN),
    type: 'user_prompt',
    toolName: null,
    parentEventId: null,
    status: null,
    durationMs: null,
    payload: {
      text: 'Port the order-confirmation and vendor-notify templates to the provider v2 API. Keep backwards compatibility with old event payloads.',
    },
  },
  {
    id: 101,
    turnId: 12,
    ts: iso(17.6 * MIN),
    type: 'tool_call',
    toolName: 'Read',
    parentEventId: null,
    status: 'ok',
    durationMs: 300,
    payload: { file_path: 'internal/mail/templates.go' },
  },
  {
    id: 102,
    turnId: 12,
    ts: iso(17.2 * MIN),
    type: 'tool_call',
    toolName: 'Grep',
    parentEventId: null,
    status: 'ok',
    durationMs: 200,
    payload: { pattern: '"CreateTemplate" — 7 matches in 3 files' },
  },
  {
    id: 103,
    turnId: 12,
    ts: iso(17 * MIN),
    type: 'subagent_start',
    toolName: 'Agent',
    parentEventId: null,
    status: 'ok',
    durationMs: null,
    payload: {
      subagent_type: 'backend-tests',
      description: 'Port template rendering to provider v2 and make the mail tests pass',
    },
  },
  {
    id: 104,
    turnId: 12,
    ts: iso(16.6 * MIN),
    type: 'tool_call',
    toolName: 'Edit',
    parentEventId: 103,
    status: 'ok',
    durationMs: 1100,
    payload: { file_path: 'internal/mail/providerv2.go · +64 −0' },
  },
  {
    id: 105,
    turnId: 12,
    ts: iso(15.4 * MIN),
    type: 'tool_call',
    toolName: 'Edit',
    parentEventId: 103,
    status: 'ok',
    durationMs: 900,
    payload: { file_path: 'internal/mail/templates.go · +18 −41' },
  },
  {
    id: 106,
    turnId: 12,
    ts: iso(14.8 * MIN),
    type: 'tool_call',
    toolName: 'Bash',
    parentEventId: 103,
    status: 'error',
    durationMs: 8400,
    payload: {
      command: 'go test ./internal/mail/...',
      error: 'FAIL: TestRenderOrderConfirmation — missing field .VendorName (exit 1)',
    },
  },
  {
    id: 107,
    turnId: 12,
    ts: iso(13.9 * MIN),
    type: 'tool_call',
    toolName: 'Edit',
    parentEventId: 103,
    status: 'ok',
    durationMs: 700,
    payload: { file_path: 'internal/mail/providerv2.go · +6 −1' },
  },
  {
    id: 108,
    turnId: 12,
    ts: iso(13.2 * MIN),
    type: 'tool_call',
    toolName: 'Bash',
    parentEventId: 103,
    status: 'ok',
    durationMs: 7900,
    payload: { command: 'go test ./internal/mail/... — PASS (14 tests)' },
  },
  {
    id: 109,
    turnId: 12,
    ts: iso(12.8 * MIN),
    type: 'subagent_stop',
    toolName: 'Agent',
    parentEventId: 103,
    status: 'ok',
    durationMs: 252_000,
    payload: { agentType: 'backend-tests', tokens: 96_000, result: 'mail tests green' },
  },
  {
    id: 110,
    turnId: 12,
    ts: iso(12 * MIN),
    type: 'skill_use',
    toolName: 'Skill',
    parentEventId: null,
    status: 'ok',
    durationMs: 31_000,
    payload: {
      input: { skill: 'code-review-checklist' },
      result: { commandName: 'code-review-checklist', success: true },
    },
  },
  {
    id: 111,
    turnId: 12,
    ts: iso(10 * MIN),
    type: 'file_change',
    toolName: 'Edit',
    parentEventId: null,
    status: 'ok',
    durationMs: 600,
    payload: { file_path: 'internal/mail/render_test.go · +12 −3' },
  },
  {
    id: 112,
    turnId: 13,
    ts: iso(8 * MIN),
    type: 'user_prompt',
    toolName: null,
    parentEventId: null,
    status: null,
    durationMs: null,
    payload: { text: 'Looks good — run the full suite and commit.' },
  },
  {
    id: 113,
    turnId: 14,
    ts: iso(7.5 * MIN),
    type: 'test_run',
    toolName: 'Bash',
    parentEventId: null,
    status: 'ok',
    durationMs: 41_000,
    payload: { command: 'go test ./... — PASS (212 tests)' },
  },
  {
    id: 114,
    turnId: 14,
    ts: iso(6 * MIN),
    type: 'commit',
    toolName: 'Bash',
    parentEventId: null,
    status: 'ok',
    durationMs: 900,
    payload: { message: 'feat(mail): port templates to provider v2 API' },
  },
  {
    id: 115,
    turnId: 14,
    ts: iso(2 * MIN),
    type: 'permission_request',
    toolName: 'Bash',
    parentEventId: null,
    status: null,
    durationMs: null,
    payload: { command: 'cloud mail delete-template --name order-v1 — awaiting decision' },
  },
];

const s1FileChanges: FileChange[] = [
  {
    id: 1,
    eventId: 104,
    filePath: 'internal/mail/providerv2.go',
    changeType: 'create',
    additions: 70,
    deletions: 1,
    diff: `@@ -12,7 +12,11 @@ func (c *Client) Render(
 	ctx := req.Context()
-	out, err := c.legacy.CreateTemplate(ctx, in)
+	out, err := c.v2.CreateEmailTemplate(ctx, &v2in)
+	if err != nil {
+		return fmt.Errorf("provider v2 template: %w", err)
+	}
 	return c.render(out)`,
    outOfScope: false,
  },
  {
    id: 2,
    eventId: 105,
    filePath: 'internal/mail/templates.go',
    changeType: 'edit',
    additions: 18,
    deletions: 41,
    diff: `@@ -3,9 +3,8 @@ package mail
 import (
-	legacy "provider/sdk/mail"
-	"provider/sdk/mail/types"
+	v2 "provider/sdk/mailv2"
 )
@@ -44,12 +43,6 @@ func loadTemplates() error {
-	// v1 templates required a separate part per locale;
-	// v2 renders locales from one template document.
-	for _, loc := range locales {
-		parts = append(parts, renderPart(loc))
-	}
+	doc := buildTemplateDoc(locales)`,
    outOfScope: false,
  },
  {
    id: 3,
    eventId: 111,
    filePath: 'internal/mail/render_test.go',
    changeType: 'edit',
    additions: 12,
    deletions: 3,
    diff: `@@ -18,6 +18,15 @@ func TestRenderOrderConfirmation(t *testing.T) {
 	ev := fixtureOrderEvent()
+	ev.VendorName = "ACME Corp"
+
+	out, err := c.Render(ctx, "order-confirmation", ev)
+	if err != nil {
+		t.Fatalf("render: %v", err)
+	}
+	if !strings.Contains(out.HTML, ev.VendorName) {
+		t.Errorf("vendor name missing from body")
+	}`,
    outOfScope: false,
  },
  {
    id: 4,
    eventId: 111,
    filePath: 'docs/email-templates.md',
    changeType: 'edit',
    additions: 4,
    deletions: 0,
    diff: `@@ -1,3 +1,7 @@
 # Email templates
+
+Templates are managed through the provider v2 API as of feat/templates-v2.
+Old v1 template names remain as aliases until the next release.`,
    outOfScope: true,
  },
];

// --- Session 2 detail: many agents of one type --------------------------------
// Exercises the aggregated SummaryChips path (>4 agents → "general-purpose ×5"
// with the task descriptions in the native tooltip) and description-first
// subagent headers in the timeline.

const s2Turns: Turn[] = [
  {
    id: 21,
    seq: 1,
    role: 'user',
    messageId: null,
    model: null,
    startedAt: iso(6 * MIN),
    endedAt: iso(6 * MIN),
    tokensIn: null,
    tokensOut: null,
    tokensCacheRead: null,
    tokensCacheWrite: null,
    costUsd: null,
    text: 'Analyze the agent system and summarize orchestration.',
  },
  {
    id: 22,
    seq: 2,
    role: 'assistant',
    model: 'claude-fable-5',
    messageId: 'msg_01AAAAAAAAAAAAAAAAAAAA0003',
    startedAt: iso(6 * MIN - 3000),
    endedAt: null,
    tokensIn: 122_000,
    tokensOut: 19_000,
    tokensCacheRead: 98_000,
    tokensCacheWrite: 6_000,
    costUsd: 0.34,
    text: 'I fanned the analysis out to five *general-purpose* agents, one per subsystem. Early signal: orchestration is **queue-driven** — the planner emits tasks, workers claim them via `claimNextTask()`, and the reviewer gates every merge.',
  },
];

const S2_AGENT_TASKS: readonly string[] = [
  'Agent A: live ingest pipeline',
  'Agent B: session list + filters',
  'Agent C: timeline rendering',
  'Gate 09 checks for branches B and C',
  'Docs sweep: orchestration summary',
];

const s2Events: Event[] = (() => {
  const events: Event[] = [
    {
      id: 200,
      turnId: 21,
      ts: iso(6 * MIN),
      type: 'user_prompt',
      toolName: null,
      parentEventId: null,
      status: null,
      durationMs: null,
      payload: { text: 'Analyze the agent system and summarize orchestration.' },
    },
  ];
  let id = 200;
  S2_AGENT_TASKS.forEach((description, i) => {
    const startId = (id += 1);
    const startedAgo = (5.6 - i * 0.9) * MIN;
    events.push(
      {
        id: startId,
        turnId: 22,
        ts: iso(startedAgo),
        type: 'subagent_start',
        toolName: 'Agent',
        parentEventId: null,
        status: 'ok',
        durationMs: null,
        // Real daemon payload keys: description, prompt, subagent_type, tool_use_id.
        payload: {
          description,
          prompt: `${description} — full task brief`,
          subagent_type: 'general-purpose',
          tool_use_id: `toolu_mock_${String(startId)}`,
        },
      },
      {
        id: (id += 1),
        turnId: 22,
        ts: iso(startedAgo - 0.2 * MIN),
        type: 'tool_call',
        toolName: 'Read',
        parentEventId: startId,
        status: 'ok',
        durationMs: 350,
        payload: { file_path: `internal/orchestrator/part-${String(i + 1)}.go` },
      },
      {
        id: (id += 1),
        turnId: 22,
        ts: iso(startedAgo - 0.7 * MIN),
        type: 'subagent_stop',
        toolName: 'Agent',
        parentEventId: startId,
        status: 'ok',
        durationMs: 42_000,
        payload: { agentType: 'general-purpose', result: 'done' },
      },
    );
  });
  return events;
})();

// --- Simpler details for the remaining sessions ------------------------------

/** Mirror of the backend heuristic: errors a later same-tool success cleared. */
function recoveredOf(events: Event[]): number {
  let n = 0;
  events.forEach((e, i) => {
    if (e.status !== 'error' || e.toolName === null) return;
    if (events.slice(i + 1).some((o) => o.toolName === e.toolName && o.status === 'ok')) n += 1;
  });
  return n;
}

function simpleDetail(session: Session, events: Event[], turns: Turn[]): SessionDetail {
  return { ...session, turns, events, fileChanges: [], recovered: recoveredOf(events) };
}

function promptTurn(id: number, seq: number, ts: string, text: string | null): Turn {
  return {
    id,
    seq,
    role: seq % 2 === 1 ? 'user' : 'assistant',
    messageId: seq % 2 === 0 ? `msg_mock_${id}` : null,
    model: seq % 2 === 0 ? 'claude-fable-5' : null,
    startedAt: ts,
    endedAt: null,
    tokensIn: seq % 2 === 0 ? 52_000 : null,
    tokensOut: seq % 2 === 0 ? 8_000 : null,
    tokensCacheRead: null,
    tokensCacheWrite: null,
    costUsd: seq % 2 === 0 ? 0.11 : null,
    text,
  };
}

function buildDetails(): Map<number, SessionDetail> {
  const details = new Map<number, SessionDetail>();
  const s1 = mockSessions[0];
  if (s1) {
    details.set(1, {
      ...s1,
      turns: s1Turns,
      events: s1Events,
      fileChanges: s1FileChanges,
      recovered: recoveredOf(s1Events),
    });
  }
  const s2 = mockSessions[1];
  if (s2) {
    details.set(2, {
      ...s2,
      turns: s2Turns,
      events: s2Events,
      fileChanges: [],
      recovered: recoveredOf(s2Events),
    });
  }
  let eventId = 500;
  for (const session of mockSessions.slice(2)) {
    const t1 = promptTurn(session.id * 10 + 1, 1, session.startedAt, session.title);
    const t2 = promptTurn(
      session.id * 10 + 2,
      2,
      session.startedAt,
      session.status === 'killed'
        ? null
        : `Starting from \`README.md\` and a quick \`git status\` — I'll report back once I've mapped the task.`,
    );
    const events: Event[] = [
      {
        id: (eventId += 1),
        turnId: t1.id,
        ts: session.startedAt,
        type: 'user_prompt',
        toolName: null,
        parentEventId: null,
        status: null,
        durationMs: null,
        payload: { text: session.title ?? 'untitled prompt' },
      },
      {
        id: (eventId += 1),
        turnId: t2.id,
        ts: session.startedAt,
        type: 'tool_call',
        toolName: 'Read',
        parentEventId: null,
        status: 'ok',
        durationMs: 400,
        payload: { file_path: 'README.md' },
      },
      {
        id: (eventId += 1),
        turnId: t2.id,
        ts: session.startedAt,
        type: 'tool_call',
        toolName: 'Bash',
        parentEventId: null,
        status: session.status === 'killed' ? 'error' : 'ok',
        durationMs: 2400,
        payload:
          session.status === 'killed'
            ? { command: 'npm run build', error: 'session killed by operator during build' }
            : { command: 'git status --short' },
      },
    ];
    details.set(session.id, simpleDetail(session, events, [t1, t2]));
  }
  return details;
}

export const mockDetails: Map<number, SessionDetail> = buildDetails();

// --- Phase 3.5: workspaces — tasks (14-day slice + detail) ---------------------

export const mockTasks: TaskSummary[] = [
  {
    id: 1,
    externalId: '2026-07-10-email-templates-v2',
    workspaceSlug: 'orders-api',
    projectSlug: 'orders-api',
    projectName: 'Orders API',
    title: 'Migrate email templates to the provider v2 API',
    status: 'running',
    outcome: 'active',
    startedAt: iso(3 * 24 * 60 * MIN),
    archivedAt: null,
    sessions: 3,
    costUsd: 2.41,
  },
  {
    id: 2,
    externalId: '2026-07-08-swarmery-control-plane',
    workspaceSlug: 'swarmery',
    projectSlug: 'swarmery',
    projectName: 'Swarmery',
    title: 'swarmery control plane MVP',
    status: 'done',
    outcome: 'archived',
    startedAt: iso(5 * 24 * 60 * MIN),
    archivedAt: iso(1 * 24 * 60 * MIN),
    sessions: 5,
    costUsd: 11.06,
  },
  {
    id: 3,
    externalId: '2026-07-09-vendor-pagination-fix',
    workspaceSlug: 'orders-api',
    projectSlug: 'orders-api',
    projectName: 'Orders API',
    title: 'Fix pagination in the vendor portal',
    status: 'done',
    outcome: 'done',
    startedAt: iso(4 * 24 * 60 * MIN),
    archivedAt: null,
    sessions: 1,
    costUsd: 0.52,
  },
  {
    id: 4,
    externalId: '2026-07-11-agent-system-research',
    workspaceSlug: 'example-app',
    projectSlug: 'example-app',
    projectName: 'Example App',
    title: 'agent system research',
    status: 'running',
    outcome: 'active',
    startedAt: iso(2 * 24 * 60 * MIN),
    archivedAt: null,
    sessions: 0,
    costUsd: null,
  },
];

function mockTaskDetail(id: number | string): TaskDetail {
  const summary = mockTasks.find((t) => t.id === id || t.externalId === id);
  if (!summary) throw new Error(`mock: task ${String(id)} not found`);
  const links = mockSessions
    .filter((s) => s.taskId === summary.id)
    .map((s) => ({
      sessionId: s.id,
      sessionUuid: s.sessionUuid,
      title: s.title,
      startedAt: s.startedAt,
      endedAt: s.endedAt,
      linkSource: s.taskLinkSource ?? 'heuristic',
      confidence: s.taskConfidence ?? null,
      costUsd: s.costUsd ?? null,
    }));
  return { ...summary, goal: 'mock goal line from the README card', sessionLinks: links };
}

// --- Mock API ----------------------------------------------------------------

const delay = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

export interface MockFilters {
  project?: string;
  status?: string;
}

export const mockApi = {
  async projects(): Promise<Project[]> {
    await delay(120);
    return mockProjects.map((p) => ({ ...p }));
  },

  async project(id: number | string): Promise<ProjectDetail> {
    await delay(140);
    const numeric = typeof id === 'number' ? id : Number.parseInt(id, 10);
    const found = mockProjects.find((p) => p.id === numeric);
    if (!found) throw new Error(`mock: project ${String(id)} not found`);
    const managed = found.plugin?.managed ?? false;
    const components: ProjectComponents = {
      agents: managed ? [{ name: 'tech-lead', source: 'local' }] : [],
      skills: managed ? [{ name: 'browser-verification', source: 'local' }] : [],
      commands: [],
      hooks: managed ? [{ name: 'session-start.sh', source: 'local' }] : [],
      counts: { agents: managed ? 1 : 0, skills: managed ? 1 : 0, commands: 0, hooks: managed ? 1 : 0 },
    };
    return {
      project: { ...found },
      components,
      stats: {
        sessions: found.sessions,
        tokens: found.tokens,
        costUsd: found.costUsd,
        firstSeen: found.firstSeen,
        lastActivity: found.lastActivity,
        recentSessions: mockSessions
          .filter((s) => s.projectSlug === found.slug)
          .slice(0, 10)
          .map((s) => ({
            id: s.id,
            sessionUuid: s.sessionUuid,
            title: s.title,
            status: s.status,
            startedAt: s.startedAt,
            model: s.model,
            tokens: s.tokens ?? null,
            costUsd: s.costUsd ?? null,
          })),
      },
    };
  },

  async projectsHealth(): Promise<ProjectHealth[]> {
    await delay(130);
    return mockProjects
      .filter((p) => !p.archived)
      .map((p, i) => ({
        id: p.id,
        slug: p.slug,
        name: p.name,
        pinned: p.pinned,
        tags: p.tags,
        costWeekUsd: p.costUsd !== null ? Number((p.costUsd / 4).toFixed(2)) : null,
        costPrevWeekUsd: p.costUsd !== null ? Number((p.costUsd / 5).toFixed(2)) : null,
        errorRate: i % 2 === 0 ? 0.042 : null,
        avgSessionMs: 22 * 60_000 + i * 5 * 60_000,
        lastActivity: p.lastActivity,
      }));
  },

  async sessions(filters: MockFilters = {}): Promise<SessionsResponse> {
    await delay(150);
    const sessions = mockSessions
      .filter((s) => {
        if (filters.project !== undefined && filters.project !== s.projectSlug) return false;
        if (filters.status !== undefined && filters.status !== s.status) return false;
        return true;
      })
      .map((s) => ({ ...s }));
    return { sessions, nextCursor: null };
  },

  async session(id: number | string): Promise<SessionDetail> {
    await delay(180);
    const numeric = typeof id === 'number' ? id : Number.parseInt(id, 10);
    const found = Number.isNaN(numeric)
      ? [...mockDetails.values()].find((d) => d.sessionUuid === id)
      : mockDetails.get(numeric);
    if (!found) throw new Error(`mock: session ${String(id)} not found`);
    return { ...found };
  },

  async statsToday(): Promise<StatsToday> {
    await delay(100);
    return { ...mockStatsToday };
  },

  async statsOverview(day: string): Promise<StatsOverview> {
    await delay(120);
    return mockStatsOverview(day);
  },

  async health(): Promise<HealthResponse> {
    await delay(60);
    return { ...mockHealth };
  },

  // analytics
  async timeseries(
    metric: AnalyticsMetric,
    group: AnalyticsDimension,
    range: AnalyticsRangeArg = {},
  ): Promise<TimeseriesResp> {
    await delay(140);
    return mockTimeseries(metric, group, range);
  },

  async breakdown(by: AnalyticsDimension, range: AnalyticsRangeArg = {}): Promise<BreakdownResp> {
    await delay(120);
    return mockBreakdown(by, range);
  },

  async matrix(
    rows: 'agent' | 'skill',
    metric: 'runs' | 'cost' = 'runs',
    range: AnalyticsRangeArg = {},
  ): Promise<MatrixResp> {
    await delay(120);
    return mockMatrix(rows, metric, range);
  },

  async toolStats(range: AnalyticsRangeArg = {}, agent?: string): Promise<ToolsResp> {
    await delay(120);
    return mockToolStats(range, agent);
  },

  async skillStats(range: AnalyticsRangeArg = {}, agent?: string): Promise<SkillsResp> {
    await delay(120);
    return mockSkillStats(range, agent);
  },

  async durations(range: AnalyticsRangeArg = {}): Promise<DurationsResp> {
    await delay(100);
    return mockDurations(range);
  },

  async errorGroups(range: AnalyticsRangeArg = {}): Promise<ErrorsResp> {
    await delay(110);
    return mockErrorGroups(range);
  },

  // retro loop — empty shells (no retro fixtures yet)
  async retroAgents(): Promise<RetroAgentsResp> {
    await delay(120);
    return {
      from: '',
      to: '',
      approx: false,
      main: { cost_usd: 0, tokens_out: 0, errors: 0 },
      agents: [],
    };
  },

  async retroFriction(): Promise<RetroFrictionResp> {
    await delay(120);
    return {
      denied_tools: [],
      error_groups: [],
      approvals: { resolved: 0, avg_resolve_sec: null, wait_total_min: 0, pending: 0 },
      approx: false,
    };
  },

  async docs(): Promise<DocMeta[]> {
    await delay(90);
    return mockDocs.map(({ slug, title, file }) => ({ slug, title, file }));
  },

  async doc(slug: string): Promise<DocDetail> {
    await delay(110);
    const found = mockDocs.find((d) => d.slug === slug);
    if (!found) throw new Error(`mock: doc ${slug} not found`);
    return { ...found };
  },

  // phase 3.5: workspaces
  async tasks(): Promise<TaskSummary[]> {
    await delay(120);
    return mockTasks.map((t) => ({ ...t }));
  },

  async task(id: number | string): Promise<TaskDetail> {
    await delay(140);
    const numeric = typeof id === 'number' ? id : Number.parseInt(id, 10);
    return mockTaskDetail(Number.isNaN(numeric) ? id : numeric);
  },

  // --- phase 2 — approvals (mutable store in ./approvals.ts) ---

  async approvals(status?: string): Promise<PermissionRequest[]> {
    await delay(110);
    return mockApprovalsList(status);
  },

  async resolveApproval(
    id: number,
    action: 'approve' | 'deny' | 'answer' | 'terminal',
    reason?: string,
    answers?: Record<string, string | string[]>,
  ): Promise<PermissionRequest> {
    await delay(140);
    return mockResolveApproval(id, action, reason, answers);
  },
};
