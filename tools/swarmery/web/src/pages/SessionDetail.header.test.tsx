// @vitest-environment jsdom
//
// The open session's header (artboards 2b–2d), rendered through the pane that
// /sessions/:id shows (pages/detail/SessionPane.tsx). The claims:
//
//   1. The state is one sentence, for every state, and no state chip stands in the
//      header (no link-mod chips, no cache-cold chip, no status key/value).
//   2. The numbers strip keeps a fixed order (tokens · cost · context · plan ·
//      errors · recovered · cold restarts), leaves a zero column out, and turns a
//      column past its threshold red, label included.
//   3. Waiting on a permission, allow and deny go through resolveApproval, and the
//      countdown to the request's expiry is in view.
//   4. `⋯` is reachable from the keyboard, closes on Escape, and rename works from it.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../api';
import type { NeedsYouItem, PermissionRequest, SessionDetail } from '../api/types';
import { SwarmConflictProvider } from '../lib/swarm';
import { cleanup, fireEvent, render, screen, waitFor, within } from '../test/render';
import { SessionPane } from './detail/SessionPane';

vi.mock('../api', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  fetchSession: vi.fn(),
  fetchApprovals: vi.fn(async () => []),
  resolveApproval: vi.fn(async () => undefined),
  fetchSwarmClaims: vi.fn(async () => ({ claims: [], conflicts: [] })),
}));
vi.mock('../api/decisions', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  fetchSessionLabels: vi.fn(async () => null),
}));
vi.mock('../api/lessons', () => ({ fetchLessons: vi.fn(async () => []) }));
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));
vi.mock('../lib/scope', () => ({
  useScope: () => ({ scope: null, scopeName: null, scopeProject: null, projects: [], setScope: () => undefined }),
}));

const ago = (minutes: number): string => new Date(Date.now() - minutes * 60_000).toISOString();

function turn(tokensIn: number, costUsd: number | null): SessionDetail['turns'][number] {
  return {
    id: tokensIn,
    seq: 1,
    role: 'assistant',
    tokensIn,
    tokensOut: 0,
    costUsd,
    text: null,
  } as unknown as SessionDetail['turns'][number];
}

function makeDetail(over: Partial<SessionDetail> = {}): SessionDetail {
  return {
    id: 7,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'orders-api',
    sessionUuid: 'uuid-7',
    model: 'claude-opus-5-5',
    modelLast: 'claude-opus-5-5',
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'feat/x',
    cwd: '/w',
    status: 'active',
    startedAt: ago(30),
    endedAt: ago(1),
    title: 'session 7',
    source: 'jsonl',
    turns: [turn(12_000, 0.42)],
    events: [],
    fileChanges: [],
    recovered: 0,
    ...over,
  };
}

const GATE: NeedsYouItem = {
  kind: 'approval',
  sessionId: 7,
  sessionUuid: 'uuid-7',
  sessionName: 'session 7',
  projectSlug: 'orders-api',
  requestId: 11,
  toolName: 'Bash',
  preview: 'npm test',
  question: '',
  asksQuestion: false,
  blockingSince: ago(3),
  blockingSeconds: 180,
  termFocusUrl: null,
  suggestion: null,
};

function request(expiresInSec: number): PermissionRequest {
  return {
    id: 11,
    sessionId: 7,
    toolName: 'Bash',
    requestJson: '{}',
    status: 'pending',
    requestedAt: ago(3),
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt: new Date(Date.now() + expiresInSec * 1000).toISOString(),
  };
}

function renderPane(detail: SessionDetail, opts: { gate?: NeedsYouItem; now?: string | null } = {}): void {
  vi.mocked(api.fetchSession).mockResolvedValue(detail);
  render(
    <MemoryRouter>
      <SwarmConflictProvider>
        <SessionPane
          id={String(detail.id)}
          listHref="/sessions"
          gate={opts.gate}
          onGateChanged={() => undefined}
          actions={{ current: {} }}
          now={opts.now ?? null}
        />
      </SwarmConflictProvider>
    </MemoryRouter>,
  );
}

const sentence = async (): Promise<string> => (await screen.findByTestId('state-sentence')).textContent ?? '';

beforeEach(() => {
  vi.clearAllMocks();
  Element.prototype.scrollIntoView = () => undefined;
});
afterEach(cleanup);

describe('the state sentence', () => {
  it('working: the last action, how long ago, and what it was', async () => {
    renderPane(makeDetail(), { now: 'editing src/orders.ts' });
    const text = await sentence();
    expect(text).toMatch(/^Working · last action \d+[smh] ago — editing src\/orders\.ts\.$/);
  });

  it('waiting on a permission: how long, the tool and its command, the countdown', async () => {
    vi.mocked(api.fetchApprovals).mockResolvedValue([request(252)]);
    renderPane(makeDetail(), { gate: GATE });
    await waitFor(async () => expect(await sentence()).toContain('Expires in'));
    expect(await sentence()).toMatch(/^Waiting for your permission for 3m · Bash: npm test\. Expires in 4:[01]\d\.$/);
  });

  it('waiting on an answer', async () => {
    renderPane(makeDetail({ status: 'awaiting_reply' }));
    expect(await sentence()).toMatch(/^Waiting for your answer for /);
  });

  it('quiet: a live process with nothing in the transcript for a while', async () => {
    renderPane(makeDetail({ status: 'idle', endedAt: ago(45), procPid: 9, procState: 'running' }));
    expect(await sentence()).toMatch(/^Quiet for 45m · /);
  });

  it('finished: how long it took, the files it changed and the result', async () => {
    const changes = ['a.ts', 'b.ts', 'a.ts'].map((filePath, id) => ({ id, filePath })) as unknown as SessionDetail['fileChanges'];
    renderPane(
      makeDetail({ status: 'completed', startedAt: ago(40), endedAt: ago(2), outcome: 'success', fileChanges: changes }),
    );
    expect(await sentence()).toBe('Finished in 38m · 2 files changed, marked success.');
  });

  it('stopped', async () => {
    renderPane(makeDetail({ status: 'completed', outcome: 'abandoned' }));
    expect(await sentence()).toMatch(/^Stopped by you/);
  });

  it('says it once, with no state chip anywhere in the header', async () => {
    renderPane(
      makeDetail({
        status: 'idle',
        endedAt: ago(45),
        procPid: 9,
        procState: 'running',
        cacheCold: { since: ago(40), kind: 'subscription', footprint: 420_000 },
        link: {
          status: 'idle',
          seenAt: ago(1),
          live: true,
          contextPct: 30,
          rateLimits: [],
          costUsd: 0.4,
          controllable: false,
        } as unknown as SessionDetail['link'],
      }),
    );
    const detail = await screen.findByTestId('session-detail');
    expect(await sentence()).toContain('Prompt cache cold since');
    expect(within(detail).getAllByTestId('state-sentence')).toHaveLength(1);
    expect(within(detail).queryByTestId('session-link')).toBeNull();
    expect(within(detail).queryByText('cache cold')).toBeNull();
    expect(within(detail).queryByRole('button', { name: 'continue from handoff' })).toBeNull();
    expect(detail.textContent).not.toMatch(/status/i);
  });
});

describe('the numbers strip', () => {
  // Each column: the value, then the label (then the bar, on the ones with a limit).
  const columns = (): HTMLElement[] =>
    Array.from(screen.getByTestId('session-numbers').children[0]?.children ?? []) as HTMLElement[];
  const labels = (): string[] => columns().map((col) => col.children[1]?.textContent ?? '');
  const column = (label: string): HTMLElement => {
    const found = columns().find((col) => col.children[1]?.textContent === label);
    if (found === undefined) throw new Error(`no ${label} column`);
    return found;
  };

  it('keeps the fixed order, leaves zero columns out, and reds a column past its threshold with its label', async () => {
    renderPane(
      makeDetail({
        events: [
          { id: 1, type: 'error', status: null, ts: ago(2) },
          { id: 2, type: 'tool', status: 'denied', ts: ago(1) },
        ] as unknown as SessionDetail['events'],
        coldRestarts: 1,
        recovered: 0,
        link: {
          status: 'busy',
          seenAt: ago(0),
          live: true,
          contextPct: 62,
          rateLimits: [{ kind: 'five_hour', percentUsed: 40, resetsAt: null }],
          costUsd: 0.4,
          controllable: true,
        } as unknown as SessionDetail['link'],
      }),
    );
    await screen.findByTestId('session-numbers');
    expect(labels()).toEqual(['tokens', 'cost', 'context', 'plan', 'errors', 'cold restarts']);
    const context = column('context');
    expect(context.hasAttribute('data-alarm')).toBe(true);
    expect(context.children[0]?.className).toContain('text-red');
    expect(context.children[1]?.className).toContain('text-red');
    expect(column('plan').hasAttribute('data-alarm')).toBe(false);
    expect(column('plan').children[1]?.className).toContain('text-ink-faint');
    expect(column('errors').hasAttribute('data-alarm')).toBe(true);
  });

  it('without the link mod: context in tokens (red past the fat line), and no plan column', async () => {
    renderPane(makeDetail({ turns: [], contextTokens: 320_000, link: null }));
    await screen.findByTestId('session-numbers');
    expect(labels()).toEqual(['context']);
    expect(column('context').children[0]?.textContent).toBe('320K');
    expect(column('context').hasAttribute('data-alarm')).toBe(true);
  });

  it('draws no strip at all when every column is zero', async () => {
    renderPane(makeDetail({ turns: [] }));
    await screen.findByTestId('state-sentence');
    expect(screen.queryByTestId('session-numbers')).toBeNull();
  });
});

describe('waiting on a permission', () => {
  it('allows and denies through resolveApproval, with the countdown in view', async () => {
    vi.mocked(api.fetchApprovals).mockResolvedValue([request(252)]);
    renderPane(makeDetail(), { gate: GATE });
    await waitFor(() => expect(screen.getByText(/^expires in 4:[01]\d$/)).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: /allow once/ }));
    await waitFor(() => expect(api.resolveApproval).toHaveBeenCalledWith(11, 'approve', undefined, undefined));
    fireEvent.click(screen.getByRole('button', { name: /^deny/ }));
    fireEvent.click(screen.getByRole('button', { name: /^deny/ }));
    await waitFor(() => expect(api.resolveApproval).toHaveBeenCalledWith(11, 'deny', undefined, undefined));
    expect(api.fetchApprovals).toHaveBeenCalledWith('pending', 'orders-api');
  });
});

describe('the action bar and ⋯', () => {
  it('working: stop turn and send prompt, the reason in grey when no link mod reaches it', async () => {
    renderPane(makeDetail({ procPid: 9, procState: 'running' }));
    const bar = await screen.findByTestId('session-actions');
    const stopTurn = within(bar).getByRole('button', { name: 'Stop turn' }) as HTMLButtonElement;
    const send = within(bar).getByRole('button', { name: 'send prompt' }) as HTMLButtonElement;
    expect(stopTurn.disabled).toBe(true);
    expect(send.disabled).toBe(true);
    expect(within(bar).getByText('no link mod on this session').className).toContain('text-ink-faint');
  });

  it('opens from the keyboard, closes on Escape with focus back, and renames from it', async () => {
    renderPane(makeDetail({ status: 'completed', endedAt: ago(2) }));
    const trigger = await screen.findByRole('button', { name: 'more for this session' });
    trigger.focus();
    fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    const menu = screen.getByRole('menu', { name: 'more for this session' });
    expect(document.activeElement?.getAttribute('role')).toBe('menuitem');
    fireEvent.keyDown(menu, { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);

    fireEvent.keyDown(trigger, { key: 'Enter' });
    fireEvent.click(screen.getByRole('menuitem', { name: 'rename' }));
    const field = await screen.findByRole('textbox', { name: 'session title' });
    expect((field as HTMLInputElement).value).toBe('session 7');
  });

  it('asks before stopping the process from ⋯', async () => {
    renderPane(makeDetail({ procPid: 9, procState: 'running' }));
    fireEvent.click(await screen.findByRole('button', { name: 'more for this session' }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'stop process…' }));
    const confirm = screen.getByTestId('stop-process');
    expect(confirm.textContent).toContain('Kill');
    fireEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByTestId('stop-process')).toBeNull();
  });
});
