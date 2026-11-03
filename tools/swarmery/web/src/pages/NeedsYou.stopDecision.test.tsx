// @vitest-environment jsdom
//
// The needs-you phase_decision row for an agent-declared stop (verify-not-trust
// phase 10; artboard 3b on v4, plan D-9): the executor hit one of its phase's stop
// triggers and stopped. The claims:
//
//   1. The pane titles the stop with its reason; the facts line carries the
//      trigger (stop-trigger) and the files allowing releases (stop-paths); the
//      options are a radio group with each consequence under its name, and the
//      first option is picked.
//   2. One button repeats the pick; `+ note` opens the note, sent with the answer.
//   3. `own answer…` opens a field; once typed, the radio is cleared and the one
//      button sends `other` with that text.
//   4. The stop block's contract flags (stop_options_count, stop_paths_rejected,
//      …) are on the facts line (stop-flags); a clean block shows none.
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
  it('shows the reason, the trigger, both options with consequences and the released files', async () => {
    serve([STOP]);
    const row = await renderPage();
    expect(within(row).getByText('Guard stopped the run · D-6')).toBeTruthy();
    expect(within(row).getByRole('heading', { name: REASON })).toBeTruthy();
    expect(within(row).getByTestId('stop-trigger').textContent).toBe('protected-path');
    expect(within(row).getByTestId('stop-paths').textContent).toBe('allow releases: pkg/orders/line_items_test.go');
    expect(within(row).getByRole('radiogroup')).toBeTruthy();
    const first = within(row).getByRole('radio', { name: /Let me edit the test/ }) as HTMLInputElement;
    expect(first.checked).toBe(true);
    expect(within(row).getByRole('radio', { name: /Leave the test alone/ })).toBeTruthy();
    expect(within(row).getByText('the test changes with the code it covers')).toBeTruthy();
    expect(within(row).getByText('the retry criterion stays open')).toBeTruthy();
    expect(within(row).queryByTestId('stop-flags')).toBeNull();
    expect(within(row).queryByRole('textbox')).toBeNull();
  });

  it('has one button that repeats the pick', async () => {
    serve([STOP]);
    const row = await renderPage();
    const answers = (): HTMLElement[] => within(row).getAllByRole('button', { name: /^answer/ });
    expect(answers()).toHaveLength(1);
    expect(answers()[0].textContent).toMatch(/^answer: Let me edit the test/);
    fireEvent.click(within(row).getByRole('radio', { name: /Leave the test alone/ }));
    expect(answers()).toHaveLength(1);
    expect(answers()[0].textContent).toMatch(/^answer: Leave the test alone/);
  });

  it('answers with the picked option and the note', async () => {
    serve([STOP]);
    const row = await renderPage();
    fireEvent.click(within(row).getByRole('button', { name: '+ note' }));
    fireEvent.change(within(row).getByRole('textbox'), { target: { value: ' keep it small ' } });
    serve([]);
    fireEvent.click(within(row).getByRole('radio', { name: /Leave the test alone/ }));
    fireEvent.click(within(row).getByRole('button', { name: /^answer: Leave the test alone/ }));
    await waitFor(() =>
      expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(9, 41, 'D-6', 'keep-test', 'keep it small'),
    );
  });

  it('sends own words as other, once there is text, through the same button', async () => {
    serve([STOP]);
    const row = await renderPage();
    fireEvent.click(within(row).getByRole('button', { name: 'own answer…' }));
    fireEvent.change(within(row).getByRole('textbox'), { target: { value: 'split the test file first' } });
    const radios = within(row).getAllByRole('radio') as HTMLInputElement[];
    expect(radios.some((r) => r.checked)).toBe(false);
    fireEvent.click(within(row).getByRole('button', { name: /^answer: split the test file first/ }));
    await waitFor(() =>
      expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(9, 41, 'D-6', 'other', 'split the test file first'),
    );
  });

  it('shows the block contract flags on the facts line', async () => {
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
    expect(within(row).getByTestId('stop-flags').textContent).toBe(
      'contract broken: stop_options_count, stop_paths_rejected',
    );
  });
});
