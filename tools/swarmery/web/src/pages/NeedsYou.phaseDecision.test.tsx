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
//   4. A decision with nothing to allow (only .swarmery refusals, no vetted
//      path) offers one Dismiss button that answers reject, never an Allow that
//      would release nothing, and names the refused paths.
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
    expect(within(row).getByRole('alert').textContent).toContain('the decision is already answered');
    expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(7, 31, 'D-4', 'reject', '');
  });

  it('offers one Dismiss when there is nothing to allow', async () => {
    const q = 'The run touched protected paths: (the guard refused 2 attempts; see guard.log). Allow (add to scope) or reject?';
    const blocks = [
      { at: '2026-11-17T19:53:47Z', tool: 'Bash', path: '.swarmery/plan', reason: 'implicit ".swarmery/plan"' },
      { at: '2026-11-17T20:07:51Z', tool: 'Bash', path: '.swarmery/plan/phase-3.md', reason: 'implicit ".swarmery/plan/**"' },
    ];
    serve([
      {
        ...DECISION,
        question: q,
        decision: {
          ...(DECISION.decision as NonNullable<NeedsYouItem['decision']>),
          question: q,
          guard: { protectedChanged: [], guardBlocks: blocks, guardBlocksTotal: 2, releasablePaths: [] },
        },
      },
    ]);
    const row = await renderPage();
    expect(within(row).queryByRole('button', { name: /^Allow · add to scope/ })).toBeNull();
    expect(within(row).queryByRole('button', { name: 'Reject' })).toBeNull();
    expect(within(row).getByTestId('guard-refused').textContent).toBe('refused: .swarmery/plan, .swarmery/plan/phase-3.md');
    expect(within(row).getByText(/Nothing here can be added to the scope/)).toBeTruthy();
    fireEvent.click(within(row).getByRole('button', { name: /^Dismiss · nothing to allow/ }));
    await waitFor(() => expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(7, 31, 'D-4', 'reject', ''));
  });
});
