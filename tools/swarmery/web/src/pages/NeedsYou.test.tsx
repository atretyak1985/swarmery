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

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
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
