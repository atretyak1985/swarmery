// Offline fixture data for VITE_MOCK=1 — mirrors the shapes the Go daemon
// serves (frozen contract in ../api/types.ts) and the stories in
// testdata/fixtures/*.jsonl (simple, tool-heavy, and subagent sessions).

import type {
  Event,
  FileChange,
  Project,
  Session,
  SessionDetail,
  SessionsResponse,
  StatsToday,
  Turn,
} from '../api/types';

const now = Date.now();
const iso = (msAgo: number): string => new Date(now - msAgo).toISOString();
const MIN = 60_000;

export const mockProjects: Project[] = [
  {
    id: 1,
    path: '/Users/user/work/orders-api',
    slug: 'orders-api',
    name: 'Orders API',
    firstSeen: iso(30 * 24 * 60 * MIN),
    lastActivity: iso(2 * MIN),
    archived: false,
    sessions: 41,
  },
  {
    id: 2,
    path: '/Users/user/work/example-app',
    slug: 'example-app',
    name: 'Example App',
    firstSeen: iso(21 * 24 * 60 * MIN),
    lastActivity: iso(6 * MIN),
    archived: false,
    sessions: 27,
  },
  {
    id: 3,
    path: '/Users/user/work/swarmery',
    slug: 'swarmery',
    name: 'Swarmery',
    firstSeen: iso(9 * 24 * 60 * MIN),
    lastActivity: iso(4 * MIN),
    archived: false,
    sessions: 18,
  },
  {
    id: 4,
    path: '/Users/user/work/docs-site',
    slug: 'docs-site',
    name: null,
    firstSeen: iso(60 * 24 * 60 * MIN),
    lastActivity: iso(26 * 60 * MIN),
    archived: false,
    sessions: 6,
  },
];

export const mockSessions: Session[] = [
  {
    id: 1,
    projectId: 1,
    projectSlug: 'orders-api',
    sessionUuid: 'a3f2b8c1-4d5e-4f60-8a71-b2c3d4e5f601',
    model: 'claude-fable-5',
    gitBranch: 'feat/templates-v2',
    cwd: '/Users/user/work/orders-api',
    status: 'active',
    startedAt: iso(18 * MIN),
    endedAt: null,
    title: 'Migrate email templates to the provider v2 API',
    source: 'jsonl',
  },
  {
    id: 2,
    projectId: 2,
    projectSlug: 'example-app',
    sessionUuid: 'e1f2a3b4-c5d6-4e7f-8091-a2b3c4d5e6f7',
    model: 'claude-fable-5',
    gitBranch: 'main',
    cwd: '/Users/user/work/example-app',
    status: 'active',
    startedAt: iso(6 * MIN),
    endedAt: null,
    title: 'Analyze the agent system and summarize orchestration',
    source: 'jsonl',
  },
  {
    id: 3,
    projectId: 3,
    projectSlug: 'swarmery',
    sessionUuid: '9c11d2e3-f4a5-4b6c-8d7e-90f1a2b3c4d5',
    model: 'claude-fable-5',
    gitBranch: 'feat/swarmery-ingest',
    cwd: '/Users/user/work/swarmery',
    status: 'waiting_approval',
    startedAt: iso(52 * MIN),
    endedAt: null,
    title: 'Agent A: fsnotify watcher + offsets',
    source: 'both',
  },
  {
    id: 4,
    projectId: 1,
    projectSlug: 'orders-api',
    sessionUuid: 'b4c5d6e7-f8a9-4b0c-8d1e-2f3a4b5c6d7e',
    model: 'claude-fable-5',
    gitBranch: 'main',
    cwd: '/Users/user/work/orders-api',
    status: 'idle',
    startedAt: iso(95 * MIN),
    endedAt: null,
    title: 'Investigate flaky pagination test',
    source: 'jsonl',
  },
  {
    id: 5,
    projectId: 1,
    projectSlug: 'orders-api',
    sessionUuid: 'c5d6e7f8-a9b0-4c1d-8e2f-3a4b5c6d7e8f',
    model: 'claude-fable-5',
    gitBranch: 'fix/vendor-pagination',
    cwd: '/Users/user/work/orders-api',
    status: 'completed',
    startedAt: iso(3 * 60 * MIN),
    endedAt: iso(139 * MIN),
    title: 'Fix pagination in the vendor portal',
    source: 'jsonl',
  },
  {
    id: 6,
    projectId: 4,
    projectSlug: 'docs-site',
    sessionUuid: 'd6e7f8a9-b0c1-4d2e-8f3a-4b5c6d7e8f90',
    model: 'claude-fable-5',
    gitBranch: 'main',
    cwd: '/Users/user/work/docs-site',
    status: 'completed',
    startedAt: iso(6 * 60 * MIN),
    endedAt: iso(5 * 60 * MIN),
    title: 'Regenerate API reference pages',
    source: 'jsonl',
  },
  {
    id: 7,
    projectId: 2,
    projectSlug: 'example-app',
    sessionUuid: 'e7f8a9b0-c1d2-4e3f-8a4b-5c6d7e8f9012',
    model: 'claude-fable-5',
    gitBranch: 'chore/deps',
    cwd: '/Users/user/work/example-app',
    status: 'killed',
    startedAt: iso(26 * 60 * MIN),
    endedAt: iso(25 * 60 * MIN),
    title: 'Bulk dependency upgrade',
    source: 'hook',
  },
];

export const mockStatsToday: StatsToday = {
  sessions: 14,
  active: 2,
  tokens_in: 1_240_000,
  tokens_out: 860_000,
  cost_usd: 4.87,
  errors: 3,
};

// --- Session 1 detail: the subagent showcase (mirrors subagent-session.jsonl)

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

// --- Simpler details for the remaining sessions ------------------------------

function simpleDetail(session: Session, events: Event[], turns: Turn[]): SessionDetail {
  return { ...session, turns, events, fileChanges: [] };
}

function promptTurn(id: number, seq: number, ts: string): Turn {
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
  };
}

function buildDetails(): Map<number, SessionDetail> {
  const details = new Map<number, SessionDetail>();
  const s1 = mockSessions[0];
  if (s1) {
    details.set(1, { ...s1, turns: s1Turns, events: s1Events, fileChanges: s1FileChanges });
  }
  let eventId = 500;
  for (const session of mockSessions.slice(1)) {
    const t1 = promptTurn(session.id * 10 + 1, 1, session.startedAt);
    const t2 = promptTurn(session.id * 10 + 2, 2, session.startedAt);
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

  async sessions(filters: MockFilters = {}): Promise<SessionsResponse> {
    await delay(150);
    return mockSessions
      .filter((s) => {
        if (filters.project !== undefined && filters.project !== s.projectSlug) return false;
        if (filters.status !== undefined && filters.status !== s.status) return false;
        return true;
      })
      .map((s) => ({ ...s }));
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
};
