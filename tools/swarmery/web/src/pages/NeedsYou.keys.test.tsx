// @vitest-environment jsdom
//
// `e` and `x` act on the open card and nothing else. The claim that matters: once
// the last request is answered and the card is gone, `e` must not answer it again.

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

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.fetchApprovals).mockResolvedValue([REQUEST]);
});
afterEach(cleanup);

describe('NeedsYou keys', () => {
  it('does nothing on e once the card is gone', async () => {
    serve([ITEM]);
    await renderPage();
    serve([]);
    await act(async () => {
      fireEvent.keyDown(window, { key: 'e' });
    });
    expect(api.resolveApproval).toHaveBeenCalledTimes(1);
    await screen.findByText('Nothing blocked on you.');
    await act(async () => {
      fireEvent.keyDown(window, { key: 'e' });
    });
    expect(api.resolveApproval).toHaveBeenCalledTimes(1);
  });

  it('names what e does on a prod-deploy request: it denies', async () => {
    vi.mocked(api.fetchApprovals).mockResolvedValue([{ ...REQUEST, riskClass: 'prod-deploy' }]);
    serve([ITEM]);
    await renderPage();
    await waitFor(() => expect(screen.getByText('e deny')).toBeTruthy());
    expect(screen.queryByText('x deny')).toBeNull();
  });
});
