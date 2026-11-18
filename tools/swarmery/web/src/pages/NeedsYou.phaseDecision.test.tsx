// @vitest-environment jsdom
//
// The needs-you phase_decision row (verify-not-trust phase 4): a phase run that
// touched protected paths. The claims:
//
//   1. The row names the kind in red and the phase, the guard's finding as its
//      hint; the pane shows the decision id, the guard facts, the question and a
//      link to the phase's Review tab (the same-instant rank is the server's,
//      TestNeedsYouPhaseDecisionSortAndDedupe; the page keeps it).
//   2. Allow-or-reject is two buttons, no radio list; `+ note` opens the note,
//      and Allow POSTs it with the answer, then the list is refetched.
//   3. A refused answer (409) is shown inline and nothing is refetched.
//
// `npx vitest run src/pages/NeedsYou.phaseDecision.test.tsx`.

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
const at = new Date(NOW - 20 * 60_000).toISOString();

const DECISION: NeedsYouItem = {
  kind: 'phase_decision',
  sessionId: 0,
  sessionUuid: '',
  sessionName: 'Trap plan · Phase 1: Make the test suite pass',
  projectSlug: 'trap',
  requestId: null,
  toolName: '',
  preview: 'D-4: The run touched protected paths: trap_test.go. Allow (add to scope) or reject?',
  question: 'The run touched protected paths: trap_test.go. Allow (add to scope) or reject?',
  asksQuestion: false,
  blockingSince: at,
  blockingSeconds: 1200,
  termFocusUrl: null,
  suggestion: null,
  phase: { taskId: 7, planExternalId: '2026-10-22-trap', planTitle: 'Trap plan', phaseId: 31, seq: 1, name: 'Make the test suite pass', manualOpen: 0 },
  decision: {
    taskId: 7,
    phaseId: 31,
    decisionId: 'D-4',
    question: 'The run touched protected paths: trap_test.go. Allow (add to scope) or reject?',
    options: [
      { id: 'allow-add-to-scope', label: 'Allow (add to scope)' },
      { id: 'reject', label: 'Reject' },
    ],
    guard: { protectedChanged: ['trap_test.go'], guardBlocksTotal: 2 },
  },
};

const FAILED: NeedsYouItem = { ...DECISION, kind: 'failed', sessionId: 2, sessionName: 'nightly build', preview: 'exit 1', phase: undefined, decision: undefined };

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

describe('NeedsYou phase_decision', () => {
  it('shows the decision, the guard facts and the question', async () => {
    serve([FAILED, DECISION]);
    const row = await renderPage();
    expect(within(row).getByText('Guard stopped the run · D-4')).toBeTruthy();
    expect(within(row).getByText('Guard stopped the run · D-4').className).toContain('text-red');
    expect(within(row).getByText('touched 1 protected file · 2 blocked attempts')).toBeTruthy();
    expect(within(row).getByRole('heading', { name: DECISION.question })).toBeTruthy();
    expect(within(row).getByRole('button', { name: 'phase review →' })).toBeTruthy();
    expect(within(row).queryByRole('radiogroup')).toBeNull();
    expect(within(row).getByRole('button', { name: /^Allow · add to scope/ })).toBeTruthy();
    expect(within(row).getByRole('button', { name: 'Reject' })).toBeTruthy();
  });

  it('heads the list row with the kind in red, the phase and the guard finding', async () => {
    serve([FAILED, DECISION]);
    await renderPage();
    const listRow = screen.getAllByTestId('needs-you-row').find((r) => r.dataset.kind === 'phase_decision') as HTMLElement;
    const kicker = within(listRow).getByTestId('row-kicker');
    expect(kicker.textContent).toBe('Guard stopped the run');
    expect(kicker.className).toContain('text-red');
    expect(within(listRow).getByText('Make the test suite pass')).toBeTruthy();
    expect(within(listRow).getByText('1 protected path')).toBeTruthy();
    const failed = screen.getAllByTestId('needs-you-row').find((r) => r.dataset.kind === 'failed') as HTMLElement;
    expect(within(failed).getByTestId('row-kicker').className).not.toContain('text-red');
  });

  it('answers with the clicked option and the note, then refetches', async () => {
    serve([DECISION]);
    const row = await renderPage();
    const calls = vi.mocked(api.getNeedsYou).mock.calls.length;
    expect(within(row).queryByRole('textbox')).toBeNull();
    fireEvent.click(within(row).getByRole('button', { name: '+ note' }));
    fireEvent.change(within(row).getByRole('textbox'), { target: { value: ' the test was wrong ' } });
    serve([]);
    fireEvent.click(within(row).getByRole('button', { name: /^Allow · add to scope/ }));
    await waitFor(() =>
      expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(7, 31, 'D-4', 'allow-add-to-scope', 'the test was wrong'),
    );
    await waitFor(() => expect(vi.mocked(api.getNeedsYou).mock.calls.length).toBeGreaterThan(calls));
  });

  it('shows a refused answer inline', async () => {
    serve([DECISION]);
    const row = await renderPage();
    vi.mocked(guardApi.answerGuardDecision).mockRejectedValueOnce(new Error('the decision is already answered'));
    fireEvent.click(within(row).getByRole('button', { name: 'Reject' }));
    expect(await within(row).findByRole('alert')).toBeTruthy();
    expect(within(row).getByRole('alert').textContent).toBe('the decision is already answered');
    expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(7, 31, 'D-4', 'reject', '');
  });
});
