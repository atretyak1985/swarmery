// @vitest-environment jsdom
//
// The needs-you phase_decision row for an agent-declared stop (verify-not-trust
// phase 10): the executor hit one of its phase's stop triggers and stopped. The
// claims:
//
//   1. The row shows the trigger chip, the reason, each option's label as a
//      button with its consequence beside it, and — next to allow-add-to-scope —
//      the files that answer releases.
//   2. Clicking an option POSTs its id with the typed note.
//   3. "Send my own answer" is disabled until there is text, then POSTs
//      `other` with that text.
//   4. The stop block's contract flags (stop_options_count, stop_paths_rejected,
//      …) are shown as a visible warning; a clean block shows none.
//
// `npx vitest run src/pages/NeedsYou.stopDecision.test.tsx`.

import { cleanup, fireEvent, render, screen, waitFor, within } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../api';
import * as guardApi from '../api/guard';
import type { NeedsYouItem } from '../api/types';
import { NeedsYou } from './NeedsYou';

vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  getNeedsYou: vi.fn(),
  fetchApprovals: vi.fn(async () => []),
}));
vi.mock('../api/guard', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api/guard')>()),
  answerGuardDecision: vi.fn(async () => undefined),
}));
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

const NOW = Date.now();
const at = new Date(NOW - 5 * 60_000).toISOString();
const REASON = 'The retry criterion needs pkg/orders/line_items_test.go, which the guard protects.';

const STOP: NeedsYouItem = {
  kind: 'phase_decision',
  sessionId: 0,
  sessionUuid: '',
  sessionName: 'Orders plan · Phase 2: Line-item CRUD',
  projectSlug: 'orders',
  requestId: null,
  toolName: '',
  preview: `D-6: ${REASON}`,
  question: REASON,
  asksQuestion: false,
  blockingSince: at,
  blockingSeconds: 300,
  termFocusUrl: null,
  suggestion: null,
  phase: { taskId: 9, planExternalId: '2026-10-23-orders', planTitle: 'Orders plan', phaseId: 41, seq: 2, name: 'Line-item CRUD', manualOpen: 0 },
  decision: {
    taskId: 9,
    phaseId: 41,
    decisionId: 'D-6',
    source: 'stop',
    question: REASON,
    options: [
      { id: 'allow-add-to-scope', label: 'Let me edit the test', consequence: 'the test changes with the code it covers' },
      { id: 'keep-test', label: 'Leave the test alone', consequence: 'the retry criterion stays open' },
    ],
    guard: {},
    trigger: 'protected-path',
    reason: REASON,
    paths: ['pkg/orders/line_items_test.go'],
    freeText: true,
  },
};

function serve(items: NeedsYouItem[]): void {
  vi.mocked(api.getNeedsYou).mockResolvedValue({ items, generatedAt: new Date(NOW).toISOString() });
}

async function renderPage(): Promise<HTMLElement> {
  render(
    <MemoryRouter initialEntries={['/needs-you']}>
      <NeedsYou />
    </MemoryRouter>,
  );
  await screen.findAllByTestId('needs-you-row');
  const row = screen.getAllByTestId('needs-you-row').find((r) => r.dataset.kind === 'phase_decision');
  if (row === undefined) throw new Error('no phase_decision row');
  fireEvent.click(within(row).getByRole('button'));
  return screen.getByTestId('needs-you-detail');
}

beforeEach(() => {
  vi.clearAllMocks();
});
afterEach(cleanup);

describe('NeedsYou agent stop decision', () => {
  it('shows the trigger, the reason, both options with consequences and the released files', async () => {
    serve([STOP]);
    const row = await renderPage();
    expect(within(row).getByText('guard stop · D-6')).toBeTruthy();
    expect(within(row).getByRole('heading', { name: 'The executor stopped: protected-path' })).toBeTruthy();
    expect(within(row).getByText(REASON)).toBeTruthy();
    expect(within(row).getByRole('radiogroup')).toBeTruthy();
    expect(within(row).getByRole('radio', { name: /Let me edit the test/ })).toBeTruthy();
    expect(within(row).getByRole('radio', { name: /Leave the test alone/ })).toBeTruthy();
    expect(
      within(row).getByText(
        'the test changes with the code it covers · Allowing this releases: pkg/orders/line_items_test.go',
      ),
    ).toBeTruthy();
    expect(within(row).getByText('the retry criterion stays open')).toBeTruthy();
    expect(within(row).queryByText(/contract broken/)).toBeNull();
  });

  it('answers with the clicked option and the note', async () => {
    serve([STOP]);
    const row = await renderPage();
    fireEvent.change(within(row).getByRole('textbox'), { target: { value: ' keep it small ' } });
    serve([]);
    fireEvent.click(within(row).getByRole('radio', { name: /Leave the test alone/ }));
    fireEvent.click(within(row).getByRole('button', { name: /^answer: Leave the test alone/ }));
    await waitFor(() =>
      expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(9, 41, 'D-6', 'keep-test', 'keep it small'),
    );
  });

  it('sends free text as other, only once there is text', async () => {
    serve([STOP]);
    const row = await renderPage();
    const own = within(row).getByRole('button', { name: /^own answer/ }) as HTMLButtonElement;
    expect(own.disabled).toBe(true);
    fireEvent.change(within(row).getByRole('textbox'), { target: { value: 'split the test file first' } });
    expect(own.disabled).toBe(false);
    fireEvent.click(own);
    await waitFor(() =>
      expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(9, 41, 'D-6', 'other', 'split the test file first'),
    );
  });

  it('shows the block contract flags as a warning', async () => {
    const flagged: NeedsYouItem = {
      ...STOP,
      decision: {
        ...(STOP.decision as NonNullable<NeedsYouItem['decision']>),
        options: [{ id: 'allow-add-to-scope', label: 'Let me edit the test' }],
        flags: ['stop_options_count', 'stop_paths_rejected'],
      },
    };
    serve([flagged]);
    const row = await renderPage();
    expect(within(row).getByText('contract broken: stop_options_count, stop_paths_rejected')).toBeTruthy();
  });
});
