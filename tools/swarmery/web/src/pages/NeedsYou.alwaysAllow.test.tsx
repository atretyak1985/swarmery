// @vitest-environment jsdom
//
// Always-allow in Needs you: `/approvals` and `/inbox?tab=approvals` land here, so
// it has to be a real control. The claims:
//
//   1. On a write-tier permission it opens the suggested pattern for a look, and a
//      second press saves that rule for this project.
//   2. The request itself still waits for an answer.
//   3. A prod-deploy request offers no always-allow at all.
//   4. ?tab=approvals opens on a permission, not on whatever is oldest, and
//      &request= opens on that request; one already answered falls back.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../api';
import type { NeedsYouItem, PermissionRequest } from '../api/types';
import { cleanup, fireEvent, render, screen, waitFor, within } from '../test/render';
import { NeedsYou } from './NeedsYou';

vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  getNeedsYou: vi.fn(),
  fetchApprovals: vi.fn(async () => []),
  resolveApproval: vi.fn(async () => ({})),
  createApprovalRule: vi.fn(async () => ({})),
}));
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));
vi.mock('../lib/scope', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../lib/scope')>()),
  useScope: () => ({ projects: [{ id: 5, slug: 'shop', name: 'Shop' }] }),
}));

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

function pending(patch: Partial<PermissionRequest> = {}): PermissionRequest {
  return {
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
    ...patch,
  };
}

async function renderPage(path = '/needs-you'): Promise<HTMLElement> {
  render(
    <MemoryRouter initialEntries={[path]}>
      <NeedsYou />
    </MemoryRouter>,
  );
  await screen.findAllByTestId('needs-you-row');
  return screen.getByTestId('needs-you-detail');
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.getNeedsYou).mockResolvedValue({ items: [ITEM], generatedAt: new Date(NOW).toISOString() });
  vi.mocked(api.fetchApprovals).mockResolvedValue([pending()]);
});
afterEach(cleanup);

describe('NeedsYou always allow', () => {
  it('shows the suggested pattern first, then saves the rule for this project', async () => {
    const card = await renderPage();
    fireEvent.click(await within(card).findByRole('button', { name: 'always allow in Shop' }));
    expect(api.createApprovalRule).not.toHaveBeenCalled();
    const pattern = within(card).getByRole('textbox') as HTMLInputElement;
    expect(pattern.value).toContain('Bash');
    fireEvent.change(pattern, { target: { value: 'Bash(npm run lint)' } });
    fireEvent.click(within(card).getByRole('button', { name: 'add rule' }));
    await waitFor(() =>
      expect(api.createApprovalRule).toHaveBeenCalledWith({ projectId: 5, toolPattern: 'Bash(npm run lint)' }),
    );
    expect(await within(card).findByText(/rule added: Bash\(npm run lint\)/)).toBeTruthy();
    expect(api.resolveApproval).not.toHaveBeenCalled();
  });

  it('offers none on a prod-deploy request', async () => {
    vi.mocked(api.fetchApprovals).mockResolvedValue([pending({ riskClass: 'prod-deploy' })]);
    const card = await renderPage();
    await within(card).findByText(/asks every time, no always-allow/);
    expect(within(card).queryByRole('button', { name: /always allow/ })).toBeNull();
    expect(within(card).queryByRole('button', { name: /^allow once/ })).toBeNull();
  });

  it('opens on a permission from ?tab=approvals', async () => {
    const reply: NeedsYouItem = {
      ...ITEM,
      kind: 'awaiting_reply',
      sessionId: 9,
      sessionName: 'older question',
      requestId: null,
      blockingSince: new Date(NOW - 3_600_000).toISOString(),
    };
    vi.mocked(api.getNeedsYou).mockResolvedValue({ items: [reply, ITEM], generatedAt: new Date(NOW).toISOString() });
    const card = await renderPage('/needs-you?tab=approvals');
    expect(card.dataset.kind).toBe('approval');
  });

  describe('with &request=', () => {
    const second: NeedsYouItem = { ...ITEM, sessionId: 5, sessionUuid: 'u-5', sessionName: 'build', requestId: 12, preview: 'npm run build' };

    beforeEach(() => {
      vi.mocked(api.getNeedsYou).mockResolvedValue({ items: [ITEM, second], generatedAt: new Date(NOW).toISOString() });
      vi.mocked(api.fetchApprovals).mockResolvedValue([pending(), pending({ id: 12, sessionId: 5 })]);
    });

    it('opens on that request, not the first permission', async () => {
      const card = await renderPage('/needs-you?tab=approvals&request=12');
      expect(await within(card).findByText('npm run build')).toBeTruthy();
    });

    it('falls back to the first permission when the request is gone', async () => {
      const card = await renderPage('/needs-you?tab=approvals&request=99');
      expect(await within(card).findByText('npm run lint')).toBeTruthy();
    });
  });
});
