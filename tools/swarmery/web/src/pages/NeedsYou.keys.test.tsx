// @vitest-environment jsdom
//
// The page's keys (artboard 3b on v4, plan D-4/D-9). The claims:
//
//   1. `e` only moves focus onto the open card's main action; it never answers.
//   2. `x` acts on the open card and nothing else: once the answered card is gone,
//      or while the refetch after an answer is still pending, it is dead.
//   3. `o` / → go into the decision pane, ← back to its row in the list.
//   4. The footer names `e` as focusing, and drops `x` where it does nothing.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../api';
import type { NeedsYouItem, PermissionRequest } from '../api/types';
import { act, cleanup, fireEvent, render, screen, waitFor } from '../test/render';
import { NeedsYou } from './NeedsYou';

vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  getNeedsYou: vi.fn(),
  fetchApprovals: vi.fn(async () => []),
  resolveApproval: vi.fn(async () => ({})),
}));
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

const NOW = Date.now();
const at = new Date(NOW - 5 * 60_000).toISOString();

const ITEM: NeedsYouItem = {
  kind: 'approval',
  sessionId: 4,
  sessionUuid: 'u-4',
  sessionName: 'lint fix',
  projectSlug: 'shop',
  requestId: 11,
  toolName: 'Bash',
  preview: 'npm run lint',
  question: '',
  asksQuestion: false,
  blockingSince: at,
  blockingSeconds: 300,
  termFocusUrl: null,
  suggestion: null,
};

const REQUEST: PermissionRequest = {
  id: 11,
  sessionId: 4,
  toolName: 'Bash',
  requestJson: JSON.stringify({ tool_name: 'Bash', tool_input: { command: 'npm run lint' } }),
  status: 'pending',
  requestedAt: at,
  resolvedAt: null,
  resolvedVia: null,
  reason: null,
  expiresAt: new Date(NOW + 120_000).toISOString(),
  riskClass: '',
};

function serve(items: NeedsYouItem[]): void {
  vi.mocked(api.getNeedsYou).mockResolvedValue({ items, generatedAt: new Date(NOW).toISOString() });
}

async function renderPage(): Promise<void> {
  render(
    <MemoryRouter initialEntries={['/needs-you']}>
      <NeedsYou />
    </MemoryRouter>,
  );
  await screen.findAllByRole('button', { name: /^(allow once|deny)/ });
}

async function press(...keys: string[]): Promise<void> {
  for (const key of keys) {
    await act(async () => {
      fireEvent.keyDown(window, { key });
    });
  }
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.fetchApprovals).mockResolvedValue([REQUEST]);
});
afterEach(cleanup);

describe('NeedsYou keys', () => {
  it('moves focus onto the main action on e, and never answers with it', async () => {
    serve([ITEM]);
    await renderPage();
    const allow = await screen.findByRole('button', { name: /^allow once/ });
    await press('e');
    expect(document.activeElement).toBe(allow);
    expect(api.resolveApproval).not.toHaveBeenCalled();
    fireEvent.click(allow);
    await waitFor(() => expect(api.resolveApproval).toHaveBeenCalledWith(11, 'approve', undefined, undefined));
  });

  it('does nothing on x once the card is gone', async () => {
    serve([ITEM]);
    await renderPage();
    serve([]);
    fireEvent.click(await screen.findByRole('button', { name: /^allow once/ }));
    await screen.findByText('Nothing blocked on you.');
    await press('x', 'x', 'e');
    expect(api.resolveApproval).toHaveBeenCalledTimes(1);
  });

  it('does nothing on x while the queue refetch after an answer is still pending', async () => {
    serve([ITEM]);
    await renderPage();
    let release: () => void = () => undefined;
    vi.mocked(api.getNeedsYou).mockImplementation(
      () =>
        new Promise((resolve) => {
          release = () => resolve({ items: [], generatedAt: new Date(NOW).toISOString() });
        }),
    );
    fireEvent.click(await screen.findByRole('button', { name: /^allow once/ }));
    // The answered card is still on screen; its key must stay dead.
    await press('x', 'x');
    expect(api.resolveApproval).toHaveBeenCalledTimes(1);
    await act(async () => release());
    await screen.findByText('Nothing blocked on you.');
    expect(api.resolveApproval).toHaveBeenCalledTimes(1);
  });

  it('goes into the pane on o and →, and back to the row on ←', async () => {
    serve([ITEM]);
    await renderPage();
    const detail = screen.getByTestId('needs-you-detail');
    const row = screen.getByRole('button', { current: true });
    for (const key of ['o', 'ArrowRight']) {
      row.focus();
      await press(key);
      expect(document.activeElement, key).toBe(detail);
    }
    await press('ArrowLeft');
    expect(document.activeElement).toBe(row);
  });

  it('names e as focusing, and hides x on a production deploy request', async () => {
    vi.mocked(api.fetchApprovals).mockResolvedValue([{ ...REQUEST, riskClass: 'prod-deploy' }]);
    serve([ITEM]);
    await renderPage();
    await waitFor(() => expect(screen.getByText('e focus action')).toBeTruthy());
    expect(screen.queryByText('x deny')).toBeNull();
    expect(screen.getByText('o → open')).toBeTruthy();
  });
});
