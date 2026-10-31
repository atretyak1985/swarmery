// @vitest-environment jsdom
//
// The "Needs you" page (handoff §7). The claims:
//
//   1. Rows list Decide here first, then Open & tick by hand, oldest blocker first
//      inside each, whatever order the API answered in.
//   2. A prod_deploy_local card offers no allow / approve (and no deny) button —
//      only "Confirm locally", the terminal instruction and Focus terminal.
//   3. An approval card allows and denies, and hides allow for a riskClass
//      'prod-deploy' request.
//   4. Copy reply writes `[<session>] Re: <question>\n\n` to the clipboard; a
//      picked suggestion appends the option, and the main button repeats the pick.
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
import { itemKey } from '../lib/needsYou';
import { NeedsYou } from './NeedsYou';

vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  getNeedsYou: vi.fn(),
  fetchApprovals: vi.fn(async () => []),
  resolveApproval: vi.fn(async () => ({})),
  sendSessionMessage: vi.fn(async () => undefined),
}));

const nav = vi.hoisted(() => ({ to: vi.fn() }));
vi.mock('react-router-dom', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router-dom')>()),
  useNavigate: () => nav.to,
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

async function renderPage(url = '/needs-you'): Promise<void> {
  render(
    <MemoryRouter initialEntries={[url]}>
      <NeedsYou />
    </MemoryRouter>,
  );
  await screen.findByText(/blocked on you/);
}

function rows(): HTMLElement[] {
  return screen.queryAllByTestId('needs-you-row');
}

/** Selects the row and returns the detail, which now shows that decision. */
function rowOf(name: string): HTMLElement {
  const found = rows().find((r) => r.dataset.name === name);
  if (found === undefined) throw new Error(`no row for ${name}`);
  fireEvent.click(within(found).getByRole('button'));
  return screen.getByTestId('needs-you-detail');
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
  it('lists Decide here before Open & tick by hand, oldest blocker first inside each', async () => {
    serve([AWAITING, PROD, FAILED]); // deliberately out of order
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['prod_deploy_local', 'awaiting_reply', 'failed']);
    expect(screen.getByText('Decide here')).toBeTruthy();
    expect(screen.getByText('Open & tick by hand')).toBeTruthy();
    expect(within(rowOf('nightly build')).getByText('failed')).toBeTruthy();
  });

  it('renders every kind as a decision: an eyebrow, a heading and a main action with its key', async () => {
    const phase = { taskId: 7, planExternalId: 'p', planTitle: 'Plan', phaseId: 4, seq: 1, name: 'One', manualOpen: 1 };
    const decision = {
      taskId: 7,
      phaseId: 4,
      decisionId: 'D-1',
      question: 'Allow it?',
      options: [{ id: 'reject', label: 'Reject' }],
      guard: {},
    };
    const all = [
      item({ kind: 'approval', sessionName: 'k1', requestId: 1, toolName: 'Bash' }),
      item({ kind: 'question', sessionName: 'k2', requestId: 2, preview: 'Which?' }),
      item({ kind: 'prod_deploy_local', sessionName: 'k3', requestId: 3 }),
      item({ kind: 'awaiting_reply', sessionName: 'k4' }),
      item({ kind: 'failed', sessionName: 'k5' }),
      item({ kind: 'stalled_phase', sessionName: 'k6', phase }),
      item({ kind: 'manual_phase', sessionName: 'k7', phase }),
      item({ kind: 'awaiting_merge', sessionName: 'k8', phase }),
      item({ kind: 'phase_decision', sessionName: 'k9', phase, decision }),
      item({
        kind: 'worktree_cleanup',
        sessionName: 'k10',
        cleanup: { id: 1, repoRoot: '/r', candidates: [], withoutProof: 0 },
      }),
    ];
    serve(all);
    await renderPage();
    expect(new Set(all.map((i) => i.kind)).size).toBe(10);
    for (const it of all) {
      const card = rowOf(it.sessionName);
      expect(card.dataset.kind, it.kind).toBe(it.kind);
      expect(within(card).getByRole('heading', { level: 2 }), it.kind).toBeTruthy();
      const buttons = within(card).getAllByRole('button');
      expect(buttons.some((b) => /\be$/.test(b.textContent ?? '')), it.kind).toBe(true);
    }
  });

  it('heads the decision with the band: eyebrow, title and command in it, the rest under it', async () => {
    serve([item({ kind: 'approval', sessionName: 'one', requestId: 1, toolName: 'Bash', preview: 'rm -rf build' })]);
    await renderPage();
    const heading = within(screen.getByTestId('needs-you-detail')).getByRole('heading', { level: 2 });
    const band = heading.parentElement?.parentElement as HTMLElement;
    expect(band.className).toContain('border-b');
    expect(band.className).toContain('bg-bg');
    const code = band.querySelector('code') as HTMLElement;
    expect(code.textContent).toBe('rm -rf build');
    expect(code.className).toContain('bg-surface');
    expect(code.className).toContain('[overflow-wrap:anywhere]');
    expect(band.contains(within(screen.getByTestId('needs-you-detail')).getByRole('button', { name: /allow once/ }))).toBe(false);
  });

  it('e allows the selected request; x asks for a reason, then denies', async () => {
    vi.mocked(api.fetchApprovals).mockResolvedValue([pending(11, { sessionId: 4 })]);
    serve([item({ kind: 'approval', sessionId: 4, sessionName: 'lint fix', requestId: 11, toolName: 'Bash' })]);
    await renderPage();
    await screen.findByRole('button', { name: /^allow once/ });
    await act(async () => {
      fireEvent.keyDown(window, { key: 'e' });
    });
    expect(api.resolveApproval).toHaveBeenCalledWith(11, 'approve', undefined, undefined);
    await act(async () => {
      fireEvent.keyDown(window, { key: 'x' });
    });
    expect(api.resolveApproval).toHaveBeenCalledTimes(1);
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'not this table' } });
    await act(async () => {
      fireEvent.keyDown(window, { key: 'x' });
    });
    expect(api.resolveApproval).toHaveBeenCalledWith(11, 'deny', 'not this table', undefined);
  });

  it('opens the decision ?item= names, falls back when it is gone, and a click wins', async () => {
    const first = item({ kind: 'awaiting_reply', sessionName: 'older', sessionId: 1, blockingSince: iso(-60) });
    const second = item({ kind: 'awaiting_reply', sessionName: 'newer', sessionId: 2, blockingSince: iso(-5) });
    serve([first, second]);
    await renderPage(`/needs-you?item=${encodeURIComponent(itemKey(second))}`);
    const detail = (): string | null => screen.getByTestId('needs-you-detail').textContent;
    await waitFor(() => expect(detail()).toContain('newer'));
    fireEvent.click(screen.getByRole('button', { name: /older/ }));
    await waitFor(() => expect(detail()).toContain('older'));
    cleanup();
    await renderPage('/needs-you?item=gone%3A1');
    await waitFor(() => expect(detail()).toContain('older'));
  });

  it('asks one question as a radio list, and the main button repeats the pick', async () => {
    const ask = {
      tool_name: 'AskUserQuestion',
      tool_input: {
        questions: [
          {
            question: 'Which layout?',
            header: 'Layout',
            options: [
              { label: 'split', description: 'two panes' },
              { label: 'stack', description: 'one column' },
            ],
            multiSelect: false,
          },
        ],
      },
    };
    vi.mocked(api.fetchApprovals).mockResolvedValue([
      pending(12, { toolName: 'AskUserQuestion', requestJson: JSON.stringify(ask) }),
    ]);
    serve([item({ kind: 'question', sessionName: 'layout', requestId: 12, toolName: 'AskUserQuestion' })]);
    await renderPage();
    const card = screen.getByTestId('needs-you-detail');
    await within(card).findByRole('radiogroup');
    fireEvent.click(within(card).getByRole('radio', { name: /stack/ }));
    fireEvent.click(within(card).getByRole('button', { name: /^reply: stack/ }));
    await waitFor(() =>
      expect(api.resolveApproval).toHaveBeenCalledWith(12, 'answer', undefined, { 'Which layout?': 'stack' }),
    );
  });

  it('offers no allow / approve / deny on a prod_deploy_local row', async () => {
    serve([PROD]);
    await renderPage();
    const row = rowOf('release train');
    expect(within(row).getByText(/confirm locally/)).toBeTruthy();
    expect(within(row).getByText("Production deploy — confirm in the session's terminal")).toBeTruthy();
    expect(within(row).queryByRole('button', { name: /allow|approve/i })).toBeNull();
    expect(within(row).queryByRole('button', { name: /deny/i })).toBeNull();
    expect(within(row).getByRole('button', { name: /focus terminal/i })).toBeTruthy();
  });

  it('allows an approval, and hides allow for a prod-deploy request', async () => {
    const ordinary = item({ kind: 'approval', sessionId: 4, sessionName: 'lint fix', requestId: 11, toolName: 'Bash' });
    const deploy = item({ kind: 'approval', sessionId: 5, sessionName: 'ship it', requestId: 12, toolName: 'Bash' });
    vi.mocked(api.fetchApprovals).mockResolvedValue([
      pending(11, { sessionId: 4 }),
      pending(12, { sessionId: 5, riskClass: 'prod-deploy' }),
    ]);
    serve([ordinary, deploy]);
    await renderPage();

    const ok = rowOf('lint fix');
    fireEvent.click(await within(ok).findByRole('button', { name: /^allow once/ }));
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

    fireEvent.click(within(rowOf('orders refactor')).getByRole('button', { name: /^copy reply/ }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith('[orders refactor] Re: Keep the old endpoint or remove it?\n\n'),
    );
    expect(await screen.findByText(/Copied/)).toBeTruthy();

    const card = rowOf('api cleanup');
    expect((within(card).getByRole('radio', { name: /remove/ }) as HTMLInputElement).checked).toBe(true);
    fireEvent.click(within(card).getByRole('radio', { name: /keep/ }));
    fireEvent.click(within(card).getByRole('button', { name: /^copy reply/ }));
    await waitFor(() => expect(writeText).toHaveBeenLastCalledWith('[api cleanup] Re: Keep the old endpoint?\n\nkeep'));
    fireEvent.click(within(card).getByRole('button', { name: /^reply: keep/ }));
    await waitFor(() => expect(api.sendSessionMessage).toHaveBeenCalledWith(6, 'keep'));
  });

  it('dismisses a row across a remount, and re-surfaces a new blocking episode', async () => {
    serve([AWAITING, FAILED]);
    await renderPage();
    fireEvent.click(within(rowOf('orders refactor')).getByRole('button', { name: /^dismiss/ }));
    expect(rows().map((r) => r.dataset.kind)).toEqual(['failed']);

    cleanup();
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['failed']);

    cleanup();
    serve([{ ...AWAITING, blockingSince: iso(-1) }, FAILED]);
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['awaiting_reply', 'failed']);
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

  it('opens the phase Criteria tab from a manual_phase card, not a session', async () => {
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
    expect(rows().map((r) => r.dataset.kind)).toEqual(['awaiting_reply', 'manual_phase']);
    const row = rowOf('Line items · Phase 3: Rollout');
    expect(within(row).getByText('manual check')).toBeTruthy();
    expect(within(row).getByText('2 [MANUAL] criteria left — only you can close them')).toBeTruthy();
    const want = '/p/shop/plans/2026-10-01-line-items/phase/3/criteria';
    fireEvent.click(within(row).getByRole('button', { name: /^open criteria/ }));
    expect(nav.to).toHaveBeenCalledWith(want);
    expect(within(row).queryByRole('button', { name: /copy reply|dismiss/i })).toBeNull();
  });

  it('opens the phase Review tab from awaiting_merge, and the PR once one is open', async () => {
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
    expect(rows().map((r) => r.dataset.kind)).toEqual(['awaiting_reply', 'awaiting_merge', 'awaiting_merge']);

    const ready = rowOf('Line items · Phase 1: Land 1');
    expect(within(ready).getByText('awaiting merge')).toBeTruthy();
    expect(within(ready).getByText('1 [LAND] criterion left — ready')).toBeTruthy();
    const reviewHref = '/p/shop/plans/2026-10-01-line-items/phase/1/review';
    fireEvent.click(within(ready).getByRole('button', { name: /^open review/ }));
    expect(nav.to).toHaveBeenCalledWith(reviewHref);
    expect(within(ready).queryByRole('button', { name: 'open PR ↗' })).toBeNull();

    const prOpen = rowOf('Line items · Phase 2: Land 2');
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    fireEvent.click(within(prOpen).getByRole('button', { name: 'open PR ↗' }));
    expect(open).toHaveBeenCalledWith('https://example.test/pr/2', '_blank', 'noopener,noreferrer');
    expect(within(prOpen).queryByRole('button', { name: /copy reply|dismiss/i })).toBeNull();
  });

  it('opens the phase Runs tab from a stalled_phase card and offers no restart', async () => {
    const stalled = item({
      kind: 'stalled_phase',
      sessionId: 0,
      sessionUuid: 'exec-1',
      sessionName: 'Line items · Phase 4: Sync',
      question: '',
      asksQuestion: false,
      preview: 'executor — no new turn for 25 min',
      blockingSince: iso(-25),
      phase: {
        taskId: 7,
        planExternalId: '2026-10-01-line-items',
        planTitle: 'Line items',
        phaseId: 44,
        seq: 4,
        name: 'Sync',
        manualOpen: 0,
      },
      stall: { stage: 'executor', reason: 'no-turns', sinceMinutes: 25 },
    });
    serve([AWAITING, stalled]);
    await renderPage();
    expect(rows().map((r) => r.dataset.kind)).toEqual(['awaiting_reply', 'stalled_phase']);
    const row = rowOf('Line items · Phase 4: Sync');
    expect(within(row).getByText('stalled phase')).toBeTruthy();
    expect(within(row).getByText('executor — no new turn for 25 min')).toBeTruthy();
    const want = '/p/shop/plans/2026-10-01-line-items/phase/4/runs';
    fireEvent.click(within(row).getByRole('button', { name: /^open phase/ }));
    expect(nav.to).toHaveBeenCalledWith(want);
    expect(within(row).getAllByRole('button')).toHaveLength(1);
  });

  it('adds «open phase» to an approval a stalled phase waits on, and keeps the decision there', async () => {
    const held = item({
      kind: 'approval',
      sessionId: 4,
      sessionName: 'verifier of sync',
      requestId: 101,
      toolName: 'Bash',
      phase: {
        taskId: 7,
        planExternalId: '2026-10-01-line-items',
        planTitle: 'Line items',
        phaseId: 44,
        seq: 4,
        name: 'Sync',
        manualOpen: 0,
      },
      stall: { stage: 'verifier', reason: 'permission-pending', sinceMinutes: 25 },
    });
    const plain = item({ kind: 'approval', sessionId: 5, sessionName: 'lint fix', requestId: 102, toolName: 'Bash' });
    vi.mocked(api.fetchApprovals).mockResolvedValue([pending(101, { sessionId: 4 }), pending(102, { sessionId: 5 })]);
    serve([held, plain]);
    await renderPage();

    const row = rowOf('verifier of sync');
    expect(await within(row).findByRole('button', { name: /^allow once/ })).toBeTruthy();
    const open = within(row).getByRole('link', { name: 'open phase →' });
    expect(open.getAttribute('href')).toBe('/p/shop/plans/2026-10-01-line-items/phase/4/runs');
    expect(within(row).getByText('stalls Line items · Phase 4: Sync')).toBeTruthy();
    // The card still opens the session that asked, not the phase.
    fireEvent.click(within(row).getByRole('button', { name: 'open session →' }));
    expect(nav.to.mock.calls.at(-1)?.[0]).not.toContain('/plans/');
    expect(within(rowOf('lint fix')).queryByTestId('needs-you-stalled-on')).toBeNull();
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
    expect(within(row).getByText('worktree cleanup')).toBeTruthy();
    fireEvent.click(within(row).getByRole('button', { name: 'janitor journal →' }));
    expect(nav.to).toHaveBeenCalledWith('/settings');

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
    expect(within(row).getByText('without proof: 3')).toBeTruthy();
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
    const remove = within(row).getByRole('button', { name: /^Remove selected/ });
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
    fireEvent.click(within(row).getByRole('button', { name: /^Remove selected/ }));
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
