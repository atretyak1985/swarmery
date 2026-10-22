// @vitest-environment jsdom
//
// The "Needs you" page (needs-you-queue phase 5). The claims:
//
//   1. Rows render oldest blocker first, whatever order the API answered in.
//   2. A prod_deploy_local row offers no allow / approve (and no deny) button —
//      only "Confirm locally", the terminal instruction and Focus terminal.
//   3. An approval row is the Approvals PendingCard (approve / deny), and that
//      card hides approve for a riskClass 'prod-deploy' request.
//   4. Copy reply writes `[<session>] Re: <question>\n\n` to the clipboard; a
//      suggestion option chip appends the option.
//   5. Dismiss hides the row, survives a remount, and a new blocking episode
//      of the same session re-surfaces.
//   6. A WS permission_resolved frame refetches GET /api/needs-you.
//
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/pages/NeedsYou.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx — treat the casts as documentation.

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../api';
import type { NeedsYouItem, PermissionRequest, WSMessage } from '../api/types';
import { NeedsYou } from './NeedsYou';

vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  getNeedsYou: vi.fn(),
  fetchApprovals: vi.fn(async () => []),
  resolveApproval: vi.fn(async () => ({})),
}));

// Capture the page's WS subscriber so a test can push a frame through it.
const ws = vi.hoisted(() => ({ onMessage: null as ((msg: unknown) => void) | null }));
vi.mock('../lib/ws', () => ({
  useLiveUpdates: (onMessage: (msg: unknown) => void) => {
    ws.onMessage = onMessage;
  },
}));

const NOW = Date.now();
const iso = (offsetMin: number): string => new Date(NOW + offsetMin * 60_000).toISOString();

function item(patch: Partial<NeedsYouItem>): NeedsYouItem {
  return {
    kind: 'awaiting_reply',
    sessionId: 1,
    sessionUuid: 'u-1',
    sessionName: 'orders refactor',
    projectSlug: 'shop',
    requestId: null,
    toolName: '',
    preview: '',
    question: 'Keep the old endpoint or remove it?',
    asksQuestion: true,
    blockingSince: iso(-10),
    blockingSeconds: 600,
    termFocusUrl: null,
    suggestion: null,
    ...patch,
  };
}

function pending(id: number, patch: Partial<PermissionRequest> = {}): PermissionRequest {
  return {
    id,
    sessionId: 3,
    toolName: 'Bash',
    requestJson: JSON.stringify({ tool_name: 'Bash', tool_input: { command: 'npm run deploy:prod' } }),
    status: 'pending',
    requestedAt: iso(-30),
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt: iso(2),
    riskClass: '',
    ...patch,
  };
}

const AWAITING = item({ sessionId: 1, sessionName: 'orders refactor', blockingSince: iso(-10) });
const FAILED = item({ kind: 'failed', sessionId: 2, sessionName: 'nightly build', preview: 'exit 1', blockingSince: iso(-60) });
const PROD = item({
  kind: 'prod_deploy_local',
  sessionId: 3,
  sessionName: 'release train',
  requestId: 90,
  toolName: 'Bash',
  preview: 'npm run deploy:prod',
  question: '',
  blockingSince: iso(-30),
  termFocusUrl: 'warp://action/focus_tab?id=7',
});

function serve(items: NeedsYouItem[]): void {
  vi.mocked(api.getNeedsYou).mockResolvedValue({ items, generatedAt: new Date(NOW).toISOString() });
}

async function renderPage(): Promise<void> {
  render(
    <MemoryRouter initialEntries={['/needs-you']}>
      <NeedsYou />
    </MemoryRouter>,
  );
  await screen.findByText(/blocked on you/);
}

function rows(): HTMLElement[] {
  return screen.queryAllByTestId('needs-you-row');
}

function rowOf(name: string): HTMLElement {
  const found = rows().find((r) => r.getAttribute('aria-label')?.endsWith(`: ${name}`));
  if (found === undefined) throw new Error(`no row for ${name}`);
  return found;
}

let writeText: ReturnType<typeof vi.fn>;

beforeEach(() => {
  window.localStorage.clear();
  vi.clearAllMocks();
  ws.onMessage = null;
  writeText = vi.fn(async () => undefined);
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
});

afterEach(cleanup);

describe('NeedsYou page', () => {
  it('renders rows oldest blocker first', async () => {
    serve([AWAITING, PROD, FAILED]); // deliberately out of order
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['failed', 'prod_deploy_local', 'awaiting_reply']);
    expect(within(rowOf('nightly build')).getByText('Failed')).toBeTruthy();
  });

  it('offers no allow / approve / deny on a prod_deploy_local row', async () => {
    serve([PROD]);
    await renderPage();
    const row = rowOf('release train');
    expect(within(row).getByText('Confirm locally')).toBeTruthy();
    expect(within(row).getByText("Production deploy — confirm in the session's terminal")).toBeTruthy();
    expect(within(row).queryByRole('button', { name: /allow|approve/i })).toBeNull();
    expect(within(row).queryByRole('button', { name: /deny/i })).toBeNull();
    expect(within(row).queryByRole('link', { name: /allow|approve/i })).toBeNull();
    const focus = within(row).getByRole('link', { name: /focus the terminal/i });
    expect(focus.getAttribute('href')).toBe('warp://action/focus_tab?id=7');
  });

  it('renders an approval with the Approvals card, and hides approve for a prod-deploy request', async () => {
    const ordinary = item({ kind: 'approval', sessionId: 4, sessionName: 'lint fix', requestId: 11, toolName: 'Bash' });
    const deploy = item({ kind: 'approval', sessionId: 5, sessionName: 'ship it', requestId: 12, toolName: 'Bash' });
    vi.mocked(api.fetchApprovals).mockResolvedValue([
      pending(11, { sessionId: 4 }),
      pending(12, { sessionId: 5, riskClass: 'prod-deploy' }),
    ]);
    serve([ordinary, deploy]);
    await renderPage();

    const ok = rowOf('lint fix');
    fireEvent.click(await within(ok).findByRole('button', { name: /^approve$/i }));
    await waitFor(() => expect(api.resolveApproval).toHaveBeenCalledWith(11, 'approve', undefined, undefined));

    const prod = rowOf('ship it');
    await within(prod).findByText("Production deploy — confirm in the session's terminal");
    expect(within(prod).queryByRole('button', { name: /allow|approve/i })).toBeNull();
    expect(within(prod).getByRole('button', { name: /deny/i })).toBeTruthy();
  });

  it('shows the channel badge on approval rows, and nothing without a channel', async () => {
    serve([
      item({ kind: 'approval', sessionId: 4, sessionName: 'via the mod', requestId: 21, toolName: 'Bash' }),
      item({ kind: 'approval', sessionId: 5, sessionName: 'via the shim', requestId: 22, toolName: 'Bash' }),
      item({ kind: 'approval', sessionId: 6, sessionName: 'old daemon', requestId: 23, toolName: 'Bash' }),
    ]);
    vi.mocked(api.fetchApprovals).mockResolvedValue([
      pending(21, { sessionId: 4, channel: 'mod' }),
      pending(22, { sessionId: 5, channel: 'shim' }),
      pending(23, { sessionId: 6 }),
    ]);
    await renderPage();

    const mod = await within(rowOf('via the mod')).findByTestId('channel-badge');
    expect(mod.textContent).toBe('via mod');
    expect(within(rowOf('via the shim')).getByTestId('channel-badge').textContent).toBe('via shim');
    expect(within(rowOf('old daemon')).queryByTestId('channel-badge')).toBeNull();
  });

  it('shows the item channel on the fallback row while the permission row is unloaded', async () => {
    vi.mocked(api.fetchApprovals).mockResolvedValue([]);
    serve([item({ kind: 'approval', sessionId: 4, sessionName: 'fallback', requestId: 31, channel: 'mod' })]);
    await renderPage();
    expect(within(rowOf('fallback')).getByTestId('channel-badge').textContent).toBe('via mod');
  });

  it('copies the reply prefix, and a suggestion option appended to it', async () => {
    const suggested = item({
      sessionId: 6,
      sessionName: 'api cleanup',
      suggestion: { question: 'Keep the old endpoint?', options: ['keep', 'remove'], recommended: 'remove' },
    });
    serve([AWAITING, suggested]);
    await renderPage();

    fireEvent.click(within(rowOf('orders refactor')).getByRole('button', { name: 'Copy reply to orders refactor' }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith('[orders refactor] Re: Keep the old endpoint or remove it?\n\n'),
    );
    expect(await within(rowOf('orders refactor')).findByText(/Copied/)).toBeTruthy();

    const chips = within(rowOf('api cleanup')).getByRole('group', { name: 'suggested replies' });
    expect(within(chips).getByRole('button', { name: /remove \(suggested\)/ })).toBeTruthy();
    fireEvent.click(within(chips).getByRole('button', { name: /option: keep$/ }));
    await waitFor(() => expect(writeText).toHaveBeenLastCalledWith('[api cleanup] Re: Keep the old endpoint?\n\nkeep'));
  });

  it('dismisses a row across a remount, and re-surfaces a new blocking episode', async () => {
    serve([AWAITING, FAILED]);
    await renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss orders refactor' }));
    expect(rows().map((r) => r.dataset.kind)).toEqual(['failed']);

    cleanup();
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['failed']);

    cleanup();
    serve([{ ...AWAITING, blockingSince: iso(-1) }, FAILED]);
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['failed', 'awaiting_reply']);
  });

  it('refetches on a WS permission_resolved frame', async () => {
    serve([AWAITING]);
    await renderPage();
    expect(api.getNeedsYou).toHaveBeenCalledTimes(1);

    serve([AWAITING, FAILED]);
    act(() => {
      ws.onMessage?.({ type: 'permission_resolved', payload: pending(90) } satisfies WSMessage);
    });
    await waitFor(() => expect(api.getNeedsYou).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(rows()).toHaveLength(2));
  });

  it('renders a manual_phase row that links to the phase Criteria tab, not a session', async () => {
    const manual = item({
      kind: 'manual_phase',
      sessionId: 0,
      sessionUuid: '',
      sessionName: 'Line items · Phase 3: Rollout',
      question: '',
      asksQuestion: false,
      preview: '2 [MANUAL] criteria left — only you can close them',
      blockingSince: iso(-90),
      phase: {
        taskId: 7,
        planExternalId: '2026-10-01-line-items',
        planTitle: 'Line items',
        phaseId: 42,
        seq: 3,
        name: 'Rollout',
        manualOpen: 2,
      },
    });
    serve([AWAITING, manual]);
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['manual_phase', 'awaiting_reply']);
    const row = rowOf('Line items · Phase 3: Rollout');
    expect(within(row).getByText('Manual check')).toBeTruthy();
    expect(within(row).getByText('2 [MANUAL] criteria left — only you can close them')).toBeTruthy();
    const want = '/p/shop/plans/2026-10-01-line-items/phase/3/criteria';
    expect(within(row).getByRole('link', { name: 'open criteria →' }).getAttribute('href')).toBe(want);
    expect(within(row).getByRole('link', { name: 'Line items · Phase 3: Rollout' }).getAttribute('href')).toBe(want);
    expect(within(row).queryByRole('button', { name: /copy reply|dismiss/i })).toBeNull();
  });

  it('renders awaiting_merge rows that link to the phase Review tab, and to the PR once one is open', async () => {
    const mergeItem = (seq: number, landingState: 'ready' | 'pushed' | 'pr_open', prUrl?: string): NeedsYouItem =>
      item({
        kind: 'awaiting_merge',
        sessionId: 0,
        sessionUuid: '',
        sessionName: `Line items · Phase ${String(seq)}: Land ${String(seq)}`,
        question: '',
        asksQuestion: false,
        preview: `1 [LAND] criterion left — ${landingState}`,
        blockingSince: iso(-90 + seq),
        phase: {
          taskId: 7,
          planExternalId: '2026-10-01-line-items',
          planTitle: 'Line items',
          phaseId: 40 + seq,
          seq,
          name: `Land ${String(seq)}`,
          manualOpen: 0,
          landOpen: 1,
          landingState,
          ...(prUrl !== undefined ? { prUrl } : {}),
        },
      });
    serve([AWAITING, mergeItem(1, 'ready'), mergeItem(2, 'pr_open', 'https://example.test/pr/2')]);
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['awaiting_merge', 'awaiting_merge', 'awaiting_reply']);

    const ready = rowOf('Line items · Phase 1: Land 1');
    expect(within(ready).getByText('Awaiting merge')).toBeTruthy();
    expect(within(ready).getByText('1 [LAND] criterion left — ready')).toBeTruthy();
    const reviewHref = '/p/shop/plans/2026-10-01-line-items/phase/1/review';
    expect(within(ready).getByRole('link', { name: 'open review →' }).getAttribute('href')).toBe(reviewHref);
    expect(within(ready).getByRole('link', { name: 'Line items · Phase 1: Land 1' }).getAttribute('href')).toBe(
      reviewHref,
    );
    expect(within(ready).queryByRole('link', { name: 'open PR ↗' })).toBeNull();

    const prOpen = rowOf('Line items · Phase 2: Land 2');
    expect(within(prOpen).getByRole('link', { name: 'open review →' }).getAttribute('href')).toBe(
      '/p/shop/plans/2026-10-01-line-items/phase/2/review',
    );
    const pr = within(prOpen).getByRole('link', { name: 'open PR ↗' });
    expect(pr.getAttribute('href')).toBe('https://example.test/pr/2');
    expect(pr.getAttribute('target')).toBe('_blank');
    expect(pr.getAttribute('rel')).toBe('noopener noreferrer');
    expect(within(prOpen).queryByRole('button', { name: /copy reply|dismiss/i })).toBeNull();
  });

  const CLEANUP = item({
    kind: 'worktree_cleanup',
    sessionId: 0,
    sessionUuid: '',
    sessionName: 'shop',
    question: '',
    asksQuestion: false,
    preview: '2 finished worktrees with their content in main · 3 without proof',
    blockingSince: iso(-120),
    cleanup: {
      id: 11,
      repoRoot: '/work/shop',
      withoutProof: 3,
      candidates: [
        {
          id: 11,
          path: '/wt/shop/phase-41',
          branch: 'swarm/phase-41',
          owner: 'Line items · Phase 2: Totals',
          phaseId: 41,
          taskId: 7,
          planExternalId: '2026-10-01-line-items',
          phaseSeq: 2,
          proofKind: 'landing',
          proofDetail: 'phase landing merged',
        },
        {
          id: 12,
          path: '/wt/shop/T-abc',
          branch: 'swarm/T-abc',
          owner: 'T-abc: Fix the thing',
          phaseId: null,
          taskId: 9,
          proofKind: 'content-diff',
          proofDetail: 'merging into origin/main changes nothing',
        },
      ],
    },
  });

  it('renders a worktree_cleanup card: candidates with their proof, and the without-proof count', async () => {
    serve([CLEANUP]);
    await renderPage();
    const row = rowOf('shop');
    expect(row.dataset.kind).toBe('worktree_cleanup');
    expect(within(row).getByText('Worktree cleanup')).toBeTruthy();
    expect(within(row).getByRole('link', { name: 'shop' }).getAttribute('href')).toBe('/settings');

    const cands = within(row).getAllByTestId('cleanup-candidate');
    expect(cands).toHaveLength(2);
    expect(within(cands[0]).getByText('/wt/shop/phase-41')).toBeTruthy();
    expect(within(cands[0]).getByText('swarm/phase-41')).toBeTruthy();
    expect(within(cands[0]).getByText('PR merged')).toBeTruthy();
    expect(
      within(cands[0]).getByRole('link', { name: 'Line items · Phase 2: Totals' }).getAttribute('href'),
    ).toBe('/p/shop/plans/2026-10-01-line-items/phase/2');
    expect(within(cands[1]).getByText('content in main')).toBeTruthy();
    expect(within(cands[1]).getByText('T-abc: Fix the thing')).toBeTruthy();
    expect(within(cands[1]).queryByRole('link')).toBeNull();
    expect(within(row).getByTestId('cleanup-without-proof').textContent).toBe('without proof: 3');
  });

  it('removes only the selected worktrees, after the click, then refetches', async () => {
    serve([CLEANUP]);
    const apply = vi.spyOn(api, 'applyWorktreeCleanup').mockResolvedValue({
      id: 11,
      repoRoot: '/work/shop',
      paths: [{ path: '/wt/shop/T-abc', outcome: 'removed', salvageBranch: 'salvage/swarm-T-abc-20261022T033000Z' }],
    });
    await renderPage();
    const row = rowOf('shop');
    const remove = within(row).getByRole('button', { name: 'Remove selected' });
    expect((remove as HTMLButtonElement).disabled).toBe(true);
    expect(apply).not.toHaveBeenCalled();

    fireEvent.click(within(row).getByRole('checkbox', { name: 'Select /wt/shop/T-abc' }));
    expect(within(row).getByText('1 selected')).toBeTruthy();
    expect((remove as HTMLButtonElement).disabled).toBe(false);
    expect(apply).not.toHaveBeenCalled(); // ticking is not removing

    fireEvent.click(remove);
    await waitFor(() => expect(apply).toHaveBeenCalledTimes(1));
    expect(apply).toHaveBeenCalledWith(11, ['/wt/shop/T-abc']);
    const outcome = await within(row).findByTestId('cleanup-outcome');
    expect(outcome.dataset.outcome).toBe('removed');
    expect(outcome.textContent).toContain('salvage/swarm-T-abc-20261022T033000Z');
    await waitFor(() => expect(api.getNeedsYou).toHaveBeenCalledTimes(2));
  });

  it('select-all ticks every listed candidate, never the ones without proof', async () => {
    serve([CLEANUP]);
    const apply = vi.spyOn(api, 'applyWorktreeCleanup').mockResolvedValue({ id: 11, repoRoot: '/work/shop', paths: [] });
    await renderPage();
    const row = rowOf('shop');
    fireEvent.click(within(row).getByRole('checkbox', { name: 'Select all worktrees' }));
    expect(within(row).getByText('2 selected')).toBeTruthy();
    fireEvent.click(within(row).getByRole('button', { name: 'Remove selected' }));
    await waitFor(() => expect(apply).toHaveBeenCalledTimes(1));
    expect(apply.mock.calls[0][1]).toEqual(['/wt/shop/phase-41', '/wt/shop/T-abc']);
  });

  it('ignores frames that cannot change the queue', async () => {
    serve([AWAITING]);
    await renderPage();
    act(() => {
      ws.onMessage?.({ type: 'task_deleted', payload: { taskId: 1, projectId: 1 } } satisfies WSMessage);
    });
    await new Promise((r) => setTimeout(r, 500));
    expect(api.getNeedsYou).toHaveBeenCalledTimes(1);
  });
});
